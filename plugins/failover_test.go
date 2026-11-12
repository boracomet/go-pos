package plugins

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

// failoverTestOnce answers the next calls of an operation from answers, then lets
// the provider's own behaviour run. It is the port of mockResolvedValueOnce.
func failoverTestOnce(p *failoverTestProvider, operation core.PaymentOperation, answers ...any) {
	failoverTestStub(p, operation, answers, false)
}

// failoverTestAlways answers every call of an operation with answer.
func failoverTestAlways(p *failoverTestProvider, operation core.PaymentOperation, answer any) {
	failoverTestStub(p, operation, []any{answer}, true)
}

func failoverTestStub(p *failoverTestProvider, operation core.PaymentOperation, answers []any, repeat bool) {
	var mu sync.Mutex
	index := 0
	previous := p.respond
	p.respond = func(op core.PaymentOperation, request any) (any, error) {
		if op != operation {
			if previous != nil {
				return previous(op, request)
			}
			return nil, nil
		}
		mu.Lock()
		position := index
		index++
		mu.Unlock()
		if position < len(answers) {
			return answers[position], nil
		}
		if repeat && len(answers) > 0 {
			return answers[len(answers)-1], nil
		}
		if previous != nil {
			return previous(op, request)
		}
		return nil, nil
	}
}

func failoverTestFailure(code core.PaymentErrorCode, errorCode, errorMessage string) *core.PaymentResponse {
	return &core.PaymentResponse{
		Status:       core.StatusFailure,
		Code:         code,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}
}

func failoverTestSuccess(paymentID string) *core.PaymentResponse {
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: paymentID}
}

type failoverTestCircuit struct {
	status func() map[string]FailoverProviderCircuitStatus
	reset  func(providerID string)
	trip   func(providerID string, duration any) error
}

func failoverTestCircuitOf(t *testing.T, facade *failoverTestFacade) failoverTestCircuit {
	t.Helper()
	member, ok := facade.method(t, "failover").(map[string]any)
	if !ok {
		t.Fatalf("failover member has type %T", facade.method(t, "failover"))
	}
	circuit := failoverTestCircuit{}
	if status, ok := member["status"].(func() map[string]FailoverProviderCircuitStatus); ok {
		circuit.status = status
	} else {
		t.Fatalf("status has type %T", member["status"])
	}
	if reset, ok := member["reset"].(func(string)); ok {
		circuit.reset = reset
	} else {
		t.Fatalf("reset has type %T", member["reset"])
	}
	if trip, ok := member["trip"].(func(string, any) error); ok {
		circuit.trip = trip
	} else {
		t.Fatalf("trip has type %T", member["trip"])
	}
	return circuit
}

func (c failoverTestCircuit) state(t *testing.T, providerID string) FailoverCircuitState {
	t.Helper()
	status, ok := c.status()[providerID]
	if !ok {
		t.Fatalf("no circuit status for %q", providerID)
	}
	return status.State
}

func failoverTestWantState(t *testing.T, circuit failoverTestCircuit, providerID string, want FailoverCircuitState) {
	t.Helper()
	if got := circuit.state(t, providerID); got != want {
		t.Errorf("circuit %s = %q, want %q", providerID, got, want)
	}
}

func failoverTestWantCount(t *testing.T, label string, p *failoverTestProvider, operation core.PaymentOperation, want int) {
	t.Helper()
	if got := p.count(operation); got != want {
		t.Errorf("%s %s calls = %d, want %d", label, operation, got, want)
	}
}

func failoverTestWantField(t *testing.T, record map[string]any, key, want string) {
	t.Helper()
	if got := failoverTestText(record, key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func failoverTestTwoProviders(t *testing.T, options FailoverOptions) (*failoverTestFacade, *failoverTestProvider, *failoverTestProvider) {
	t.Helper()
	plugin, err := Failover(options)
	if err != nil {
		t.Fatalf("failover: %v", err)
	}
	a, b := failoverTestNewProvider("a"), failoverTestNewProvider("b")
	facade := failoverTestNewFacade(t, failoverTestSetup{
		Providers:       []*failoverTestProvider{a, b},
		DefaultProvider: "a",
		Plugins:         []core.Plugin{plugin},
	})
	return facade, a, b
}

func TestFailover(t *testing.T) {
	t.Run("switches to the next provider on a transient provider failure", func(t *testing.T) {
		var switches []FailoverEvent
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{
			Order:    []string{"a", "b"},
			OnSwitch: func(event FailoverEvent) error { switches = append(switches, event); return nil },
		})
		failoverTestOnce(a, core.OperationCreatePayment,
			failoverTestFailure(core.CodeProviderError, "PROVIDER_ERROR", "Service temporarily unavailable"))
		failoverTestOnce(b, core.OperationCreatePayment, failoverTestSuccess("pay_from_b"))

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusSuccess))
		failoverTestWantField(t, result, "paymentId", "pay_from_b")
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 1)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 1)
		if len(switches) != 1 {
			t.Fatalf("onSwitch calls = %d, want 1", len(switches))
		}
		event := switches[0]
		if event.From != "a" || event.To != "b" {
			t.Errorf("switch = %s -> %s, want a -> b", event.From, event.To)
		}
		if event.Operation != core.OperationCreatePayment {
			t.Errorf("operation = %q", event.Operation)
		}
		if event.Attempt != 2 {
			t.Errorf("attempt = %d, want 2", event.Attempt)
		}
		if event.Error.ErrorCode != "PROVIDER_ERROR" {
			t.Errorf("error = %+v", event.Error)
		}
	})

	t.Run("never retries when the result is NETWORK_ERROR or pending", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{Order: []string{"a", "b"}})
		failoverTestOnce(a, core.OperationCreatePayment, &core.PaymentResponse{
			Status:       core.StatusPending,
			Code:         core.CodeNetworkError,
			ErrorCode:    "NETWORK_ERROR",
			ErrorMessage: "Socket hang up after request sent",
		})

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusPending))
		failoverTestWantField(t, result, "code", string(core.CodeNetworkError))
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 1)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 0)
	})

	t.Run("does not retry when a card is declined", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{Order: []string{"a", "b"}})
		failoverTestOnce(a, core.OperationCreatePayment,
			failoverTestFailure(core.CodeInsufficientFunds, "51", "Yetersiz bakiye"))

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusFailure))
		failoverTestWantField(t, result, "code", string(core.CodeInsufficientFunds))
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 1)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 0)
	})

	t.Run("circuit breaker opens after the failure threshold and skips the faulty provider", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{
			Order: []string{"a", "b"},
			CircuitBreaker: &FailoverCircuitBreakerOptions{
				Failures: 2, Window: "1m", Cooldown: "5m",
			},
		})
		failoverTestAlways(a, core.OperationCreatePayment,
			failoverTestFailure(core.CodeProviderError, "TIMEOUT_BEFORE_SEND", "Timeout before connect"))
		failoverTestAlways(b, core.OperationCreatePayment, failoverTestSuccess("pay_ok"))
		circuit := failoverTestCircuitOf(t, facade)

		facade.createPayment(t, failoverTestRequest())
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 1)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 1)
		failoverTestWantState(t, circuit, "a", FailoverCircuitClosed)

		facade.createPayment(t, failoverTestRequest())
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 2)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 2)
		failoverTestWantState(t, circuit, "a", FailoverCircuitOpen)

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusSuccess))
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 2)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 3)

		circuit.reset("a")
		failoverTestWantState(t, circuit, "a", FailoverCircuitClosed)
	})

	t.Run("supports dynamic order and a manual trip", func(t *testing.T) {
		plugin, err := Failover(FailoverOptions{
			OrderFunc:      func(core.OperationContext) []string { return []string{"a", "b", "c"} },
			CircuitBreaker: &FailoverCircuitBreakerOptions{Cooldown: "10m"},
		})
		if err != nil {
			t.Fatalf("failover: %v", err)
		}
		a, b, c := failoverTestNewProvider("a"), failoverTestNewProvider("b"), failoverTestNewProvider("c")
		failoverTestAlways(a, core.OperationCreatePayment, failoverTestSuccess("pay_a"))
		failoverTestAlways(b, core.OperationCreatePayment, failoverTestSuccess("pay_b"))
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers:       []*failoverTestProvider{a, b, c},
			DefaultProvider: "a",
			Plugins:         []core.Plugin{plugin},
		})
		circuit := failoverTestCircuitOf(t, facade)
		if err := circuit.trip("a", nil); err != nil {
			t.Fatalf("trip: %v", err)
		}
		failoverTestWantState(t, circuit, "a", FailoverCircuitOpen)

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "paymentId", "pay_b")
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 0)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 1)
	})

	t.Run("does not failover non-routable direct provider calls", func(t *testing.T) {
		plugin, err := Failover(FailoverOptions{Order: []string{"a", "b"}})
		if err != nil {
			t.Fatalf("failover: %v", err)
		}
		a, b := failoverTestNewProvider("a"), failoverTestNewProvider("b")
		failoverTestOnce(a, core.OperationCreatePayment,
			failoverTestFailure(core.CodeProviderError, "PROVIDER_ERROR", "Down"))
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{a, b},
			Plugins:   []core.Plugin{plugin},
		})
		provider, err := facade.Use("a")
		if err != nil {
			t.Fatalf("use: %v", err)
		}
		response, err := provider.CreatePayment(context.Background(), failoverTestRequest())
		if err != nil {
			t.Fatalf("createPayment: %v", err)
		}
		if response.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", response.Status)
		}
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 0)
	})

	t.Run("fails over for authorize as well", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{Order: []string{"a", "b"}})
		failoverTestOnce(a, core.OperationAuthorize,
			failoverTestFailure(core.CodeProviderError, "TIMEOUT", "Gateway timed out"))
		failoverTestOnce(b, core.OperationAuthorize, failoverTestSuccess("auth_ok_b"))

		result := facade.authorize(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusSuccess))
		failoverTestWantField(t, result, "paymentId", "auth_ok_b")
		failoverTestWantCount(t, "a", a, core.OperationAuthorize, 1)
		failoverTestWantCount(t, "b", b, core.OperationAuthorize, 1)
	})

	t.Run("returns the last failure when every provider in order fails", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{Order: []string{"a", "b"}})
		failoverTestOnce(a, core.OperationCreatePayment,
			failoverTestFailure(core.CodeProviderError, "ERR_A", "A down"))
		failoverTestOnce(b, core.OperationCreatePayment,
			failoverTestFailure(core.CodeProviderError, "ERR_B", "B down"))

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusFailure))
		failoverTestWantField(t, result, "errorCode", "ERR_B")
		failoverTestWantCount(t, "a", a, core.OperationCreatePayment, 1)
		failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 1)
	})

	t.Run("circuit breaker enters half-open and recovers on a successful probe", func(t *testing.T) {
		facade, a, _ := failoverTestTwoProviders(t, FailoverOptions{
			Order:          []string{"a", "b"},
			CircuitBreaker: &FailoverCircuitBreakerOptions{Cooldown: 50},
		})
		circuit := failoverTestCircuitOf(t, facade)
		if err := circuit.trip("a", 50); err != nil {
			t.Fatalf("trip: %v", err)
		}
		failoverTestWantState(t, circuit, "a", FailoverCircuitOpen)

		time.Sleep(60 * time.Millisecond)
		failoverTestWantState(t, circuit, "a", FailoverCircuitHalfOpen)

		failoverTestOnce(a, core.OperationCreatePayment, failoverTestSuccess("probe_ok"))
		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "status", string(core.StatusSuccess))
		failoverTestWantField(t, result, "paymentId", "probe_ok")
		failoverTestWantState(t, circuit, "a", FailoverCircuitClosed)
	})

	for _, httpStatus := range []int{500, 502, 504} {
		t.Run(fmt.Sprintf("never fails over after an HTTP %d answer, which may follow a charge", httpStatus), func(t *testing.T) {
			facade, a, b := failoverTestTwoProviders(t, FailoverOptions{
				Order: []string{"a", "b"},
				On:    []string{"PROVIDER_ERROR", fmt.Sprint(httpStatus)},
			})
			answer := failoverTestFailure(core.CodeProviderError, fmt.Sprint(httpStatus), "Gateway error")
			answer.HTTPStatus = httpStatus
			failoverTestOnce(a, core.OperationCreatePayment, answer)

			result := facade.createPayment(t, failoverTestRequest())
			failoverTestWantField(t, result, "status", string(core.StatusFailure))
			failoverTestWantCount(t, "b", b, core.OperationCreatePayment, 0)
		})
	}

	t.Run("fails over after an HTTP 503 answer", func(t *testing.T) {
		facade, a, b := failoverTestTwoProviders(t, FailoverOptions{Order: []string{"a", "b"}})
		failoverTestOnce(a, core.OperationCreatePayment, &core.PaymentResponse{
			Status:       core.StatusFailure,
			ErrorCode:    "503",
			ErrorMessage: "Service unavailable",
			HTTPStatus:   503,
		})
		failoverTestOnce(b, core.OperationCreatePayment, failoverTestSuccess("pay_from_b"))

		result := facade.createPayment(t, failoverTestRequest())
		failoverTestWantField(t, result, "paymentId", "pay_from_b")
	})
}

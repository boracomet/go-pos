package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func chaosTestNew(t *testing.T, options ChaosOptions, mode core.Mode) (core.Plugin, *pluginTestProvider, *pluginTestContext) {
	t.Helper()
	plugin, err := Chaos(options)
	if err != nil {
		t.Fatalf("chaos() failed: %v", err)
	}
	pctx := newPluginTestContext()
	pctx.mode = mode
	pctx.providerIDs = []string{"a", "b"}
	pctx.defaultProvider = "a"
	provider := newPluginTestProvider("a")
	pctx.providers["a"] = provider
	pctx.providers["b"] = newPluginTestProvider("b")
	return plugin, provider, pctx
}

func chaosTestReady(t *testing.T, options ChaosOptions, mode core.Mode) (core.Plugin, *pluginTestProvider, *pluginTestContext) {
	t.Helper()
	plugin, provider, pctx := chaosTestNew(t, options, mode)
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	return plugin, provider, pctx
}

// chaosTestCreate runs the before hooks and, when nothing was injected, the provider.
func chaosTestCreate(t *testing.T, plugin core.Plugin, provider *pluginTestProvider, providerID string) *core.PaymentResponse {
	t.Helper()
	request := pluginTestPaymentRequest()
	ctx := pluginTestOperation(core.OperationCreatePayment, providerID, request)
	injected := pluginTestRunBefore(t, plugin, ctx)
	if injected != nil && injected.Result != nil {
		result, ok := injected.Result.(*core.PaymentResponse)
		if !ok {
			t.Fatalf("injected result is %T, want *core.PaymentResponse", injected.Result)
		}
		return result
	}
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	return result
}

func chaosTestMethods(t *testing.T, plugin core.Plugin, pctx core.PluginContext) *ChaosMethods {
	t.Helper()
	methods, ok := pluginTestMethods(t, plugin, pctx)["chaos"].(*ChaosMethods)
	if !ok {
		t.Fatal("the chaos member is missing")
	}
	return methods
}

func TestChaosInjectsFailureWithoutCallingProvider(t *testing.T) {
	plugin, provider, _ := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{{Fail: core.CodeCardDeclined}},
	}, core.ModeSandbox)

	result := chaosTestCreate(t, plugin, provider, "a")
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeCardDeclined {
		t.Errorf("code = %q, want CARD_DECLINED", result.Code)
	}
	if !strings.Contains(result.ErrorMessage, "chaos") {
		t.Errorf("errorMessage = %q, want it to mention chaos", result.ErrorMessage)
	}
	if provider.count("createPayment") != 0 {
		t.Errorf("the provider was called %d times, want 0", provider.count("createPayment"))
	}
}

func TestChaosNetworkErrorIsPendingWithCustomMessage(t *testing.T) {
	plugin, provider, _ := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{{Fail: core.CodeNetworkError, Message: "lost response"}},
	}, core.ModeSandbox)

	result := chaosTestCreate(t, plugin, provider, "a")
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want NETWORK_ERROR", result.Code)
	}
	if result.ErrorMessage != "lost response" {
		t.Errorf("errorMessage = %q, want 'lost response'", result.ErrorMessage)
	}
}

func TestChaosInjectedResultEmitsItsEvent(t *testing.T) {
	plugin, provider, pctx := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{{Fail: core.CodeInsufficientFunds}},
	}, core.ModeSandbox)

	request := pluginTestPaymentRequest()
	result := chaosTestCreate(t, plugin, provider, "a")
	event := core.EventFor(core.OperationCreatePayment, "a", request, result)
	if event == nil {
		t.Fatal("the injected result triggers no event")
	}
	if event.Type != core.EventPaymentFailed {
		t.Errorf("event type = %q, want payment.failed", event.Type)
	}
	if err := pctx.Emit(context.Background(), event); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if len(pctx.emitted()) != 1 {
		t.Errorf("emitted %d events, want 1", len(pctx.emitted()))
	}
}

func TestChaosRulesMatchOperationsAndProviders(t *testing.T) {
	plugin, provider, pctx := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{
			{Operations: []core.PaymentOperation{core.OperationRefund}, Fail: core.CodeProviderError},
			{Operations: []core.PaymentOperation{core.OperationCreatePayment}, Providers: []string{"b"}, Fail: core.CodeCardDeclined},
		},
	}, core.ModeSandbox)

	viaA := chaosTestCreate(t, plugin, provider, "a")
	if viaA.Status != core.StatusSuccess {
		t.Errorf("provider a status = %q, want success", viaA.Status)
	}
	if provider.count("createPayment") != 1 {
		t.Errorf("provider a was called %d times, want 1", provider.count("createPayment"))
	}

	providerB := pctx.providers["b"].(*pluginTestProvider)
	viaB := chaosTestCreate(t, plugin, providerB, "b")
	if viaB.Code != core.CodeCardDeclined {
		t.Errorf("provider b code = %q, want CARD_DECLINED", viaB.Code)
	}
	if providerB.count("createPayment") != 0 {
		t.Errorf("provider b was called %d times, want 0", providerB.count("createPayment"))
	}

	refundRequest := &core.RefundRequest{PaymentTransactionID: "x", Price: "1.00"}
	ctx := pluginTestOperation(core.OperationRefund, "a", refundRequest)
	injected := pluginTestRunBefore(t, plugin, ctx)
	if injected == nil || injected.Result == nil {
		t.Fatal("the refund rule did not fire")
	}
	refund, ok := injected.Result.(*core.RefundResponse)
	if !ok {
		t.Fatalf("injected refund is %T, want *core.RefundResponse", injected.Result)
	}
	if refund.Code != core.CodeProviderError {
		t.Errorf("refund code = %q, want PROVIDER_ERROR", refund.Code)
	}
	if provider.count("refund") != 0 {
		t.Errorf("the refund reached the provider %d times, want 0", provider.count("refund"))
	}
}

func TestChaosUsesFirstMatchingRuleThatFires(t *testing.T) {
	plugin, provider, _ := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{
			{Probability: pluginTestPtr(0.0), Fail: core.CodeCardDeclined},
			{Fail: core.CodeExpiredCard},
			{Fail: core.CodeInvalidCVC},
		},
	}, core.ModeSandbox)

	result := chaosTestCreate(t, plugin, provider, "a")
	if result.Code != core.CodeExpiredCard {
		t.Errorf("code = %q, want EXPIRED_CARD", result.Code)
	}
}

func TestChaosSameSeedFiresSameCalls(t *testing.T) {
	run := func() []core.PaymentStatus {
		plugin, provider, _ := chaosTestReady(t, ChaosOptions{
			Seed:  pluginTestPtr(int64(42)),
			Rules: []ChaosRule{{Probability: pluginTestPtr(0.5), Fail: core.CodeCardDeclined}},
		}, core.ModeSandbox)
		statuses := make([]core.PaymentStatus, 0, 20)
		for i := 0; i < 20; i++ {
			statuses = append(statuses, chaosTestCreate(t, plugin, provider, "a").Status)
		}
		return statuses
	}
	first := run()
	second := run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("call %d: first run %q, second run %q", i, first[i], second[i])
		}
	}
	var failures, successes int
	for _, status := range first {
		switch status {
		case core.StatusFailure:
			failures++
		case core.StatusSuccess:
			successes++
		}
	}
	if failures == 0 || successes == 0 {
		t.Errorf("a seeded run fired %d failures and %d successes, want both", failures, successes)
	}
}

func TestChaosDelaysThenRunsOnProvider(t *testing.T) {
	plugin, provider, _ := chaosTestReady(t, ChaosOptions{
		Rules: []ChaosRule{{Delay: "30ms"}},
	}, core.ModeSandbox)

	started := time.Now()
	result := chaosTestCreate(t, plugin, provider, "a")
	elapsed := time.Since(started)
	if elapsed < 25*time.Millisecond {
		t.Errorf("the call took %s, want at least the 30ms delay", elapsed)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if provider.count("createPayment") != 1 {
		t.Errorf("the provider was called %d times, want 1", provider.count("createPayment"))
	}
}

func TestChaosCanBeTurnedOffAndOn(t *testing.T) {
	plugin, provider, pctx := chaosTestReady(t, ChaosOptions{
		Enabled: pluginTestPtr(false),
		Rules:   []ChaosRule{{Fail: core.CodeCardDeclined}},
	}, core.ModeSandbox)
	methods := chaosTestMethods(t, plugin, pctx)

	if methods.Enabled() {
		t.Error("the plugin reports itself enabled, want disabled")
	}
	if status := chaosTestCreate(t, plugin, provider, "a").Status; status != core.StatusSuccess {
		t.Errorf("disabled status = %q, want success", status)
	}
	if err := methods.Enable(); err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if code := chaosTestCreate(t, plugin, provider, "a").Code; code != core.CodeCardDeclined {
		t.Errorf("enabled code = %q, want CARD_DECLINED", code)
	}
	methods.Disable()
	if status := chaosTestCreate(t, plugin, provider, "a").Status; status != core.StatusSuccess {
		t.Errorf("disabled again status = %q, want success", status)
	}
	if provider.count("createPayment") != 2 {
		t.Errorf("the provider was called %d times, want 2", provider.count("createPayment"))
	}
}

func TestChaosRefusesProductionUnlessAllowed(t *testing.T) {
	blocked, _, blockedContext := chaosTestNew(t, ChaosOptions{
		Rules: []ChaosRule{{Fail: core.CodeCardDeclined}},
	}, core.ModeProduction)
	err := pluginTestInit(blocked, blockedContext)
	if err == nil {
		t.Fatal("init in production mode succeeded, want a configuration error")
	}
	if _, ok := err.(*core.ConfigurationError); !ok {
		t.Errorf("init error is %T, want *core.ConfigurationError", err)
	}

	disabled, disabledProvider, disabledContext := chaosTestReady(t, ChaosOptions{
		Enabled: pluginTestPtr(false),
		Rules:   []ChaosRule{{Fail: core.CodeCardDeclined}},
	}, core.ModeProduction)
	if status := chaosTestCreate(t, disabled, disabledProvider, "a").Status; status != core.StatusSuccess {
		t.Errorf("disabled production status = %q, want success", status)
	}
	if err := chaosTestMethods(t, disabled, disabledContext).Enable(); err == nil {
		t.Error("enable in production mode succeeded, want a configuration error")
	}

	allowed, allowedProvider, _ := chaosTestReady(t, ChaosOptions{
		AllowProduction: true,
		Rules:           []ChaosRule{{Fail: core.CodeCardDeclined}},
	}, core.ModeProduction)
	if code := chaosTestCreate(t, allowed, allowedProvider, "a").Code; code != core.CodeCardDeclined {
		t.Errorf("allowed production code = %q, want CARD_DECLINED", code)
	}
}

func TestChaosValidatesOptions(t *testing.T) {
	cases := []struct {
		name    string
		options ChaosOptions
	}{
		{"probability", ChaosOptions{Rules: []ChaosRule{{Probability: pluginTestPtr(2.0)}}}},
		{"code", ChaosOptions{Rules: []ChaosRule{{Fail: core.PaymentErrorCode("NOPE")}}}},
		{"delay", ChaosOptions{Rules: []ChaosRule{{Delay: "soon"}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := Chaos(testCase.options); err == nil {
				t.Fatal("chaos() accepted invalid options")
			}
		})
	}
}

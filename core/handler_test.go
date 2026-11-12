package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	pos "github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
)

type handlerTestCall struct {
	operation core.PaymentOperation
	request   any
}

type handlerTestProvider struct {
	core.BaseProvider
	mu      sync.Mutex
	calls   []handlerTestCall
	respond func(core.PaymentOperation, any) (any, error)
}

func newHandlerTestProvider(name string) *handlerTestProvider {
	return &handlerTestProvider{BaseProvider: core.NewBaseProvider(name, core.ProviderConfig{})}
}

func (p *handlerTestProvider) record(operation core.PaymentOperation, request any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, handlerTestCall{operation: operation, request: request})
}

func (p *handlerTestProvider) count(operation core.PaymentOperation) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, call := range p.calls {
		if call.operation == operation {
			total++
		}
	}
	return total
}

func (p *handlerTestProvider) requests(operation core.PaymentOperation) []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var found []any
	for _, call := range p.calls {
		if call.operation == operation {
			found = append(found, call.request)
		}
	}
	return found
}

func handlerTestRun[T any](p *handlerTestProvider, operation core.PaymentOperation, request any, fallback T) (T, error) {
	p.record(operation, request)
	p.mu.Lock()
	respond := p.respond
	p.mu.Unlock()
	if respond == nil {
		return fallback, nil
	}
	value, err := respond(operation, request)
	if err != nil {
		var zero T
		return zero, err
	}
	if value == nil {
		return fallback, nil
	}
	typed, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("unexpected result type %T for %s", value, operation)
	}
	return typed, nil
}

func (p *handlerTestProvider) CreatePayment(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return handlerTestRun(p, core.OperationCreatePayment, request,
		&core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-1"})
}

func (p *handlerTestProvider) InitThreeDSPayment(_ context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return handlerTestRun(p, core.OperationInitThreeDSPayment, request,
		&core.ThreeDSInitResponse{Status: core.StatusPending, ThreeDSHTMLContent: "<form>"})
}

func (p *handlerTestProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	return handlerTestRun(p, core.OperationCompleteThreeDSPayment, callbackData,
		&core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-1"})
}

func (p *handlerTestProvider) Refund(_ context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	return handlerTestRun(p, core.OperationRefund, request, &core.RefundResponse{Status: core.StatusSuccess})
}

func (p *handlerTestProvider) Cancel(_ context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	return handlerTestRun(p, core.OperationCancel, request, &core.CancelResponse{Status: core.StatusSuccess})
}

func (p *handlerTestProvider) GetPayment(_ context.Context, paymentID string) (*core.PaymentResponse, error) {
	return handlerTestRun(p, core.OperationGetPayment, paymentID,
		&core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-1"})
}

func (p *handlerTestProvider) BinCheck(_ context.Context, binNumber string) (*core.BinCheckResponse, error) {
	return handlerTestRun(p, core.OperationBinCheck, binNumber, &core.BinCheckResponse{BinNumber: "454360"})
}

func (p *handlerTestProvider) InstallmentInfo(_ context.Context, request *core.InstallmentInfoRequest) (*core.InstallmentInfoResponse, error) {
	return handlerTestRun(p, core.OperationInstallmentInfo, request,
		&core.InstallmentInfoResponse{Status: core.StatusSuccess, InstallmentDetails: []core.InstallmentPrice{}})
}

type handlerTestPayTR struct{ *handlerTestProvider }

func (p *handlerTestPayTR) CallbackAcknowledgement() core.CallbackAcknowledgement {
	return core.CallbackAcknowledgement{Success: "OK", Failure: "PAYTR notification failed: bad hash"}
}

type handlerTestToken struct{ *handlerTestProvider }

func (p *handlerTestToken) CreatePaymentWithToken(_ context.Context, request any) (*core.ThreeDSInitResponse, error) {
	return handlerTestRun(p.handlerTestProvider, "createPaymentWithToken", request, &core.ThreeDSInitResponse{Status: core.StatusPending})
}

type handlerTestIyzico struct{ *handlerTestProvider }

func (p *handlerTestIyzico) InitCheckoutForm(_ context.Context, request *core.CheckoutFormRequest) (*core.CheckoutFormInitResponse, error) {
	p.record("initCheckoutForm", request)
	return &core.CheckoutFormInitResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) RetrieveCheckoutForm(_ context.Context, token, conversationID string) (*core.CheckoutFormRetrieveResponse, error) {
	p.record("retrieveCheckoutForm", []string{token, conversationID})
	return &core.CheckoutFormRetrieveResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) InitPWIPayment(_ context.Context, request *core.PWIPaymentRequest) (*core.PWIPaymentInitResponse, error) {
	p.record("initPWIPayment", request)
	return &core.PWIPaymentInitResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) RetrievePWIPayment(_ context.Context, token, conversationID string) (*core.PWIPaymentRetrieveResponse, error) {
	p.record("retrievePWIPayment", []string{token, conversationID})
	return &core.PWIPaymentRetrieveResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) InitializeSubscription(_ context.Context, request *core.SubscriptionInitializeRequest) (*core.SubscriptionInitializeResponse, error) {
	p.record("initializeSubscription", request)
	return &core.SubscriptionInitializeResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) CancelSubscription(_ context.Context, request *core.SubscriptionCancelRequest) (*core.SubscriptionCancelResponse, error) {
	p.record("cancelSubscription", request)
	return &core.SubscriptionCancelResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) UpgradeSubscription(_ context.Context, request *core.SubscriptionUpgradeRequest) (*core.SubscriptionUpgradeResponse, error) {
	p.record("upgradeSubscription", request)
	return &core.SubscriptionUpgradeResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) RetrieveSubscription(_ context.Context, request *core.SubscriptionRetrieveRequest) (*core.SubscriptionRetrieveResponse, error) {
	p.record("retrieveSubscription", request)
	return &core.SubscriptionRetrieveResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) UpdateSubscriptionCard(_ context.Context, request *core.SubscriptionCardUpdateRequest) (*core.SubscriptionCardUpdateResponse, error) {
	p.record("updateSubscriptionCard", request)
	return &core.SubscriptionCardUpdateResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) CreateSubscriptionProduct(_ context.Context, request *core.SubscriptionProductCreateRequest) (*core.SubscriptionProductResponse, error) {
	p.record("createSubscriptionProduct", request)
	return &core.SubscriptionProductResponse{Status: core.StatusSuccess}, nil
}

func (p *handlerTestIyzico) CreatePricingPlan(_ context.Context, request *core.PricingPlanCreateRequest) (*core.PricingPlanResponse, error) {
	p.record("createPricingPlan", request)
	return &core.PricingPlanResponse{Status: core.StatusSuccess}, nil
}

func handlerTestAllowAll() core.HandlerOptions {
	return core.HandlerOptions{
		AllowedActions: core.AllHandlerActions,
		Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
	}
}

func handlerTestFacade(t *testing.T, providers map[string]core.ProviderEntry, options *core.HandlerOptions) *pos.Payment {
	t.Helper()
	config := pos.Config{Providers: providers}
	if options != nil {
		config.Handler = options
	}
	payment, err := pos.New(config)
	if err != nil {
		t.Fatalf("pos.New: %v", err)
	}
	return payment
}

func handlerTestRequest(method, url string, body any, headers map[string]string) *core.HandlerRequest {
	merged := map[string]string{"content-type": "application/json"}
	for name, value := range headers {
		merged[name] = value
	}
	return &core.HandlerRequest{Method: method, URL: url, Headers: merged, Body: body}
}

func handlerTestHandle(t *testing.T, handler *core.Handler, request *core.HandlerRequest) *core.HandlerResponse {
	t.Helper()
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle(%s %s): %v", request.Method, request.URL, err)
	}
	return response
}

func handlerTestJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return record
}

func handlerTestBody(t *testing.T, response *core.HandlerResponse) map[string]any {
	t.Helper()
	return handlerTestJSON(t, response.Body)
}

func handlerTestWantStatus(t *testing.T, response *core.HandlerResponse, want int) {
	t.Helper()
	if response.Status != want {
		t.Fatalf("status = %d, want %d (body %v)", response.Status, want, response.Body)
	}
}

func TestHandlerHealth(t *testing.T) {
	provider := newHandlerTestProvider("iyzico")
	payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, nil)

	t.Run("returns status and version without listing providers", func(t *testing.T) {
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodGet, "/api/pay/health", nil, nil))
		handlerTestWantStatus(t, response, http.StatusOK)
		body := handlerTestBody(t, response)
		if body["status"] != "ok" || body["service"] != "go-pos" || body["version"] != core.Version {
			t.Fatalf("body = %v", body)
		}
		if _, listed := body["providers"]; listed {
			t.Fatalf("health must not list providers: %v", body)
		}
	})

	t.Run("does not treat a provider health path as the health check", func(t *testing.T) {
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodGet, "/api/pay/iyzico/payment/health", nil, nil))
		if response.Status == http.StatusOK {
			t.Fatalf("status = 200, want a rejection")
		}
	})
}

func TestHandlerRouting(t *testing.T) {
	t.Run("returns 404 outside basePath for unknown providers and unknown actions", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		for _, url := range []string{"/other/iyzico/payment", "/api/pay/stripe/payment", "/api/pay/iyzico/unknown"} {
			response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, url, map[string]any{}, nil))
			handlerTestWantStatus(t, response, http.StatusNotFound)
		}
	})

	t.Run("supports absolute URLs query strings and trailing slashes", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"https://shop.example/api/pay/iyzico/payment/?x=1", map[string]any{"price": "1"}, nil))
		handlerTestWantStatus(t, response, http.StatusOK)
		requests := provider.requests(core.OperationCreatePayment)
		if len(requests) != 1 {
			t.Fatalf("createPayment calls = %d, want 1", len(requests))
		}
		request, ok := requests[0].(*core.PaymentRequest)
		if !ok || request.Price != "1" {
			t.Fatalf("createPayment request = %#v", requests[0])
		}
	})

	t.Run("supports a custom basePath", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		options.BasePath = "/payments/"
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/payments/iyzico/payment", map[string]any{}, nil)), http.StatusOK)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil)), http.StatusNotFound)
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Fatalf("createPayment calls = %d, want 1", got)
		}
	})

	t.Run("returns 400 when the provider is not enabled", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/paytr/payment", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusBadRequest)
	})

	t.Run("routes GET payment id to getPayment URL-decoded", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodGet, "/api/pay/iyzico/payment/ORD%201", nil, nil))
		handlerTestWantStatus(t, response, http.StatusOK)
		requests := provider.requests(core.OperationGetPayment)
		if len(requests) != 1 || requests[0] != "ORD 1" {
			t.Fatalf("getPayment requests = %#v, want [ORD 1]", requests)
		}
	})

	t.Run("returns 405 for wrong methods", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodGet, "/api/pay/iyzico/payment", nil, nil)), http.StatusMethodNotAllowed)
	})

	t.Run("returns 400 when the body is missing", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", nil, nil)), http.StatusBadRequest)
	})
}

func TestHandlerSecureDefaults(t *testing.T) {
	t.Run("only exposes callbacks and card queries by default", func(t *testing.T) {
		want := []core.HandlerAction{
			core.ActionPaymentComplete3DS, core.ActionCallback, core.ActionInstallment, core.ActionBinCheck,
		}
		if !reflect.DeepEqual(core.DefaultHandlerActions, want) {
			t.Fatalf("DefaultHandlerActions = %v, want %v", core.DefaultHandlerActions, want)
		}
		provider := newHandlerTestProvider("iyzico")
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, nil)
		routes := []struct{ method, path string }{
			{http.MethodPost, "payment"},
			{http.MethodPost, "payment/init-3ds"},
			{http.MethodPost, "refund"},
			{http.MethodPost, "cancel"},
			{http.MethodGet, "payment/PMT-1"},
			{http.MethodPost, "subscription/cancel"},
		}
		for _, route := range routes {
			response := handlerTestHandle(t, payment.Handler(),
				handlerTestRequest(route.method, "/api/pay/iyzico/"+route.path, map[string]any{}, nil))
			if response.Status != http.StatusNotFound {
				t.Fatalf("%s: status = %d, want 404", route.path, response.Status)
			}
		}
		if got := provider.count(core.OperationRefund); got != 0 {
			t.Fatalf("refund calls = %d, want 0", got)
		}
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/bin-check", map[string]any{"binNumber": "454360"}, nil)), http.StatusOK)
	})

	t.Run("refuses to enable privileged actions without an authorize hook", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		providers := map[string]core.ProviderEntry{"iyzico": provider}
		for _, actions := range [][]core.HandlerAction{{core.ActionRefund}, core.AllHandlerActions} {
			_, err := pos.New(pos.Config{Providers: providers, Handler: &core.HandlerOptions{AllowedActions: actions}})
			if err == nil || !strings.Contains(err.Error(), "authorize") {
				t.Fatalf("error = %v, want it to mention authorize", err)
			}
		}
		if _, err := pos.New(pos.Config{Providers: providers, Handler: &core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPaymentInit3DS},
		}}); err != nil {
			t.Fatalf("payment/init-3ds must not need authorize: %v", err)
		}
	})

	t.Run("returns 403 when authorize rejects or fails", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		providers := map[string]core.ProviderEntry{"iyzico": provider}
		deny := core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionRefund},
			Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return false, nil },
		}
		payment := handlerTestFacade(t, providers, &deny)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/refund", map[string]any{}, nil)), http.StatusForbidden)

		boom := core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionRefund},
			Authorize: func(context.Context, core.HandlerContext) (bool, error) {
				return false, fmt.Errorf("no session")
			},
		}
		failing := handlerTestFacade(t, providers, &boom)
		handlerTestWantStatus(t, handlerTestHandle(t, failing.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/refund", map[string]any{}, nil)), http.StatusForbidden)
		if got := provider.count(core.OperationRefund); got != 0 {
			t.Fatalf("refund calls = %d, want 0", got)
		}
	})

	t.Run("passes the context to authorize", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		var seen core.HandlerContext
		options := core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionRefund},
			Authorize: func(_ context.Context, hctx core.HandlerContext) (bool, error) {
				seen = hctx
				return true, nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, "/api/pay/iyzico/refund",
			map[string]any{"paymentId": "1"}, map[string]string{"authorization": "Bearer x"}))
		if seen.Provider != "iyzico" || seen.Action != core.ActionRefund {
			t.Fatalf("context = %+v", seen)
		}
		if !reflect.DeepEqual(seen.Body, map[string]any{"paymentId": "1"}) {
			t.Fatalf("body = %#v", seen.Body)
		}
		if seen.Request.Headers["authorization"] != "Bearer x" {
			t.Fatalf("headers = %#v", seen.Request.Headers)
		}
	})

	t.Run("transformRequest replaces the client body", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPaymentInit3DS},
			TransformRequest: func(_ context.Context, hctx core.HandlerContext) (map[string]any, error) {
				transformed := map[string]any{}
				for name, value := range hctx.Body {
					transformed[name] = value
				}
				transformed["price"] = "100.00"
				transformed["paidPrice"] = "100.00"
				return transformed, nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment/init-3ds",
			map[string]any{"conversationId": "o1", "price": "0.01"}, nil))
		requests := provider.requests(core.OperationInitThreeDSPayment)
		if len(requests) != 1 {
			t.Fatalf("initThreeDSPayment calls = %d, want 1", len(requests))
		}
		request, ok := requests[0].(*core.ThreeDSPaymentRequest)
		if !ok {
			t.Fatalf("request type %T", requests[0])
		}
		if request.ConversationID != "o1" || request.Price != "100.00" || request.PaidPrice != "100.00" {
			t.Fatalf("request = %+v", request.PaymentRequest)
		}
	})

	t.Run("does not leak internal error messages by default", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(operation core.PaymentOperation, _ any) (any, error) {
			if operation == core.OperationCreatePayment {
				return nil, fmt.Errorf("db password wrong")
			}
			return nil, nil
		}
		providers := map[string]core.ProviderEntry{"iyzico": provider}
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, providers, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusInternalServerError)
		if message := handlerTestBody(t, response)["message"]; message != "Internal server error" {
			t.Fatalf("message = %v", message)
		}

		exposedOptions := handlerTestAllowAll()
		exposedOptions.ExposeErrors = true
		exposed := handlerTestFacade(t, providers, &exposedOptions)
		exposedResponse := handlerTestHandle(t, exposed.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil))
		if message := handlerTestBody(t, exposedResponse)["message"]; message != "db password wrong" {
			t.Fatalf("message = %v", message)
		}
	})
}

func TestHandlerStatusCodes(t *testing.T) {
	t.Run("returns 422 when the provider reports a failure", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(core.PaymentOperation, any) (any, error) {
			return &core.PaymentResponse{Status: core.StatusFailure, ErrorMessage: "declined"}, nil
		}
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusUnprocessableEntity)
		if message := handlerTestBody(t, response)["errorMessage"]; message != "declined" {
			t.Fatalf("errorMessage = %v", message)
		}
	})

	t.Run("returns 200 for pending results", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/payment/init-3ds", map[string]any{}, nil)), http.StatusOK)
	})

	t.Run("validates binNumber and maps bin-check errors to 422", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(operation core.PaymentOperation, _ any) (any, error) {
			if operation == core.OperationBinCheck {
				return nil, fmt.Errorf("not supported")
			}
			return nil, nil
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, nil)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/bin-check", map[string]any{"binNumber": "abc"}, nil)), http.StatusBadRequest)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/bin-check", map[string]any{"binNumber": "454360"}, nil)), http.StatusUnprocessableEntity)
	})
}

func TestHandlerResponseData(t *testing.T) {
	raw := map[string]any{"cardUserKey": "card-user", "merchantCommissionRate": "2.5"}

	t.Run("leaves out rawResponse by default", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(operation core.PaymentOperation, _ any) (any, error) {
			switch operation {
			case core.OperationCreatePayment:
				return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-1", RawResponse: raw}, nil
			case core.OperationBinCheck:
				return &core.BinCheckResponse{BinNumber: "454360", RawResponse: raw}, nil
			}
			return nil, nil
		}
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil))
		want := map[string]any{"status": "success", "paymentId": "PMT-1"}
		if got := handlerTestBody(t, response); !reflect.DeepEqual(got, want) {
			t.Fatalf("body = %v, want %v", got, want)
		}
		bin := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/bin-check", map[string]any{"binNumber": "454360"}, nil))
		if got := handlerTestBody(t, bin); !reflect.DeepEqual(got, map[string]any{"binNumber": "454360"}) {
			t.Fatalf("bin body = %v", got)
		}
	})

	t.Run("returns rawResponse with exposeRawResponse", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(core.PaymentOperation, any) (any, error) {
			return &core.PaymentResponse{Status: core.StatusSuccess, RawResponse: raw}, nil
		}
		options := handlerTestAllowAll()
		options.ExposeRawResponse = true
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, nil))
		if got := handlerTestBody(t, response)["rawResponse"]; !reflect.DeepEqual(got, raw) {
			t.Fatalf("rawResponse = %v, want %v", got, raw)
		}
	})

	t.Run("passes rawResponse to onCallback", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		provider.respond = func(core.PaymentOperation, any) (any, error) {
			return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-1", RawResponse: raw}, nil
		}
		var seen *core.PaymentResponse
		options := core.HandlerOptions{
			OnCallback: func(_ context.Context, result *core.PaymentResponse, _ core.CallbackContext) error {
				seen = result
				return nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/payment/complete-3ds", map[string]any{"paymentId": "PMT-1"}, nil))
		if _, exposed := handlerTestBody(t, response)["rawResponse"]; exposed {
			t.Fatalf("rawResponse leaked: %v", response.Body)
		}
		if seen == nil || !reflect.DeepEqual(seen.RawResponse, raw) {
			t.Fatalf("onCallback rawResponse = %#v", seen)
		}
	})
}

func TestHandlerIdempotencyKeyScope(t *testing.T) {
	user := func(id string) map[string]string {
		return map[string]string{"idempotency-key": "order-1", "x-user": id}
	}
	paymentRequest := func(headers map[string]string) *core.HandlerRequest {
		return handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment", map[string]any{}, headers)
	}

	t.Run("shares stored responses between callers by default", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestHandle(t, payment.Handler(), paymentRequest(user("a")))
		second := handlerTestHandle(t, payment.Handler(), paymentRequest(user("b")))
		if second.Headers["Idempotent-Replayed"] != "true" {
			t.Fatalf("headers = %v", second.Headers)
		}
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Fatalf("createPayment calls = %d, want 1", got)
		}
	})

	t.Run("keeps the keys of different callers apart with scope", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		options.Idempotency = &core.HandlerIdempotencyOptions{
			Scope: func(_ context.Context, hctx core.HandlerContext) (string, error) {
				return hctx.Request.Headers["x-user"], nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestHandle(t, payment.Handler(), paymentRequest(user("a")))
		other := handlerTestHandle(t, payment.Handler(), paymentRequest(user("b")))
		if _, replayed := other.Headers["Idempotent-Replayed"]; replayed {
			t.Fatalf("another caller replayed: %v", other.Headers)
		}
		same := handlerTestHandle(t, payment.Handler(), paymentRequest(user("a")))
		if same.Headers["Idempotent-Replayed"] != "true" {
			t.Fatalf("headers = %v", same.Headers)
		}
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Fatalf("createPayment calls = %d, want 2", got)
		}
	})
}

func TestHandlerCallbacks(t *testing.T) {
	t.Run("parses form-urlencoded callbacks", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, nil)
		handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, "/api/pay/iyzico/payment/complete-3ds",
			"status=success&paymentId=1&mdStatus=1", map[string]string{"content-type": "application/x-www-form-urlencoded"}))
		requests := provider.requests(core.OperationCompleteThreeDSPayment)
		want := map[string]any{"status": "success", "paymentId": "1", "mdStatus": "1"}
		if len(requests) != 1 || !reflect.DeepEqual(requests[0], want) {
			t.Fatalf("completeThreeDSPayment requests = %#v, want [%v]", requests, want)
		}
	})

	for _, contentType := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
		"text/plain",
	} {
		t.Run("rejects "+contentType+" bodies on non-callback actions", func(t *testing.T) {
			provider := newHandlerTestProvider("iyzico")
			options := handlerTestAllowAll()
			payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
			response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, "/api/pay/iyzico/refund",
				"paymentId=1&price=100", map[string]string{"content-type": contentType}))
			handlerTestWantStatus(t, response, http.StatusUnsupportedMediaType)
			if got := provider.count(core.OperationRefund); got != 0 {
				t.Fatalf("refund calls = %d, want 0", got)
			}
		})
	}

	t.Run("rejects form bodies already parsed by a body parser on non-callback actions", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost, "/api/pay/iyzico/refund",
			map[string]any{"paymentId": "1", "price": "100"},
			map[string]string{"content-type": "application/x-www-form-urlencoded"}))
		handlerTestWantStatus(t, response, http.StatusUnsupportedMediaType)
		if got := provider.count(core.OperationRefund); got != 0 {
			t.Fatalf("refund calls = %d, want 0", got)
		}
	})

	t.Run("answers notifications with plain OK and calls onCallback", func(t *testing.T) {
		provider := newHandlerTestProvider("paytr")
		provider.respond = func(core.PaymentOperation, any) (any, error) {
			return &core.PaymentResponse{Status: core.StatusFailure, PaymentID: "O1", ErrorMessage: "declined"}, nil
		}
		var result *core.PaymentResponse
		var cctx core.CallbackContext
		options := core.HandlerOptions{
			OnCallback: func(_ context.Context, value *core.PaymentResponse, ctx core.CallbackContext) error {
				result, cctx = value, ctx
				return nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"paytr": &handlerTestPayTR{provider}}, &options)
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/paytr/callback", map[string]any{"merchant_oid": "O1"}, nil))
		handlerTestWantStatus(t, response, http.StatusOK)
		if response.Headers["Content-Type"] != "text/plain" || response.Body != "OK" {
			t.Fatalf("response = %+v", response)
		}
		if result == nil || result.Status != core.StatusFailure || result.PaymentID != "O1" {
			t.Fatalf("onCallback result = %+v", result)
		}
		if cctx.Provider != "paytr" || cctx.Action != core.ActionCallback {
			t.Fatalf("onCallback context = %+v", cctx)
		}
	})

	t.Run("does not acknowledge a bad hash and skips onCallback", func(t *testing.T) {
		provider := newHandlerTestProvider("paytr")
		provider.respond = func(core.PaymentOperation, any) (any, error) {
			return &core.PaymentResponse{Status: core.StatusFailure, ErrorCode: "INVALID_HASH"}, nil
		}
		called := false
		options := core.HandlerOptions{
			OnCallback: func(context.Context, *core.PaymentResponse, core.CallbackContext) error {
				called = true
				return nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"paytr": &handlerTestPayTR{provider}}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/paytr/callback", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusBadRequest)
		if response.Body == "OK" {
			t.Fatalf("forged callback acknowledged: %v", response.Body)
		}
		if called {
			t.Fatal("onCallback ran for a forged callback")
		}
	})

	t.Run("returns 500 so the provider retries when onCallback fails", func(t *testing.T) {
		provider := newHandlerTestProvider("paytr")
		options := core.HandlerOptions{
			OnCallback: func(context.Context, *core.PaymentResponse, core.CallbackContext) error {
				return fmt.Errorf("db down")
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"paytr": &handlerTestPayTR{provider}}, &options)
		response := handlerTestHandle(t, payment.Handler(),
			handlerTestRequest(http.MethodPost, "/api/pay/paytr/callback", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusInternalServerError)
	})

	t.Run("redirects browser callbacks when callbackRedirect returns a URL", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := core.HandlerOptions{
			CallbackRedirect: func(_ context.Context, result *core.PaymentResponse, _ core.CallbackContext) (string, error) {
				return "https://shop.example/orders/" + result.PaymentID, nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/payment/complete-3ds", map[string]any{}, nil))
		handlerTestWantStatus(t, response, http.StatusSeeOther)
		if response.Headers["Location"] != "https://shop.example/orders/PMT-1" {
			t.Fatalf("headers = %v", response.Headers)
		}
	})

	t.Run("does not run transformRequest for callbacks", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		transformed := false
		options := core.HandlerOptions{
			TransformRequest: func(context.Context, core.HandlerContext) (map[string]any, error) {
				transformed = true
				return nil, nil
			},
		}
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/payment/complete-3ds", map[string]any{"a": "1"}, nil))
		if transformed {
			t.Fatal("transformRequest ran for a callback")
		}
		requests := provider.requests(core.OperationCompleteThreeDSPayment)
		if len(requests) != 1 || !reflect.DeepEqual(requests[0], map[string]any{"a": "1"}) {
			t.Fatalf("completeThreeDSPayment requests = %#v", requests)
		}
	})

	t.Run("CallbackHandlerActions lists the callback routes", func(t *testing.T) {
		want := []core.HandlerAction{core.ActionPaymentComplete3DS, core.ActionCallback}
		if !reflect.DeepEqual(core.CallbackHandlerActions, want) {
			t.Fatalf("CallbackHandlerActions = %v, want %v", core.CallbackHandlerActions, want)
		}
	})
}

func TestHandlerProviderSpecificRoutes(t *testing.T) {
	t.Run("iyzico-only routes return 400 for other providers", func(t *testing.T) {
		provider := newHandlerTestProvider("paytr")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"paytr": &handlerTestPayTR{provider}}, &options)
		for _, path := range []string{"checkout/init", "subscription/initialize"} {
			handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
				"/api/pay/paytr/"+path, map[string]any{}, nil)), http.StatusBadRequest)
		}
	})

	routes := []struct{ path, method string }{
		{"checkout/init", "initCheckoutForm"},
		{"checkout/retrieve", "retrieveCheckoutForm"},
		{"pwi/init", "initPWIPayment"},
		{"pwi/retrieve", "retrievePWIPayment"},
		{"subscription/initialize", "initializeSubscription"},
		{"subscription/cancel", "cancelSubscription"},
		{"subscription/upgrade", "upgradeSubscription"},
		{"subscription/retrieve", "retrieveSubscription"},
		{"subscription/card-update", "updateSubscriptionCard"},
		{"subscription/product", "createSubscriptionProduct"},
		{"subscription/pricing-plan", "createPricingPlan"},
	}
	for _, route := range routes {
		t.Run(route.path+" calls "+route.method, func(t *testing.T) {
			provider := &handlerTestIyzico{newHandlerTestProvider("iyzico")}
			options := handlerTestAllowAll()
			payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
			response := handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
				"/api/pay/iyzico/"+route.path, map[string]any{"token": "t", "conversationId": "c"}, nil))
			handlerTestWantStatus(t, response, http.StatusOK)
			if got := provider.count(core.PaymentOperation(route.method)); got != 1 {
				t.Fatalf("%s calls = %d, want 1", route.method, got)
			}
		})
	}

	t.Run("token payments require provider support", func(t *testing.T) {
		provider := newHandlerTestProvider("iyzico")
		options := handlerTestAllowAll()
		payment := handlerTestFacade(t, map[string]core.ProviderEntry{"iyzico": provider}, &options)
		handlerTestWantStatus(t, handlerTestHandle(t, payment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/iyzico/payment/token", map[string]any{}, nil)), http.StatusBadRequest)

		withToken := &handlerTestToken{newHandlerTestProvider("paytr")}
		tokenPayment := handlerTestFacade(t, map[string]core.ProviderEntry{"paytr": withToken}, &options)
		response := handlerTestHandle(t, tokenPayment.Handler(), handlerTestRequest(http.MethodPost,
			"/api/pay/paytr/payment/token", map[string]any{"utoken": "u"}, nil))
		handlerTestWantStatus(t, response, http.StatusOK)
		requests := withToken.requests("createPaymentWithToken")
		if len(requests) != 1 || !reflect.DeepEqual(requests[0], map[string]any{"utoken": "u"}) {
			t.Fatalf("createPaymentWithToken requests = %#v", requests)
		}
	})
}

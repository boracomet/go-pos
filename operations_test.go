package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
	postesting "github.com/boracomet/go-pos/testing"
)

// ---- helpers -------------------------------------------------------------

// operationsTestWithCard copies a request and charges another card.
func operationsTestWithCard(request *core.PaymentRequest, number string) *core.PaymentRequest {
	out := *request
	card := *request.PaymentCard
	card.CardNumber = number
	out.PaymentCard = &card
	return &out
}

// operationsTestThreeDS returns a valid 3D Secure request.
func operationsTestThreeDS() *core.ThreeDSPaymentRequest {
	request := postesting.FixtureThreeDSPaymentRequest()
	return &request
}

// operationsTestRefund builds a refund of a payment.
func operationsTestRefund(paymentID, price string) *core.RefundRequest {
	return &core.RefundRequest{
		PaymentID: paymentID,
		Price:     price,
		Currency:  string(core.CurrencyTRY),
		IP:        "85.34.78.112",
	}
}

// operationsTestBool is a pointer to a bool, for core.RunOptions.Emit.
func operationsTestBool(value bool) *bool { return &value }

// operationsTestCall is one operation of the unified API.
type operationsTestCall struct {
	name      string
	operation core.PaymentOperation
	run       func(*Payment) (any, error)
}

// operationsTestCalls lists every operation of the payment object. The ids are
// placeholders: a call that never reaches a provider does not need real ones.
func operationsTestCalls(ctx context.Context, paidID, authID, customerToken, cardToken string, callback any) []operationsTestCall {
	return []operationsTestCall{
		{"createPayment", core.OperationCreatePayment, func(p *Payment) (any, error) {
			return p.CreatePayment(ctx, posTestRequest())
		}},
		{"initThreeDSPayment", core.OperationInitThreeDSPayment, func(p *Payment) (any, error) {
			return p.InitThreeDSPayment(ctx, operationsTestThreeDS())
		}},
		{"completeThreeDSPayment", core.OperationCompleteThreeDSPayment, func(p *Payment) (any, error) {
			return p.CompleteThreeDSPayment(ctx, callback)
		}},
		{"authorize", core.OperationAuthorize, func(p *Payment) (any, error) {
			return p.Authorize(ctx, posTestRequest())
		}},
		{"initThreeDSAuthorize", core.OperationInitThreeDSAuthorize, func(p *Payment) (any, error) {
			return p.InitThreeDSAuthorize(ctx, operationsTestThreeDS())
		}},
		{"capture", core.OperationCapture, func(p *Payment) (any, error) {
			return p.Capture(ctx, &core.CaptureRequest{PaymentID: authID, Amount: "1.2", IP: "85.34.78.112"})
		}},
		{"voidAuthorization", core.OperationVoidAuthorization, func(p *Payment) (any, error) {
			return p.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: authID, IP: "85.34.78.112"})
		}},
		{"refund", core.OperationRefund, func(p *Payment) (any, error) {
			return p.Refund(ctx, operationsTestRefund(paidID, "0.5"))
		}},
		{"cancel", core.OperationCancel, func(p *Payment) (any, error) {
			return p.Cancel(ctx, &core.CancelRequest{PaymentID: paidID, IP: "85.34.78.112"})
		}},
		{"saveCard", core.OperationSaveCard, func(p *Payment) (any, error) {
			return p.SaveCard(ctx, &core.SaveCardRequest{Card: postesting.FixtureCard()})
		}},
		{"listCards", core.OperationListCards, func(p *Payment) (any, error) {
			return p.ListCards(ctx, &core.ListCardsRequest{CustomerToken: customerToken})
		}},
		{"deleteCard", core.OperationDeleteCard, func(p *Payment) (any, error) {
			return p.DeleteCard(ctx, &core.DeleteCardRequest{CustomerToken: customerToken, CardToken: cardToken})
		}},
		{"getPayment", core.OperationGetPayment, func(p *Payment) (any, error) {
			return p.GetPayment(ctx, paidID)
		}},
		{"binCheck", core.OperationBinCheck, func(p *Payment) (any, error) {
			return p.BinCheck(ctx, "552879")
		}},
		{"installmentInfo", core.OperationInstallmentInfo, func(p *Payment) (any, error) {
			return p.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "552879", Price: "1"})
		}},
	}
}

// operationsTestRecorder collects the operation contexts a hook sees.
type operationsTestRecorder struct {
	mu     sync.Mutex
	before []core.OperationContext
	after  []core.OperationResultContext
}

func (r *operationsTestRecorder) recordBefore(octx core.OperationContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before = append(r.before, octx)
}

func (r *operationsTestRecorder) recordAfter(octx core.OperationResultContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.after = append(r.after, octx)
}

func (r *operationsTestRecorder) operations() []core.PaymentOperation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]core.PaymentOperation, 0, len(r.before))
	for _, octx := range r.before {
		out = append(out, octx.Operation)
	}
	return out
}

// operationsTestPlugin wraps a recorder in a plugin that only observes.
func operationsTestPlugin(id string, recorder *operationsTestRecorder) core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID: id,
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{Handler: func(octx core.OperationContext) (*core.BeforeHookResult, error) {
				recorder.recordBefore(octx)
				return nil, nil
			}}},
			After: []core.AfterHook{{Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
				recorder.recordAfter(octx)
				return nil, nil
			}}},
		},
	})
}

// operationsTestLogEntry is one logged message.
type operationsTestLogEntry struct {
	message string
	err     error
	fields  map[string]any
}

// operationsTestLogger records log calls with their fields.
type operationsTestLogger struct {
	mu      sync.Mutex
	entries []operationsTestLogEntry
}

func (l *operationsTestLogger) Debug(message string, fields map[string]any) {
	l.add(message, nil, fields)
}
func (l *operationsTestLogger) Info(message string, fields map[string]any) {
	l.add(message, nil, fields)
}
func (l *operationsTestLogger) Error(message string, err error, fields map[string]any) {
	l.add(message, err, fields)
}

func (l *operationsTestLogger) add(message string, err error, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, operationsTestLogEntry{message: message, err: err, fields: fields})
}

func (l *operationsTestLogger) find(message string) (operationsTestLogEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.message == message {
			return entry, true
		}
	}
	return operationsTestLogEntry{}, false
}

var _ core.Logger = (*operationsTestLogger)(nil)

// operationsTestPost builds a JSON handler request.
func operationsTestPost(url string, body any) *core.HandlerRequest {
	return &core.HandlerRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}
}

// operationsTestGet builds a handler request without a body.
func operationsTestGet(url string) *core.HandlerRequest {
	return &core.HandlerRequest{Method: http.MethodGet, URL: url, Headers: map[string]string{}}
}

// operationsTestHandle serves a request, failing the test on an unexpected error.
func operationsTestHandle(t *testing.T, handler *core.Handler, request *core.HandlerRequest) *core.HandlerResponse {
	t.Helper()
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle(%s %s): %v", request.Method, request.URL, err)
	}
	if response == nil {
		t.Fatalf("Handle(%s %s): no response", request.Method, request.URL)
	}
	return response
}

// operationsTestBody decodes a handler response body as a JSON object.
func operationsTestBody(t *testing.T, response *core.HandlerResponse) map[string]any {
	t.Helper()
	data, err := json.Marshal(response.Body)
	if err != nil {
		t.Fatalf("marshal the response body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("response body %v is not an object: %v", response.Body, err)
	}
	return out
}

// operationsTestWantStatus fails unless the response has the wanted status.
func operationsTestWantStatus(t *testing.T, response *core.HandlerResponse, want int) {
	t.Helper()
	if response.Status != want {
		t.Errorf("status = %d, want %d (body %v)", response.Status, want, response.Body)
	}
}

// ---- delegation ----------------------------------------------------------

func TestOperationsDelegateToTheDefaultProvider(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")
	mock := mocks["a"]

	// Payments the later operations need.
	paid, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	authorized, err := payment.Authorize(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	saved, err := payment.SaveCard(ctx, &core.SaveCardRequest{Card: postesting.FixtureCard()})
	if err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	init, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	callback, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatalf("ThreeDSCallback: %v", err)
	}

	recorder := &operationsTestRecorder{}
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mock},
		Plugins:   []core.Plugin{operationsTestPlugin("observe", recorder)},
	})

	calls := operationsTestCalls(ctx, paid.PaymentID, authorized.PaymentID, saved.CustomerToken, saved.Card.CardToken, callback)
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			recorder.mu.Lock()
			recorder.before = nil
			recorder.mu.Unlock()

			result, err := call.run(payment)
			if err != nil {
				t.Fatalf("%s: %v", call.name, err)
			}
			if result == nil {
				t.Fatalf("%s: no result", call.name)
			}
			seen := recorder.operations()
			if len(seen) != 1 || seen[0] != call.operation {
				t.Errorf("%s ran %v, want [%s]", call.name, seen, call.operation)
			}
			recorder.mu.Lock()
			provider := recorder.before[0].Provider
			routable := recorder.before[0].Routable
			recorder.mu.Unlock()
			if provider != "a" {
				t.Errorf("%s ran on provider %q, want a", call.name, provider)
			}
			if !routable {
				t.Errorf("%s must be routable: a before hook may move it", call.name)
			}
		})
	}
}

func TestOperationsWithoutADefaultProvider(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a", "b")
	if payment.DefaultProvider() != "" {
		t.Fatalf("DefaultProvider() = %q, want none", payment.DefaultProvider())
	}
	for _, call := range operationsTestCalls(ctx, "P1", "P2", "C1", "T1", nil) {
		t.Run(call.name, func(t *testing.T) {
			_, err := call.run(payment)
			posTestWantConfigurationError(t, err, "No default provider set")
		})
	}
}

func TestOperationsUseTheExplicitDefaultProvider(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{
			"a": mocks["a"],
			"b": mocks["b"],
		},
		DefaultProvider: "b",
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if got := len(mocks["a"].Payments(ctx)); got != 0 {
		t.Errorf("provider a holds %d payments, want 0", got)
	}
	if got := len(mocks["b"].Payments(ctx)); got != 1 {
		t.Errorf("provider b holds %d payments, want 1", got)
	}
}

func TestUseRunsTheOperationOnThatProvider(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a", "b")
	provider, err := payment.Use("b")
	if err != nil {
		t.Fatalf("Use(b): %v", err)
	}
	result, err := provider.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if got := len(mocks["a"].Payments(ctx)); got != 0 {
		t.Errorf("provider a holds %d payments, want 0", got)
	}
	if got := len(mocks["b"].Payments(ctx)); got != 1 {
		t.Errorf("provider b holds %d payments, want 1", got)
	}
}

func TestUseRunsEveryOperation(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")
	mock := mocks["a"]
	paid, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	authorized, err := payment.Authorize(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	saved, err := payment.SaveCard(ctx, &core.SaveCardRequest{Card: postesting.FixtureCard()})
	if err != nil {
		t.Fatalf("SaveCard: %v", err)
	}
	init, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	callback, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatalf("ThreeDSCallback: %v", err)
	}

	recorder := &operationsTestRecorder{}
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mock},
		Plugins:   []core.Plugin{operationsTestPlugin("observe", recorder)},
	})
	provider, err := payment.Use("a")
	if err != nil {
		t.Fatalf("Use(a): %v", err)
	}

	calls := operationsTestCalls(ctx, paid.PaymentID, authorized.PaymentID, saved.CustomerToken, saved.Card.CardToken, callback)
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			recorder.mu.Lock()
			recorder.before = nil
			recorder.mu.Unlock()
			if _, err := call.run(payment); err != nil {
				t.Fatalf("payment.%s: %v", call.name, err)
			}
			// The same operation on the provider proxy must not be routable.
			if err := operationsTestRunOnProvider(ctx, provider, call, callback); err != nil {
				t.Fatalf("provider.%s: %v", call.name, err)
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if len(recorder.before) != 2 {
				t.Fatalf("%s ran %d times, want 2", call.name, len(recorder.before))
			}
			for _, octx := range recorder.before {
				if octx.Operation != call.operation {
					t.Errorf("operation = %q, want %q", octx.Operation, call.operation)
				}
				if octx.Provider != "a" {
					t.Errorf("provider = %q, want a", octx.Provider)
				}
				if octx.Routable {
					t.Errorf("%s on a provider must not be routable", call.name)
				}
			}
		})
	}
}

// operationsTestRunOnProvider repeats a call on a provider proxy.
func operationsTestRunOnProvider(ctx context.Context, provider core.Provider, call operationsTestCall, callback any) error {
	switch call.operation {
	case core.OperationCreatePayment:
		_, err := provider.CreatePayment(ctx, posTestRequest())
		return err
	case core.OperationInitThreeDSPayment:
		_, err := provider.InitThreeDSPayment(ctx, operationsTestThreeDS())
		return err
	case core.OperationCompleteThreeDSPayment:
		_, err := provider.CompleteThreeDSPayment(ctx, callback)
		return err
	case core.OperationAuthorize:
		_, err := provider.Authorize(ctx, posTestRequest())
		return err
	case core.OperationInitThreeDSAuthorize:
		_, err := provider.InitThreeDSAuthorize(ctx, operationsTestThreeDS())
		return err
	case core.OperationCapture:
		_, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "unknown", Amount: "1", IP: "1.1.1.1"})
		return err
	case core.OperationVoidAuthorization:
		_, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: "unknown", IP: "1.1.1.1"})
		return err
	case core.OperationRefund:
		_, err := provider.Refund(ctx, operationsTestRefund("unknown", "0.5"))
		return err
	case core.OperationCancel:
		_, err := provider.Cancel(ctx, &core.CancelRequest{PaymentID: "unknown", IP: "1.1.1.1"})
		return err
	case core.OperationSaveCard:
		_, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: postesting.FixtureCard()})
		return err
	case core.OperationListCards:
		_, err := provider.ListCards(ctx, &core.ListCardsRequest{CustomerToken: "unknown"})
		return err
	case core.OperationDeleteCard:
		_, err := provider.DeleteCard(ctx, &core.DeleteCardRequest{CustomerToken: "unknown", CardToken: "unknown"})
		return err
	case core.OperationGetPayment:
		_, err := provider.GetPayment(ctx, "unknown")
		return err
	case core.OperationBinCheck:
		_, err := provider.BinCheck(ctx, "552879")
		return err
	case core.OperationInstallmentInfo:
		_, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "552879", Price: "1"})
		return err
	}
	return fmt.Errorf("unhandled operation %s", call.operation)
}

// ---- Run -----------------------------------------------------------------

func TestRunRejectsAnUnknownOperation(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	_, err := payment.Run(context.Background(), core.PaymentOperation("teleport"), "a", nil, core.RunOptions{})
	if err == nil {
		t.Fatal("Run with an unknown operation: want an error")
	}
	var base *core.BaseError
	if !errors.As(err, &base) {
		t.Fatalf("error = %T, want a library error", err)
	}
	if base.Code != "INVALID_REQUEST" {
		t.Errorf("Code = %q, want INVALID_REQUEST", base.Code)
	}
	if !strings.Contains(err.Error(), "teleport") {
		t.Errorf("error = %q, want it to name the operation", err)
	}
}

func TestRunAcceptsAParsedBody(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")

	data, err := json.Marshal(posTestRequest())
	if err != nil {
		t.Fatalf("marshal the request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("parse the request: %v", err)
	}
	body["conversationId"] = "PARSED-1"

	result, err := payment.Run(ctx, core.OperationCreatePayment, "a", body, core.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	paid, ok := result.(*core.PaymentResponse)
	if !ok {
		t.Fatalf("result = %T, want *core.PaymentResponse", result)
	}
	if paid.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", paid.Status)
	}
	if paid.ConversationID != "PARSED-1" {
		t.Errorf("conversationId = %q, want PARSED-1", paid.ConversationID)
	}
	if got := len(mocks["a"].Payments(ctx)); got != 1 {
		t.Errorf("the provider holds %d payments, want 1", got)
	}
}

func TestRunAcceptsAValueRequest(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	result, err := payment.Run(context.Background(), core.OperationCreatePayment, "a", *posTestRequest(), core.RunOptions{})
	if err != nil {
		t.Fatalf("Run with a value request: %v", err)
	}
	if _, ok := result.(*core.PaymentResponse); !ok {
		t.Errorf("result = %T, want *core.PaymentResponse", result)
	}
}

func TestRunRejectsAnUnusableRequest(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	_, err := payment.Run(context.Background(), core.OperationCreatePayment, "a", make(chan int), core.RunOptions{})
	if err == nil {
		t.Fatal("Run with an unserializable request: want an error")
	}
}

func TestRunRejectsAMismatchedRequestBody(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	_, err := payment.Run(context.Background(), core.OperationCreatePayment, "a", []string{"not", "a", "request"}, core.RunOptions{})
	if err == nil {
		t.Fatal("Run with a request of the wrong shape: want an error")
	}
	if !strings.Contains(err.Error(), "INVALID_REQUEST") && !strings.Contains(err.Error(), "Invalid") {
		t.Errorf("error = %q, want an invalid-request error", err)
	}
}

func TestRunStringOperationsIgnoreANonStringRequest(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	// GetPayment takes an id: anything else becomes the empty string.
	result, err := payment.Run(ctx, core.OperationGetPayment, "a", 42, core.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	response, ok := result.(*core.PaymentResponse)
	if !ok {
		t.Fatalf("result = %T, want *core.PaymentResponse", result)
	}
	if response.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure for an unknown payment", response.Status)
	}
	if response.PaymentID != "" {
		t.Errorf("paymentId = %q, want the empty string", response.PaymentID)
	}
}

func TestRunCanSkipEvents(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	var events int
	payment.On(core.EventAll, func(context.Context, *core.PaymentEvent) error {
		events++
		return nil
	})
	if _, err := payment.Run(ctx, core.OperationCreatePayment, "a", posTestRequest(), core.RunOptions{
		Emit: operationsTestBool(false),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if events != 0 {
		t.Errorf("emitted %d events, want 0", events)
	}
	if _, err := payment.Run(ctx, core.OperationCreatePayment, "a", posTestRequest(), core.RunOptions{
		Emit: operationsTestBool(true),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if events != 1 {
		t.Errorf("emitted %d events, want 1", events)
	}
}

func TestRunConvertsAReplacementResult(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		replacing any
		wantID    string
		wantError string
	}{
		{"pointer", &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "P-POINTER"}, "P-POINTER", ""},
		{"value", core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "P-VALUE"}, "P-VALUE", ""},
		{"object", map[string]any{"status": "success", "paymentId": "P-OBJECT"}, "P-OBJECT", ""},
		{"json", `{"status":"success","paymentId":"P-JSON"}`, "", "Unexpected result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replacement := tc.replacing
			payment := posTestNew(t, Config{
				Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
				Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
					ID: "replace",
					Hooks: core.PluginHooks{After: []core.AfterHook{{
						Handler: func(core.OperationResultContext) (*core.AfterHookResult, error) {
							return &core.AfterHookResult{Result: replacement}, nil
						},
					}}},
				})},
			})
			result, err := payment.CreatePayment(ctx, posTestRequest())
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantError)
				}
				var base *core.BaseError
				if !errors.As(err, &base) || base.Code != "PROVIDER_ERROR" {
					t.Errorf("error = %v, want a PROVIDER_ERROR", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreatePayment: %v", err)
			}
			if result.PaymentID != tc.wantID {
				t.Errorf("paymentId = %q, want %q", result.PaymentID, tc.wantID)
			}
		})
	}
}

// ---- before hooks --------------------------------------------------------

func TestBeforeHookRoutesTheCall(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"], "b": mocks["b"]},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "router",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Matcher: func(octx core.OperationContext) bool {
					return octx.Operation == core.OperationCreatePayment
				},
				Handler: func(octx core.OperationContext) (*core.BeforeHookResult, error) {
					if !octx.Routable {
						return nil, nil
					}
					return &core.BeforeHookResult{Provider: "b"}, nil
				},
			}}},
		})},
		DefaultProvider: "a",
	})

	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	provider, err := payment.Use("a")
	if err != nil {
		t.Fatalf("Use(a): %v", err)
	}
	if _, err := provider.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("provider.CreatePayment: %v", err)
	}
	if got := len(mocks["b"].Payments(ctx)); got != 1 {
		t.Errorf("provider b holds %d payments, want 1 (the routed call)", got)
	}
	if got := len(mocks["a"].Payments(ctx)); got != 1 {
		t.Errorf("provider a holds %d payments, want 1 (the explicit call)", got)
	}
}

func TestBeforeHookChoosesAProviderWithoutADefault(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"], "b": mocks["b"]},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "pick",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
					return &core.BeforeHookResult{Provider: "b"}, nil
				},
			}}},
		})},
	})
	result, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment without a default provider: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if got := len(mocks["b"].Payments(ctx)); got != 1 {
		t.Errorf("provider b holds %d payments, want 1", got)
	}
}

func TestBeforeHookCannotMoveACallMadeOnAProvider(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"], "b": mocks["b"]},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "pick",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
					return &core.BeforeHookResult{Provider: "b"}, nil
				},
			}}},
		})},
	})
	provider, err := payment.Use("a")
	if err != nil {
		t.Fatalf("Use(a): %v", err)
	}
	_, err = provider.CreatePayment(ctx, posTestRequest())
	posTestWantConfigurationError(t, err, "Only calls made on the payment object")
	if got := len(mocks["b"].Payments(ctx)); got != 0 {
		t.Errorf("provider b holds %d payments, want 0", got)
	}
}

func TestBeforeHookCannotRouteToAnUnknownProvider(t *testing.T) {
	payment, _ := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{
			"a": postesting.NewMockProvider(),
			"b": postesting.NewMockProvider(),
		},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "pick",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
					return &core.BeforeHookResult{Provider: "c"}, nil
				},
			}}},
		})},
	})
	_, err := payment.CreatePayment(context.Background(), posTestRequest())
	var notEnabled *core.ProviderNotEnabledError
	if !errors.As(err, &notEnabled) {
		t.Fatalf("error = %T (%v), want a ProviderNotEnabledError", err, err)
	}
	if notEnabled.Provider != "c" {
		t.Errorf("Provider = %q, want c", notEnabled.Provider)
	}
}

func TestBeforeHookReplacesTheRequest(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "order",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Matcher: func(octx core.OperationContext) bool {
					return octx.Operation == core.OperationCreatePayment
				},
				Handler: func(octx core.OperationContext) (*core.BeforeHookResult, error) {
					request, ok := octx.Request.(*core.PaymentRequest)
					if !ok {
						return nil, fmt.Errorf("request = %T, want *core.PaymentRequest", octx.Request)
					}
					replaced := *request
					replaced.ConversationID = "ORDER-9"
					return &core.BeforeHookResult{Request: &replaced}, nil
				},
			}}},
		})},
	})
	request := posTestRequest()
	paid, err := payment.CreatePayment(ctx, request)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if paid.ConversationID != "ORDER-9" {
		t.Errorf("conversationId = %q, want ORDER-9", paid.ConversationID)
	}
	if request.ConversationID == "ORDER-9" {
		t.Error("the hook must not mutate the caller's request")
	}
}

func TestBeforeHookResultSkipsTheProviderAndTheLaterHooks(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")
	mock := mocks["a"]
	paid, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	var after []string
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mock},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "guard",
			Hooks: core.PluginHooks{
				Before: []core.BeforeHook{
					{
						Matcher: func(octx core.OperationContext) bool {
							return octx.Operation == core.OperationRefund
						},
						Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
							blocked := &core.RefundResponse{Status: core.StatusFailure}
							blocked.Code = core.CodeInvalidRequest
							return &core.BeforeHookResult{Result: blocked}, nil
						},
					},
					{
						Matcher: func(octx core.OperationContext) bool {
							return octx.Operation == core.OperationRefund
						},
						Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
							after = append(after, "skipped")
							return nil, nil
						},
					},
				},
				After: []core.AfterHook{{Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
					after = append(after, string(octx.Operation)+":"+octx.Provider)
					return nil, nil
				}}},
			},
		})},
	})

	refunded, err := payment.Refund(ctx, operationsTestRefund(paid.PaymentID, "1"))
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if refunded.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", refunded.Status)
	}
	if refunded.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", refunded.Code, core.CodeInvalidRequest)
	}
	record := mock.GetRecord(ctx, paid.PaymentID)
	if record == nil {
		t.Fatal("the payment disappeared")
	}
	if len(record.Refunds) != 0 {
		t.Errorf("the provider refunded %d times, want 0", len(record.Refunds))
	}
	want := []string{"refund:a"}
	if !reflect.DeepEqual(after, want) {
		t.Errorf("after hooks ran %v, want %v", after, want)
	}
}

func TestBeforeHookErrorStopsTheCall(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")
	failure := errors.New("routing table unavailable")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"]},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "broken",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) { return nil, failure },
			}}},
		})},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); !errors.Is(err, failure) {
		t.Errorf("error = %v, want %v", err, failure)
	}
	if got := len(mocks["a"].Payments(ctx)); got != 0 {
		t.Errorf("the provider holds %d payments, want 0", got)
	}
}

func TestBeforeHookSeesTheOperationContext(t *testing.T) {
	ctx := context.Background()
	recorder := &operationsTestRecorder{}
	payment, _ := posTestMocks(t, "a")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{operationsTestPlugin("observe", recorder)},
	})
	request := posTestRequest()
	if _, err := payment.CreatePayment(ctx, request); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.before) != 1 {
		t.Fatalf("the before hook ran %d times, want 1", len(recorder.before))
	}
	octx := recorder.before[0]
	if octx.Operation != core.OperationCreatePayment {
		t.Errorf("Operation = %q, want createPayment", octx.Operation)
	}
	if octx.Provider != "a" {
		t.Errorf("Provider = %q, want a", octx.Provider)
	}
	if !octx.Routable {
		t.Error("Routable = false, want true for a call on the payment object")
	}
	if got, ok := octx.Request.(*core.PaymentRequest); !ok || got != request {
		t.Errorf("Request = %v, want the caller's request", octx.Request)
	}
	if len(recorder.after) != 1 {
		t.Fatalf("the after hook ran %d times, want 1", len(recorder.after))
	}
	if recorder.after[0].Result == nil {
		t.Error("the after hook got no result")
	}
}

func TestBeforeHooksRunInRegistrationOrder(t *testing.T) {
	ctx := context.Background()
	var order []string
	plugin := func(id string) core.Plugin {
		return core.DefinePlugin(core.Plugin{
			ID: id,
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
					order = append(order, id)
					return nil, nil
				},
			}}},
		})
	}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{plugin("one"), plugin("two"), plugin("three")},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(order, want) {
		t.Errorf("hooks ran %v, want %v", order, want)
	}
}

// ---- after hooks ---------------------------------------------------------

func TestAfterHookReplacesTheResult(t *testing.T) {
	ctx := context.Background()
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "translate",
			Hooks: core.PluginHooks{After: []core.AfterHook{{
				Matcher: func(octx core.OperationResultContext) bool {
					response, ok := octx.Result.(*core.PaymentResponse)
					return ok && response.Status == core.StatusFailure
				},
				Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
					response := octx.Result.(*core.PaymentResponse)
					translated := *response
					translated.ErrorMessage = "Kart reddedildi"
					return &core.AfterHookResult{Result: &translated}, nil
				},
			}}},
		})},
	})
	result, err := payment.CreatePayment(ctx, operationsTestWithCard(posTestRequest(), postesting.CardDeclined))
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.ErrorMessage != "Kart reddedildi" {
		t.Errorf("errorMessage = %q, want the translated message", result.ErrorMessage)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
}

func TestAfterHookReplacesTheRequestSeenByTheEvent(t *testing.T) {
	ctx := context.Background()
	var events []*core.PaymentEvent
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "tag",
			Hooks: core.PluginHooks{After: []core.AfterHook{{
				Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
					request, ok := octx.Request.(*core.PaymentRequest)
					if !ok {
						return nil, nil
					}
					tagged := *request
					tagged.ConversationID = "TAGGED"
					tagged.PaidPrice = "9.99"
					return &core.AfterHookResult{Request: &tagged}, nil
				},
			}}},
			Events: core.PaymentEventListeners{
				core.EventAll: func(_ context.Context, event *core.PaymentEvent) error {
					events = append(events, event)
					return nil
				},
			},
		})},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if events[0].Amount != "9.99" {
		t.Errorf("amount = %q, want 9.99 from the replaced request", events[0].Amount)
	}
	request, ok := events[0].Request.(*core.PaymentRequest)
	if !ok {
		t.Fatalf("event request = %T, want *core.PaymentRequest", events[0].Request)
	}
	if request.ConversationID != "TAGGED" {
		t.Error("the event carries the original request, want the replaced one")
	}
}

func TestAfterHookErrorStopsTheCall(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("audit log unavailable")
	payment, mocks := posTestMocks(t, "a")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"]},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "broken",
			Hooks: core.PluginHooks{After: []core.AfterHook{{
				Handler: func(core.OperationResultContext) (*core.AfterHookResult, error) { return nil, failure },
			}}},
		})},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); !errors.Is(err, failure) {
		t.Errorf("error = %v, want %v", err, failure)
	}
	// The provider already ran: the payment is not lost.
	if got := len(mocks["a"].Payments(ctx)); got != 1 {
		t.Errorf("the provider holds %d payments, want 1", got)
	}
}

func TestAfterHooksRunInRegistrationOrder(t *testing.T) {
	ctx := context.Background()
	var order []string
	plugin := func(id string) core.Plugin {
		return core.DefinePlugin(core.Plugin{
			ID: id,
			Hooks: core.PluginHooks{After: []core.AfterHook{{
				Handler: func(core.OperationResultContext) (*core.AfterHookResult, error) {
					order = append(order, id)
					return nil, nil
				},
			}}},
		})
	}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{plugin("one"), plugin("two")},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(order, want) {
		t.Errorf("hooks ran %v, want %v", order, want)
	}
}

func TestAfterHookChainsReplacements(t *testing.T) {
	ctx := context.Background()
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{
			core.DefinePlugin(core.Plugin{
				ID: "first",
				Hooks: core.PluginHooks{After: []core.AfterHook{{
					Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
						response := octx.Result.(*core.PaymentResponse)
						out := *response
						out.ConversationID = "FIRST"
						return &core.AfterHookResult{Result: &out}, nil
					},
				}}},
			}),
			core.DefinePlugin(core.Plugin{
				ID: "second",
				Hooks: core.PluginHooks{After: []core.AfterHook{{
					Handler: func(octx core.OperationResultContext) (*core.AfterHookResult, error) {
						response := octx.Result.(*core.PaymentResponse)
						if response.ConversationID != "FIRST" {
							return nil, fmt.Errorf("the second hook saw %q, want FIRST", response.ConversationID)
						}
						out := *response
						out.ConversationID = "SECOND"
						return &core.AfterHookResult{Result: &out}, nil
					},
				}}},
			}),
		},
	})
	result, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.ConversationID != "SECOND" {
		t.Errorf("conversationId = %q, want SECOND", result.ConversationID)
	}
}

// ---- plugin Init ---------------------------------------------------------

func TestPluginInitRunsOnceInOrder(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var calls []string
	init := func(name string) func(context.Context, core.PluginContext) error {
		return func(context.Context, core.PluginContext) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name)
			return nil
		}
	}
	payment, mocks := posTestMocks(t, "a", "b")
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": mocks["a"], "b": mocks["b"]},
		Plugins: []core.Plugin{
			core.DefinePlugin(core.Plugin{ID: "one", Init: init("one")}),
			core.DefinePlugin(core.Plugin{ID: "two", Init: init("two")}),
		},
		DefaultProvider: "a",
	})
	mu.Lock()
	started := len(calls)
	mu.Unlock()
	if started != 0 {
		t.Fatalf("Init ran %d times before the first operation, want 0", started)
	}

	provider, err := payment.Use("b")
	if err != nil {
		t.Fatalf("Use(b): %v", err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
			t.Errorf("CreatePayment: %v", err)
		}
	}()
	go func() {
		defer group.Done()
		if _, err := provider.CreatePayment(ctx, posTestRequest()); err != nil {
			t.Errorf("provider.CreatePayment: %v", err)
		}
	}()
	group.Wait()

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"one", "two"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Init ran %v, want %v", calls, want)
	}
}

func TestPluginInitRetriesAfterAFailure(t *testing.T) {
	ctx := context.Background()
	var first, second int
	failure := errors.New("rates unavailable")
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{
			core.DefinePlugin(core.Plugin{ID: "one", Init: func(context.Context, core.PluginContext) error {
				first++
				return nil
			}}),
			core.DefinePlugin(core.Plugin{ID: "two", Init: func(context.Context, core.PluginContext) error {
				second++
				if second == 1 {
					return failure
				}
				return nil
			}}),
		},
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); !errors.Is(err, failure) {
		t.Fatalf("first CreatePayment error = %v, want %v", err, failure)
	}
	result, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if first != 1 {
		t.Errorf("the first plugin ran Init %d times, want 1", first)
	}
	if second != 2 {
		t.Errorf("the failing plugin ran Init %d times, want 2", second)
	}
}

func TestPluginWithoutInitIsSkipped(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{core.DefinePlugin(core.Plugin{ID: "inert"})},
	})
	if _, err := payment.CreatePayment(context.Background(), posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
}

// ---- events --------------------------------------------------------------

func TestEventsForPaymentOutcomes(t *testing.T) {
	ctx := context.Background()
	var events []*core.PaymentEvent
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "log",
			Events: core.PaymentEventListeners{
				core.EventAll: func(_ context.Context, event *core.PaymentEvent) error {
					events = append(events, event)
					return nil
				},
			},
		})},
	})

	request := posTestRequest()
	request.ConversationID = "O-1"
	paid, err := payment.CreatePayment(ctx, request)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if _, err := payment.CreatePayment(ctx, operationsTestWithCard(request, postesting.CardInsufficientFunds)); err != nil {
		t.Fatalf("CreatePayment (declined): %v", err)
	}
	if _, err := payment.CreatePayment(ctx, operationsTestWithCard(request, postesting.CardNetworkError)); err != nil {
		t.Fatalf("CreatePayment (lost response): %v", err)
	}
	if _, err := payment.Refund(ctx, operationsTestRefund(paid.PaymentID, "1.00")); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if _, err := payment.Refund(ctx, operationsTestRefund(paid.PaymentID, "999.00")); err != nil {
		t.Fatalf("Refund (too much): %v", err)
	}
	authorized, err := payment.Authorize(ctx, request)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if _, err := payment.Capture(ctx, &core.CaptureRequest{
		PaymentID: authorized.PaymentID, Amount: request.PaidPrice, IP: "85.34.78.112",
	}); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	authorizedAgain, err := payment.Authorize(ctx, request)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if _, err := payment.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{
		PaymentID: authorizedAgain.PaymentID, IP: "85.34.78.112",
	}); err != nil {
		t.Fatalf("VoidAuthorization: %v", err)
	}
	if _, err := payment.GetPayment(ctx, paid.PaymentID); err != nil {
		t.Fatalf("GetPayment: %v", err)
	}

	want := [][2]string{
		{core.EventPaymentSucceeded, string(core.OperationCreatePayment)},
		{core.EventPaymentFailed, string(core.OperationCreatePayment)},
		{core.EventPaymentPending, string(core.OperationCreatePayment)},
		{core.EventRefundSucceeded, string(core.OperationRefund)},
		{core.EventRefundFailed, string(core.OperationRefund)},
		{core.EventPaymentAuthorized, string(core.OperationAuthorize)},
		{core.EventPaymentSucceeded, string(core.OperationCapture)},
		{core.EventPaymentAuthorized, string(core.OperationAuthorize)},
		{core.EventPaymentCancelled, string(core.OperationVoidAuthorization)},
	}
	if len(events) != len(want) {
		t.Fatalf("emitted %d events, want %d: %v", len(events), len(want), operationsTestEventTypes(events))
	}
	for i, pair := range want {
		if string(events[i].Type) != pair[0] || string(events[i].Operation) != pair[1] {
			t.Errorf("event %d = [%s %s], want %v", i, events[i].Type, events[i].Operation, pair)
		}
	}

	first := events[0]
	if first.Provider != "a" {
		t.Errorf("provider = %q, want a", first.Provider)
	}
	if first.PaymentID != paid.PaymentID {
		t.Errorf("paymentId = %q, want %q", first.PaymentID, paid.PaymentID)
	}
	if first.ConversationID != "O-1" {
		t.Errorf("conversationId = %q, want O-1", first.ConversationID)
	}
	if first.Amount != request.PaidPrice {
		t.Errorf("amount = %q, want %q", first.Amount, request.PaidPrice)
	}
	if first.Currency != request.Currency {
		t.Errorf("currency = %q, want %q", first.Currency, request.Currency)
	}
	if events[1].Code != core.CodeInsufficientFunds {
		t.Errorf("the declined event code = %q, want %q", events[1].Code, core.CodeInsufficientFunds)
	}
	if events[3].Amount != "1.00" {
		t.Errorf("the refund amount = %q, want 1.00", events[3].Amount)
	}
	if events[6].Amount != request.PaidPrice {
		t.Errorf("the capture amount = %q, want %q", events[6].Amount, request.PaidPrice)
	}
}

func operationsTestEventTypes(events []*core.PaymentEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event.Type))
	}
	return out
}

func TestEventsForThreeDSecure(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "a")
	mock := mocks["a"]
	var types []core.PaymentEventType
	payment.On(core.EventAll, func(_ context.Context, event *core.PaymentEvent) error {
		types = append(types, event.Type)
		return nil
	})

	init, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if init.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", init.Status)
	}
	callback, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatalf("ThreeDSCallback: %v", err)
	}
	forged := make(map[string]string, len(callback))
	for key, value := range callback {
		forged[key] = value
	}
	forged["signature"] = "x"

	rejected, err := payment.CompleteThreeDSPayment(ctx, forged)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment (forged): %v", err)
	}
	if rejected.ErrorCode != "INVALID_HASH" {
		t.Errorf("errorCode = %q, want INVALID_HASH", rejected.ErrorCode)
	}
	completed, err := payment.CompleteThreeDSPayment(ctx, callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if completed.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", completed.Status)
	}

	mock.FailNext(ctx, postesting.OperationThreeDSInit, "")
	failed, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
	if err != nil {
		t.Fatalf("InitThreeDSPayment (declined): %v", err)
	}
	if failed.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", failed.Status)
	}

	want := []core.PaymentEventType{core.EventPaymentSucceeded, core.EventPaymentFailed}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("events = %v, want %v", types, want)
	}
}

func TestEventsForCancelledPayments(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	var events []*core.PaymentEvent
	payment.On(core.EventAll, func(_ context.Context, event *core.PaymentEvent) error {
		events = append(events, event)
		return nil
	})
	paid, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	cancelled, err := payment.Cancel(ctx, &core.CancelRequest{PaymentID: paid.PaymentID, IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", cancelled.Status)
	}
	// A second cancel fails: only a successful payment without refunds can be cancelled.
	if _, err := payment.Cancel(ctx, &core.CancelRequest{PaymentID: paid.PaymentID, IP: "85.34.78.112"}); err != nil {
		t.Fatalf("Cancel (again): %v", err)
	}
	want := []core.PaymentEventType{core.EventPaymentSucceeded, core.EventPaymentCancelled}
	got := make([]core.PaymentEventType, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestEmitNotifiesTheListeners(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	var events []*core.PaymentEvent
	payment.On(core.EventPaymentSucceeded, func(_ context.Context, event *core.PaymentEvent) error {
		events = append(events, event)
		return nil
	})
	result := &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "P1"}
	if err := payment.Emit(ctx, core.OperationCreatePayment, "a", posTestRequest(), result); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if events[0].Result != any(result) {
		t.Error("the event must carry the result it was given")
	}
	// A query has no event.
	if err := payment.Emit(ctx, core.OperationGetPayment, "a", "P1", result); err != nil {
		t.Fatalf("Emit (query): %v", err)
	}
	if len(events) != 1 {
		t.Errorf("a query emitted %d events, want 0", len(events)-1)
	}
}

func TestPluginEventListenersAndOnRunTogether(t *testing.T) {
	ctx := context.Background()
	var order []string
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "plugin",
			Events: core.PaymentEventListeners{
				core.EventPaymentSucceeded: func(context.Context, *core.PaymentEvent) error {
					order = append(order, "plugin")
					return nil
				},
			},
		})},
	})
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		order = append(order, "typed")
		return nil
	})
	payment.On(core.EventAll, func(context.Context, *core.PaymentEvent) error {
		order = append(order, "wildcard")
		return nil
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if want := []string{"plugin", "typed", "wildcard"}; !reflect.DeepEqual(order, want) {
		t.Errorf("listeners ran %v, want %v", order, want)
	}
}

func TestAFailingListenerStopsTheRest(t *testing.T) {
	ctx := context.Background()
	payment, _ := posTestMocks(t, "a")
	failure := errors.New("database down")
	var reached bool
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error { return failure })
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		reached = true
		return nil
	})
	_, err := payment.CreatePayment(ctx, posTestRequest())
	var listenerErr *core.EventListenerError
	if !errors.As(err, &listenerErr) {
		t.Fatalf("error = %T (%v), want an EventListenerError", err, err)
	}
	if reached {
		t.Error("a listener after the failing one must not run")
	}
	if !errors.Is(err, failure) {
		t.Errorf("the error must wrap the listener's error, got %v", err)
	}
}

// ---- handler with plugins ------------------------------------------------

// operationsTestQuotePlugin serves three endpoints: a JSON one, one building its
// own response, and a privileged one.
func operationsTestQuotePlugin(admin *int) core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID: "router",
		Endpoints: map[string]core.PaymentEndpoint{
			"quote": {
				Method: http.MethodPost,
				Path:   "/router/quote",
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					return map[string]any{"providers": ectx.ProviderIDs(), "bin": ectx.Body()["binNumber"]}, nil
				},
			},
			"status": {
				Method: http.MethodGet,
				Path:   "router/status/",
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					return ectx.JSON(map[string]any{"q": ectx.Query().Get("q")}, http.StatusAccepted), nil
				},
			},
			"admin": {
				Method:     http.MethodPost,
				Path:       "/router/admin",
				Privileged: true,
				Handler: func(context.Context, core.EndpointContext) (any, error) {
					if admin != nil {
						*admin++
					}
					return "ok", nil
				},
			},
		},
	})
}

func TestHandlerServesPluginEndpoints(t *testing.T) {
	var adminCalls int
	var authorized []core.HandlerContext
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{operationsTestQuotePlugin(&adminCalls)},
		Handler: &core.HandlerOptions{
			Authorize: func(_ context.Context, hctx core.HandlerContext) (bool, error) {
				authorized = append(authorized, hctx)
				return hctx.Action != core.PluginEndpointAction("router", "admin"), nil
			},
		},
	})
	handler := payment.Handler()

	response := operationsTestHandle(t, handler, operationsTestPost("/api/pay/router/quote", map[string]any{"binNumber": "454360"}))
	operationsTestWantStatus(t, response, http.StatusOK)
	body := operationsTestBody(t, response)
	if body["bin"] != "454360" {
		t.Errorf("bin = %v, want 454360", body["bin"])
	}
	providers, ok := body["providers"].([]any)
	if !ok || len(providers) != 1 || providers[0] != "mock" {
		t.Errorf("providers = %v, want [mock]", body["providers"])
	}
	if len(authorized) == 0 {
		t.Fatal("the authorize hook did not run")
	}
	last := authorized[len(authorized)-1]
	if last.Action != core.PluginEndpointAction("router", "quote") {
		t.Errorf("action = %q, want plugin:router/quote", last.Action)
	}
	if last.Provider != "" {
		t.Errorf("provider = %q, want none for a plugin endpoint", last.Provider)
	}

	status := operationsTestHandle(t, handler, operationsTestGet("/api/pay/router/status?q=1"))
	operationsTestWantStatus(t, status, http.StatusAccepted)
	if got := operationsTestBody(t, status)["q"]; got != "1" {
		t.Errorf("q = %v, want 1", got)
	}

	wrongMethod := operationsTestHandle(t, handler, operationsTestGet("/api/pay/router/quote"))
	operationsTestWantStatus(t, wrongMethod, http.StatusMethodNotAllowed)

	forbidden := operationsTestHandle(t, handler, operationsTestPost("/api/pay/router/admin", map[string]any{}))
	operationsTestWantStatus(t, forbidden, http.StatusForbidden)
	if adminCalls != 0 {
		t.Errorf("the privileged endpoint ran %d times, want 0", adminCalls)
	}
}

func TestHandlerRejectsFormBodiesOnPostEndpoints(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{
			operationsTestQuotePlugin(nil),
			core.DefinePlugin(core.Plugin{
				ID: "bank",
				Endpoints: map[string]core.PaymentEndpoint{
					"callback": {
						Method:     http.MethodPost,
						Path:       "/bank/callback",
						AcceptForm: true,
						Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
							return map[string]any{"orderId": ectx.Body()["orderId"]}, nil
						},
					},
				},
			}),
		},
		Handler: &core.HandlerOptions{Authorize: func(context.Context, core.HandlerContext) (bool, error) { return true, nil }},
	})
	handler := payment.Handler()
	form := func(path, contentType string) *core.HandlerResponse {
		return operationsTestHandle(t, handler, &core.HandlerRequest{
			Method:  http.MethodPost,
			URL:     path,
			Headers: map[string]string{"Content-Type": contentType},
			Body:    "orderId=1&binNumber=454360",
		})
	}
	for _, contentType := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data",
		"text/plain",
	} {
		t.Run(contentType, func(t *testing.T) {
			operationsTestWantStatus(t, form("/api/pay/router/admin", contentType), http.StatusUnsupportedMediaType)
		})
	}
	accepted := form("/api/pay/bank/callback", "application/x-www-form-urlencoded")
	operationsTestWantStatus(t, accepted, http.StatusOK)
	if got := operationsTestBody(t, accepted)["orderId"]; got != "1" {
		t.Errorf("orderId = %v, want 1", got)
	}
}

func TestHandlerFailsClosedWhenOnRequestFails(t *testing.T) {
	var served int
	logger := &operationsTestLogger{}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Logger:    logger,
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "guard",
			OnRequest: func(context.Context, *core.HandlerRequest, core.RequestContext) (*core.HandlerResponse, error) {
				return nil, errors.New("store down")
			},
			Endpoints: map[string]core.PaymentEndpoint{
				"quote": {
					Method: http.MethodPost,
					Path:   "/guarded",
					Handler: func(context.Context, core.EndpointContext) (any, error) {
						served++
						return "ok", nil
					},
				},
			},
		})},
	})
	response := operationsTestHandle(t, payment.Handler(), operationsTestPost("/api/pay/guarded", map[string]any{}))
	operationsTestWantStatus(t, response, http.StatusServiceUnavailable)
	if served != 0 {
		t.Errorf("the endpoint ran %d times, want 0", served)
	}
	body := operationsTestBody(t, response)
	if body["error"] != true {
		t.Errorf("body = %v, want an error body", body)
	}
	entry, ok := logger.find("onRequest of plugin 'guard' failed")
	if !ok {
		t.Fatal("the hook failure was not logged")
	}
	if entry.err == nil || !strings.Contains(entry.err.Error(), "store down") {
		t.Errorf("logged error = %v, want the hook's error", entry.err)
	}
}

func TestHandlerOnRequestCanShortCircuit(t *testing.T) {
	var served int
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "limit",
			OnRequest: func(_ context.Context, _ *core.HandlerRequest, rctx core.RequestContext) (*core.HandlerResponse, error) {
				if rctx.Path != "/mock/payment" {
					return nil, nil
				}
				return &core.HandlerResponse{
					Status:  http.StatusTooManyRequests,
					Headers: map[string]string{"Content-Type": "application/json"},
					Body:    map[string]any{"error": true, "message": "Slow down"},
				}, nil
			},
			Endpoints: map[string]core.PaymentEndpoint{
				"quote": {
					Method: http.MethodPost,
					Path:   "/guarded",
					Handler: func(context.Context, core.EndpointContext) (any, error) {
						served++
						return "ok", nil
					},
				},
			},
		})},
		Handler: &core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPayment},
			Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
		},
	})
	handler := payment.Handler()
	limited := operationsTestHandle(t, handler, operationsTestPost("/api/pay/mock/payment", posTestRequest()))
	operationsTestWantStatus(t, limited, http.StatusTooManyRequests)

	// The health check is never intercepted.
	health := operationsTestHandle(t, handler, operationsTestGet("/api/pay/health"))
	operationsTestWantStatus(t, health, http.StatusOK)

	other := operationsTestHandle(t, handler, operationsTestPost("/api/pay/guarded", map[string]any{}))
	operationsTestWantStatus(t, other, http.StatusOK)
	if served != 1 {
		t.Errorf("the endpoint ran %d times, want 1", served)
	}
}

func TestHandlerRequiresAuthorizeForPrivilegedEndpoints(t *testing.T) {
	_, err := New(Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins:   []core.Plugin{operationsTestQuotePlugin(nil)},
	})
	posTestWantConfigurationError(t, err, "plugin:router/admin")
}

func TestHandlerRejectsReservedAndDuplicateEndpointPaths(t *testing.T) {
	cases := []struct {
		name     string
		plugins  []core.Plugin
		contains string
	}{
		{
			name: "a provider id",
			plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
				ID: "x",
				Endpoints: map[string]core.PaymentEndpoint{
					"e": {Method: http.MethodGet, Path: "/mock/x", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
				},
			})},
			contains: "reserved path",
		},
		{
			name: "the health check",
			plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
				ID: "x",
				Endpoints: map[string]core.PaymentEndpoint{
					"e": {Method: http.MethodGet, Path: "/health", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
				},
			})},
			contains: "reserved path",
		},
		{
			name: "two plugins on one path",
			plugins: []core.Plugin{
				core.DefinePlugin(core.Plugin{
					ID: "x",
					Endpoints: map[string]core.PaymentEndpoint{
						"e": {Method: http.MethodGet, Path: "/x", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
					},
				}),
				core.DefinePlugin(core.Plugin{
					ID: "y",
					Endpoints: map[string]core.PaymentEndpoint{
						"e": {Method: http.MethodGet, Path: "x", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
					},
				}),
			},
			contains: "Two plugin endpoints",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{
				Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
				Plugins:   tc.plugins,
			})
			posTestWantConfigurationError(t, err, tc.contains)
		})
	}
}

func TestHandlerRunsHooksAndEvents(t *testing.T) {
	var conversations []string
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "x",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(octx core.OperationContext) (*core.BeforeHookResult, error) {
					request, ok := octx.Request.(*core.PaymentRequest)
					if !ok {
						return nil, nil
					}
					replaced := *request
					replaced.ConversationID = "H-1"
					return &core.BeforeHookResult{Request: &replaced}, nil
				},
			}}},
			Events: core.PaymentEventListeners{
				core.EventPaymentSucceeded: func(_ context.Context, event *core.PaymentEvent) error {
					conversations = append(conversations, event.ConversationID)
					return nil
				},
			},
		})},
		Handler: &core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPayment},
			Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
		},
	})
	response := operationsTestHandle(t, payment.Handler(), operationsTestPost("/api/pay/mock/payment", posTestRequest()))
	operationsTestWantStatus(t, response, http.StatusOK)
	if want := []string{"H-1"}; !reflect.DeepEqual(conversations, want) {
		t.Errorf("the event carried %v, want %v", conversations, want)
	}
}

func TestHandlerKeepsTheResultWhenAListenerFails(t *testing.T) {
	logger := &operationsTestLogger{}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Logger:    logger,
		Handler: &core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPayment},
			Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
		},
	})
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		return errors.New("boom")
	})
	response := operationsTestHandle(t, payment.Handler(), operationsTestPost("/api/pay/mock/payment", posTestRequest()))
	operationsTestWantStatus(t, response, http.StatusOK)
	if got := operationsTestBody(t, response)["status"]; got != string(core.StatusSuccess) {
		t.Errorf("body status = %v, want success", got)
	}
	entry, ok := logger.find("Payment event listener failed")
	if !ok {
		t.Fatal("the listener failure was not logged")
	}
	var listenerErr *core.EventListenerError
	if !errors.As(entry.err, &listenerErr) {
		t.Errorf("logged error = %T, want an EventListenerError", entry.err)
	}
	if entry.fields["provider"] != "mock" || entry.fields["action"] != string(core.ActionPayment) {
		t.Errorf("logged fields = %v, want the provider and the action", entry.fields)
	}
}

func TestHandlerOnResponseHooksRunInOrder(t *testing.T) {
	logger := &operationsTestLogger{}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Logger:    logger,
		Plugins: []core.Plugin{
			core.DefinePlugin(core.Plugin{
				ID: "header",
				OnResponse: func(_ context.Context, response *core.HandlerResponse, _ core.ResponseContext) (*core.HandlerResponse, error) {
					out := *response
					out.Headers = map[string]string{}
					for key, value := range response.Headers {
						out.Headers[key] = value
					}
					out.Headers["X-A"] = "1"
					return &out, nil
				},
			}),
			core.DefinePlugin(core.Plugin{
				ID: "broken",
				OnResponse: func(context.Context, *core.HandlerResponse, core.ResponseContext) (*core.HandlerResponse, error) {
					return nil, errors.New("boom")
				},
			}),
			core.DefinePlugin(core.Plugin{
				ID: "noop",
				OnResponse: func(context.Context, *core.HandlerResponse, core.ResponseContext) (*core.HandlerResponse, error) {
					return nil, nil
				},
			}),
			core.DefinePlugin(core.Plugin{
				ID: "last",
				OnResponse: func(_ context.Context, response *core.HandlerResponse, _ core.ResponseContext) (*core.HandlerResponse, error) {
					if response.Headers["X-A"] != "1" {
						return nil, fmt.Errorf("the last hook saw %v, want the first hook's header", response.Headers)
					}
					out := *response
					out.Headers = map[string]string{"X-A": "1", "X-B": "2"}
					return &out, nil
				},
			}),
		},
	})
	response := operationsTestHandle(t, payment.Handler(), operationsTestGet("/api/pay/health"))
	operationsTestWantStatus(t, response, http.StatusOK)
	if response.Headers["X-A"] != "1" || response.Headers["X-B"] != "2" {
		t.Errorf("headers = %v, want X-A and X-B", response.Headers)
	}
	entry, ok := logger.find("onResponse of plugin 'broken' failed")
	if !ok {
		t.Fatal("the failing hook was not logged")
	}
	if entry.err == nil || !strings.Contains(entry.err.Error(), "boom") {
		t.Errorf("logged error = %v, want the hook's error", entry.err)
	}
}

func TestHandlerOnResponseSeesTheRequest(t *testing.T) {
	var seen *core.HandlerRequest
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "observe",
			OnResponse: func(_ context.Context, response *core.HandlerResponse, rctx core.ResponseContext) (*core.HandlerResponse, error) {
				seen = rctx.Request
				return response, nil
			},
		})},
	})
	request := operationsTestGet("/api/pay/health")
	operationsTestHandle(t, payment.Handler(), request)
	if seen != request {
		t.Error("OnResponse must receive the incoming request")
	}
}

func TestHandlerRetriesCallbackEventsWithTheStoredResult(t *testing.T) {
	ctx := context.Background()
	payment, mocks := posTestMocks(t, "mock")
	mock := mocks["mock"]

	var completions int
	var mu sync.Mutex
	fail := true
	var seen []string
	payment = posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": mock},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "count",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Matcher: func(octx core.OperationContext) bool {
					return octx.Operation == core.OperationCompleteThreeDSPayment
				},
				Handler: func(core.OperationContext) (*core.BeforeHookResult, error) {
					mu.Lock()
					defer mu.Unlock()
					completions++
					return nil, nil
				},
			}}},
		})},
	})
	payment.On(core.EventPaymentSucceeded, func(_ context.Context, event *core.PaymentEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return errors.New("database down")
		}
		seen = append(seen, event.PaymentID)
		return nil
	})

	init, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	callback, err := mock.ThreeDSCallback(ctx, init.PaymentID)
	if err != nil {
		t.Fatalf("ThreeDSCallback: %v", err)
	}
	body := make(map[string]any, len(callback))
	for key, value := range callback {
		body[key] = value
	}
	handler := payment.Handler()
	first := operationsTestHandle(t, handler, operationsTestPost("/api/pay/mock/payment/complete-3ds", body))
	operationsTestWantStatus(t, first, http.StatusInternalServerError)

	mu.Lock()
	fail = false
	mu.Unlock()
	second := operationsTestHandle(t, handler, operationsTestPost("/api/pay/mock/payment/complete-3ds", body))
	operationsTestWantStatus(t, second, http.StatusOK)

	mu.Lock()
	defer mu.Unlock()
	if want := []string{init.PaymentID}; !reflect.DeepEqual(seen, want) {
		t.Errorf("the listener saw %v, want %v", seen, want)
	}
	if completions != 1 {
		t.Errorf("the provider completed the payment %d times, want 1", completions)
	}
}

func TestInterceptRequestAndRespondAreCallableDirectly(t *testing.T) {
	ctx := context.Background()
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "guard",
			OnRequest: func(context.Context, *core.HandlerRequest, core.RequestContext) (*core.HandlerResponse, error) {
				return nil, nil
			},
			OnResponse: func(_ context.Context, response *core.HandlerResponse, _ core.ResponseContext) (*core.HandlerResponse, error) {
				out := *response
				out.Status = http.StatusTeapot
				return &out, nil
			},
		})},
	})
	request := operationsTestGet("/api/pay/health")
	intercepted, err := payment.InterceptRequest(ctx, request, core.RequestContext{Path: "/health"})
	if err != nil {
		t.Fatalf("InterceptRequest: %v", err)
	}
	if intercepted != nil {
		t.Errorf("InterceptRequest = %v, want nil to continue routing", intercepted)
	}
	response, err := payment.Respond(ctx, &core.HandlerResponse{Status: http.StatusOK}, core.ResponseContext{Request: request})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if response.Status != http.StatusTeapot {
		t.Errorf("status = %d, want the replacement", response.Status)
	}
}

func TestInterceptRequestWithoutHooks(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	response, err := payment.InterceptRequest(context.Background(), operationsTestGet("/api/pay/health"), core.RequestContext{Path: "/health"})
	if err != nil || response != nil {
		t.Errorf("InterceptRequest = (%v, %v), want (nil, nil)", response, err)
	}
	original := &core.HandlerResponse{Status: http.StatusOK}
	got, err := payment.Respond(context.Background(), original, core.ResponseContext{})
	if err != nil || got != original {
		t.Errorf("Respond = (%v, %v), want the response unchanged", got, err)
	}
}

func TestEndpointsAreListedInRegistrationOrder(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Plugins: []core.Plugin{
			core.DefinePlugin(core.Plugin{
				ID: "one",
				Endpoints: map[string]core.PaymentEndpoint{
					"b": {Method: http.MethodGet, Path: "/one/b", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
					"a": {Method: http.MethodGet, Path: "/one/a", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
				},
			}),
			core.DefinePlugin(core.Plugin{
				ID: "two",
				Endpoints: map[string]core.PaymentEndpoint{
					"c": {Method: http.MethodGet, Path: "/two/c", Handler: func(context.Context, core.EndpointContext) (any, error) { return 1, nil }},
				},
			}),
		},
	})
	endpoints := payment.Endpoints()
	want := []string{"one/a", "one/b", "two/c"}
	if len(endpoints) != len(want) {
		t.Fatalf("Endpoints() returned %d endpoints, want %d", len(endpoints), len(want))
	}
	for i, endpoint := range endpoints {
		got := endpoint.PluginID + "/" + endpoint.Name
		if got != want[i] {
			t.Errorf("endpoint %d = %q, want %q", i, got, want[i])
		}
	}
	// The returned slice is a copy.
	endpoints[0].Name = "changed"
	if payment.Endpoints()[0].Name != "a" {
		t.Error("Endpoints() must return a copy")
	}

	infos := payment.PluginContext().Endpoints()
	if len(infos) != len(want) {
		t.Fatalf("PluginContext().Endpoints() returned %d, want %d", len(infos), len(want))
	}
	for i, info := range infos {
		if info.PluginID+"/"+info.Name != want[i] {
			t.Errorf("info %d = %q, want %q", i, info.PluginID+"/"+info.Name, want[i])
		}
	}
}

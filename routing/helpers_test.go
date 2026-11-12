package routing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// fakeContext is the payment facade as the plugin sees it.
type fakeContext struct {
	ids             []string
	defaultProvider string
	logger          core.Logger
	providers       map[string]core.Provider
}

func (c *fakeContext) ProviderIDs() []string                          { return c.ids }
func (c *fakeContext) ErrorCodes() map[string]string                  { return map[string]string{} }
func (c *fakeContext) DefaultProvider() string                        { return c.defaultProvider }
func (c *fakeContext) Mode() core.Mode                                { return core.ModeSandbox }
func (c *fakeContext) Logger() core.Logger                            { return c.logger }
func (c *fakeContext) Endpoints() []core.EndpointInfo                 { return nil }
func (c *fakeContext) Emit(context.Context, *core.PaymentEvent) error { return nil }

func (c *fakeContext) Use(providerID string) (core.Provider, error) {
	provider, ok := c.providers[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

// recordingLogger keeps messages so a test can assert that no card data is logged.
type recordingLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *recordingLogger) record(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, message)
}

func (l *recordingLogger) Debug(message string, _ map[string]any) { l.record(message) }
func (l *recordingLogger) Info(message string, _ map[string]any)  { l.record(message) }
func (l *recordingLogger) Error(message string, err error, _ map[string]any) {
	if err != nil {
		message += ": " + err.Error()
	}
	l.record(message)
}

func (l *recordingLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := ""
	for _, message := range l.messages {
		out += message + "\n"
	}
	return out
}

type stubPayment struct {
	id        string
	amount    int64
	state     string
	refunds   int
	cancelled bool
}

// stubProvider is an in-memory gateway: enough of core.Provider to exercise the
// hooks without a network.
type stubProvider struct {
	core.BaseProvider
	mu          sync.Mutex
	payments    []*stubPayment
	seq         int
	nextFailure core.PaymentErrorCode
	nextNetwork bool
}

func newStubProvider(id string) *stubProvider {
	return &stubProvider{BaseProvider: core.NewBaseProvider(id, core.ProviderConfig{})}
}

func (p *stubProvider) record(id string) *stubPayment {
	for _, payment := range p.payments {
		if payment.id == id {
			return payment
		}
	}
	return nil
}

func (p *stubProvider) paymentCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.payments)
}

func (p *stubProvider) newPayment(amount string, state string) *stubPayment {
	minor, _ := core.ToMinorUnits(amount, "price")
	p.seq++
	payment := &stubPayment{id: fmt.Sprintf("%s-pay-%d", p.Name(), p.seq), amount: minor, state: state}
	p.payments = append(p.payments, payment)
	return payment
}

func (p *stubProvider) charge(request *core.PaymentRequest, state string) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nextNetwork {
		p.nextNetwork = false
		payment := p.newPayment(request.PaidPrice, "pending")
		return &core.PaymentResponse{Status: core.StatusPending, PaymentID: payment.id, Code: core.CodeNetworkError}, nil
	}
	if p.nextFailure != "" {
		code := p.nextFailure
		p.nextFailure = ""
		payment := p.newPayment(request.PaidPrice, "failed")
		return &core.PaymentResponse{Status: core.StatusFailure, PaymentID: payment.id, Code: code}, nil
	}
	payment := p.newPayment(request.PaidPrice, state)
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id, ConversationID: request.ConversationID}, nil
}

func (p *stubProvider) CreatePayment(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.charge(request, "succeeded")
}

func (p *stubProvider) Authorize(_ context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	return p.charge(request, "authorized")
}

func (p *stubProvider) InitThreeDSPayment(_ context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.newPayment(request.PaidPrice, "pending")
	return &core.ThreeDSInitResponse{
		Status:             core.StatusPending,
		PaymentID:          payment.id,
		ThreeDSHTMLContent: "<form>3ds</form>",
	}, nil
}

func (p *stubProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := text(toRecord(callbackData)["paymentId"])
	payment := p.record(id)
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "succeeded"
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) Refund(_ context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.record(request.PaymentID)
	if payment == nil {
		return &core.RefundResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.refunds++
	return &core.RefundResponse{Status: core.StatusSuccess, RefundID: payment.id + "-refund"}, nil
}

func (p *stubProvider) Cancel(_ context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.record(request.PaymentID)
	if payment == nil {
		return &core.CancelResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.cancelled = true
	return &core.CancelResponse{Status: core.StatusSuccess, TransactionID: payment.id}, nil
}

func (p *stubProvider) GetPayment(_ context.Context, paymentID string) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.record(paymentID)
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) Capture(_ context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.record(request.PaymentID)
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "succeeded"
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) VoidAuthorization(_ context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.record(request.PaymentID)
	if payment == nil {
		return &core.CancelResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "cancelled"
	return &core.CancelResponse{Status: core.StatusSuccess, VoidID: payment.id}, nil
}

// manualStore is a shared store a test can inspect and pre-seed.
type manualStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newManualStore(initial map[string]string) *manualStore {
	data := make(map[string]string, len(initial))
	for key, value := range initial {
		data[key] = value
	}
	return &manualStore{data: data}
}

func (s *manualStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

func (s *manualStore) Set(_ context.Context, key, value string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *manualStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *manualStore) peek(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key]
}

// brokenStore fails every read and write, like a store that is down.
type brokenStore struct{}

func (brokenStore) Get(context.Context, string) (string, error) {
	return "", core.NewError("store down", "STORE_ERROR")
}

func (brokenStore) Set(context.Context, string, string, int) error {
	return core.NewError("store down", "STORE_ERROR")
}

func (brokenStore) Delete(context.Context, string) error {
	return core.NewError("store down", "STORE_ERROR")
}

// harness runs an operation through the plugin hooks and a stub provider, the way
// the payment facade does.
type harness struct {
	router    *Router
	plugin    core.Plugin
	pctx      *fakeContext
	providers map[string]*stubProvider
}

func newHarness(t *testing.T, options Options, ids []string, defaultProvider string) *harness {
	t.Helper()
	return newHarnessWithLogger(t, options, ids, defaultProvider, core.NopLogger{})
}

// newHarnessWithLogger installs the logger before Init, because Bind copies the
// context logger into the engine.
func newHarnessWithLogger(t *testing.T, options Options, ids []string, defaultProvider string, logger core.Logger) *harness {
	t.Helper()
	if ids == nil {
		ids = []string{"a", "b"}
	}
	providers := make(map[string]*stubProvider, len(ids))
	registry := make(map[string]core.Provider, len(ids))
	for _, id := range ids {
		provider := newStubProvider(id)
		providers[id] = provider
		registry[id] = provider
	}
	pctx := &fakeContext{ids: ids, defaultProvider: defaultProvider, providers: registry, logger: logger}
	router, err := New(options)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	plugin := router.Plugin()
	if plugin.Init != nil {
		if err := plugin.Init(context.Background(), pctx); err != nil {
			t.Fatalf("Init() failed: %v", err)
		}
	}
	return &harness{router: router, plugin: plugin, pctx: pctx, providers: providers}
}

func (h *harness) explain(request any, operation core.PaymentOperation) (*Decision, error) {
	var options *ExplainOptions
	if operation != "" {
		options = &ExplainOptions{Operation: operation}
	}
	return h.router.Explain(context.Background(), request, options)
}

func (h *harness) call(operation core.PaymentOperation, request any, routable bool) (any, error) {
	octx := core.OperationContext{
		Operation: operation,
		Request:   request,
		Provider:  h.pctx.DefaultProvider(),
		Routable:  routable,
	}
	var result any
	skipProvider := false
	for _, hook := range h.plugin.Hooks.Before {
		if hook.Matcher != nil && !hook.Matcher(octx) {
			continue
		}
		replacement, err := hook.Handler(octx)
		if err != nil {
			return nil, err
		}
		if replacement == nil {
			continue
		}
		if replacement.Provider != "" {
			octx.Provider = replacement.Provider
		}
		if replacement.Request != nil {
			octx.Request = replacement.Request
		}
		if replacement.Result != nil {
			result = replacement.Result
			skipProvider = true
			break
		}
	}
	if !skipProvider {
		provider, ok := h.providers[octx.Provider]
		if !ok {
			return nil, core.NewProviderNotEnabledError(octx.Provider)
		}
		var err error
		result, err = callStub(provider, operation, octx.Request)
		if err != nil {
			return nil, err
		}
	}
	rctx := core.OperationResultContext{OperationContext: octx, Result: result}
	for _, hook := range h.plugin.Hooks.After {
		if hook.Matcher != nil && !hook.Matcher(rctx) {
			continue
		}
		replacement, err := hook.Handler(rctx)
		if err != nil {
			return nil, err
		}
		if replacement != nil && replacement.Result != nil {
			rctx.Result = replacement.Result
		}
	}
	return rctx.Result, nil
}

func callStub(provider *stubProvider, operation core.PaymentOperation, request any) (any, error) {
	ctx := context.Background()
	switch operation {
	case core.OperationCreatePayment:
		return provider.CreatePayment(ctx, request.(*core.PaymentRequest))
	case core.OperationAuthorize:
		return provider.Authorize(ctx, request.(*core.PaymentRequest))
	case core.OperationInitThreeDSPayment:
		return provider.InitThreeDSPayment(ctx, request.(*core.ThreeDSPaymentRequest))
	case core.OperationCompleteThreeDSPayment:
		return provider.CompleteThreeDSPayment(ctx, request)
	case core.OperationRefund:
		return provider.Refund(ctx, request.(*core.RefundRequest))
	case core.OperationCancel:
		return provider.Cancel(ctx, request.(*core.CancelRequest))
	case core.OperationCapture:
		return provider.Capture(ctx, request.(*core.CaptureRequest))
	case core.OperationVoidAuthorization:
		return provider.VoidAuthorization(ctx, request.(*core.VoidAuthorizationRequest))
	case core.OperationGetPayment:
		return provider.GetPayment(ctx, request.(string))
	}
	return nil, core.NotSupported(provider.Name(), string(operation), "")
}

// baseRequest mirrors the shared payment fixture: a 1.2 TRY order.
func baseRequest() map[string]any {
	return map[string]any{
		"price":          "1",
		"paidPrice":      "1.2",
		"currency":       "TRY",
		"basketId":       "B67832",
		"conversationId": "123456789",
		"paymentCard": map[string]any{
			"cardHolderName": "John Doe",
			"cardNumber":     "5528790000000008",
			"expireMonth":    "12",
			"expireYear":     "2030",
			"cvc":            "123",
		},
	}
}

func withRequest(overrides map[string]any) map[string]any {
	request := baseRequest()
	for key, value := range overrides {
		request[key] = value
	}
	return request
}

func threeDSRequest() map[string]any {
	request := baseRequest()
	request["callbackUrl"] = "https://example.com/callback"
	return request
}

// paymentRequest is the fixture as the typed core request, with no routing extras.
func paymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:          "1",
		PaidPrice:      "1.2",
		Currency:       "TRY",
		BasketID:       "B67832",
		ConversationID: "123456789",
		PaymentCard: &core.PaymentCard{
			CardHolderName: "John Doe",
			CardNumber:     "5528790000000008",
			ExpireMonth:    "12",
			ExpireYear:     "2030",
			CVC:            "123",
		},
	}
}

func boolPtr(value bool) *bool { return &value }

func isConfigurationError(err error) bool {
	var target *core.ConfigurationError
	return errors.As(err, &target)
}

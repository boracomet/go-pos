package commissionrouter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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

func (c *fakeContext) ProviderIDs() []string         { return c.ids }
func (c *fakeContext) ErrorCodes() map[string]string { return map[string]string{} }
func (c *fakeContext) DefaultProvider() string       { return c.defaultProvider }
func (c *fakeContext) Mode() core.Mode               { return core.ModeSandbox }
func (c *fakeContext) Logger() core.Logger           { return c.logger }
func (c *fakeContext) Endpoints() []core.EndpointInfo {
	return nil
}
func (c *fakeContext) Emit(context.Context, *core.PaymentEvent) error { return nil }

func (c *fakeContext) Use(providerID string) (core.Provider, error) {
	provider, ok := c.providers[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

// fakeEndpoint is an incoming handler request served to a plugin endpoint.
type fakeEndpoint struct {
	*fakeContext
	request *core.HandlerRequest
	body    map[string]any
}

func (e *fakeEndpoint) Request() *core.HandlerRequest { return e.request }
func (e *fakeEndpoint) Body() map[string]any          { return e.body }
func (e *fakeEndpoint) Query() url.Values             { return url.Values{} }
func (e *fakeEndpoint) Handler() core.EndpointHandlerInfo {
	return core.EndpointHandlerInfo{BasePath: "/api/pay"}
}

func (e *fakeEndpoint) JSON(body any, status ...int) *core.HandlerResponse {
	code := 200
	if len(status) > 0 && status[0] != 0 {
		code = status[0]
	}
	return &core.HandlerResponse{Status: code, Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

func (e *fakeEndpoint) HTML(body string, status ...int) *core.HandlerResponse {
	code := 200
	if len(status) > 0 && status[0] != 0 {
		code = status[0]
	}
	return &core.HandlerResponse{Status: code, Headers: map[string]string{"content-type": "text/html"}, Body: body}
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
	refunds   []string
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

func (p *stubProvider) failNext(_ core.PaymentOperation, code core.PaymentErrorCode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextFailure = code
}

func (p *stubProvider) networkErrorNext(_ core.PaymentOperation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextNetwork = true
}

func (p *stubProvider) getRecord(id string) *stubPayment {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	return &core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: payment.id, ThreeDSHTMLContent: "<form>3ds</form>"}, nil
}

func (p *stubProvider) InitThreeDSAuthorize(_ context.Context, request *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.newPayment(request.PaidPrice, "pending")
	return &core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: payment.id, ThreeDSHTMLContent: "<form>3ds</form>"}, nil
}

func (p *stubProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(text(toRecord(callbackData)["paymentId"]))
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "succeeded"
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) Refund(_ context.Context, request *core.RefundRequest) (*core.RefundResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(request.PaymentID)
	if payment == nil {
		return &core.RefundResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.refunds = append(payment.refunds, payment.id+"-refund")
	return &core.RefundResponse{Status: core.StatusSuccess, RefundID: payment.id + "-refund"}, nil
}

func (p *stubProvider) Cancel(_ context.Context, request *core.CancelRequest) (*core.CancelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(request.PaymentID)
	if payment == nil {
		return &core.CancelResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.cancelled = true
	return &core.CancelResponse{Status: core.StatusSuccess, TransactionID: payment.id}, nil
}

func (p *stubProvider) GetPayment(_ context.Context, paymentID string) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(paymentID)
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) Capture(_ context.Context, request *core.CaptureRequest) (*core.PaymentResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(request.PaymentID)
	if payment == nil {
		return &core.PaymentResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "succeeded"
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: payment.id}, nil
}

func (p *stubProvider) VoidAuthorization(_ context.Context, request *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	payment := p.getRecordLocked(request.PaymentID)
	if payment == nil {
		return &core.CancelResponse{Status: core.StatusFailure, Code: core.CodeInvalidRequest}, nil
	}
	payment.state = "cancelled"
	return &core.CancelResponse{Status: core.StatusSuccess, VoidID: payment.id}, nil
}

func (p *stubProvider) getRecordLocked(id string) *stubPayment {
	for _, payment := range p.payments {
		if payment.id == id {
			return payment
		}
	}
	return nil
}

// harness runs an operation through the plugin hooks and a stub provider, the way
// the payment facade does.
type harness struct {
	router    *Router
	plugin    core.Plugin
	pctx      *fakeContext
	providers map[string]*stubProvider
}

func newHarness(t *testing.T, options Options, ids []string) *harness {
	t.Helper()
	return newHarnessWithLogger(t, options, ids, core.NopLogger{})
}

func newHarnessWithLogger(t *testing.T, options Options, ids []string, logger core.Logger) *harness {
	t.Helper()
	if ids == nil {
		ids = []string{"alpha", "beta", "gamma"}
	}
	providers := make(map[string]*stubProvider, len(ids))
	registry := make(map[string]core.Provider, len(ids))
	for _, id := range ids {
		provider := newStubProvider(id)
		providers[id] = provider
		registry[id] = provider
	}
	pctx := &fakeContext{ids: ids, defaultProvider: ids[0], providers: registry, logger: logger}
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

func (h *harness) quote(input QuoteInput) (*Quote, error) {
	return h.router.Quote(context.Background(), input)
}

func (h *harness) mustQuote(t *testing.T, input QuoteInput) *Quote {
	t.Helper()
	quote, err := h.quote(input)
	if err != nil {
		t.Fatalf("quote(%+v): %v", input, err)
	}
	return quote
}

func (h *harness) status() Status { return h.router.Status(context.Background()) }

// call runs one routable operation through the hooks and the stub providers.
func (h *harness) call(operation core.PaymentOperation, request any) (any, error) {
	octx := core.OperationContext{
		Operation: operation,
		Request:   request,
		Provider:  h.pctx.DefaultProvider(),
		Routable:  true,
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
	case core.OperationInitThreeDSAuthorize:
		return provider.InitThreeDSAuthorize(ctx, request.(*core.ThreeDSPaymentRequest))
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

// order is an order whose paid amount is exactly price.
func order(price string) *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:          price,
		PaidPrice:      price,
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

func orderWith(price, conversationID string) *core.PaymentRequest {
	request := order(price)
	request.ConversationID = conversationID
	return request
}

func refundRequest(paymentID string) *core.RefundRequest {
	return &core.RefundRequest{
		PaymentID:      paymentID,
		Price:          "0.5",
		Currency:       "TRY",
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

func cancelRequest(paymentID string) *core.CancelRequest {
	return &core.CancelRequest{PaymentID: paymentID, IP: "85.34.78.112", ConversationID: "123456789"}
}

func asPayment(t *testing.T, result any) RoutedPaymentResponse {
	t.Helper()
	typed, ok := result.(RoutedPaymentResponse)
	if !ok {
		t.Fatalf("expected a routed payment response, got %T", result)
	}
	return typed
}

func asRefund(t *testing.T, result any) RoutedRefundResponse {
	t.Helper()
	typed, ok := result.(RoutedRefundResponse)
	if !ok {
		t.Fatalf("expected a routed refund response, got %T", result)
	}
	return typed
}

func asCancel(t *testing.T, result any) RoutedCancelResponse {
	t.Helper()
	typed, ok := result.(RoutedCancelResponse)
	if !ok {
		t.Fatalf("expected a routed cancel response, got %T", result)
	}
	return typed
}

func intPtr(value int) *int { return &value }

func selectedOf(quote *Quote) string {
	if quote.Selected == nil {
		return ""
	}
	return *quote.Selected
}

func reasonOf(quote *Quote) SelectionReason {
	if quote.SelectionReason == nil {
		return ""
	}
	return *quote.SelectionReason
}

func costOf(candidate Candidate) string {
	if candidate.Cost == nil {
		return ""
	}
	return *candidate.Cost
}

func hasNote(quote *Quote, note string) bool {
	for _, entry := range quote.Notes {
		if entry == note {
			return true
		}
	}
	return false
}

func isConfigurationError(err error) bool {
	var target *core.ConfigurationError
	return errors.As(err, &target)
}

func isValidationError(err error) bool {
	var target *core.ValidationError
	return errors.As(err, &target)
}

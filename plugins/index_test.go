package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func pluginTestPtr[T any](value T) *T { return &value }

// pluginTestContext is a stub plugin context recording the events a plugin emits.
type pluginTestContext struct {
	mu              sync.Mutex
	providerIDs     []string
	errorCodes      map[string]string
	defaultProvider string
	mode            core.Mode
	logger          core.Logger
	providers       map[string]core.Provider
	events          []*core.PaymentEvent
	emitErr         error
	endpoints       []core.EndpointInfo
}

func newPluginTestContext() *pluginTestContext {
	return &pluginTestContext{
		providerIDs: []string{"mock"},
		errorCodes:  map[string]string{},
		mode:        core.ModeSandbox,
		logger:      core.NopLogger{},
		providers:   map[string]core.Provider{},
	}
}

func (c *pluginTestContext) ProviderIDs() []string { return c.providerIDs }

func (c *pluginTestContext) ErrorCodes() map[string]string { return c.errorCodes }

func (c *pluginTestContext) DefaultProvider() string { return c.defaultProvider }

func (c *pluginTestContext) Mode() core.Mode { return c.mode }

func (c *pluginTestContext) Logger() core.Logger { return c.logger }

func (c *pluginTestContext) Use(providerID string) (core.Provider, error) {
	if provider, ok := c.providers[providerID]; ok {
		return provider, nil
	}
	return nil, core.NewProviderNotEnabledError(providerID)
}

func (c *pluginTestContext) Emit(_ context.Context, event *core.PaymentEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitErr != nil {
		return c.emitErr
	}
	c.events = append(c.events, event)
	return nil
}

func (c *pluginTestContext) Endpoints() []core.EndpointInfo { return c.endpoints }

func (c *pluginTestContext) emitted() []*core.PaymentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*core.PaymentEvent{}, c.events...)
}

// pluginTestEndpointContext serves a plugin endpoint outside the HTTP handler.
type pluginTestEndpointContext struct {
	*pluginTestContext
	request *core.HandlerRequest
	body    map[string]any
	query   url.Values
	handler core.EndpointHandlerInfo
}

func (c *pluginTestEndpointContext) Request() *core.HandlerRequest { return c.request }

func (c *pluginTestEndpointContext) Body() map[string]any { return c.body }

func (c *pluginTestEndpointContext) Query() url.Values { return c.query }

func (c *pluginTestEndpointContext) JSON(body any, status ...int) *core.HandlerResponse {
	return &core.HandlerResponse{
		Status:  pluginTestStatus(status),
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}
}

func (c *pluginTestEndpointContext) HTML(body string, status ...int) *core.HandlerResponse {
	return &core.HandlerResponse{
		Status:  pluginTestStatus(status),
		Headers: map[string]string{"Content-Type": "text/html; charset=utf-8"},
		Body:    body,
	}
}

func (c *pluginTestEndpointContext) Handler() core.EndpointHandlerInfo { return c.handler }

func pluginTestStatus(status []int) int {
	if len(status) > 0 && status[0] != 0 {
		return status[0]
	}
	return http.StatusOK
}

// pluginTestProvider is a provider stub returning canned results and counting calls.
type pluginTestProvider struct {
	core.BaseProvider
	mu       sync.Mutex
	calls    []string
	payment  *core.PaymentResponse
	queue    []*core.PaymentResponse
	threeDS  *core.ThreeDSInitResponse
	refund   *core.RefundResponse
	cancel   *core.CancelResponse
	err      error
	getError error
}

func newPluginTestProvider(name string) *pluginTestProvider {
	return &pluginTestProvider{BaseProvider: core.BaseProvider{ProviderName: name}}
}

func (p *pluginTestProvider) record(operation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, operation)
}

func (p *pluginTestProvider) count(operation string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, call := range p.calls {
		if call == operation {
			total++
		}
	}
	return total
}

func (p *pluginTestProvider) result() *core.PaymentResponse {
	if p.payment != nil {
		return p.payment
	}
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "mock-payment"}
}

func (p *pluginTestProvider) CreatePayment(context.Context, *core.PaymentRequest) (*core.PaymentResponse, error) {
	p.record("createPayment")
	if p.err != nil {
		return nil, p.err
	}
	return p.result(), nil
}

func (p *pluginTestProvider) Authorize(context.Context, *core.PaymentRequest) (*core.PaymentResponse, error) {
	p.record("authorize")
	if p.err != nil {
		return nil, p.err
	}
	return p.result(), nil
}

func (p *pluginTestProvider) Capture(context.Context, *core.CaptureRequest) (*core.PaymentResponse, error) {
	p.record("capture")
	if p.err != nil {
		return nil, p.err
	}
	return p.result(), nil
}

func (p *pluginTestProvider) InitThreeDSPayment(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	p.record("initThreeDSPayment")
	if p.err != nil {
		return nil, p.err
	}
	if p.threeDS != nil {
		return p.threeDS, nil
	}
	return &core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "mock-payment"}, nil
}

func (p *pluginTestProvider) CompleteThreeDSPayment(context.Context, any) (*core.PaymentResponse, error) {
	p.record("completeThreeDSPayment")
	if p.err != nil {
		return nil, p.err
	}
	return p.result(), nil
}

func (p *pluginTestProvider) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	p.record("getPayment")
	p.mu.Lock()
	if len(p.queue) > 0 {
		next := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()
		return next, nil
	}
	p.mu.Unlock()
	if p.getError != nil {
		return nil, p.getError
	}
	return p.result(), nil
}

func (p *pluginTestProvider) Refund(context.Context, *core.RefundRequest) (*core.RefundResponse, error) {
	p.record("refund")
	if p.err != nil {
		return nil, p.err
	}
	if p.refund != nil {
		return p.refund, nil
	}
	return &core.RefundResponse{Status: core.StatusSuccess}, nil
}

func (p *pluginTestProvider) Cancel(context.Context, *core.CancelRequest) (*core.CancelResponse, error) {
	p.record("cancel")
	if p.err != nil {
		return nil, p.err
	}
	if p.cancel != nil {
		return p.cancel, nil
	}
	return &core.CancelResponse{Status: core.StatusSuccess}, nil
}

func (p *pluginTestProvider) VoidAuthorization(context.Context, *core.VoidAuthorizationRequest) (*core.CancelResponse, error) {
	p.record("voidAuthorization")
	if p.err != nil {
		return nil, p.err
	}
	if p.cancel != nil {
		return p.cancel, nil
	}
	return &core.CancelResponse{Status: core.StatusSuccess}, nil
}

// pluginTestLogger records log output so a test can assert what was logged.
type pluginTestLogger struct {
	mu      sync.Mutex
	entries []pluginTestLogEntry
}

type pluginTestLogEntry struct {
	Level   string
	Message string
	Err     error
	Meta    map[string]any
}

func (l *pluginTestLogger) add(level, message string, err error, meta map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, pluginTestLogEntry{Level: level, Message: message, Err: err, Meta: meta})
}

func (l *pluginTestLogger) Debug(message string, meta map[string]any) {
	l.add("debug", message, nil, meta)
}

func (l *pluginTestLogger) Info(message string, meta map[string]any) {
	l.add("info", message, nil, meta)
}

func (l *pluginTestLogger) Error(message string, err error, meta map[string]any) {
	l.add("error", message, err, meta)
}

func (l *pluginTestLogger) all() []pluginTestLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]pluginTestLogEntry{}, l.entries...)
}

func (l *pluginTestLogger) messages() []string {
	messages := []string{}
	for _, entry := range l.all() {
		messages = append(messages, entry.Message)
	}
	return messages
}

// pluginTestDoer is a transport stub recording every request it is given.
type pluginTestDoer struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	status   int
	body     string
	err      error
	respond  func(*http.Request) (*http.Response, error)
}

func (d *pluginTestDoer) Do(request *http.Request) (*http.Response, error) {
	body := ""
	if request.Body != nil {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body = string(data)
		request.Body = io.NopCloser(strings.NewReader(body))
	}
	d.mu.Lock()
	d.requests = append(d.requests, request)
	d.bodies = append(d.bodies, body)
	respond, status, text, failure := d.respond, d.status, d.body, d.err
	d.mu.Unlock()
	if respond != nil {
		return respond(request)
	}
	if failure != nil {
		return nil, failure
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(text)),
	}, nil
}

func (d *pluginTestDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *pluginTestDoer) requestAt(index int) *http.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[index]
}

func (d *pluginTestDoer) bodyAt(index int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bodies[index]
}

func pluginTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// pluginTestBefore runs the before hooks the way the facade does: in order,
// stopping at the first hook that returns a result.
func pluginTestBefore(plugin core.Plugin, ctx core.OperationContext) (*core.BeforeHookResult, error) {
	var out *core.BeforeHookResult
	for _, hook := range plugin.Hooks.Before {
		if hook.Matcher != nil && !hook.Matcher(ctx) {
			continue
		}
		result, err := hook.Handler(ctx)
		if err != nil {
			return out, err
		}
		if result == nil {
			continue
		}
		if out == nil {
			out = &core.BeforeHookResult{}
		}
		if result.Provider != "" {
			out.Provider, ctx.Provider = result.Provider, result.Provider
		}
		if result.Request != nil {
			out.Request, ctx.Request = result.Request, result.Request
		}
		if result.Result != nil {
			out.Result = result.Result
			return out, nil
		}
	}
	return out, nil
}

func pluginTestRunBefore(t *testing.T, plugin core.Plugin, ctx core.OperationContext) *core.BeforeHookResult {
	t.Helper()
	result, err := pluginTestBefore(plugin, ctx)
	if err != nil {
		t.Fatalf("before hook failed: %v", err)
	}
	return result
}

// pluginTestAfterContext runs the after hooks in order, threading the result and
// the request through, and returns the context the events would be built from.
func pluginTestAfterContext(plugin core.Plugin, ctx core.OperationResultContext) (core.OperationResultContext, error) {
	for _, hook := range plugin.Hooks.After {
		if hook.Matcher != nil && !hook.Matcher(ctx) {
			continue
		}
		hooked, err := hook.Handler(ctx)
		if err != nil {
			return ctx, err
		}
		if hooked == nil {
			continue
		}
		if hooked.Result != nil {
			ctx.Result = hooked.Result
		}
		if hooked.Request != nil {
			ctx.Request = hooked.Request
		}
	}
	return ctx, nil
}

// pluginTestAfter runs the after hooks in order, threading the result through.
func pluginTestAfter(plugin core.Plugin, ctx core.OperationResultContext) (any, error) {
	out, err := pluginTestAfterContext(plugin, ctx)
	return out.Result, err
}

func pluginTestRunAfter(t *testing.T, plugin core.Plugin, ctx core.OperationResultContext) any {
	t.Helper()
	result, err := pluginTestAfter(plugin, ctx)
	if err != nil {
		t.Fatalf("after hook failed: %v", err)
	}
	return result
}

func pluginTestMethods(t *testing.T, plugin core.Plugin, pctx core.PluginContext) map[string]any {
	t.Helper()
	if plugin.Methods == nil {
		return map[string]any{}
	}
	methods, err := plugin.Methods(context.Background(), pctx)
	if err != nil {
		t.Fatalf("methods failed: %v", err)
	}
	return methods
}

func pluginTestInit(plugin core.Plugin, pctx core.PluginContext) error {
	if plugin.Init == nil {
		return nil
	}
	return plugin.Init(context.Background(), pctx)
}

func pluginTestOperation(operation core.PaymentOperation, provider string, request any) core.OperationContext {
	return core.OperationContext{Operation: operation, Provider: provider, Request: request, Routable: true}
}

func pluginTestOutcome(operation core.PaymentOperation, provider string, request, result any) core.OperationResultContext {
	return core.OperationResultContext{
		OperationContext: pluginTestOperation(operation, provider, request),
		Result:           result,
	}
}

// pluginTestJSON turns a value into the generic tree a test asserts on.
func pluginTestJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	return out
}

func pluginTestCard() *core.PaymentCard {
	return &core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "123",
		RegisterCard:   pluginTestPtr(true),
	}
}

func pluginTestPaymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:       "1",
		PaidPrice:   "1.2",
		Currency:    string(core.CurrencyTRY),
		BasketID:    "B67832",
		PaymentCard: pluginTestCard(),
		Buyer: core.Buyer{
			ID:                  "BY789",
			Name:                "John",
			Surname:             "Doe",
			GSMNumber:           "+905350000000",
			Email:               "john.doe@example.com",
			IdentityNumber:      "74300864791",
			RegistrationAddress: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
			IP:                  "85.34.78.112",
			City:                "Istanbul",
			Country:             "Turkey",
			ZipCode:             "34732",
		},
		ShippingAddress: core.Address{
			ContactName: "Jane Doe",
			City:        "Istanbul",
			Country:     "Turkey",
			Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
			ZipCode:     "34732",
		},
		BillingAddress: core.Address{
			ContactName: "John Doe",
			City:        "Istanbul",
			Country:     "Turkey",
			Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
			ZipCode:     "34732",
		},
		BasketItems: []core.BasketItem{
			{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: string(core.BasketItemPhysical), Price: "0.3"},
			{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: string(core.BasketItemVirtual), Price: "0.5"},
			{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: string(core.BasketItemPhysical), Price: "0.2"},
		},
		ConversationID: "123456789",
	}
}

func pluginTestPost(path string, body any, headers map[string]string) *core.HandlerRequest {
	request := &core.HandlerRequest{
		Method:  "POST",
		URL:     path,
		Headers: map[string]string{"content-type": "application/json"},
		Body:    body,
	}
	for name, value := range headers {
		request.Headers[name] = value
	}
	return request
}

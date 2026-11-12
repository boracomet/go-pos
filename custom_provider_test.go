package pos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
	postesting "github.com/boracomet/go-pos/testing"
)

// A custom provider is an ordinary core.Provider: embed core.BaseProvider for the
// shared configuration, validation and error-code resolution, implement the six
// mandatory operations, and hand the facade a core.ProviderDefinition so the
// shared settings (mode, logger, retry, transport) reach it.

// customProviderConfig are the credentials of the example gateway.
type customProviderConfig struct {
	TerminalID string
	SecretKey  string
	// BaseURL overrides the gateway host, for a sandbox or a test server.
	BaseURL string
}

// customProvider talks to a gateway that answers {approved, transactionId} or
// {approved:false, responseCode}.
type customProvider struct {
	core.BaseProvider
	terminalID string
	secretKey  string
	client     *core.HTTPClient
}

var _ core.Provider = (*customProvider)(nil)

// customProviderDefinition builds the provider once the shared settings are known.
func customProviderDefinition(cfg customProviderConfig) core.ProviderDefinition {
	return core.DefineProvider(func(setup core.ProviderSetupContext) (core.Provider, error) {
		if cfg.TerminalID == "" {
			return nil, core.NewConfigurationError("terminalId is required", setup.ID)
		}
		if cfg.SecretKey == "" {
			return nil, core.NewConfigurationError("secretKey is required", setup.ID)
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.custom-gateway.example"
		}
		provider := &customProvider{
			BaseProvider: core.NewBaseProvider(setup.ID, core.ProviderConfig{
				BaseURL:  baseURL,
				Logger:   setup.Logger,
				Retry:    setup.Retry,
				Doer:     setup.Doer,
				Validate: setup.Validate,
			}),
			terminalID: cfg.TerminalID,
			secretKey:  cfg.SecretKey,
		}
		// The gateway returns ISO 8583 host codes, plus its own "99".
		codes := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes)+1)
		for raw, code := range core.ISO8583ErrorCodes {
			codes[raw] = code
		}
		codes["99"] = core.CodeProviderError
		provider.ErrorCodes = codes
		provider.client = provider.NewHTTPClient(30*time.Second, map[string]string{
			"Content-Type": "application/json",
		})
		return provider, nil
	})
}

// customRules are the fields this gateway needs before it is called.
var customRules = core.PaymentValidationRules{Card: true}

// CreatePayment charges a card.
func (p *customProvider) CreatePayment(ctx context.Context, request *core.PaymentRequest) (*core.PaymentResponse, error) {
	if err := p.ValidatePayment(request, customRules); err != nil {
		return customPaymentFailure(p.BuildFailure(err)), nil
	}
	card, err := p.CardOf(request)
	if err != nil {
		return customPaymentFailure(p.BuildFailure(err)), nil
	}
	body, err := json.Marshal(map[string]any{
		"terminalId":     p.terminalID,
		"secretKey":      p.secretKey,
		"amount":         request.PaidPrice,
		"currency":       request.Currency,
		"conversationId": request.ConversationID,
		"cardNumber":     card.CardNumber,
	})
	if err != nil {
		return customPaymentFailure(p.BuildFailure(err)), nil
	}
	response, err := p.client.Post(ctx, "/payments", string(body), core.RequestConfig{})
	if err != nil {
		return customPaymentFailure(p.BuildFailure(err)), nil
	}
	record, _ := response.Data.(map[string]any)
	if approved, _ := record["approved"].(bool); approved {
		return &core.PaymentResponse{
			Status:         core.StatusSuccess,
			PaymentID:      customString(record["transactionId"]),
			ConversationID: request.ConversationID,
			RawResponse:    record,
		}, nil
	}
	rawCode := customString(record["responseCode"])
	out := &core.PaymentResponse{ConversationID: request.ConversationID, RawResponse: record}
	out.SetFailure(core.Failure{
		Status:       core.StatusFailure,
		ErrorCode:    rawCode,
		Code:         p.ResolveErrorCode(rawCode),
		ErrorMessage: "The gateway declined the payment",
	})
	return out, nil
}

// InitThreeDSPayment is not supported by this gateway.
func (p *customProvider) InitThreeDSPayment(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

// CompleteThreeDSPayment is not supported by this gateway.
func (p *customProvider) CompleteThreeDSPayment(context.Context, any) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

// Refund is not supported by this gateway.
func (p *customProvider) Refund(context.Context, *core.RefundRequest) (*core.RefundResponse, error) {
	return nil, p.NotSupported("Refunds")
}

// Cancel is not supported by this gateway.
func (p *customProvider) Cancel(context.Context, *core.CancelRequest) (*core.CancelResponse, error) {
	return nil, p.NotSupported("Cancellations")
}

// GetPayment is not supported by this gateway.
func (p *customProvider) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("Payment queries")
}

// BuildFailure normalizes an error into a result failure.
func (p *customProvider) BuildFailure(err error) core.Failure {
	return core.BuildFailure(p.ProviderName, err, "The payment could not be completed")
}

func customPaymentFailure(failure core.Failure) *core.PaymentResponse {
	out := &core.PaymentResponse{}
	out.SetFailure(failure)
	return out
}

func customString(value any) string {
	text, _ := value.(string)
	return text
}

// ---- test doubles --------------------------------------------------------

// customTestCall is one request the test gateway received.
type customTestCall struct {
	method string
	path   string
	body   map[string]any
}

// customTestGateway is an httptest server standing in for the gateway.
type customTestGateway struct {
	mu       sync.Mutex
	calls    []customTestCall
	response string
	status   int
}

func (g *customTestGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	g.mu.Lock()
	g.calls = append(g.calls, customTestCall{method: r.Method, path: r.URL.Path, body: body})
	response, status := g.response, g.status
	g.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, response)
}

func (g *customTestGateway) recorded() []customTestCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]customTestCall(nil), g.calls...)
}

// customTestServe starts a local gateway answering with response.
func customTestServe(t *testing.T, response string) (*httptest.Server, *customTestGateway) {
	t.Helper()
	gateway := &customTestGateway{response: response}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	return server, gateway
}

// customTestConfig is a valid configuration pointing at a local gateway.
func customTestConfig(baseURL string) customProviderConfig {
	return customProviderConfig{TerminalID: "T1", SecretKey: "secret", BaseURL: baseURL}
}

// customTestPayment builds a facade over the custom provider.
func customTestPayment(t *testing.T, cfg Config) *Payment {
	t.Helper()
	return posTestNew(t, cfg)
}

// customTestDoer is a transport that answers without a server.
type customTestDoer struct {
	mu       sync.Mutex
	requests []*http.Request
	response string
	err      error
}

func (d *customTestDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	body := d.response
	if body == "" {
		body = `{"approved":true,"transactionId":"T-DOER"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (d *customTestDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

var _ core.Doer = (*customTestDoer)(nil)

// ---- tests ---------------------------------------------------------------

func TestCustomProviderChargesThroughTheFacade(t *testing.T) {
	server, gateway := customTestServe(t, `{"approved":true,"transactionId":"T-1"}`)
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customTestConfig(server.URL)),
		},
	})
	if payment.DefaultProvider() != "custom" {
		t.Errorf("DefaultProvider() = %q, want custom", payment.DefaultProvider())
	}

	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if result.PaymentID != "T-1" {
		t.Errorf("paymentId = %q, want T-1 (the gateway's transactionId)", result.PaymentID)
	}
	if result.Code != "" {
		t.Errorf("code = %q, want none on success", result.Code)
	}

	calls := gateway.recorded()
	if len(calls) != 1 {
		t.Fatalf("the gateway received %d calls, want 1", len(calls))
	}
	if calls[0].method != http.MethodPost {
		t.Errorf("method = %q, want POST", calls[0].method)
	}
	if calls[0].path != "/payments" {
		t.Errorf("path = %q, want /payments", calls[0].path)
	}
	if calls[0].body["terminalId"] != "T1" {
		t.Errorf("terminalId = %v, want T1", calls[0].body["terminalId"])
	}
	if calls[0].body["amount"] != "1.2" {
		t.Errorf("amount = %v, want the paid price 1.2", calls[0].body["amount"])
	}
}

func TestCustomProviderValidatesBeforeCalling(t *testing.T) {
	server, gateway := customTestServe(t, `{"approved":true,"transactionId":"T-1"}`)
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customTestConfig(server.URL)),
		},
	})
	request := operationsTestWithCard(posTestRequest(), "1")
	result, err := payment.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment with an invalid card: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
	if result.ErrorCode != "VALIDATION_ERROR" {
		t.Errorf("errorCode = %q, want VALIDATION_ERROR", result.ErrorCode)
	}
	if result.ErrorMessage == "" {
		t.Error("errorMessage must name the invalid field")
	}
	if calls := gateway.recorded(); len(calls) != 0 {
		t.Errorf("the gateway received %d calls, want 0: validation runs first", len(calls))
	}
}

func TestCustomProviderMapsTheHostResponseCodes(t *testing.T) {
	cases := []struct {
		raw  string
		want core.PaymentErrorCode
	}{
		{"51", core.CodeInsufficientFunds},
		{"05", core.CodeCardDeclined},
		{"04", core.CodeFraudSuspected},
		{"13", core.CodeInvalidRequest},
		{"54", core.CodeExpiredCard},
		{"61", core.CodeLimitExceeded},
		{"82", core.CodeInvalidCVC},
		{"94", core.CodeDuplicateOrder},
		{"96", core.CodeProviderError},
		{"99", core.CodeProviderError}, // the gateway's own code
		{"ZZ", core.CodeUnknown},
		{"", core.CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"approved": false, "responseCode": tc.raw})
			if err != nil {
				t.Fatalf("marshal the gateway answer: %v", err)
			}
			server, _ := customTestServe(t, string(body))
			payment := customTestPayment(t, Config{
				Providers: map[string]core.ProviderEntry{
					"custom": customProviderDefinition(customTestConfig(server.URL)),
				},
			})
			result, err := payment.CreatePayment(context.Background(), posTestRequest())
			if err != nil {
				t.Fatalf("CreatePayment: %v", err)
			}
			if result.Status != core.StatusFailure {
				t.Errorf("status = %q, want failure", result.Status)
			}
			if result.Code != tc.want {
				t.Errorf("code = %q, want %q", result.Code, tc.want)
			}
			if result.ErrorCode != tc.raw {
				t.Errorf("errorCode = %q, want the raw code %q", result.ErrorCode, tc.raw)
			}
		})
	}
}

func TestCustomProviderRequiresCredentials(t *testing.T) {
	cases := []struct {
		name    string
		cfg     customProviderConfig
		missing string
	}{
		{"no terminal", customProviderConfig{SecretKey: "s"}, "terminalId"},
		{"no secret", customProviderConfig{TerminalID: "T1"}, "secretKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{
				Providers: map[string]core.ProviderEntry{"custom": customProviderDefinition(tc.cfg)},
			})
			posTestWantConfigurationError(t, err, tc.missing)
		})
	}
}

func TestCustomProviderReceivesTheSharedSettings(t *testing.T) {
	doer := &customTestDoer{}
	logger := &operationsTestLogger{}
	validate := false
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"my-gateway": customProviderDefinition(customProviderConfig{TerminalID: "T1", SecretKey: "s"}),
		},
		Mode:     core.ModeSandbox,
		Logger:   logger,
		Doer:     doer,
		Validate: &validate,
		Retry:    &core.RetryConfig{Attempts: 3},
	})
	raw, err := payment.RawProvider("my-gateway")
	if err != nil {
		t.Fatalf("RawProvider: %v", err)
	}
	provider, ok := raw.(*customProvider)
	if !ok {
		t.Fatalf("RawProvider = %T, want the custom provider", raw)
	}
	if provider.Name() != "my-gateway" {
		t.Errorf("Name() = %q, want the configured id", provider.Name())
	}
	if provider.Config.Doer != core.Doer(doer) {
		t.Error("Config.Doer must reach the provider")
	}
	if provider.Config.Validate == nil || *provider.Config.Validate {
		t.Error("Config.Validate must reach the provider")
	}
	if provider.Config.Retry == nil || provider.Config.Retry.Attempts != 3 {
		t.Error("Config.Retry must reach the provider")
	}
	if provider.Config.Logger != core.Logger(logger) {
		t.Error("Config.Logger must reach the provider")
	}

	// Validation is off, so an invalid card still reaches the transport.
	result, err := payment.CreatePayment(context.Background(), operationsTestWithCard(posTestRequest(), "1"))
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if result.PaymentID != "T-DOER" {
		t.Errorf("paymentId = %q, want T-DOER", result.PaymentID)
	}
	if doer.count() != 1 {
		t.Errorf("the transport ran %d times, want 1", doer.count())
	}
}

func TestCustomProviderUsesTheConfiguredTransport(t *testing.T) {
	doer := &customTestDoer{response: `{"approved":true,"transactionId":"T-9"}`}
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customProviderConfig{TerminalID: "T1", SecretKey: "s"}),
		},
		Doer: doer,
	})
	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.PaymentID != "T-9" {
		t.Errorf("paymentId = %q, want T-9", result.PaymentID)
	}
	doer.mu.Lock()
	defer doer.mu.Unlock()
	if len(doer.requests) != 1 {
		t.Fatalf("the transport ran %d times, want 1", len(doer.requests))
	}
	if got := doer.requests[0].URL.String(); got != "https://api.custom-gateway.example/payments" {
		t.Errorf("URL = %q, want the default host and /payments", got)
	}
}

func TestCustomProviderWithoutAResponseIsPending(t *testing.T) {
	doer := &customTestDoer{err: errors.New("connection reset by peer")}
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customProviderConfig{TerminalID: "T1", SecretKey: "s"}),
		},
		Doer: doer,
	})
	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending: the gateway may have charged the card", result.Status)
	}
	if result.ErrorCode != core.NetworkErrorCode {
		t.Errorf("errorCode = %q, want %q", result.ErrorCode, core.NetworkErrorCode)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}

func TestCustomProviderReportsAGatewayError(t *testing.T) {
	server, gateway := customTestServe(t, `{"message":"terminal disabled"}`)
	gateway.mu.Lock()
	gateway.status = http.StatusInternalServerError
	gateway.mu.Unlock()
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customTestConfig(server.URL)),
		},
	})
	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("httpStatus = %d, want 500", result.HTTPStatus)
	}
	if result.Code == "" {
		t.Error("a failed result must carry a normalized code")
	}
}

func TestCustomProviderEmitsEventsAndRunsHooks(t *testing.T) {
	ctx := context.Background()
	server, _ := customTestServe(t, `{"approved":true,"transactionId":"T-1"}`)
	var events []*core.PaymentEvent
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customTestConfig(server.URL)),
		},
		Plugins: []core.Plugin{core.DefinePlugin(core.Plugin{
			ID: "tag",
			Hooks: core.PluginHooks{Before: []core.BeforeHook{{
				Handler: func(octx core.OperationContext) (*core.BeforeHookResult, error) {
					request, ok := octx.Request.(*core.PaymentRequest)
					if !ok {
						return nil, nil
					}
					replaced := *request
					replaced.ConversationID = "CUSTOM-1"
					return &core.BeforeHookResult{Request: &replaced}, nil
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
	result, err := payment.CreatePayment(ctx, posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.ConversationID != "CUSTOM-1" {
		t.Errorf("conversationId = %q, want CUSTOM-1", result.ConversationID)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	event := events[0]
	if event.Type != core.EventPaymentSucceeded {
		t.Errorf("type = %q, want payment.succeeded", event.Type)
	}
	if event.Provider != "custom" {
		t.Errorf("provider = %q, want custom", event.Provider)
	}
	if event.PaymentID != "T-1" {
		t.Errorf("paymentId = %q, want T-1", event.PaymentID)
	}
	if event.ConversationID != "CUSTOM-1" {
		t.Errorf("conversationId = %q, want CUSTOM-1", event.ConversationID)
	}
}

func TestCustomProviderUnsupportedOperations(t *testing.T) {
	ctx := context.Background()
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customProviderConfig{TerminalID: "T1", SecretKey: "s"}),
		},
	})
	calls := []struct {
		name string
		run  func() error
	}{
		{"initThreeDSPayment", func() error {
			_, err := payment.InitThreeDSPayment(ctx, operationsTestThreeDS())
			return err
		}},
		{"completeThreeDSPayment", func() error {
			_, err := payment.CompleteThreeDSPayment(ctx, map[string]any{})
			return err
		}},
		{"refund", func() error {
			_, err := payment.Refund(ctx, operationsTestRefund("T-1", "1"))
			return err
		}},
		{"cancel", func() error {
			_, err := payment.Cancel(ctx, &core.CancelRequest{PaymentID: "T-1", IP: "1.1.1.1"})
			return err
		}},
		{"getPayment", func() error {
			_, err := payment.GetPayment(ctx, "T-1")
			return err
		}},
		{"authorize", func() error {
			_, err := payment.Authorize(ctx, posTestRequest())
			return err
		}},
		{"capture", func() error {
			_, err := payment.Capture(ctx, &core.CaptureRequest{PaymentID: "T-1", Amount: "1", IP: "1.1.1.1"})
			return err
		}},
		{"saveCard", func() error {
			_, err := payment.SaveCard(ctx, &core.SaveCardRequest{Card: postesting.FixtureCard()})
			return err
		}},
		{"binCheck", func() error {
			_, err := payment.BinCheck(ctx, "552879")
			return err
		}},
		{"installmentInfo", func() error {
			_, err := payment.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "552879", Price: "1"})
			return err
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			err := call.run()
			if err == nil {
				t.Fatalf("%s: want a NOT_SUPPORTED error", call.name)
			}
			var base *core.BaseError
			if !errors.As(err, &base) {
				t.Fatalf("error = %T, want a library error", err)
			}
			if base.Code != "NOT_SUPPORTED" {
				t.Errorf("code = %q, want NOT_SUPPORTED", base.Code)
			}
			if base.Provider != "custom" {
				t.Errorf("provider = %q, want custom", base.Provider)
			}
		})
	}
}

func TestCustomProviderServesTheHandlerRoute(t *testing.T) {
	server, gateway := customTestServe(t, `{"approved":true,"transactionId":"T-1"}`)
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{
			"custom": customProviderDefinition(customTestConfig(server.URL)),
		},
		Handler: &core.HandlerOptions{
			AllowedActions: []core.HandlerAction{core.ActionPayment},
			Authorize:      func(context.Context, core.HandlerContext) (bool, error) { return true, nil },
		},
	})
	response := operationsTestHandle(t, payment.Handler(), operationsTestPost("/api/pay/custom/payment", posTestRequest()))
	operationsTestWantStatus(t, response, http.StatusOK)
	body := operationsTestBody(t, response)
	if body["status"] != string(core.StatusSuccess) {
		t.Errorf("status = %v, want success", body["status"])
	}
	if body["paymentId"] != "T-1" {
		t.Errorf("paymentId = %v, want T-1", body["paymentId"])
	}
	if _, exposed := body["rawResponse"]; exposed {
		t.Error("rawResponse must stay out of the HTTP response by default")
	}
	if calls := gateway.recorded(); len(calls) != 1 {
		t.Errorf("the gateway received %d calls, want 1", len(calls))
	}
}

func TestCustomProviderInstanceIsAcceptedDirectly(t *testing.T) {
	server, _ := customTestServe(t, `{"approved":true,"transactionId":"T-2"}`)
	definition := customProviderDefinition(customTestConfig(server.URL))
	provider, err := definition.Create(core.ProviderSetupContext{ID: "custom", Mode: core.ModeSandbox})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	payment := customTestPayment(t, Config{
		Providers: map[string]core.ProviderEntry{"custom": provider},
	})
	raw, err := payment.RawProvider("custom")
	if err != nil {
		t.Fatalf("RawProvider: %v", err)
	}
	if raw != provider {
		t.Error("a ready instance must be used as configured")
	}
	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.PaymentID != "T-2" {
		t.Errorf("paymentId = %q, want T-2", result.PaymentID)
	}
}

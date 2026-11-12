package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestDefinePluginReturnsThePluginUnchanged(t *testing.T) {
	endpoint := PaymentEndpoint{Method: "POST", Path: "/router/quote"}
	plugin := Plugin{
		ID:      "router",
		Version: "1.2.3",
		Options: map[string]any{"spread": 2},
		Hooks: PluginHooks{
			Before: []BeforeHook{{Handler: func(OperationContext) (*BeforeHookResult, error) { return nil, nil }}},
			After:  []AfterHook{{Handler: func(OperationResultContext) (*AfterHookResult, error) { return nil, nil }}},
		},
		Events:     PaymentEventListeners{EventAll: func(context.Context, *PaymentEvent) error { return nil }},
		Endpoints:  map[string]PaymentEndpoint{"quote": endpoint},
		ErrorCodes: map[string]string{"LIMIT_REACHED": "Limit reached"},
	}
	got := DefinePlugin(plugin)
	if got.ID != plugin.ID || got.Version != plugin.Version {
		t.Errorf("DefinePlugin changed the identity: %+v", got)
	}
	if len(got.Hooks.Before) != 1 || len(got.Hooks.After) != 1 {
		t.Errorf("DefinePlugin changed the hooks: %+v", got.Hooks)
	}
	if len(got.Events) != 1 || len(got.Endpoints) != 1 {
		t.Errorf("DefinePlugin changed the events or endpoints: %+v", got)
	}
	if got.ErrorCodes["LIMIT_REACHED"] != "Limit reached" {
		t.Errorf("DefinePlugin changed the error codes: %v", got.ErrorCodes)
	}
	if !reflect.DeepEqual(got.Options, plugin.Options) {
		t.Errorf("DefinePlugin changed the options: %v", got.Options)
	}
}

func TestAllPaymentOperationsAreCompleteAndUnique(t *testing.T) {
	want := []PaymentOperation{
		OperationCreatePayment,
		OperationInitThreeDSPayment,
		OperationCompleteThreeDSPayment,
		OperationAuthorize,
		OperationInitThreeDSAuthorize,
		OperationCapture,
		OperationVoidAuthorization,
		OperationRefund,
		OperationCancel,
		OperationGetPayment,
		OperationSaveCard,
		OperationListCards,
		OperationDeleteCard,
		OperationBinCheck,
		OperationInstallmentInfo,
	}
	if len(AllPaymentOperations) != len(want) {
		t.Fatalf("AllPaymentOperations has %d entries, want %d", len(AllPaymentOperations), len(want))
	}
	seen := make(map[PaymentOperation]bool, len(want))
	for i, operation := range want {
		if AllPaymentOperations[i] != operation {
			t.Errorf("AllPaymentOperations[%d] = %q, want %q", i, AllPaymentOperations[i], operation)
		}
		if seen[operation] {
			t.Errorf("AllPaymentOperations lists %q twice", operation)
		}
		seen[operation] = true
	}
}

func TestPaymentOperationValues(t *testing.T) {
	cases := []struct {
		got  PaymentOperation
		want string
	}{
		{OperationCreatePayment, "createPayment"},
		{OperationInitThreeDSPayment, "initThreeDSPayment"},
		{OperationCompleteThreeDSPayment, "completeThreeDSPayment"},
		{OperationAuthorize, "authorize"},
		{OperationInitThreeDSAuthorize, "initThreeDSAuthorize"},
		{OperationCapture, "capture"},
		{OperationVoidAuthorization, "voidAuthorization"},
		{OperationRefund, "refund"},
		{OperationCancel, "cancel"},
		{OperationGetPayment, "getPayment"},
		{OperationSaveCard, "saveCard"},
		{OperationListCards, "listCards"},
		{OperationDeleteCard, "deleteCard"},
		{OperationBinCheck, "binCheck"},
		{OperationInstallmentInfo, "installmentInfo"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Errorf("operation = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestPluginZeroValueIsInert(t *testing.T) {
	var plugin Plugin
	if plugin.Init != nil || plugin.Methods != nil || plugin.OnRequest != nil || plugin.OnResponse != nil {
		t.Error("a zero plugin must have no callbacks")
	}
	if len(plugin.Hooks.Before) != 0 || len(plugin.Hooks.After) != 0 {
		t.Error("a zero plugin must have no hooks")
	}
	if len(plugin.Events) != 0 || len(plugin.Endpoints) != 0 || len(plugin.ErrorCodes) != 0 {
		t.Error("a zero plugin must register nothing")
	}
}

func TestOperationContextCarriesTheCall(t *testing.T) {
	request := &PaymentRequest{Price: "1.00"}
	ctx := OperationContext{
		Operation: OperationCreatePayment,
		Request:   request,
		Provider:  "test-pos",
		Routable:  true,
	}
	if ctx.Request != request {
		t.Error("Request must be the caller's request")
	}
	result := &PaymentResponse{Status: StatusSuccess}
	after := OperationResultContext{OperationContext: ctx, Result: result}
	if after.Operation != ctx.Operation || after.Provider != ctx.Provider || after.Routable != ctx.Routable {
		t.Errorf("OperationResultContext lost the operation context: %+v", after)
	}
	if after.Result != result {
		t.Error("Result must be the provider's result")
	}
}

func TestHookResultsAreOptional(t *testing.T) {
	var before BeforeHookResult
	if before.Provider != "" || before.Request != nil || before.Result != nil {
		t.Errorf("a zero BeforeHookResult must leave the call unchanged: %+v", before)
	}
	var after AfterHookResult
	if after.Result != nil || after.Request != nil {
		t.Errorf("a zero AfterHookResult must leave the result unchanged: %+v", after)
	}

	// A nil matcher means the hook runs for every operation.
	hook := BeforeHook{Handler: func(OperationContext) (*BeforeHookResult, error) { return nil, nil }}
	if hook.Matcher != nil {
		t.Error("a hook without a matcher must run for every operation")
	}
	for _, operation := range AllPaymentOperations {
		if hook.Matcher != nil && !hook.Matcher(OperationContext{Operation: operation}) {
			t.Errorf("hook skipped %q", operation)
		}
	}
}

func TestRegisteredEndpointJSON(t *testing.T) {
	endpoint := PaymentEndpoint{
		Method:     "POST",
		Path:       "/router/quote",
		Privileged: true,
		AcceptForm: true,
		OpenAPI:    &EndpointOpenAPIMetadata{Summary: "Quote", Tags: []string{"router"}},
	}
	registered := RegisteredEndpoint{PaymentEndpoint: endpoint, PluginID: "router", Name: "quote"}
	if registered.Method != "POST" || registered.Path != "/router/quote" {
		t.Errorf("RegisteredEndpoint lost the endpoint: %+v", registered)
	}

	data, err := json.Marshal(registered)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"Method", "Path", "Privileged", "AcceptForm", "PluginID", "Name"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("RegisteredEndpoint JSON is missing %q: %s", key, data)
		}
	}
}

func TestEndpointInfoJSON(t *testing.T) {
	info := EndpointInfo{
		PluginID:   "router",
		Name:       "quote",
		Method:     "POST",
		Path:       "/router/quote",
		Privileged: true,
		OpenAPI:    &EndpointOpenAPIMetadata{Summary: "Quote", Description: "Returns a quote"},
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"pluginId":"router","name":"quote","method":"POST","path":"/router/quote","privileged":true,` +
		`"openapi":{"summary":"Quote","description":"Returns a quote"}}`
	if string(data) != want {
		t.Errorf("EndpointInfo JSON = %s, want %s", data, want)
	}

	var back EndpointInfo
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, info) {
		t.Errorf("round trip = %+v, want %+v", back, info)
	}
}

func TestEndpointInfoOmitsEmptyOpenAPI(t *testing.T) {
	data, err := json.Marshal(EndpointInfo{PluginID: "p", Name: "n", Method: "GET", Path: "/n"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"pluginId":"p","name":"n","method":"GET","path":"/n","privileged":false}` {
		t.Errorf("EndpointInfo JSON = %s, want no openapi key", data)
	}
}

func TestPaymentEndpointFormBodiesAreOffByDefault(t *testing.T) {
	var endpoint PaymentEndpoint
	if endpoint.AcceptForm {
		t.Error("AcceptForm must default to false: a cross-site form must not reach an endpoint")
	}
	if endpoint.Privileged {
		t.Error("Privileged must default to false")
	}
}

// pluginTestContext is a minimal PluginContext, the contract a plugin is written
// against.
type pluginTestContext struct {
	ids      []string
	provider Provider
	codes    map[string]string
	def      string
	mode     Mode
	logger   Logger
	emitted  []*PaymentEvent
	info     []EndpointInfo
}

func (c *pluginTestContext) ProviderIDs() []string         { return c.ids }
func (c *pluginTestContext) ErrorCodes() map[string]string { return c.codes }
func (c *pluginTestContext) DefaultProvider() string       { return c.def }
func (c *pluginTestContext) Mode() Mode                    { return c.mode }
func (c *pluginTestContext) Logger() Logger                { return c.logger }
func (c *pluginTestContext) Endpoints() []EndpointInfo     { return c.info }

func (c *pluginTestContext) Use(providerID string) (Provider, error) {
	if providerID != c.def {
		return nil, NewProviderNotEnabledError(providerID)
	}
	return c.provider, nil
}

func (c *pluginTestContext) Emit(_ context.Context, event *PaymentEvent) error {
	c.emitted = append(c.emitted, event)
	return nil
}

var _ PluginContext = (*pluginTestContext)(nil)

func TestPluginInitAndMethodsUseThePluginContext(t *testing.T) {
	provider := NewBaseProvider("test-pos", ProviderConfig{})
	pctx := &pluginTestContext{
		ids:      []string{"test-pos"},
		provider: stubPluginProvider{BaseProvider: provider},
		codes:    map[string]string{"LIMIT_REACHED": "Limit reached"},
		def:      "test-pos",
		mode:     ModeSandbox,
		logger:   NopLogger{},
		info:     []EndpointInfo{{PluginID: "counter", Name: "stats", Method: "GET", Path: "/counter/stats"}},
	}

	var initialized int
	plugin := DefinePlugin(Plugin{
		ID: "counter",
		Init: func(_ context.Context, ctx PluginContext) error {
			initialized++
			if ctx.Mode() != ModeSandbox {
				t.Errorf("Mode() = %q, want sandbox", ctx.Mode())
			}
			if ctx.DefaultProvider() != "test-pos" {
				t.Errorf("DefaultProvider() = %q, want test-pos", ctx.DefaultProvider())
			}
			return nil
		},
		Methods: func(_ context.Context, ctx PluginContext) (map[string]any, error) {
			return map[string]any{
				"counter": map[string]any{
					"providers": ctx.ProviderIDs(),
					"codes":     ctx.ErrorCodes(),
					"endpoints": ctx.Endpoints(),
				},
			}, nil
		},
	})

	ctx := context.Background()
	if err := plugin.Init(ctx, pctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if initialized != 1 {
		t.Errorf("Init ran %d times, want 1", initialized)
	}
	methods, err := plugin.Methods(ctx, pctx)
	if err != nil {
		t.Fatalf("Methods: %v", err)
	}
	counter, ok := methods["counter"].(map[string]any)
	if !ok {
		t.Fatalf("Methods returned %v, want a counter member", methods)
	}
	if !reflect.DeepEqual(counter["providers"], []string{"test-pos"}) {
		t.Errorf("providers = %v, want [test-pos]", counter["providers"])
	}
	if counter["codes"].(map[string]string)["LIMIT_REACHED"] != "Limit reached" {
		t.Errorf("codes = %v, want the plugin error codes", counter["codes"])
	}
	if len(counter["endpoints"].([]EndpointInfo)) != 1 {
		t.Errorf("endpoints = %v, want the registered endpoint", counter["endpoints"])
	}

	used, err := pctx.Use("test-pos")
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if used.Name() != "test-pos" {
		t.Errorf("Name() = %q, want test-pos", used.Name())
	}
	if _, err := pctx.Use("other"); err == nil {
		t.Error("Use of an unconfigured provider: want an error")
	} else {
		var notEnabled *ProviderNotEnabledError
		if !errors.As(err, &notEnabled) {
			t.Errorf("Use error = %T, want a ProviderNotEnabledError", err)
		}
	}

	event := &PaymentEvent{Type: EventPaymentSucceeded, Provider: "test-pos"}
	if err := pctx.Emit(ctx, event); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(pctx.emitted) != 1 || pctx.emitted[0] != event {
		t.Errorf("Emit recorded %v, want the event", pctx.emitted)
	}
}

func TestPluginMethodsErrorStopsRegistration(t *testing.T) {
	failure := errors.New("rates unavailable")
	plugin := DefinePlugin(Plugin{
		ID: "rates",
		Methods: func(context.Context, PluginContext) (map[string]any, error) {
			return nil, failure
		},
	})
	if _, err := plugin.Methods(context.Background(), &pluginTestContext{}); !errors.Is(err, failure) {
		t.Errorf("Methods error = %v, want %v", err, failure)
	}
}

func TestPluginEventListenersAreKeyedByEventName(t *testing.T) {
	var typed, all int
	plugin := DefinePlugin(Plugin{
		ID: "log",
		Events: PaymentEventListeners{
			pluginTestEventName(EventPaymentSucceeded): func(context.Context, *PaymentEvent) error { typed++; return nil },
			EventAll: func(context.Context, *PaymentEvent) error { all++; return nil },
		},
	})
	ctx := context.Background()
	if err := plugin.Events[pluginTestEventName(EventPaymentSucceeded)](ctx, &PaymentEvent{}); err != nil {
		t.Fatalf("typed listener: %v", err)
	}
	if err := plugin.Events[EventAll](ctx, &PaymentEvent{}); err != nil {
		t.Fatalf("wildcard listener: %v", err)
	}
	if typed != 1 || all != 1 {
		t.Errorf("listeners ran %d/%d times, want 1/1", typed, all)
	}
	if len(plugin.Events) != 2 {
		t.Errorf("Events has %d entries, want 2", len(plugin.Events))
	}
}

// pluginTestEventName converts an event type to the listener map key.
func pluginTestEventName(eventType string) PaymentEventName { return PaymentEventName(eventType) }

// stubPluginProvider is a provider that only implements the mandatory operations.
type stubPluginProvider struct{ BaseProvider }

func (stubPluginProvider) CreatePayment(context.Context, *PaymentRequest) (*PaymentResponse, error) {
	return &PaymentResponse{Status: StatusSuccess}, nil
}

func (stubPluginProvider) InitThreeDSPayment(context.Context, *ThreeDSPaymentRequest) (*ThreeDSInitResponse, error) {
	return &ThreeDSInitResponse{Status: StatusPending}, nil
}

func (stubPluginProvider) CompleteThreeDSPayment(context.Context, any) (*PaymentResponse, error) {
	return &PaymentResponse{Status: StatusSuccess}, nil
}

func (stubPluginProvider) Refund(context.Context, *RefundRequest) (*RefundResponse, error) {
	return &RefundResponse{Status: StatusSuccess}, nil
}

func (stubPluginProvider) Cancel(context.Context, *CancelRequest) (*CancelResponse, error) {
	return &CancelResponse{Status: StatusSuccess}, nil
}

func (stubPluginProvider) GetPayment(context.Context, string) (*PaymentResponse, error) {
	return &PaymentResponse{Status: StatusSuccess}, nil
}

var _ Provider = stubPluginProvider{}

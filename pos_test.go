package pos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
	postesting "github.com/boracomet/go-pos/testing"
)

// posTestRequest returns a valid payment request.
func posTestRequest() *core.PaymentRequest {
	request := postesting.FixturePaymentRequest()
	return &request
}

// posTestNew builds a payment object, failing the test on a configuration error.
func posTestNew(t *testing.T, cfg Config) *Payment {
	t.Helper()
	payment, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return payment
}

// posTestMocks builds a payment object over one in-memory provider per id.
func posTestMocks(t *testing.T, ids ...string) (*Payment, map[string]*postesting.MockProvider) {
	t.Helper()
	providers := make(map[string]core.ProviderEntry, len(ids))
	mocks := make(map[string]*postesting.MockProvider, len(ids))
	for _, id := range ids {
		mock := postesting.NewMockProvider()
		providers[id] = mock
		mocks[id] = mock
	}
	return posTestNew(t, Config{Providers: providers}), mocks
}

func posTestWantConfigurationError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("New: want a ConfigurationError containing %q", contains)
	}
	var configuration *core.ConfigurationError
	if !errors.As(err, &configuration) {
		t.Fatalf("New error = %T (%v), want a ConfigurationError", err, err)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Errorf("New error = %q, want it to contain %q", err, contains)
	}
}

func TestNewEnablesOnlyTheConfiguredProviders(t *testing.T) {
	payment, _ := posTestMocks(t, "iyzico", "paytr")
	if !payment.IsProviderEnabled("iyzico") || !payment.IsProviderEnabled("paytr") {
		t.Error("the configured providers must be enabled")
	}
	if payment.IsProviderEnabled("akbank") {
		t.Error("an unconfigured provider must not be enabled")
	}
	want := []string{"iyzico", "paytr"}
	got := payment.EnabledProviders()
	if len(got) != len(want) {
		t.Fatalf("EnabledProviders() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("EnabledProviders() = %v, want %v in id order", got, want)
		}
	}

	// The returned slice is a copy.
	got[0] = "changed"
	if payment.EnabledProviders()[0] != "iyzico" {
		t.Error("EnabledProviders() must return a copy")
	}
}

func TestNewAcceptsAnyProviderID(t *testing.T) {
	payment, _ := posTestMocks(t, "test-pos")
	if !payment.IsProviderEnabled("test-pos") {
		t.Error("a custom provider id must be accepted")
	}
	if payment.DefaultProvider() != "test-pos" {
		t.Errorf("DefaultProvider() = %q, want test-pos", payment.DefaultProvider())
	}
}

func TestNewInfersTheDefaultProvider(t *testing.T) {
	cases := []struct {
		name     string
		ids      []string
		explicit string
		want     string
	}{
		{"one provider", []string{"iyzico"}, "", "iyzico"},
		{"two providers", []string{"iyzico", "paytr"}, "", ""},
		{"explicit default", []string{"iyzico", "paytr"}, "paytr", "paytr"},
		{"explicit default with one provider", []string{"iyzico"}, "iyzico", "iyzico"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providers := make(map[string]core.ProviderEntry, len(tc.ids))
			for _, id := range tc.ids {
				providers[id] = postesting.NewMockProvider()
			}
			payment := posTestNew(t, Config{Providers: providers, DefaultProvider: tc.explicit})
			if got := payment.DefaultProvider(); got != tc.want {
				t.Errorf("DefaultProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewRejectsAnUnknownDefaultProvider(t *testing.T) {
	_, err := New(Config{
		Providers:       map[string]core.ProviderEntry{"iyzico": postesting.NewMockProvider()},
		DefaultProvider: "paytr",
	})
	posTestWantConfigurationError(t, err, "Default provider 'paytr'")
}

func TestNewRejectsReservedProviderIDs(t *testing.T) {
	for _, id := range []string{"Refund", "refund", "CreatePayment", "Use", "handler", "Mode", "On", "Methods", "Run"} {
		t.Run(id, func(t *testing.T) {
			_, err := New(Config{Providers: map[string]core.ProviderEntry{id: postesting.NewMockProvider()}})
			posTestWantConfigurationError(t, err, "clashes")
		})
	}
}

func TestNewRejectsAnEmptyProviderID(t *testing.T) {
	_, err := New(Config{Providers: map[string]core.ProviderEntry{"": postesting.NewMockProvider()}})
	posTestWantConfigurationError(t, err, "needs a name")
}

func TestNewRejectsInvalidProviderEntries(t *testing.T) {
	var typedNil *postesting.MockProvider
	cases := []struct {
		name  string
		entry core.ProviderEntry
	}{
		{"nil", nil},
		{"typed nil", typedNil},
		{"empty object", struct{}{}},
		{"unknown provider id", map[string]any{"apiKey": "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Providers: map[string]core.ProviderEntry{"x": tc.entry}})
			posTestWantConfigurationError(t, err, "")
		})
	}
}

func TestNewRejectsAConfigurationOfAnotherProvider(t *testing.T) {
	_, err := New(Config{Providers: map[string]core.ProviderEntry{
		"iyzico": PayTRConfig{MerchantID: "1", MerchantKey: "k", MerchantSalt: "s"},
	}})
	posTestWantConfigurationError(t, err, "expects")
}

func TestNewReportsMissingCredentials(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		entry   core.ProviderEntry
		missing []string
	}{
		{
			name:    "iyzico without an api key",
			id:      "iyzico",
			entry:   IyzicoConfig{SecretKey: "s"},
			missing: []string{"apiKey"},
		},
		{
			name:    "iyzico without both keys",
			id:      "iyzico",
			entry:   IyzicoConfig{},
			missing: []string{"apiKey", "secretKey"},
		},
		{
			name:    "paytr without a merchant id",
			id:      "paytr",
			entry:   PayTRConfig{MerchantKey: "k", MerchantSalt: "s"},
			missing: []string{"merchantId"},
		},
		{
			name:    "paytr without a key and salt",
			id:      "paytr",
			entry:   PayTRConfig{MerchantID: "1"},
			missing: []string{"merchantKey", "merchantSalt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Providers: map[string]core.ProviderEntry{tc.id: tc.entry}})
			posTestWantConfigurationError(t, err, "")
			for _, field := range tc.missing {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("New error = %q, want it to name %q", err, field)
				}
			}
		})
	}
}

func TestNewBuildsTheBuiltInProviders(t *testing.T) {
	payment := posTestNew(t, Config{
		Mode: core.ModeSandbox,
		Providers: map[string]core.ProviderEntry{
			"iyzico": Iyzico(IyzicoConfig{APIKey: "k", SecretKey: "s"}),
			"paytr":  PayTR(PayTRConfig{MerchantID: "1", MerchantKey: "k", MerchantSalt: "s"}),
		},
	})
	for _, id := range []string{"iyzico", "paytr"} {
		raw, err := payment.RawProvider(id)
		if err != nil {
			t.Fatalf("RawProvider(%q): %v", id, err)
		}
		if raw.Name() != id {
			t.Errorf("Name() = %q, want %q", raw.Name(), id)
		}
		if _, ok := raw.(*postesting.MockProvider); ok {
			t.Errorf("RawProvider(%q) = %T, want a built-in provider", id, raw)
		}
	}
}

func TestUseReturnsAHookedProvider(t *testing.T) {
	payment, mocks := posTestMocks(t, "a")
	hooked, err := payment.Use("a")
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if hooked == core.Provider(mocks["a"]) {
		t.Error("Use must return a proxy, not the raw provider")
	}
	if hooked.Name() != mocks["a"].Name() {
		t.Errorf("Name() = %q, want %q", hooked.Name(), mocks["a"].Name())
	}

	raw, err := payment.RawProvider("a")
	if err != nil {
		t.Fatalf("RawProvider: %v", err)
	}
	if raw != core.Provider(mocks["a"]) {
		t.Error("RawProvider must return the configured instance")
	}
	if _, ok := raw.(*postesting.MockProvider); !ok {
		t.Errorf("RawProvider = %T, want *testing.MockProvider", raw)
	}
}

func TestUseUnknownProvider(t *testing.T) {
	payment, _ := posTestMocks(t, "iyzico")
	for _, id := range []string{"paytr", "stripe", ""} {
		_, err := payment.Use(id)
		var notEnabled *core.ProviderNotEnabledError
		if !errors.As(err, &notEnabled) {
			t.Errorf("Use(%q) error = %T (%v), want a ProviderNotEnabledError", id, err, err)
			continue
		}
		if notEnabled.Provider != id {
			t.Errorf("Provider = %q, want %q", notEnabled.Provider, id)
		}
		if notEnabled.Code != "PROVIDER_NOT_ENABLED" {
			t.Errorf("Code = %q, want PROVIDER_NOT_ENABLED", notEnabled.Code)
		}
	}
}

func TestRawProviderUnknownProvider(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	if _, err := payment.RawProvider("b"); err == nil {
		t.Error("RawProvider(b): want a ProviderNotEnabledError")
	} else {
		var notEnabled *core.ProviderNotEnabledError
		if !errors.As(err, &notEnabled) {
			t.Errorf("error = %T, want a ProviderNotEnabledError", err)
		}
	}
}

func TestRawProviderUsesTheDefaultProvider(t *testing.T) {
	payment, mocks := posTestMocks(t, "a")
	raw, err := payment.RawProvider("")
	if err != nil {
		t.Fatalf("RawProvider(\"\"): %v", err)
	}
	if raw != core.Provider(mocks["a"]) {
		t.Error("an empty id must resolve to the default provider")
	}
}

func TestRawProviderWithoutADefault(t *testing.T) {
	payment, _ := posTestMocks(t, "a", "b")
	_, err := payment.RawProvider("")
	posTestWantConfigurationError(t, err, "No default provider")
}

func TestModeDefaultsToProduction(t *testing.T) {
	cases := []struct {
		name string
		mode core.Mode
		want core.Mode
	}{
		{"unset", "", core.ModeProduction},
		{"production", core.ModeProduction, core.ModeProduction},
		{"sandbox", core.ModeSandbox, core.ModeSandbox},
		{"unknown", core.Mode("staging"), core.ModeProduction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built := posTestNew(t, Config{
				Mode:      tc.mode,
				Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
			})
			if got := built.Mode(); got != tc.want {
				t.Errorf("Mode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProviderURLsForMode(t *testing.T) {
	for providerType, urls := range core.ProviderDefaultURLs {
		if urls.Sandbox == "" || urls.Production == "" {
			t.Errorf("%s has an empty host: %+v", providerType, urls)
		}
		if got := urls.For(core.ModeSandbox); got != urls.Sandbox {
			t.Errorf("%s.For(sandbox) = %q, want %q", providerType, got, urls.Sandbox)
		}
		for _, mode := range []core.Mode{core.ModeProduction, "", core.Mode("staging")} {
			if got := urls.For(mode); got != urls.Production {
				t.Errorf("%s.For(%q) = %q, want %q", providerType, mode, got, urls.Production)
			}
		}
	}
}

func TestSandboxSelectsTheTestHost(t *testing.T) {
	cases := []struct {
		name    string
		mode    core.Mode
		baseURL string
		want    string
	}{
		{"sandbox", core.ModeSandbox, "", "https://sandbox-api.iyzipay.com"},
		{"production", core.ModeProduction, "", "https://api.iyzipay.com"},
		{"default mode", "", "", "https://api.iyzipay.com"},
		{"explicit host wins", core.ModeSandbox, "https://pos.example.com", "https://pos.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &posTestDoer{body: `{"status":"success","paymentId":"P1"}`}
			config := IyzicoConfig{APIKey: "k", SecretKey: "s"}
			config.BaseURL = tc.baseURL
			payment := posTestNew(t, Config{
				Mode:      tc.mode,
				Doer:      doer,
				Providers: map[string]core.ProviderEntry{"iyzico": config},
			})
			if _, err := payment.GetPayment(context.Background(), "P1"); err != nil {
				t.Fatalf("GetPayment: %v", err)
			}
			urls := doer.urls()
			if len(urls) != 1 {
				t.Fatalf("recorded %d calls, want 1: %v", len(urls), urls)
			}
			if !strings.HasPrefix(urls[0], tc.want+"/") {
				t.Errorf("call URL = %q, want the host %q", urls[0], tc.want)
			}
		})
	}
}

func TestSetupContextCarriesTheSharedSettings(t *testing.T) {
	logger := &posTestLogger{}
	doer := &posTestDoer{}
	validate := false
	retry := &core.RetryConfig{Attempts: 3}

	var got core.ProviderSetupContext
	definition := core.DefineProvider(func(setup core.ProviderSetupContext) (core.Provider, error) {
		got = setup
		return postesting.NewMockProvider(), nil
	})
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"test-pos": definition},
		Mode:      core.ModeSandbox,
		Logger:    logger,
		Doer:      doer,
		Validate:  &validate,
		Retry:     retry,
	})
	if got.ID != "test-pos" {
		t.Errorf("ID = %q, want test-pos", got.ID)
	}
	if got.Mode != core.ModeSandbox {
		t.Errorf("Mode = %q, want sandbox", got.Mode)
	}
	if got.Logger != core.Logger(logger) {
		t.Error("Logger was not passed to the definition")
	}
	if got.Doer != core.Doer(doer) {
		t.Error("Doer was not passed to the definition")
	}
	if got.Validate == nil || *got.Validate {
		t.Errorf("Validate = %v, want false", got.Validate)
	}
	if got.Retry != retry {
		t.Error("Retry was not passed to the definition")
	}
	if payment.DefaultProvider() != "test-pos" {
		t.Errorf("DefaultProvider() = %q, want test-pos", payment.DefaultProvider())
	}
}

func TestOnReturnsAnUnsubscribeFunc(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	ctx := context.Background()

	var calls int
	off := payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		calls++
		return nil
	})
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if calls != 1 {
		t.Fatalf("listener ran %d times, want 1", calls)
	}

	off()
	if _, err := payment.CreatePayment(ctx, posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if calls != 1 {
		t.Errorf("listener ran %d times after unsubscribing, want 1", calls)
	}
	// Unsubscribing twice is harmless.
	off()
}

func TestOnIgnoresANilListener(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	off := payment.On(core.EventAll, nil)
	if off == nil {
		t.Fatal("On must return an unsubscribe func even for a nil listener")
	}
	off()
	if _, err := payment.CreatePayment(context.Background(), posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
}

func TestEventListenerErrorKeepsTheResult(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	cause := errors.New("database down")
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error { return cause })

	result, err := payment.CreatePayment(context.Background(), posTestRequest())
	if result != nil {
		t.Error("CreatePayment must return no result when a listener fails")
	}
	var listenerErr *core.EventListenerError
	if !errors.As(err, &listenerErr) {
		t.Fatalf("error = %T (%v), want an EventListenerError", err, err)
	}
	if !errors.Is(err, cause) {
		t.Error("the listener's error must be unwrappable")
	}
	if !strings.Contains(err.Error(), "database down") {
		t.Errorf("Error() = %q, want it to contain the cause", err)
	}
	paid, ok := listenerErr.Result.(*core.PaymentResponse)
	if !ok {
		t.Fatalf("Result = %T, want the payment response", listenerErr.Result)
	}
	if paid.Status != core.StatusSuccess || paid.PaymentID == "" {
		t.Errorf("Result = %+v, want the completed payment", paid)
	}
	if listenerErr.Event == nil || listenerErr.Event.Type != core.EventPaymentSucceeded {
		t.Errorf("Event = %+v, want payment.succeeded", listenerErr.Event)
	}
}

func TestWildcardListenerRunsAfterTheTypedOne(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	var order []string
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		order = append(order, "typed")
		return nil
	})
	payment.On(core.EventAll, func(context.Context, *core.PaymentEvent) error {
		order = append(order, "all")
		return nil
	})
	if _, err := payment.CreatePayment(context.Background(), posTestRequest()); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if len(order) != 2 || order[0] != "typed" || order[1] != "all" {
		t.Errorf("listener order = %v, want [typed all]", order)
	}
}

func TestAListenerPanicBecomesAnError(t *testing.T) {
	payment, _ := posTestMocks(t, "a")
	payment.On(core.EventPaymentSucceeded, func(context.Context, *core.PaymentEvent) error {
		panic("listener exploded")
	})
	_, err := payment.CreatePayment(context.Background(), posTestRequest())
	var listenerErr *core.EventListenerError
	if !errors.As(err, &listenerErr) {
		t.Fatalf("error = %T (%v), want an EventListenerError", err, err)
	}
	if !strings.Contains(err.Error(), "listener exploded") {
		t.Errorf("Error() = %q, want the panic value", err)
	}
}

func TestHandlerIsConfigured(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Handler:   &core.HandlerOptions{BasePath: "/payments"},
	})
	if payment.Handler() == nil {
		t.Fatal("Handler() = nil")
	}
	response, err := payment.Handler().Handle(context.Background(), &core.HandlerRequest{
		Method: "GET", URL: "/payments/health", Headers: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if response.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", response.Status)
	}

	other, err := payment.CreateHandler(context.Background(), core.HandlerOptions{BasePath: "/other"})
	if err != nil {
		t.Fatalf("CreateHandler: %v", err)
	}
	if other == payment.Handler() {
		t.Error("CreateHandler must build a separate handler")
	}
}

func TestNewRejectsAnInvalidHandlerConfiguration(t *testing.T) {
	_, err := New(Config{
		Providers: map[string]core.ProviderEntry{"mock": postesting.NewMockProvider()},
		Handler:   &core.HandlerOptions{AllowedActions: []core.HandlerAction{core.ActionRefund}},
	})
	posTestWantConfigurationError(t, err, "authorize")
}

func TestNewRejectsInvalidPlugins(t *testing.T) {
	cases := []struct {
		name    string
		plugins []core.Plugin
		want    string
	}{
		{"missing id", []core.Plugin{{}}, "needs an id"},
		{"duplicate id", []core.Plugin{{ID: "x"}, {ID: "x"}}, "registered twice"},
		{
			name: "reserved method",
			plugins: []core.Plugin{{ID: "bad", Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
				return map[string]any{"Refund": func() {}}, nil
			}}},
			want: "clashes",
		},
		{
			name: "reserved method in lower case",
			plugins: []core.Plugin{{ID: "bad", Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
				return map[string]any{"refund": func() {}}, nil
			}}},
			want: "clashes",
		},
		{
			name: "method added by two plugins",
			plugins: []core.Plugin{
				{ID: "one", Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
					return map[string]any{"counter": 1}, nil
				}},
				{ID: "two", Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
					return map[string]any{"counter": 2}, nil
				}},
			},
			want: "two plugins",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{
				Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
				Plugins:   tc.plugins,
			})
			posTestWantConfigurationError(t, err, tc.want)
		})
	}
}

func TestPluginMethodsErrorFailsConstruction(t *testing.T) {
	failure := errors.New("rates unavailable")
	_, err := New(Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{{ID: "rates", Methods: func(context.Context, core.PluginContext) (map[string]any, error) {
			return nil, failure
		}}},
	})
	if !errors.Is(err, failure) {
		t.Errorf("New error = %v, want %v", err, failure)
	}
}

func TestMethodsAndErrorCodes(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider()},
		Plugins: []core.Plugin{{
			ID:         "counter",
			ErrorCodes: map[string]string{"LIMIT_REACHED": "Limit reached"},
			Methods: func(_ context.Context, ctx core.PluginContext) (map[string]any, error) {
				return map[string]any{"counter": ctx.ProviderIDs()}, nil
			},
		}},
	})
	value, ok := payment.Method("counter")
	if !ok {
		t.Fatal("Method(counter) is missing")
	}
	if ids, ok := value.([]string); !ok || len(ids) != 1 || ids[0] != "a" {
		t.Errorf("counter = %v, want [a]", value)
	}
	if _, ok := payment.Method("missing"); ok {
		t.Error("Method(missing) must report false")
	}

	methods := payment.Methods()
	if len(methods) != 1 {
		t.Errorf("Methods() = %v, want one member", methods)
	}
	delete(methods, "counter")
	if _, ok := payment.Method("counter"); !ok {
		t.Error("Methods() must return a copy")
	}

	codes := payment.ErrorCodes()
	if codes["LIMIT_REACHED"] != "Limit reached" {
		t.Errorf("ErrorCodes() = %v, want the plugin's codes", codes)
	}
	delete(codes, "LIMIT_REACHED")
	if payment.ErrorCodes()["LIMIT_REACHED"] != "Limit reached" {
		t.Error("ErrorCodes() must return a copy")
	}
}

func TestPluginContextExposesTheConfiguration(t *testing.T) {
	logger := &posTestLogger{}
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{"a": postesting.NewMockProvider(), "b": postesting.NewMockProvider()},
		Mode:      core.ModeSandbox,
		Logger:    logger,
		Plugins: []core.Plugin{{
			ID:         "probe",
			ErrorCodes: map[string]string{"X": "x"},
			Endpoints: map[string]core.PaymentEndpoint{
				"quote": {Method: "POST", Path: "/probe/quote"},
			},
		}},
	})
	ctx := payment.PluginContext()
	if ctx == nil {
		t.Fatal("PluginContext() = nil")
	}
	if got := ctx.ProviderIDs(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ProviderIDs() = %v, want [a b]", got)
	}
	if ctx.DefaultProvider() != "" {
		t.Errorf("DefaultProvider() = %q, want an empty id", ctx.DefaultProvider())
	}
	if ctx.Mode() != core.ModeSandbox {
		t.Errorf("Mode() = %q, want sandbox", ctx.Mode())
	}
	if ctx.Logger() != core.Logger(logger) {
		t.Error("Logger() must be the configured logger")
	}
	if ctx.ErrorCodes()["X"] != "x" {
		t.Errorf("ErrorCodes() = %v, want the plugin's codes", ctx.ErrorCodes())
	}
	if _, err := ctx.Use("a"); err != nil {
		t.Errorf("Use(a): %v", err)
	}
	if _, err := ctx.Use("c"); err == nil {
		t.Error("Use(c): want a ProviderNotEnabledError")
	}

	endpoints := ctx.Endpoints()
	if len(endpoints) != 1 {
		t.Fatalf("Endpoints() = %v, want one endpoint", endpoints)
	}
	if endpoints[0].PluginID != "probe" || endpoints[0].Name != "quote" || endpoints[0].Path != "/probe/quote" {
		t.Errorf("endpoint = %+v, want the registered quote endpoint", endpoints[0])
	}

	registered := payment.Endpoints()
	if len(registered) != 1 || registered[0].PluginID != "probe" {
		t.Fatalf("Endpoints() = %+v, want the plugin's endpoint", registered)
	}
	registered[0].Name = "changed"
	if payment.Endpoints()[0].Name != "quote" {
		t.Error("Endpoints() must return a copy")
	}
}

func TestCapabilityAccessors(t *testing.T) {
	payment := posTestNew(t, Config{
		Providers: map[string]core.ProviderEntry{
			"mock":   postesting.NewMockProvider(),
			"iyzico": IyzicoConfig{APIKey: "k", SecretKey: "s"},
		},
		DefaultProvider: "mock",
	})
	if _, err := payment.CheckoutFormProvider("mock"); err == nil {
		t.Error("CheckoutFormProvider(mock): want NOT_SUPPORTED")
	} else {
		var base *core.BaseError
		if !errors.As(err, &base) || base.Code != "NOT_SUPPORTED" {
			t.Errorf("error = %v, want NOT_SUPPORTED", err)
		}
	}
	if _, err := payment.CheckoutFormProvider("iyzico"); err != nil {
		t.Errorf("CheckoutFormProvider(iyzico): %v", err)
	}
	if _, err := payment.SubscriptionProvider(""); err == nil {
		t.Error("SubscriptionProvider of the default mock provider: want NOT_SUPPORTED")
	}
	if _, err := payment.PWIProvider("unknown"); err == nil {
		t.Error("PWIProvider(unknown): want a ProviderNotEnabledError")
	}
}

// posTestDoer records the request URLs and answers with a canned body.
type posTestDoer struct {
	mu    sync.Mutex
	calls []string
	body  string
	err   error
}

func (d *posTestDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls = append(d.calls, req.URL.String())
	d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	body := d.body
	if body == "" {
		body = `{"status":"success"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (d *posTestDoer) urls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// posTestLogger records log messages without printing them.
type posTestLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *posTestLogger) Debug(message string, _ map[string]any) { l.record(message) }
func (l *posTestLogger) Info(message string, _ map[string]any)  { l.record(message) }
func (l *posTestLogger) Error(message string, _ error, _ map[string]any) {
	l.record(message)
}

func (l *posTestLogger) record(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, message)
}

var _ core.Logger = (*posTestLogger)(nil)
var _ core.Doer = (*posTestDoer)(nil)

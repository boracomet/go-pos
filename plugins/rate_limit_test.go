package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func rateLimitTestSetup(t *testing.T, options RateLimitOptions) (core.Plugin, *RateLimitMethods, *pluginTestContext) {
	t.Helper()
	plugin, err := RateLimit(options)
	if err != nil {
		t.Fatalf("rateLimit() failed: %v", err)
	}
	pctx := newPluginTestContext()
	pctx.logger = &pluginTestLogger{}
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	methods, ok := pluginTestMethods(t, plugin, pctx)["rateLimit"].(*RateLimitMethods)
	if !ok {
		t.Fatal("the rateLimit member is missing")
	}
	return plugin, methods, pctx
}

// rateLimitTestIntercept runs the OnRequest hook. A nil response means the
// request is let through to routing.
func rateLimitTestIntercept(t *testing.T, plugin core.Plugin, path string, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	if plugin.OnRequest == nil {
		t.Fatal("the plugin has no OnRequest hook")
	}
	request := pluginTestPost(path, pluginTestPaymentRequest(), headers)
	response, err := plugin.OnRequest(context.Background(), request, core.RequestContext{Path: path})
	if err != nil {
		t.Fatalf("OnRequest failed: %v", err)
	}
	return response
}

func rateLimitTestStatus(t *testing.T, plugin core.Plugin, path string, headers map[string]string) int {
	t.Helper()
	response := rateLimitTestIntercept(t, plugin, path, headers)
	if response == nil {
		return 200
	}
	return response.Status
}

func rateLimitTestForwarded(ip string) map[string]string {
	return map[string]string{"x-forwarded-for": ip}
}

// rateLimitTestBrokenStore is a counter store that is always down.
type rateLimitTestBrokenStore struct{}

func (rateLimitTestBrokenStore) Increment(string, int64) (RateLimitCounter, error) {
	return RateLimitCounter{}, errors.New("redis down")
}

func TestRateLimitParsesDurations(t *testing.T) {
	cases := []struct {
		value any
		want  int64
	}{
		{"1000", 1000},
		{1500, 1500},
		{"500ms", 500},
		{"30s", 30000},
		{"1m", 60000},
		{"2h", 7200000},
		{"1d", 86400000},
	}
	for _, testCase := range cases {
		got, err := ParseDuration(testCase.value)
		if err != nil {
			t.Fatalf("parseDuration(%v) failed: %v", testCase.value, err)
		}
		if got != testCase.want {
			t.Errorf("parseDuration(%v) = %d, want %d", testCase.value, got, testCase.want)
		}
	}
	for _, invalid := range []any{-5, "invalid"} {
		if _, err := ParseDuration(invalid); err == nil {
			t.Errorf("parseDuration(%v) succeeded, want a configuration error", invalid)
		} else if _, ok := err.(*core.ConfigurationError); !ok {
			t.Errorf("parseDuration(%v) error is %T, want *core.ConfigurationError", invalid, err)
		}
	}
}

func TestRateLimitEnforcesGlobalLimitWithHeaders(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(2),
		TrustProxy: true,
	})
	const path = "/api/pay/mock/payment"
	for i := 0; i < 2; i++ {
		if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("1.2.3.4")); status != 200 {
			t.Fatalf("request %d status = %d, want 200", i+1, status)
		}
	}
	limited := rateLimitTestIntercept(t, plugin, path, rateLimitTestForwarded("1.2.3.4"))
	if limited == nil || limited.Status != 429 {
		t.Fatalf("the third request was not rate limited: %+v", limited)
	}
	if limited.Headers["retry-after"] == "" {
		t.Error("the 429 carries no retry-after header")
	}
	if limited.Headers["x-ratelimit-limit"] != "2" {
		t.Errorf("x-ratelimit-limit = %q, want 2", limited.Headers["x-ratelimit-limit"])
	}
	if limited.Headers["x-ratelimit-remaining"] != "0" {
		t.Errorf("x-ratelimit-remaining = %q, want 0", limited.Headers["x-ratelimit-remaining"])
	}
	body, ok := limited.Body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want a map", limited.Body)
	}
	if body["code"] != "RATE_LIMITED" {
		t.Errorf("code = %v", body["code"])
	}
	message, _ := body["errorMessage"].(string)
	if !strings.Contains(message, "Too many requests") {
		t.Errorf("errorMessage = %q", message)
	}
}

func TestRateLimitEnforcesPerRouteRules(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(10),
		TrustProxy: true,
		Rules:      []RateLimitRule{{Pattern: "/payment", Window: "1m", Max: pluginTestPtr(1)}},
	})
	const paymentPath = "/api/pay/mock/payment"
	const binPath = "/api/pay/mock/bin-check"
	headers := rateLimitTestForwarded("10.0.0.1")

	if status := rateLimitTestStatus(t, plugin, paymentPath, headers); status != 200 {
		t.Fatalf("the first payment status = %d, want 200", status)
	}
	limited := rateLimitTestIntercept(t, plugin, paymentPath, headers)
	if limited == nil || limited.Status != 429 {
		t.Fatalf("the second payment was not limited: %+v", limited)
	}
	if limited.Headers["x-ratelimit-limit"] != "1" {
		t.Errorf("x-ratelimit-limit = %q, want the rule limit 1", limited.Headers["x-ratelimit-limit"])
	}
	if status := rateLimitTestStatus(t, plugin, binPath, headers); status != 200 {
		t.Errorf("the bin check status = %d, want 200 under the global limit", status)
	}
}

func TestRateLimitExcludesProviderCallbacks(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
		Rules:      []RateLimitRule{{Pattern: "/callback/*", Excluded: true}},
	})
	for i := 0; i < 5; i++ {
		if status := rateLimitTestStatus(t, plugin, "/api/pay/mock/callback", rateLimitTestForwarded("1.2.3.4")); status == 429 {
			t.Fatalf("callback %d was rate limited", i+1)
		}
	}

	byDefault, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
	})
	for i := 0; i < 5; i++ {
		if status := rateLimitTestStatus(t, byDefault, "/api/pay/mock/callback", rateLimitTestForwarded("1.2.3.4")); status == 429 {
			t.Fatalf("callback %d was rate limited", i+1)
		}
		if status := rateLimitTestStatus(t, byDefault, "/api/pay/mock/complete-3ds", rateLimitTestForwarded("1.2.3.4")); status == 429 {
			t.Fatalf("3ds completion %d was rate limited", i+1)
		}
	}
}

func TestRateLimitResetsAfterTheWindowExpires(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "30ms",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
	})
	const path = "/api/pay/mock/payment"
	headers := rateLimitTestForwarded("5.5.5.5")

	if status := rateLimitTestStatus(t, plugin, path, headers); status != 200 {
		t.Fatalf("the first request status = %d, want 200", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 429 {
		t.Fatalf("the second request status = %d, want 429", status)
	}
	time.Sleep(40 * time.Millisecond)
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 200 {
		t.Errorf("the request after the window status = %d, want 200", status)
	}
}

func TestRateLimitSupportsACustomKey(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window: "1m",
		Max:    pluginTestPtr(1),
		Key: func(request *core.HandlerRequest) (string, error) {
			if key := pluginHeader(request, "x-api-key"); key != "" {
				return key, nil
			}
			return "anon", nil
		},
	})
	const path = "/api/pay/mock/payment"
	if status := rateLimitTestStatus(t, plugin, path, map[string]string{"x-api-key": "user-a"}); status != 200 {
		t.Fatalf("user-a first request status = %d, want 200", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, map[string]string{"x-api-key": "user-a"}); status != 429 {
		t.Fatalf("user-a second request status = %d, want 429", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, map[string]string{"x-api-key": "user-b"}); status != 200 {
		t.Errorf("user-b status = %d, want 200", status)
	}
}

func TestRateLimitLimitedBodyIsTranslatedByLocalizedErrors(t *testing.T) {
	const turkish = "Çok fazla istek gönderildi, lütfen bir dakika sonra tekrar deneyin."
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
	})
	localized, err := LocalizedErrors(LocalizedErrorsOptions{
		Locale:   "tr",
		Messages: []I18nTranslation{{Locale: "tr", Messages: I18nDictionary{"RATE_LIMITED": turkish}}},
	})
	if err != nil {
		t.Fatalf("localizedErrors() failed: %v", err)
	}
	pctx := newPluginTestContext()
	if err := pluginTestInit(localized, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	const path = "/api/pay/mock/payment"
	headers := rateLimitTestForwarded("9.9.9.9")
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 200 {
		t.Fatalf("the first request status = %d, want 200", status)
	}
	limited := rateLimitTestIntercept(t, plugin, path, headers)
	if limited == nil || limited.Status != 429 {
		t.Fatalf("the second request was not limited: %+v", limited)
	}
	request := pluginTestPost(path, pluginTestPaymentRequest(), headers)
	translated, err := localized.OnResponse(context.Background(), limited, core.ResponseContext{Request: request})
	if err != nil {
		t.Fatalf("OnResponse failed: %v", err)
	}
	body, ok := translated.Body.(map[string]any)
	if !ok {
		t.Fatalf("translated body is %T, want a map", translated.Body)
	}
	if body["code"] != "RATE_LIMITED" {
		t.Errorf("code = %v", body["code"])
	}
	if body["errorMessage"] != turkish {
		t.Errorf("errorMessage = %v, want the Turkish message", body["errorMessage"])
	}
}

func TestRateLimitAllowsManualReset(t *testing.T) {
	plugin, methods, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
	})
	const path = "/api/pay/mock/payment"
	headers := rateLimitTestForwarded("7.7.7.7")
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 200 {
		t.Fatalf("the first request status = %d, want 200", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 429 {
		t.Fatalf("the second request status = %d, want 429", status)
	}
	if err := methods.Reset(""); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if status := rateLimitTestStatus(t, plugin, path, headers); status != 200 {
		t.Errorf("the request after the reset status = %d, want 200", status)
	}
}

func TestRateLimitRequiresATrustworthyIdentity(t *testing.T) {
	cases := []struct {
		name    string
		options RateLimitOptions
	}{
		{"neither key nor proxy", RateLimitOptions{Window: "1m", Max: pluginTestPtr(60)}},
		{"trustProxy false", RateLimitOptions{TrustProxy: false}},
		{"trustProxy zero", RateLimitOptions{TrustProxy: 0}},
		{"trustProxy fractional", RateLimitOptions{TrustProxy: 1.5}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := RateLimit(testCase.options)
			if err == nil {
				t.Fatal("rateLimit() accepted an untrustworthy identity")
			}
			if _, ok := err.(*core.ConfigurationError); !ok {
				t.Errorf("error is %T, want *core.ConfigurationError", err)
			}
		})
	}
}

func TestRateLimitIgnoresForgedForwardedHeadersWithoutTrustProxy(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window: "1m",
		Max:    pluginTestPtr(2),
		Key: func(request *core.HandlerRequest) (string, error) {
			if key := pluginHeader(request, "x-api-key"); key != "" {
				return key, nil
			}
			return "anonymous", nil
		},
	})
	const path = "/api/pay/mock/payment"
	for i, forwarded := range []string{"1.1.1.1", "2.2.2.2, 3.3.3.3"} {
		if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded(forwarded)); status != 200 {
			t.Fatalf("request %d status = %d, want 200", i+1, status)
		}
	}
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("4.4.4.4")); status != 429 {
		t.Errorf("the third request status = %d, want 429 in the shared bucket", status)
	}
}

func TestRateLimitUsesTheTrustedProxyHop(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(2),
		TrustProxy: true,
	})
	const path = "/api/pay/mock/payment"
	for i, forwarded := range []string{"1.1.1.1, 9.9.9.9", "2.2.2.2, 9.9.9.9"} {
		if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded(forwarded)); status != 200 {
			t.Fatalf("request %d status = %d, want 200", i+1, status)
		}
	}
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("3.3.3.3, 9.9.9.9")); status != 429 {
		t.Errorf("the third request status = %d, want 429", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("3.3.3.3, 8.8.8.8")); status != 200 {
		t.Errorf("a different real client status = %d, want its own bucket", status)
	}
}

func TestRateLimitCountsMultipleHopsFromTheRight(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: 2,
	})
	const path = "/api/pay/mock/payment"
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("1.1.1.1, 9.9.9.9, 203.0.113.1")); status != 200 {
		t.Fatalf("the first request status = %d, want 200", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("2.2.2.2, 9.9.9.9, 203.0.113.2")); status != 429 {
		t.Errorf("the second request status = %d, want 429", status)
	}
}

func TestRateLimitExplicitKeyWinsOverTrustProxy(t *testing.T) {
	plugin, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Window:     "1m",
		Max:        pluginTestPtr(1),
		TrustProxy: true,
		Key:        func(*core.HandlerRequest) (string, error) { return "explicit", nil },
	})
	const path = "/api/pay/mock/payment"
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("9.9.9.9")); status != 200 {
		t.Fatalf("the first request status = %d, want 200", status)
	}
	if status := rateLimitTestStatus(t, plugin, path, rateLimitTestForwarded("8.8.8.8")); status != 429 {
		t.Errorf("the second request status = %d, want 429 in the explicit bucket", status)
	}
}

func TestRateLimitStoreFailureClosesUnlessPassOnStoreError(t *testing.T) {
	closed, _, _ := rateLimitTestSetup(t, RateLimitOptions{
		Key:   func(*core.HandlerRequest) (string, error) { return "client", nil },
		Store: rateLimitTestBrokenStore{},
	})
	// The facade turns a failing OnRequest into a 503.
	if _, err := closed.OnRequest(context.Background(),
		pluginTestPost("/api/pay/mock/payment", pluginTestPaymentRequest(), nil),
		core.RequestContext{Path: "/api/pay/mock/payment"}); err == nil {
		t.Error("a failing store let the request through, want an error")
	}

	open, _, pctx := rateLimitTestSetup(t, RateLimitOptions{
		Key:              func(*core.HandlerRequest) (string, error) { return "client", nil },
		Store:            rateLimitTestBrokenStore{},
		PassOnStoreError: true,
	})
	if status := rateLimitTestStatus(t, open, "/api/pay/mock/payment", nil); status != 200 {
		t.Errorf("passOnStoreError status = %d, want 200", status)
	}
	logger := pctx.logger.(*pluginTestLogger)
	entries := logger.all()
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "passOnStoreError") {
		t.Errorf("logged %v, want the store failure", entries)
	}
}

func TestRateLimitMemoryStoreEvictsAndResets(t *testing.T) {
	store := NewMemoryRateLimitStore(2)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := store.Increment(key, 60000); err != nil {
			t.Fatalf("increment failed: %v", err)
		}
	}
	// "a" was evicted, so it starts a fresh window instead of counting to 2.
	counter, err := store.Increment("a", 60000)
	if err != nil {
		t.Fatalf("increment failed: %v", err)
	}
	if counter.Count != 1 {
		t.Errorf("evicted key count = %d, want 1", counter.Count)
	}
	if counter, err = store.Increment("c", 60000); err != nil || counter.Count != 2 {
		t.Errorf("counter = %+v, err = %v, want count 2", counter, err)
	}
	if err := store.Reset("c"); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if counter, err = store.Increment("c", 60000); err != nil || counter.Count != 1 {
		t.Errorf("counter after reset = %+v, err = %v, want count 1", counter, err)
	}
}

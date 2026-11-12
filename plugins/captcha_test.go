package plugins

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func captchaTestRequest(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	request := pluginTestJSON(t, pluginTestPaymentRequest())
	for name, value := range extra {
		request[name] = value
	}
	return request
}

func captchaTestPlugin(t *testing.T, options CaptchaOptions) (core.Plugin, *pluginTestProvider, *pluginTestDoer) {
	t.Helper()
	if options.Provider == "" {
		options.Provider = CaptchaProviderTurnstile
	}
	if options.SecretKey == "" {
		options.SecretKey = "mock-secret-key"
	}
	doer, ok := options.Doer.(*pluginTestDoer)
	if !ok {
		doer = &pluginTestDoer{body: `{"success":true}`}
		options.Doer = doer
	}
	plugin, err := Captcha(options)
	if err != nil {
		t.Fatalf("captcha() failed: %v", err)
	}
	return plugin, newPluginTestProvider("mock"), doer
}

// captchaTestCreate runs the before hook and, when the token was accepted, the provider.
func captchaTestCreate(t *testing.T, plugin core.Plugin, provider *pluginTestProvider, request any) *core.PaymentResponse {
	t.Helper()
	ctx := pluginTestOperation(core.OperationCreatePayment, "mock", request)
	injected := pluginTestRunBefore(t, plugin, ctx)
	if injected != nil && injected.Result != nil {
		result, ok := injected.Result.(*core.PaymentResponse)
		if !ok {
			t.Fatalf("injected result is %T, want *core.PaymentResponse", injected.Result)
		}
		return result
	}
	result, err := provider.CreatePayment(context.Background(), pluginTestPaymentRequest())
	if err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	return result
}

func TestCaptchaRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		options CaptchaOptions
		want    string
	}{
		{"missing", CaptchaOptions{}, "invalid or missing provider"},
		{"unknown", CaptchaOptions{Provider: CaptchaProvider("invalid"), SecretKey: "sec"}, "invalid or missing provider"},
		{"secret", CaptchaOptions{Provider: CaptchaProviderTurnstile}, "secretKey is required"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Captcha(testCase.options)
			if err == nil {
				t.Fatal("captcha() accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), testCase.want)
			}
		})
	}
}

func TestCaptchaMissingToken(t *testing.T) {
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, nil))
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.ErrorCode != "CAPTCHA_MISSING" {
		t.Errorf("errorCode = %q, want CAPTCHA_MISSING", result.ErrorCode)
	}
	if result.ErrorMessage != "Captcha token is required" {
		t.Errorf("errorMessage = %q, want 'Captcha token is required'", result.ErrorMessage)
	}
	if doer.count() != 0 {
		t.Errorf("siteverify was called %d times, want 0", doer.count())
	}
	if provider.count("createPayment") != 0 {
		t.Errorf("the provider was called %d times, want 0", provider.count("createPayment"))
	}
}

func TestCaptchaVerifiesTurnstile(t *testing.T) {
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{
		Provider:  CaptchaProviderTurnstile,
		SecretKey: "cf-secret",
	})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, map[string]any{
		"captchaToken": "valid-turnstile-token",
	}))
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if doer.count() != 1 {
		t.Fatalf("siteverify was called %d times, want 1", doer.count())
	}
	request := doer.requestAt(0)
	if request.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
		t.Errorf("siteverify URL = %q", request.URL.String())
	}
	if request.Method != http.MethodPost {
		t.Errorf("siteverify method = %q, want POST", request.Method)
	}
	if !strings.Contains(doer.bodyAt(0), "response=valid-turnstile-token") {
		t.Errorf("siteverify body = %q, want the token", doer.bodyAt(0))
	}
	if !strings.Contains(doer.bodyAt(0), "secret=cf-secret") {
		t.Errorf("siteverify body = %q, want the secret", doer.bodyAt(0))
	}
	if provider.count("createPayment") != 1 {
		t.Errorf("the provider was called %d times, want 1", provider.count("createPayment"))
	}
}

func TestCaptchaInvalidTurnstileToken(t *testing.T) {
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{
		Provider: CaptchaProviderTurnstile,
		Doer:     &pluginTestDoer{body: `{"success":false,"error-codes":["invalid-input-response"]}`},
	})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, map[string]any{
		"captchaToken": "bad-token",
	}))
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.ErrorCode != "CAPTCHA_INVALID" {
		t.Errorf("errorCode = %q, want CAPTCHA_INVALID", result.ErrorCode)
	}
	if provider.count("createPayment") != 0 {
		t.Errorf("the provider was called %d times, want 0", provider.count("createPayment"))
	}
	if doer.count() != 1 {
		t.Errorf("siteverify was called %d times, want 1", doer.count())
	}
}

func TestCaptchaGoogleScoreThreshold(t *testing.T) {
	high, highProvider, _ := captchaTestPlugin(t, CaptchaOptions{
		Provider: CaptchaProviderGoogle,
		MinScore: pluginTestPtr(0.5),
		Doer:     &pluginTestDoer{body: `{"success":true,"score":0.9}`},
	})
	accepted := captchaTestCreate(t, high, highProvider, captchaTestRequest(t, map[string]any{
		"g-recaptcha-response": "token-high-score",
	}))
	if accepted.Status != core.StatusSuccess {
		t.Errorf("score 0.9: status = %q, want success", accepted.Status)
	}

	low, lowProvider, _ := captchaTestPlugin(t, CaptchaOptions{
		Provider: CaptchaProviderGoogle,
		MinScore: pluginTestPtr(0.5),
		Doer:     &pluginTestDoer{body: `{"success":true,"score":0.3}`},
	})
	rejected := captchaTestCreate(t, low, lowProvider, captchaTestRequest(t, map[string]any{
		"g-recaptcha-response": "token-bot-score",
	}))
	if rejected.Status != core.StatusFailure {
		t.Errorf("score 0.3: status = %q, want failure", rejected.Status)
	}
	if rejected.ErrorCode != "CAPTCHA_INVALID" {
		t.Errorf("score 0.3: errorCode = %q, want CAPTCHA_INVALID", rejected.ErrorCode)
	}
	if lowProvider.count("createPayment") != 0 {
		t.Errorf("the provider was called %d times, want 0", lowProvider.count("createPayment"))
	}
}

func TestCaptchaVerifiesHCaptcha(t *testing.T) {
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{
		Provider:  CaptchaProviderHCaptcha,
		SecretKey: "hcaptcha-secret",
	})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, map[string]any{
		"h-captcha-response": "valid-hcaptcha-token",
	}))
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if doer.count() != 1 {
		t.Fatalf("siteverify was called %d times, want 1", doer.count())
	}
	if url := doer.requestAt(0).URL.String(); url != "https://api.hcaptcha.com/siteverify" {
		t.Errorf("siteverify URL = %q", url)
	}
	if !strings.Contains(doer.bodyAt(0), "response=valid-hcaptcha-token") {
		t.Errorf("siteverify body = %q, want the token", doer.bodyAt(0))
	}
}

func TestCaptchaCustomExtractorAndHeaders(t *testing.T) {
	extracted := 0
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{
		GetToken: func(request any) string {
			extracted++
			return pluginString(request, "customSecurityToken")
		},
	})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, map[string]any{
		"customSecurityToken": "extracted-token-123",
	}))
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if extracted == 0 {
		t.Error("the custom extractor was never called")
	}
	if !strings.Contains(doer.bodyAt(0), "response=extracted-token-123") {
		t.Errorf("siteverify body = %q, want the extracted token", doer.bodyAt(0))
	}

	fromHeader, headerProvider, headerDoer := captchaTestPlugin(t, CaptchaOptions{})
	request := captchaTestRequest(t, map[string]any{
		"headers": map[string]string{"x-captcha-response": "header-token"},
	})
	if status := captchaTestCreate(t, fromHeader, headerProvider, request).Status; status != core.StatusSuccess {
		t.Errorf("header token: status = %q, want success", status)
	}
	if !strings.Contains(headerDoer.bodyAt(0), "response=header-token") {
		t.Errorf("siteverify body = %q, want the header token", headerDoer.bodyAt(0))
	}
}

func TestCaptchaNeverInterceptsCallbacks(t *testing.T) {
	plugin, provider, doer := captchaTestPlugin(t, CaptchaOptions{
		Doer: &pluginTestDoer{body: `{"success":false}`},
	})
	provider.payment = &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-3DS-OK"}

	ctx := pluginTestOperation(core.OperationCompleteThreeDSPayment, "mock", map[string]any{
		"paymentId": "PMT-3DS-OK",
		"status":    "success",
	})
	if injected := pluginTestRunBefore(t, plugin, ctx); injected != nil && injected.Result != nil {
		t.Fatal("the callback was intercepted")
	}
	result, err := provider.CompleteThreeDSPayment(context.Background(), ctx.Request)
	if err != nil {
		t.Fatalf("completeThreeDSPayment failed: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if doer.count() != 0 {
		t.Errorf("siteverify was called %d times, want 0", doer.count())
	}
	if provider.count("completeThreeDSPayment") != 1 {
		t.Errorf("the provider was called %d times, want 1", provider.count("completeThreeDSPayment"))
	}
}

func TestCaptchaFailsOnNetworkError(t *testing.T) {
	plugin, provider, _ := captchaTestPlugin(t, CaptchaOptions{
		Doer: &pluginTestDoer{err: errors.New("Connection refused to cloudflare.com")},
	})

	result := captchaTestCreate(t, plugin, provider, captchaTestRequest(t, map[string]any{
		"captchaToken": "some-token",
	}))
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.ErrorCode != "CAPTCHA_INVALID" {
		t.Errorf("errorCode = %q, want CAPTCHA_INVALID", result.ErrorCode)
	}
	if provider.count("createPayment") != 0 {
		t.Errorf("the provider was called %d times, want 0", provider.count("createPayment"))
	}
}

package plugins

import (
	"context"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

var i18nTestCodes = []string{
	string(core.CodeInsufficientFunds),
	string(core.CodeCardDeclined),
	string(core.CodeInvalidCard),
	string(core.CodeExpiredCard),
	string(core.CodeInvalidCVC),
	string(core.CodeThreeDSFailed),
	string(core.CodeFraudSuspected),
	string(core.CodeLimitExceeded),
	string(core.CodeDuplicateOrder),
	string(core.CodeCancelledByCustomer),
	string(core.CodeInvalidRequest),
	string(core.CodeNetworkError),
	string(core.CodeInvalidHash),
	string(core.CodeProviderError),
	string(core.CodeUnknown),
}

func i18nTestSetup(t *testing.T, options I18nOptions) (core.Plugin, *pluginTestContext, *I18nMethods) {
	t.Helper()
	plugin, err := I18n(options)
	if err != nil {
		t.Fatalf("i18n() failed: %v", err)
	}
	pctx := newPluginTestContext()
	methods, ok := pluginTestMethods(t, plugin, pctx)["errors"].(*I18nMethods)
	if !ok {
		t.Fatal("the errors member is missing")
	}
	return plugin, pctx, methods
}

func i18nTestFailure(code core.PaymentErrorCode, message string) *core.PaymentResponse {
	return &core.PaymentResponse{
		Status:       core.StatusFailure,
		PaymentID:    "mock-payment",
		Code:         code,
		ErrorCode:    "raw-code",
		ErrorMessage: message,
	}
}

func i18nTestAfter(t *testing.T, plugin core.Plugin, result any) any {
	t.Helper()
	return pluginTestRunAfter(t, plugin, pluginTestOutcome(
		core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), result))
}

func i18nTestRespond(t *testing.T, plugin core.Plugin, body any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	return i18nTestRespondTo(t, plugin, body, pluginTestPost("/api/pay/mock/payment", pluginTestPaymentRequest(), headers))
}

func i18nTestRespondTo(t *testing.T, plugin core.Plugin, body any, request *core.HandlerRequest) *core.HandlerResponse {
	t.Helper()
	response := &core.HandlerResponse{Status: 200, Headers: map[string]string{}, Body: body}
	if plugin.OnResponse == nil {
		t.Fatal("the plugin has no OnResponse hook")
	}
	translated, err := plugin.OnResponse(context.Background(), response, core.ResponseContext{Request: request})
	if err != nil {
		t.Fatalf("OnResponse failed: %v", err)
	}
	if translated == nil {
		return response
	}
	return translated
}

func i18nTestMessage(t *testing.T, response *core.HandlerResponse) string {
	t.Helper()
	result, ok := response.Body.(*core.PaymentResponse)
	if !ok {
		t.Fatalf("response body is %T, want *core.PaymentResponse", response.Body)
	}
	return result.ErrorMessage
}

func TestI18nEveryCodeInEveryBuiltInLanguage(t *testing.T) {
	expected := append([]string{}, i18nTestCodes...)
	sort.Strings(expected)
	for _, translation := range I18nLocales() {
		keys := make([]string, 0, len(translation.Messages))
		for code, message := range translation.Messages {
			keys = append(keys, code)
			if message == "" {
				t.Errorf("%s: the message of %s is empty", translation.Locale, code)
			}
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, expected) {
			t.Errorf("%s: keys = %v, want every error code %v", translation.Locale, keys, expected)
		}
	}
}

func TestI18nReplacesMessageAndKeepsProviderMessage(t *testing.T) {
	plugin, _, _ := i18nTestSetup(t, I18nOptions{})

	declined := i18nTestAfter(t, plugin, i18nTestFailure(core.CodeInsufficientFunds, "raw decline")).(*core.PaymentResponse)
	if declined.ErrorMessage != I18nLocaleEN["INSUFFICIENT_FUNDS"] {
		t.Errorf("errorMessage = %q, want the English message", declined.ErrorMessage)
	}
	if declined.ProviderMessage != "raw decline" {
		t.Errorf("providerMessage = %q, want 'raw decline'", declined.ProviderMessage)
	}
	if declined.ProviderMessage == declined.ErrorMessage {
		t.Error("providerMessage was overwritten with the translation")
	}

	lost := i18nTestAfter(t, plugin, &core.PaymentResponse{
		Status:       core.StatusPending,
		Code:         core.CodeNetworkError,
		ErrorCode:    "NETWORK_ERROR",
		ErrorMessage: "no response",
	}).(*core.PaymentResponse)
	if lost.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", lost.Status)
	}
	if lost.ErrorMessage != I18nLocaleEN["NETWORK_ERROR"] {
		t.Errorf("errorMessage = %q, want the English NETWORK_ERROR message", lost.ErrorMessage)
	}

	paid := i18nTestAfter(t, plugin, &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "p1"}).(*core.PaymentResponse)
	if paid.ErrorMessage != "" || paid.ProviderMessage != "" {
		t.Errorf("a success was translated: %q / %q", paid.ErrorMessage, paid.ProviderMessage)
	}
}

func TestI18nConfiguredLanguageOverridesAndFallback(t *testing.T) {
	plugin, _, _ := i18nTestSetup(t, I18nOptions{
		Translations:  []I18nTranslation{{Locale: "tr", Messages: I18nLocaleTR}},
		DefaultLocale: "tr",
	})
	expired := i18nTestAfter(t, plugin, i18nTestFailure(core.CodeExpiredCard, "raw")).(*core.PaymentResponse)
	if expired.ErrorMessage != I18nLocaleTR["EXPIRED_CARD"] {
		t.Errorf("errorMessage = %q, want the Turkish message", expired.ErrorMessage)
	}

	overriddenTR := i18nCopyDictionary(I18nLocaleTR)
	overriddenTR["EXPIRED_CARD"] = "Kartın süresi dolmuş."
	spanish, _, spanishMethods := i18nTestSetup(t, I18nOptions{
		Translations: []I18nTranslation{
			{Locale: "es", Messages: I18nDictionary{"INSUFFICIENT_FUNDS": "Fondos insuficientes."}},
			{Locale: "tr", Messages: overriddenTR},
		},
		DefaultLocale: "es",
	})
	funds := i18nTestAfter(t, spanish, i18nTestFailure(core.CodeInsufficientFunds, "raw")).(*core.PaymentResponse)
	if funds.ErrorMessage != "Fondos insuficientes." {
		t.Errorf("errorMessage = %q, want 'Fondos insuficientes.'", funds.ErrorMessage)
	}
	declined := i18nTestAfter(t, spanish, i18nTestFailure(core.CodeCardDeclined, "raw")).(*core.PaymentResponse)
	if declined.ErrorMessage != I18nLocaleEN["CARD_DECLINED"] {
		t.Errorf("a code missing in Spanish = %q, want the English message", declined.ErrorMessage)
	}
	if message := spanishMethods.Message("EXPIRED_CARD", "tr"); message != "Kartın süresi dolmuş." {
		t.Errorf("message(EXPIRED_CARD, tr) = %q, want the override", message)
	}
	if message := spanishMethods.Message("LIMIT_EXCEEDED", "tr-TR"); message != I18nLocaleTR["LIMIT_EXCEEDED"] {
		t.Errorf("message(LIMIT_EXCEEDED, tr-TR) = %q, want the Turkish message", message)
	}
	if locales := spanishMethods.Locales(); !reflect.DeepEqual(locales, []string{"en", "es", "tr"}) {
		t.Errorf("locales = %v, want [en es tr]", locales)
	}
}

func TestI18nRejectsLanguageWithoutTranslations(t *testing.T) {
	if _, err := I18n(I18nOptions{
		Translations:  []I18nTranslation{{Locale: "tr", Messages: I18nLocaleTR}},
		DefaultLocale: "fr",
	}); err == nil {
		t.Error("a default language without translations was accepted")
	}
	_, err := I18n(I18nOptions{DefaultLocale: "tr"})
	if err == nil {
		t.Fatal("a default language without translations was accepted")
	}
	if !strings.Contains(err.Error(), "translations.tr") {
		t.Errorf("error = %q, want it to name translations.tr", err.Error())
	}
}

func TestI18nKeepsEnglishForMissingCodes(t *testing.T) {
	plugin, _, _ := i18nTestSetup(t, I18nOptions{
		Translations:  []I18nTranslation{{Locale: "tr", Messages: I18nDictionary{"EXPIRED_CARD": "Süresi dolmuş."}}},
		DefaultLocale: "tr",
	})
	expired := i18nTestAfter(t, plugin, i18nTestFailure(core.CodeExpiredCard, "raw")).(*core.PaymentResponse)
	if expired.ErrorMessage != "Süresi dolmuş." {
		t.Errorf("errorMessage = %q, want 'Süresi dolmuş.'", expired.ErrorMessage)
	}
	declined := i18nTestAfter(t, plugin, i18nTestFailure(core.CodeCardDeclined, "raw")).(*core.PaymentResponse)
	if declined.ErrorMessage != I18nLocaleEN["CARD_DECLINED"] {
		t.Errorf("errorMessage = %q, want the English message", declined.ErrorMessage)
	}
}

func TestI18nTranslatesPluginErrorCodes(t *testing.T) {
	blocked := func() *core.PaymentResponse {
		return &core.PaymentResponse{
			Status:       core.StatusFailure,
			Code:         core.CodeFraudSuspected,
			ErrorCode:    "BLOCKED_BIN",
			ErrorMessage: "bin 400000 is blocked",
		}
	}

	plugin, pctx, _ := i18nTestSetup(t, I18nOptions{})
	pctx.errorCodes["BLOCKED_BIN"] = "This card cannot be used."
	english := i18nTestAfter(t, plugin, blocked()).(*core.PaymentResponse)
	if english.ErrorMessage != "This card cannot be used." {
		t.Errorf("errorMessage = %q, want the plugin's English message", english.ErrorMessage)
	}

	translatedTR := i18nCopyDictionary(I18nLocaleTR)
	translatedTR["BLOCKED_BIN"] = "Bu kart kullanılamaz."
	turkish, turkishContext, _ := i18nTestSetup(t, I18nOptions{
		Translations:  []I18nTranslation{{Locale: "tr", Messages: translatedTR}},
		DefaultLocale: "tr",
	})
	turkishContext.errorCodes["BLOCKED_BIN"] = "This card cannot be used."
	result := i18nTestAfter(t, turkish, blocked()).(*core.PaymentResponse)
	if result.ErrorMessage != "Bu kart kullanılamaz." {
		t.Errorf("errorMessage = %q, want 'Bu kart kullanılamaz.'", result.ErrorMessage)
	}
	if result.ProviderMessage != "bin 400000 is blocked" {
		t.Errorf("providerMessage = %q, want the original message", result.ProviderMessage)
	}
}

func TestI18nTranslateMethod(t *testing.T) {
	_, _, methods := i18nTestSetup(t, I18nOptions{
		Translations:  []I18nTranslation{{Locale: "tr", Messages: I18nLocaleTR}},
		DefaultLocale: "tr",
	})

	translated := methods.Translate(map[string]any{
		"status":       string(core.StatusFailure),
		"code":         string(core.CodeUnknown),
		"errorMessage": "raw",
	}).(map[string]any)
	if translated["errorMessage"] != I18nLocaleTR["UNKNOWN"] {
		t.Errorf("errorMessage = %v, want the Turkish UNKNOWN message", translated["errorMessage"])
	}
	if translated["providerMessage"] != "raw" {
		t.Errorf("providerMessage = %v, want 'raw'", translated["providerMessage"])
	}

	untouched := methods.Translate(map[string]any{"status": string(core.StatusSuccess)}).(map[string]any)
	if !reflect.DeepEqual(untouched, map[string]any{"status": string(core.StatusSuccess)}) {
		t.Errorf("a success was changed: %v", untouched)
	}
}

func TestI18nAnswersInAcceptLanguage(t *testing.T) {
	plugin, _, _ := i18nTestSetup(t, I18nOptions{Translations: I18nLocales()})
	body := func() *core.PaymentResponse {
		return i18nTestFailure(core.CodeInsufficientFunds, "raw decline")
	}
	cases := []struct {
		headers map[string]string
		want    string
	}{
		{map[string]string{"Accept-Language": "tr-TR,tr;q=0.9,en;q=0.8"}, I18nLocaleTR["INSUFFICIENT_FUNDS"]},
		{map[string]string{"accept-language": "de-DE,de;q=0.9,en;q=0.5"}, I18nLocaleDE["INSUFFICIENT_FUNDS"]},
		{map[string]string{"accept-language": "ru-RU,ru;q=0.9,en;q=0.5"}, I18nLocaleRU["INSUFFICIENT_FUNDS"]},
		{map[string]string{"accept-language": "ar-SA,ar;q=0.9,en;q=0.5"}, I18nLocaleAR["INSUFFICIENT_FUNDS"]},
		{map[string]string{"accept-language": "es-ES, en;q=0.5"}, I18nLocaleEN["INSUFFICIENT_FUNDS"]},
		{map[string]string{"accept-language": "fr"}, I18nLocaleEN["INSUFFICIENT_FUNDS"]},
		{nil, I18nLocaleEN["INSUFFICIENT_FUNDS"]},
	}
	for _, testCase := range cases {
		response := i18nTestRespond(t, plugin, body(), testCase.headers)
		if message := i18nTestMessage(t, response); message != testCase.want {
			t.Errorf("headers %v: errorMessage = %q, want %q", testCase.headers, message, testCase.want)
		}
	}
}

func TestI18nDetectsCookieCallbackOrNothing(t *testing.T) {
	cookiePlugin, _, _ := i18nTestSetup(t, I18nOptions{
		Translations: I18nLocales(),
		Detection:    []I18nDetectionStrategy{I18nDetectCookie, I18nDetectHeader},
		LocaleCookie: "lang",
	})
	fromCookie := i18nTestRespond(t, cookiePlugin, i18nTestFailure(core.CodeInsufficientFunds, "raw"),
		map[string]string{"Cookie": "theme=dark; lang=de", "accept-language": "tr"})
	if message := i18nTestMessage(t, fromCookie); message != I18nLocaleDE["INSUFFICIENT_FUNDS"] {
		t.Errorf("cookie locale: errorMessage = %q, want the German message", message)
	}
	unsupportedCookie := i18nTestRespond(t, cookiePlugin, i18nTestFailure(core.CodeInsufficientFunds, "raw"),
		map[string]string{"cookie": "lang=fr", "accept-language": "tr"})
	if message := i18nTestMessage(t, unsupportedCookie); message != I18nLocaleTR["INSUFFICIENT_FUNDS"] {
		t.Errorf("unsupported cookie locale: errorMessage = %q, want the Turkish message", message)
	}

	fromQuery, _, queryMethods := i18nTestSetup(t, I18nOptions{
		Translations: I18nLocales(),
		Detection:    []I18nDetectionStrategy{I18nDetectCallback},
		GetLocale: func(request *core.HandlerRequest) (string, error) {
			parsed, err := url.Parse(request.URL)
			if err != nil {
				return "", err
			}
			return parsed.Query().Get("lang"), nil
		},
	})
	response := i18nTestRespondTo(t, fromQuery, i18nTestFailure(core.CodeInsufficientFunds, "raw"),
		pluginTestPost("/api/pay/mock/payment?lang=tr", pluginTestPaymentRequest(), nil))
	if message := i18nTestMessage(t, response); message != I18nLocaleTR["INSUFFICIENT_FUNDS"] {
		t.Errorf("query locale: errorMessage = %q, want the Turkish message", message)
	}
	if locale, err := queryMethods.DetectLocale(pluginTestPost("/x?lang=ru-RU", nil, nil)); err != nil || locale != "ru" {
		t.Errorf("detectLocale(/x?lang=ru-RU) = %q, %v; want ru", locale, err)
	}
	if locale, err := queryMethods.DetectLocale(pluginTestPost("/x", nil, nil)); err != nil || locale != "en" {
		t.Errorf("detectLocale(/x) = %q, %v; want en", locale, err)
	}

	fixed, _, _ := i18nTestSetup(t, I18nOptions{
		Translations:  I18nLocales(),
		DefaultLocale: "tr",
		Detection:     []I18nDetectionStrategy{},
	})
	ignored := i18nTestRespond(t, fixed, i18nTestFailure(core.CodeInsufficientFunds, "raw"),
		map[string]string{"accept-language": "en"})
	if message := i18nTestMessage(t, ignored); message != I18nLocaleTR["INSUFFICIENT_FUNDS"] {
		t.Errorf("detection disabled: errorMessage = %q, want the Turkish message", message)
	}
}

func TestI18nTranslatesReplayedResponsesPerRequest(t *testing.T) {
	plugin, _, _ := i18nTestSetup(t, I18nOptions{Translations: I18nLocales()})
	first := i18nTestRespond(t, plugin, i18nTestFailure(core.CodeInsufficientFunds, "raw decline"),
		map[string]string{"Idempotency-Key": "k1", "accept-language": "en"})
	replay := i18nTestRespond(t, plugin, first.Body,
		map[string]string{"Idempotency-Key": "k1", "accept-language": "tr"})

	if message := i18nTestMessage(t, first); message != I18nLocaleEN["INSUFFICIENT_FUNDS"] {
		t.Errorf("first response = %q, want the English message", message)
	}
	if message := i18nTestMessage(t, replay); message != I18nLocaleTR["INSUFFICIENT_FUNDS"] {
		t.Errorf("replayed response = %q, want the Turkish message", message)
	}
	original := first.Body.(*core.PaymentResponse).ProviderMessage
	replayed := replay.Body.(*core.PaymentResponse).ProviderMessage
	if original != replayed || original != "raw decline" {
		t.Errorf("providerMessage changed on replay: %q → %q", original, replayed)
	}
}

func TestLocalizedErrorsWorksLikeBefore(t *testing.T) {
	plugin, err := LocalizedErrors(LocalizedErrorsOptions{
		Locale:   "es",
		Messages: []I18nTranslation{{Locale: "es", Messages: I18nDictionary{"CARD_DECLINED": "Rechazada."}}},
	})
	if err != nil {
		t.Fatalf("localizedErrors() failed: %v", err)
	}
	pctx := newPluginTestContext()
	methods := pluginTestMethods(t, plugin, pctx)["errors"].(*I18nMethods)

	declined := i18nTestAfter(t, plugin, i18nTestFailure(core.CodeCardDeclined, "raw")).(*core.PaymentResponse)
	if declined.ErrorMessage != "Rechazada." {
		t.Errorf("errorMessage = %q, want 'Rechazada.'", declined.ErrorMessage)
	}
	if locales := methods.Locales(); !reflect.DeepEqual(locales, []string{"en", "tr", "de", "ru", "ar", "es"}) {
		t.Errorf("locales = %v, want [en tr de ru ar es]", locales)
	}
	response := i18nTestRespond(t, plugin, i18nTestFailure(core.CodeExpiredCard, "raw"),
		map[string]string{"accept-language": "tr-TR"})
	if message := i18nTestMessage(t, response); message != I18nLocaleTR["EXPIRED_CARD"] {
		t.Errorf("handler errorMessage = %q, want the Turkish message", message)
	}
}

func TestLocalizedErrorsKeepsFallbackAndDetection(t *testing.T) {
	if _, err := LocalizedErrors(LocalizedErrorsOptions{Locale: "fr"}); err == nil {
		t.Error("a locale without translations was accepted")
	}
	_, err := LocalizedErrors(LocalizedErrorsOptions{FallbackLocale: "fr"})
	if err == nil {
		t.Fatal("a fallback locale without translations was accepted")
	}
	if !strings.Contains(err.Error(), "translations.fr") {
		t.Errorf("error = %q, want it to name translations.fr", err.Error())
	}

	plugin, err := LocalizedErrors(LocalizedErrorsOptions{
		Locale:         "es",
		FallbackLocale: "tr",
		Messages:       []I18nTranslation{{Locale: "es", Messages: I18nDictionary{}}},
		DetectLocale:   false,
	})
	if err != nil {
		t.Fatalf("localizedErrors() failed: %v", err)
	}
	pctx := newPluginTestContext()
	methods := pluginTestMethods(t, plugin, pctx)["errors"].(*I18nMethods)
	if message := methods.Message("CARD_DECLINED"); message != I18nLocaleTR["CARD_DECLINED"] {
		t.Errorf("message(CARD_DECLINED) = %q, want the Turkish fallback", message)
	}
	locale, err := methods.DetectLocale(pluginTestPost("/x", nil, map[string]string{"accept-language": "de"}))
	if err != nil || locale != "es" {
		t.Errorf("detectLocale = %q, %v; want es", locale, err)
	}
}

func TestLocalizedErrorsDetectLocaleCallback(t *testing.T) {
	plugin, err := LocalizedErrors(LocalizedErrorsOptions{
		Locale:       "en",
		DetectLocale: func(*core.HandlerRequest) string { return "tr-TR" },
	})
	if err != nil {
		t.Fatalf("localizedErrors() failed: %v", err)
	}
	pctx := newPluginTestContext()
	methods := pluginTestMethods(t, plugin, pctx)["errors"].(*I18nMethods)
	locale, err := methods.DetectLocale(pluginTestPost("/x", nil, nil))
	if err != nil || locale != "tr" {
		t.Errorf("detectLocale = %q, %v; want tr", locale, err)
	}
}

func TestParseAcceptLanguageOrdersByQuality(t *testing.T) {
	got := ParseAcceptLanguage("en;q=0.5, tr-TR, de;q=0.8, *;q=0.1, fr;q=0")
	want := []string{"tr-tr", "de", "en"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseAcceptLanguage = %v, want %v", got, want)
	}
	if empty := ParseAcceptLanguage(""); len(empty) != 0 {
		t.Errorf("parseAcceptLanguage('') = %v, want []", empty)
	}
}

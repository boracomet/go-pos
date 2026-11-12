package plugins

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func redactTestAfter(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, request, result any) (any, any) {
	t.Helper()
	ctx, err := pluginTestAfterContext(plugin, pluginTestOutcome(operation, "mock", request, result))
	if err != nil {
		t.Fatalf("after hook failed: %v", err)
	}
	return ctx.Request, ctx.Result
}

func redactTestMethods(t *testing.T, options RedactOptions) *RedactMethods {
	t.Helper()
	methods, ok := pluginTestMethods(t, Redact(options), newPluginTestContext())["redact"].(*RedactMethods)
	if !ok {
		t.Fatal("the redact member is missing")
	}
	return methods
}

func TestRedactMasksEventsButNotTheProviderPayload(t *testing.T) {
	request := pluginTestPaymentRequest()
	request.PaymentCard.CardNumber = "5528790000000008"
	request.PaymentCard.CVC = "123"
	request.PaymentCard.ExpireMonth = "12"
	request.PaymentCard.ExpireYear = "2028"
	request.Buyer.IdentityNumber = "12345678901"
	request.Buyer.GSMNumber = "+905321112233"

	result := &core.PaymentResponse{
		Status:    core.StatusSuccess,
		PaymentID: "pay_123",
		RawResponse: map[string]any{
			"note": "Processed for card 5528790000000008 with TCKN 12345678901",
		},
	}
	plugin := Redact(RedactOptions{Card: CardMaskFirst6Last4})
	pctx := newPluginTestContext()

	maskedRequest, maskedResult := redactTestAfter(t, plugin, core.OperationCreatePayment, request, result)

	if request.PaymentCard.CardNumber != "5528790000000008" || request.PaymentCard.CVC != "123" {
		t.Error("the request sent to the provider was mutated")
	}
	if request.Buyer.IdentityNumber != "12345678901" {
		t.Error("the buyer sent to the provider was mutated")
	}

	eventRequest, ok := maskedRequest.(*core.PaymentRequest)
	if !ok {
		t.Fatalf("masked request is %T, want *core.PaymentRequest", maskedRequest)
	}
	if eventRequest.PaymentCard.CardNumber != "552879******0008" {
		t.Errorf("cardNumber = %q, want 552879******0008", eventRequest.PaymentCard.CardNumber)
	}
	if eventRequest.PaymentCard.CVC != "***" {
		t.Errorf("cvc = %q, want ***", eventRequest.PaymentCard.CVC)
	}
	if eventRequest.PaymentCard.ExpireMonth != "<redacted>" || eventRequest.PaymentCard.ExpireYear != "<redacted>" {
		t.Errorf("expiry = %q/%q, want <redacted>", eventRequest.PaymentCard.ExpireMonth, eventRequest.PaymentCard.ExpireYear)
	}
	if eventRequest.Buyer.IdentityNumber != "<redacted>" || eventRequest.Buyer.GSMNumber != "<redacted>" {
		t.Errorf("buyer = %q/%q, want <redacted>", eventRequest.Buyer.IdentityNumber, eventRequest.Buyer.GSMNumber)
	}

	eventResult, ok := maskedResult.(*core.PaymentResponse)
	if !ok {
		t.Fatalf("masked result is %T, want *core.PaymentResponse", maskedResult)
	}
	raw, ok := eventResult.RawResponse.(map[string]any)
	if !ok {
		t.Fatalf("rawResponse is %T, want map[string]any", eventResult.RawResponse)
	}
	if raw["note"] != "Processed for card 552879******0008 with TCKN <redacted>" {
		t.Errorf("rawResponse note = %v", raw["note"])
	}

	event := core.EventFor(core.OperationCreatePayment, "mock", maskedRequest, maskedResult)
	if event == nil {
		t.Fatal("the masked result triggers no event")
	}
	if err := pctx.Emit(context.Background(), event); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	emitted := pctx.emitted()
	if len(emitted) != 1 {
		t.Fatalf("emitted %d events, want 1", len(emitted))
	}
	delivered := emitted[0].Request.(*core.PaymentRequest)
	if delivered.PaymentCard.CardNumber != "552879******0008" {
		t.Errorf("the listener received cardNumber %q", delivered.PaymentCard.CardNumber)
	}
}

func TestRedactWrapsLogger(t *testing.T) {
	base := &pluginTestLogger{}
	logger := RedactLogger(base, RedactOptions{})

	logger.Info("Received payment request for 5454540000005454, TCKN: 98765432109", map[string]any{
		"cardNumber":     "5454540000005454",
		"cvc":            "999",
		"identityNumber": "98765432109",
	})

	entries := base.all()
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if entries[0].Message != "Received payment request for 545454******5454, TCKN: <redacted>" {
		t.Errorf("message = %q", entries[0].Message)
	}
	want := map[string]any{
		"cardNumber":     "545454******5454",
		"cvc":            "***",
		"identityNumber": "<redacted>",
	}
	if !reflect.DeepEqual(entries[0].Meta, want) {
		t.Errorf("meta = %v, want %v", entries[0].Meta, want)
	}
	if again := RedactLogger(logger, RedactOptions{}); again != logger {
		t.Error("wrapping an already wrapped logger built a second wrapper")
	}
}

func TestRedactCardMaskingStyles(t *testing.T) {
	cases := []struct {
		name string
		card any
		want string
	}{
		{"last4", CardMaskLast4, "************1234"},
		{"none", CardMaskNone, "<redacted>"},
		{"custom", func(pan string) string { return "CARD-" + pan[len(pan)-4:] }, "CARD-1234"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := pluginTestPaymentRequest()
			request.PaymentCard.CardNumber = "4543600000001234"
			plugin := Redact(RedactOptions{Card: testCase.card})
			maskedRequest, _ := redactTestAfter(t, plugin, core.OperationCreatePayment, request,
				&core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "pay_test"})
			masked := maskedRequest.(*core.PaymentRequest)
			if masked.PaymentCard.CardNumber != testCase.want {
				t.Errorf("cardNumber = %q, want %q", masked.PaymentCard.CardNumber, testCase.want)
			}
			if request.PaymentCard.CardNumber != "4543600000001234" {
				t.Error("the original request was mutated")
			}
		})
	}
}

func TestRedactUtilityMethods(t *testing.T) {
	methods := redactTestMethods(t, RedactOptions{})

	if masked := methods.MaskCard("5528790000000008"); masked != "552879******0008" {
		t.Errorf("maskCard = %q, want 552879******0008", masked)
	}
	masked, ok := methods.Mask(map[string]any{
		"user":           "Ali",
		"cardNumber":     "5528790000000008",
		"cvc":            "456",
		"identityNumber": "11122233344",
		"extra":          "Customer phone is 05554443322",
	}).(map[string]any)
	if !ok {
		t.Fatal("mask did not return a map")
	}
	want := map[string]any{
		"user":           "Ali",
		"cardNumber":     "552879******0008",
		"cvc":            "***",
		"identityNumber": "<redacted>",
		"extra":          "Customer phone is <redacted>",
	}
	if !reflect.DeepEqual(masked, want) {
		t.Errorf("mask = %v, want %v", masked, want)
	}
}

func TestRedactCyclicObject(t *testing.T) {
	redactor := CreateRedactor(RedactOptions{})
	cyclic := map[string]any{"cardNumber": "4111111111111111"}
	cyclic["self"] = cyclic

	masked, ok := redactor.Mask(cyclic).(map[string]any)
	if !ok {
		t.Fatal("mask did not return a map")
	}
	if masked["cardNumber"] != "411111******1111" {
		t.Errorf("cardNumber = %v", masked["cardNumber"])
	}
	if masked["self"] != "<redacted>" {
		t.Errorf("self = %v, want <redacted>", masked["self"])
	}
	if reflect.ValueOf(cyclic["self"]).Pointer() != reflect.ValueOf(cyclic).Pointer() {
		t.Error("the original cycle was mutated")
	}
}

func TestRedactCyclicArray(t *testing.T) {
	redactor := CreateRedactor(RedactOptions{})
	list := make([]any, 2)
	list[0] = "5454540000005454"
	list[1] = list

	masked, ok := redactor.Mask(list).([]any)
	if !ok {
		t.Fatal("mask did not return a slice")
	}
	if masked[0] != "545454******5454" {
		t.Errorf("first item = %v", masked[0])
	}
	if masked[1] != "<redacted>" {
		t.Errorf("back-reference = %v, want <redacted>", masked[1])
	}
}

// A "__proto__" key is an ordinary map key in Go: it must survive masking as data
// and never reach another value.
func TestRedactProtoKeyStaysData(t *testing.T) {
	redactor := CreateRedactor(RedactOptions{})
	masked, ok := redactor.Mask(map[string]any{
		"__proto__":  map[string]any{"polluted": "leak"},
		"cardNumber": "5528790000000008",
	}).(map[string]any)
	if !ok {
		t.Fatal("mask did not return a map")
	}
	if _, present := masked["__proto__"]; !present {
		t.Error("the __proto__ key was dropped")
	}
	if masked["cardNumber"] != "552879******0008" {
		t.Errorf("cardNumber = %v", masked["cardNumber"])
	}
	if other := redactor.Mask(map[string]any{}).(map[string]any); len(other) != 0 {
		t.Errorf("an unrelated value was polluted: %v", other)
	}
}

func TestRedactPANInRawResponse(t *testing.T) {
	redactor := CreateRedactor(RedactOptions{})
	masked := redactor.Mask(map[string]any{
		"rawResponse": map[string]any{
			"pan":       "5528790000000008",
			"echo":      "charged card_5528790000000008 with TCKN 12345678901",
			"cardToken": "tok_5454540000005454",
			"bin":       "552879",
			"last4":     "0008",
		},
	}).(map[string]any)
	raw := masked["rawResponse"].(map[string]any)

	if raw["pan"] != "552879******0008" {
		t.Errorf("pan = %v", raw["pan"])
	}
	if raw["echo"] != "charged card_552879******0008 with TCKN <redacted>" {
		t.Errorf("echo = %v", raw["echo"])
	}
	if raw["cardToken"] != "tok_545454******5454" {
		t.Errorf("cardToken = %v", raw["cardToken"])
	}
	if raw["bin"] != "552879" || raw["last4"] != "0008" {
		t.Errorf("bin/last4 = %v/%v, want them left visible", raw["bin"], raw["last4"])
	}
}

func TestRedactKeepsDefaultsWithCustomFields(t *testing.T) {
	redactor := CreateRedactor(RedactOptions{
		Fields: []string{"email"},
		Custom: []*regexp.Regexp{regexp.MustCompile(`SECRET-\w+`)},
	})
	masked := redactor.Mask(map[string]any{
		"email":          "a@b.com",
		"cvc":            "123",
		"cardHolderName": "Ali Veli",
		"note":           "TCKN 12345678901, ref SECRET-abc",
	}).(map[string]any)

	if masked["email"] != "<redacted>" {
		t.Errorf("email = %v", masked["email"])
	}
	if masked["cvc"] != "***" {
		t.Errorf("cvc = %v, want ***", masked["cvc"])
	}
	if masked["cardHolderName"] != "<redacted>" {
		t.Errorf("cardHolderName = %v", masked["cardHolderName"])
	}
	note, _ := masked["note"].(string)
	if strings.Contains(note, "12345678901") || strings.Contains(note, "SECRET-abc") {
		t.Errorf("note = %q, want the TCKN and the secret masked", note)
	}
}

func TestRedactKeepsIdentifiersAndThreeDSPage(t *testing.T) {
	html := `<form><input name="oid" value="4543601234567890123"></form>`
	result := &core.ThreeDSInitResponse{
		Status:             core.StatusSuccess,
		PaymentID:          "1234567890123456",
		ThreeDSHTMLContent: html,
	}
	plugin := Redact(RedactOptions{})
	request := pluginTestJSON(t, pluginTestPaymentRequest())
	request["callbackUrl"] = "https://shop.test/callback"

	_, maskedResult := redactTestAfter(t, plugin, core.OperationInitThreeDSPayment, request, result)
	masked, ok := maskedResult.(*core.ThreeDSInitResponse)
	if !ok {
		t.Fatalf("masked result is %T, want *core.ThreeDSInitResponse", maskedResult)
	}
	if masked.PaymentID != "1234567890123456" {
		t.Errorf("paymentId = %q, want it unmasked", masked.PaymentID)
	}
	if masked.ThreeDSHTMLContent != html {
		t.Errorf("threeDSHtmlContent = %q, want it unmasked", masked.ThreeDSHTMLContent)
	}
}

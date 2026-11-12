package sipay

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func wantText(t *testing.T, body Result, key, want string) {
	t.Helper()
	if got := jsString(body[key]); got != want {
		t.Errorf("body[%q] = %q, want %q", key, got, want)
	}
}

func wantNumber(t *testing.T, body Result, key string, want float64) {
	t.Helper()
	got, ok := jsNumber(body[key])
	if !ok || got != want {
		t.Errorf("body[%q] = %v, want %v", key, body[key], want)
	}
}

func wantBool(t *testing.T, body Result, key string, want bool) {
	t.Helper()
	if got, ok := body[key].(bool); !ok || got != want {
		t.Errorf("body[%q] = %v, want %v", key, body[key], want)
	}
}

func itemsOf(t *testing.T, body Result) []Result {
	t.Helper()
	list, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("body[items] = %v, want a list", body["items"])
	}
	items := make([]Result, 0, len(list))
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item = %v, want an object", item)
		}
		items = append(items, record)
	}
	return items
}

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"appId", "appSecret", "merchantKey"} {
		cfg := Config{
			ProviderConfig: core.ProviderConfig{BaseURL: SandboxURL},
			AppID:          testAppID,
			AppSecret:      testAppSecret,
			MerchantKey:    testMerchantKey,
		}
		switch field {
		case "appId":
			cfg.AppID = ""
		case "appSecret":
			cfg.AppSecret = ""
		case "merchantKey":
			cfg.MerchantKey = ""
		}
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("New() without %s error = %v, want it to name %s", field, err, field)
		}
	}
}

func TestBaseURLs(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{SandboxURL, "https://provisioning.sipay.com.tr"},
		{ProductionURL, "https://app.sipay.com.tr"},
		{QNBpaySandboxURL, "https://test.qnbpay.com.tr"},
		{QNBpayProductionURL, "https://portal.qnbpay.com.tr"},
	} {
		if tc.got != tc.want {
			t.Errorf("base URL = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestQNBpayBrand(t *testing.T) {
	provider := newProviderWith(t, Config{
		ProviderConfig: core.ProviderConfig{BaseURL: QNBpaySandboxURL},
		Brand:          BrandQNBpay,
		AppID:          testAppID,
		AppSecret:      testAppSecret,
		MerchantKey:    testMerchantKey,
	})
	if provider.Name() != "qnbpay" {
		t.Errorf("Name() = %q, want qnbpay", provider.Name())
	}
	if provider.Brand() != BrandQNBpay {
		t.Errorf("Brand() = %q, want %q", provider.Brand(), BrandQNBpay)
	}
}

func TestNewRejectsUnknownBrand(t *testing.T) {
	_, err := New(Config{
		ProviderConfig: core.ProviderConfig{BaseURL: SandboxURL},
		Brand:          Brand("nope"),
		AppID:          testAppID,
		AppSecret:      testAppSecret,
		MerchantKey:    testMerchantKey,
	})
	if err == nil || !strings.Contains(err.Error(), "Unknown Sipay brand") {
		t.Errorf("New() error = %v, want it to reject the brand", err)
	}
}

func TestBrandIsNamedInErrors(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointToken, `{"status_code":30,"status_description":"Invalid credentials."}`)
	provider := newProviderWith(t, Config{
		ProviderConfig: core.ProviderConfig{BaseURL: QNBpaySandboxURL, Doer: doer},
		Brand:          BrandQNBpay,
		AppID:          testAppID,
		AppSecret:      testAppSecret,
		MerchantKey:    testMerchantKey,
	})

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "30" {
		t.Errorf("errorCode = %q, want 30", result.ErrorCode)
	}
	if !strings.HasPrefix(result.ErrorMessage, "QNBpay did not issue a token") {
		t.Errorf("errorMessage = %q, want it to name QNBpay", result.ErrorMessage)
	}
}

func TestQNBpayCompletesTheSameCallback(t *testing.T) {
	doer := &stubDoer{}
	provider := newProviderWith(t, Config{
		ProviderConfig: core.ProviderConfig{BaseURL: QNBpaySandboxURL, Doer: doer},
		Brand:          BrandQNBpay,
		AppID:          testAppID,
		AppSecret:      testAppSecret,
		MerchantKey:    testMerchantKey,
	})
	hashKey, err := CreateHashKey([]string{"1", "1.20", "INVQ1", "ORDQ1", "TRY"}, testAppSecret)
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}

	result, err := provider.CompleteThreeDSPayment(context.Background(), map[string]string{
		"invoice_id":     "INVQ1",
		"order_id":       "ORDQ1",
		"payment_status": "1",
		"hash_key":       hashKey,
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.PaymentID != "INVQ1" {
		t.Errorf("paymentId = %q, want INVQ1", result.PaymentID)
	}
}

func TestTokenIsRequestedOnceAndSentAsBearer(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(nil), statusResponse(nil))
	provider := newTestProvider(t, doer)

	for i := 0; i < 2; i++ {
		if _, err := provider.GetPayment(context.Background(), "ORDER1"); err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
	}
	tokens := doer.tokenCalls()
	if len(tokens) != 1 {
		t.Fatalf("token calls = %d, want 1", len(tokens))
	}
	body := tokens[0].jsonBody(t)
	wantText(t, body, "app_id", testAppID)
	wantText(t, body, "app_secret", testAppSecret)
	wantText(t, body, "app_lang", "tr")
	if got, want := doer.apiCalls()[0].authorization(), "Bearer test-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestTokenIsRenewedWhenUnauthenticated(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointToken, tokenResponse("test-token"), tokenResponse("new-token"))
	doer.respond(EndpointCheckStatus, unauthenticatedResponse, statusResponse(nil))
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	calls := doer.apiCalls()
	if len(calls) != 2 {
		t.Fatalf("api calls = %d, want 2", len(calls))
	}
	if got, want := calls[1].authorization(), "Bearer new-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestWrongCredentialsAreAFailure(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointToken, `{"status_code":30,"status_description":"Invalid credentials"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "30" {
		t.Errorf("errorCode = %q, want 30", result.ErrorCode)
	}
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
	if !strings.Contains(result.ErrorMessage, "Invalid credentials") {
		t.Errorf("errorMessage = %q, want it to mention the credentials", result.ErrorMessage)
	}
	if len(doer.apiCalls()) != 0 {
		t.Errorf("api calls = %d, want 0", len(doer.apiCalls()))
	}
}

// The token request is read-only, so a transport failure is retried.
func TestTokenRequestIsRetried(t *testing.T) {
	doer := &stubDoer{}
	doer.respondError(EndpointToken, timeoutError())
	provider := newTestProvider(t, doer)

	if _, err := provider.GetPayment(context.Background(), "ORDER1"); err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if got := len(doer.tokenCalls()); got != 2 {
		t.Errorf("token calls = %d, want 2 (the token request is retryable)", got)
	}
}

func TestCreatePaymentSendsSignedPaySmart2D(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, paymentResponse(nil))
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.ConversationID = "ORDER2D"
	request.Installment = 3
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}

	calls := doer.apiCalls()
	if len(calls) != 1 {
		t.Fatalf("api calls = %d, want 1", len(calls))
	}
	if calls[0].Path != EndpointPay2D {
		t.Errorf("path = %q, want %q", calls[0].Path, EndpointPay2D)
	}
	body := calls[0].jsonBody(t)
	for key, want := range map[string]string{
		"cc_holder_name":   "John Doe",
		"cc_no":            "5528790000000008",
		"expiry_month":     "12",
		"expiry_year":      "2030",
		"cvv":              "123",
		"currency_code":    "TRY",
		"invoice_id":       "ORDER2D",
		"name":             "John",
		"surname":          "Doe",
		"total":            "1.20",
		"merchant_key":     testMerchantKey,
		"bill_email":       "john.doe@example.com",
		"bill_phone":       "+905350000000",
		"ip":               "85.34.78.112",
		"transaction_type": "Auth",
		"app_lang":         "tr",
	} {
		wantText(t, body, key, want)
	}
	wantNumber(t, body, "installments_number", 3)
	if jsString(body["bill_address1"]) == "" {
		t.Error("body[bill_address1] is empty, want the billing address")
	}
	if _, present := body["app_secret"]; present {
		t.Error("body carries app_secret, want it kept out of the request")
	}
	// paidPrice differs from the basket: one item for the whole order.
	items := itemsOf(t, body)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	wantText(t, items[0], "name", "Order ORDER2D")
	wantText(t, items[0], "price", "1.20")
	wantNumber(t, items[0], "quantity", 1)
	if got, want := strings.Join(hashFields(t, body), "|"), "1.20|3|TRY|"+testMerchantKey+"|ORDER2D"; got != want {
		t.Errorf("hash_key = %q, want %q", got, want)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.PaymentID != "ORDER2D" || result.ConversationID != "ORDER2D" {
		t.Errorf("ids = %q/%q, want ORDER2D", result.PaymentID, result.ConversationID)
	}
}

func TestCreatePaymentSendsBasketWhenItAddsUp(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, paymentResponse(nil))
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.PaidPrice = "1"
	if _, err := provider.CreatePayment(context.Background(), request); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if got := len(itemsOf(t, doer.apiCalls()[0].jsonBody(t))); got != 3 {
		t.Errorf("items = %d, want 3", got)
	}
}

func TestCreatePaymentSendsSaleWebhookKey(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, paymentResponse(nil))
	provider := newProviderWith(t, Config{
		ProviderConfig: core.ProviderConfig{BaseURL: SandboxURL, Doer: doer},
		AppID:          testAppID,
		AppSecret:      testAppSecret,
		MerchantKey:    testMerchantKey,
		SaleWebhookKey: "sales",
	})

	if _, err := provider.CreatePayment(context.Background(), mockPaymentRequest()); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	wantText(t, doer.apiCalls()[0].jsonBody(t), "sale_web_hook_key", "sales")
}

func TestCreatePaymentReportsBankDecline(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, paymentDeclined)
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.ConversationID = "X"
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.PaymentID != "X" {
		t.Errorf("paymentId = %q, want X", result.PaymentID)
	}
	if result.ErrorCode != "51" {
		t.Errorf("errorCode = %q, want 51", result.ErrorCode)
	}
	if result.ErrorMessage != "Limit yetersiz" {
		t.Errorf("errorMessage = %q, want Limit yetersiz", result.ErrorMessage)
	}
	if result.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
	}
}

func TestCreatePaymentMapsSipayStatusCode(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, `{"status_code":3,"status_description":"The invoice id already processed",`+
		`"data":{"invoice_id":"X","payment_status":0,"error_code":3,"error":"already processed"}}`)
	provider := newTestProvider(t, doer)

	result, err := provider.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.ErrorCode != "3" {
		t.Errorf("errorCode = %q, want 3", result.ErrorCode)
	}
	if result.Code != core.CodeDuplicateOrder {
		t.Errorf("code = %q, want %q", result.Code, core.CodeDuplicateOrder)
	}
}

func TestCreatePaymentReports422WithoutData(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, `{"status_code":422,"status_description":"Billing information is required"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "422" {
		t.Errorf("errorCode = %q, want 422", result.ErrorCode)
	}
	if result.ErrorMessage != "Billing information is required" {
		t.Errorf("errorMessage = %q", result.ErrorMessage)
	}
}

func TestCreatePaymentValidatesBeforeCalling(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.Buyer.Email = ""
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(doer.calls))
	}
}

// A payment is never retried: a timeout after the provider received it would
// charge twice, so the outcome stays PENDING.
func TestCreatePaymentTimeoutIsPendingAndNotRetried(t *testing.T) {
	doer := &stubDoer{}
	doer.respondError(EndpointPay2D, timeoutError())
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.ConversationID = "T1"
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
	if result.PaymentID != "T1" {
		t.Errorf("paymentId = %q, want T1", result.PaymentID)
	}
	if got := len(doer.apiCalls()); got != 1 {
		t.Errorf("api calls = %d, want 1 (payments are not retryable)", got)
	}
}

func TestInitThreeDSPaymentReturnsThePage(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, threeDSHTML)
	provider := newTestProvider(t, doer)

	request := &ThreeDSRequest{
		ThreeDSPaymentRequest: *mockThreeDSPaymentRequest(),
		FailURL:               "https://example.com/fail",
	}
	request.ConversationID = "ORDER3D"
	result, err := provider.InitThreeDSPaymentWithFailURL(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPaymentWithFailURL() error = %v", err)
	}

	calls := doer.apiCalls()
	if calls[0].Path != EndpointPay3D {
		t.Errorf("path = %q, want %q", calls[0].Path, EndpointPay3D)
	}
	body := calls[0].jsonBody(t)
	for key, want := range map[string]string{
		"invoice_id":       "ORDER3D",
		"total":            "1.20",
		"return_url":       "https://example.com/callback",
		"cancel_url":       "https://example.com/fail",
		"response_method":  "POST",
		"transaction_type": "Auth",
	} {
		wantText(t, body, key, want)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if result.ThreeDSHTMLContent != threeDSHTML {
		t.Error("threeDSHtmlContent is not the page returned by the provider")
	}
	if result.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
	}
}

func TestInitThreeDSPaymentPostsFailuresToCallbackURL(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, threeDSHTML)
	provider := newTestProvider(t, doer)

	if _, err := provider.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest()); err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	wantText(t, doer.apiCalls()[0].jsonBody(t), "cancel_url", "https://example.com/callback")
}

func TestInitThreeDSPaymentReportsRejectionForm(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, threeDSRejectedHTML)
	provider := newTestProvider(t, doer)

	request := mockThreeDSPaymentRequest()
	request.ConversationID = "ORDER3D"
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
	}
	if result.ErrorCode != "68" {
		t.Errorf("errorCode = %q, want 68", result.ErrorCode)
	}
	if result.ErrorMessage != "Invalid hash key" {
		t.Errorf("errorMessage = %q, want Invalid hash key", result.ErrorMessage)
	}
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
	if result.ThreeDSHTMLContent != "" {
		t.Error("threeDSHtmlContent is set for a rejected request, want it empty")
	}
}

func TestInitThreeDSPaymentReportsJSONError(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, `{"status_code":1049,"status_description":"Invalid callback URL. Please use a whitelisted domain"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "1049" {
		t.Errorf("errorCode = %q, want 1049", result.ErrorCode)
	}
	if !strings.Contains(result.ErrorMessage, "whitelisted") {
		t.Errorf("errorMessage = %q, want it to mention the whitelist", result.ErrorMessage)
	}
}

func TestInitThreeDSPaymentReportsEmptyResponse(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, "")
	provider := newTestProvider(t, doer)

	result, err := provider.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
}

func TestInitThreeDSAuthorizeSendsPreAuth(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay3D, threeDSHTML)
	provider := newTestProvider(t, doer)

	result, err := provider.InitThreeDSAuthorize(context.Background(), mockThreeDSPaymentRequest())
	if err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	wantText(t, doer.apiCalls()[0].jsonBody(t), "transaction_type", "PreAuth")
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
}

func TestCompleteThreeDSPaymentTrustsTheHashKey(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), signedCallback(t, "", "", "", "", nil))
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.PaymentID != "ORDER3D" || result.ConversationID != "ORDER3D" {
		t.Errorf("ids = %q/%q, want ORDER3D", result.PaymentID, result.ConversationID)
	}
	raw, ok := result.RawResponse.(Result)
	if !ok {
		t.Fatalf("rawResponse = %T, want Result", result.RawResponse)
	}
	want := &VerifiedHash{Status: "1", Total: "1.20", InvoiceID: "ORDER3D", OrderID: "VP17917145548423635", Currency: "TRY"}
	if !reflect.DeepEqual(raw["verified"], want) {
		t.Errorf("verified = %+v, want %+v", raw["verified"], want)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0 (the callback is verified locally)", len(doer.calls))
	}
}

func TestCompleteThreeDSPaymentReportsSignedFailure(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	callback := signedCallback(t, "0", "", "", "", map[string]string{
		"sipay_status":   "0",
		"payment_status": "0",
		"status_code":    "41",
		"error_code":     "41",
		"error":          "Insufficient funds",
	})
	result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
	}
	if result.ErrorCode != "41" {
		t.Errorf("errorCode = %q, want 41", result.ErrorCode)
	}
	if result.ErrorMessage != "Insufficient funds" {
		t.Errorf("errorMessage = %q, want Insufficient funds", result.ErrorMessage)
	}
	if result.Code != core.CodeCardDeclined {
		t.Errorf("code = %q, want %q", result.Code, core.CodeCardDeclined)
	}
}

func TestCompleteThreeDSPaymentTrustsSignedStatusNotFields(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	callback := signedCallback(t, "0", "", "", "", map[string]string{"sipay_status": "1", "payment_status": "1"})
	result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
}

func TestCompleteThreeDSPaymentRejectsMissingHashKey(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), callbackFields(nil))
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "INVALID_HASH" {
		t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
	}
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
	if result.PaymentID != "" {
		t.Errorf("paymentId = %q, want none for an unverified callback", result.PaymentID)
	}
}

func TestCompleteThreeDSPaymentRejectsAnotherSecret(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	hashKey, err := CreateHashKey([]string{"1", "1.20", "ORDER3D", "VP17917145548423635", "TRY"}, "attacker-secret")
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}
	result, err := provider.CompleteThreeDSPayment(context.Background(), callbackFields(map[string]string{"hash_key": hashKey}))
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
	if result.PaymentID != "" {
		t.Errorf("paymentId = %q, want none", result.PaymentID)
	}
}

func TestCompleteThreeDSPaymentRejectsReplayedHashKey(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	callback := signedCallback(t, "", "", "", "", nil)
	for _, changed := range []map[string]string{{"invoice_id": "OTHER"}, {"order_id": "VP0000"}} {
		replayed := map[string]string{}
		for key, value := range callback {
			replayed[key] = value
		}
		for key, value := range changed {
			replayed[key] = value
		}
		result, err := provider.CompleteThreeDSPayment(context.Background(), replayed)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Code != core.CodeInvalidHash {
			t.Errorf("changed %v: code = %q, want %q", changed, result.Code, core.CodeInvalidHash)
		}
		if result.PaymentID != "" {
			t.Errorf("changed %v: paymentId = %q, want none", changed, result.PaymentID)
		}
	}
}

func TestCompleteThreeDSPaymentHandlesMissingCallback(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), nil)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
}

func TestGetPaymentSendsSignedCheckStatus(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(nil))
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	calls := doer.apiCalls()
	if calls[0].Path != EndpointCheckStatus {
		t.Errorf("path = %q, want %q", calls[0].Path, EndpointCheckStatus)
	}
	body := calls[0].jsonBody(t)
	wantText(t, body, "merchant_key", testMerchantKey)
	wantText(t, body, "invoice_id", "ORDER1")
	wantBool(t, body, "include_pending_status", true)
	if got, want := strings.Join(hashFields(t, body), "|"), "ORDER1|"+testMerchantKey; got != want {
		t.Errorf("hash_key = %q, want %q", got, want)
	}
	if result.Status != core.StatusSuccess || result.PaymentID != "ORDER1" {
		t.Errorf("result = %q/%q, want SUCCESS/ORDER1", result.Status, result.PaymentID)
	}
}

func TestGetPaymentMapsTransactionStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   core.PaymentStatus
	}{
		{"Pending", core.StatusPending},
		{"Refunded", core.StatusCancelled},
		{"Partial Refund", core.StatusSuccess},
	} {
		doer := &stubDoer{}
		doer.respond(EndpointCheckStatus, statusResponse(Result{"transaction_status": tc.status}))
		provider := newTestProvider(t, doer)

		result, err := provider.GetPayment(context.Background(), "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.status, result.Status, tc.want)
		}
	}
}

func TestGetPaymentReportsUnknownInvoice(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusNotFound)
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "MISSING")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "32" {
		t.Errorf("errorCode = %q, want 32", result.ErrorCode)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestGetPaymentReportsBankDecline(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(Result{
		"status_code":                     41,
		"transaction_status":              "Failed",
		"original_bank_error_code":        "51",
		"original_bank_error_description": "Limit yetersiz",
	}))
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "51" {
		t.Errorf("errorCode = %q, want 51", result.ErrorCode)
	}
	if result.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
	}
}

// checkstatus is read-only, so a transport failure is retried.
func TestGetPaymentRetriesTransportFailures(t *testing.T) {
	doer := &stubDoer{}
	doer.respondError(EndpointCheckStatus, timeoutError())
	doer.respond(EndpointCheckStatus, statusResponse(nil))
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if got := len(doer.apiCalls()); got != 2 {
		t.Errorf("api calls = %d, want 2 (the query is retryable)", got)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestRefundSendsSignedRequest(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointRefund, refundResponse)
	provider := newTestProvider(t, doer)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER1", Price: "3", Currency: "TRY", IP: "85.34.78.112", ConversationID: "R1",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	calls := doer.apiCalls()
	if calls[0].Path != EndpointRefund {
		t.Errorf("path = %q, want %q", calls[0].Path, EndpointRefund)
	}
	body := calls[0].jsonBody(t)
	wantText(t, body, "invoice_id", "ORDER1")
	wantText(t, body, "amount", "3.00")
	wantText(t, body, "merchant_key", testMerchantKey)
	if _, present := body["app_secret"]; present {
		t.Error("body carries app_secret, want it kept out of the request")
	}
	if got, want := strings.Join(hashFields(t, body), "|"), "3.00|ORDER1|"+testMerchantKey; got != want {
		t.Errorf("hash_key = %q, want %q", got, want)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.RefundID != "602800085396" {
		t.Errorf("refundId = %q, want 602800085396", result.RefundID)
	}
	if result.ConversationID != "R1" {
		t.Errorf("conversationId = %q, want R1", result.ConversationID)
	}
}

func TestRefundReportsRejection(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointRefund, `{"status_code":49,"status_description":"Total refund amount must not exceed the net amount"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER1", Price: "30", Currency: "TRY", IP: "85.34.78.112",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "49" {
		t.Errorf("errorCode = %q, want 49", result.ErrorCode)
	}
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
	if result.RefundID != "" {
		t.Errorf("refundId = %q, want none", result.RefundID)
	}
}

// A refund is never retried: it moves money.
func TestRefundIsNotRetried(t *testing.T) {
	doer := &stubDoer{}
	doer.respondError(EndpointRefund, timeoutError())
	provider := newTestProvider(t, doer)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER1", Price: "3", Currency: "TRY", IP: "85.34.78.112",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if got := len(doer.apiCalls()); got != 1 {
		t.Errorf("api calls = %d, want 1 (refunds are not retryable)", got)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}

func TestCancelRefundsWhatIsLeft(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(Result{
		"transaction_status":    "Partial Refund",
		"transaction_amount":    10,
		"total_refunded_amount": 3,
	}))
	doer.respond(EndpointRefund, refundResponse)
	provider := newTestProvider(t, doer)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER1", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	calls := doer.apiCalls()
	if len(calls) != 2 {
		t.Fatalf("api calls = %d, want 2", len(calls))
	}
	if calls[1].Path != EndpointRefund {
		t.Errorf("path = %q, want %q", calls[1].Path, EndpointRefund)
	}
	wantText(t, calls[1].jsonBody(t), "amount", "7.00")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.TransactionID != "602800085396" {
		t.Errorf("transactionId = %q, want 602800085396", result.TransactionID)
	}
}

func TestCancelReleasesOpenPreAuthorization(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(Result{
		"transaction_status": "Pre-Authorization",
		"transaction_type":   "Pre-Authorization",
		"transaction_amount": 20,
	}))
	doer.respond(EndpointConfirmPayment, `{"status_code":100,"status_description":"The transaction has been Rejected","transaction_status":"Failed"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER1", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	calls := doer.apiCalls()
	if calls[1].Path != EndpointConfirmPayment {
		t.Errorf("path = %q, want %q", calls[1].Path, EndpointConfirmPayment)
	}
	body := calls[1].jsonBody(t)
	wantText(t, body, "invoice_id", "ORDER1")
	wantNumber(t, body, "status", ConfirmStatusVoid)
	wantNumber(t, body, "total", 20)
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestCancelDoesNotRefundARefundedPayment(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(Result{
		"transaction_status":    "Refunded",
		"total_refunded_amount": 10,
	}))
	provider := newTestProvider(t, doer)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER1", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "NOT_CANCELLABLE" {
		t.Errorf("errorCode = %q, want NOT_CANCELLABLE", result.ErrorCode)
	}
	if got := len(doer.apiCalls()); got != 1 {
		t.Errorf("api calls = %d, want 1", got)
	}
}

func TestCancelReportsUnknownInvoice(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusNotFound)
	provider := newTestProvider(t, doer)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "MISSING", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "32" {
		t.Errorf("errorCode = %q, want 32", result.ErrorCode)
	}
}

func TestAuthorizeSendsPreAuth(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointPay2D, paymentResponse(Result{"transaction_type": "Pre-Authorization"}))
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.ConversationID = "PA1"
	result, err := provider.Authorize(context.Background(), request)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	wantText(t, doer.apiCalls()[0].jsonBody(t), "transaction_type", "PreAuth")
	if result.Status != core.StatusSuccess || result.PaymentID != "PA1" {
		t.Errorf("result = %q/%q, want SUCCESS/PA1", result.Status, result.PaymentID)
	}
}

func TestCaptureConfirmsAmount(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointConfirmPayment, `{"status_code":100,"status_description":"The transaction has been Approved","transaction_status":"Completed"}`)
	provider := newTestProvider(t, doer)

	result, err := provider.Capture(context.Background(), &core.CaptureRequest{PaymentID: "PA1", Amount: "15", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	calls := doer.apiCalls()
	if calls[0].Path != EndpointConfirmPayment {
		t.Errorf("path = %q, want %q", calls[0].Path, EndpointConfirmPayment)
	}
	body := calls[0].jsonBody(t)
	wantText(t, body, "invoice_id", "PA1")
	wantNumber(t, body, "status", ConfirmStatusCapture)
	wantNumber(t, body, "total", 15)
	if got, want := strings.Join(hashFields(t, body), "|"), testMerchantKey+"|PA1|1"; got != want {
		t.Errorf("hash_key = %q, want %q", got, want)
	}
	if result.Status != core.StatusSuccess || result.PaymentID != "PA1" {
		t.Errorf("result = %q/%q, want SUCCESS/PA1", result.Status, result.PaymentID)
	}
}

func TestVoidAuthorizationReleasesPreAuthorization(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(Result{
		"transaction_status": "Pre-Authorization",
		"transaction_amount": 20,
	}))
	doer.respond(EndpointConfirmPayment, `{"status_code":100}`)
	provider := newTestProvider(t, doer)

	result, err := provider.VoidAuthorization(context.Background(), &core.VoidAuthorizationRequest{PaymentID: "PA1", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	body := doer.apiCalls()[1].jsonBody(t)
	wantNumber(t, body, "status", ConfirmStatusVoid)
	wantNumber(t, body, "total", 20)
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestVoidAuthorizationRefusesOtherPayments(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointCheckStatus, statusResponse(nil))
	provider := newTestProvider(t, doer)

	result, err := provider.VoidAuthorization(context.Background(), &core.VoidAuthorizationRequest{PaymentID: "PA1", IP: "85.34.78.112"})
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if result.ErrorCode != "NOT_CANCELLABLE" {
		t.Errorf("errorCode = %q, want NOT_CANCELLABLE", result.ErrorCode)
	}
	if got := len(doer.apiCalls()); got != 1 {
		t.Errorf("api calls = %d, want 1", got)
	}
}

func TestInstallmentInfoListsOptions(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointGetPos, posResponse)
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "540667", Price: "100"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	body := doer.apiCalls()[0].jsonBody(t)
	wantText(t, body, "credit_card", "540667")
	wantNumber(t, body, "amount", 100)
	wantText(t, body, "currency_code", "TRY")
	wantText(t, body, "merchant_key", testMerchantKey)
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if len(result.InstallmentDetails) != 1 {
		t.Fatalf("installmentDetails = %d, want 1", len(result.InstallmentDetails))
	}
	detail := result.InstallmentDetails[0]
	if detail.BinNumber != "540667" || detail.Price != 100 || detail.CardType != "CREDIT CARD" ||
		detail.CardAssociation != "MASTERCARD" || detail.CardFamilyName != "MAXIMUM" ||
		detail.BankName != "T. IS BANKASI A.S." || detail.BankCode != 64 || detail.Commercial != 0 {
		t.Errorf("detail = %+v", detail)
	}
	want := []core.InstallmentDetail{
		{InstallmentNumber: 1, TotalPrice: 100, InstallmentPrice: 100},
		{InstallmentNumber: 3, TotalPrice: 105, InstallmentPrice: 35},
	}
	if !reflect.DeepEqual(detail.InstallmentPrices, want) {
		t.Errorf("installmentPrices = %+v, want %+v", detail.InstallmentPrices, want)
	}
}

// getpos is read-only, so a transport failure is retried.
func TestInstallmentInfoRetriesTransportFailures(t *testing.T) {
	doer := &stubDoer{}
	doer.respondError(EndpointGetPos, timeoutError())
	doer.respond(EndpointGetPos, posResponse)
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "540667", Price: "100"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if got := len(doer.apiCalls()); got != 2 {
		t.Errorf("api calls = %d, want 2 (the query is retryable)", got)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestBinCheckReturnsCardDetails(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(EndpointGetPos, posResponse)
	provider := newTestProvider(t, doer)

	result, err := provider.BinCheck(context.Background(), "540667")
	if err != nil {
		t.Fatalf("BinCheck() error = %v", err)
	}
	if result.CardType != "CREDIT CARD" || result.CardAssociation != "MASTERCARD" ||
		result.CardFamily != "MAXIMUM" || result.BankName != "T. IS BANKASI A.S." || result.Commercial {
		t.Errorf("binCheck = %+v", result)
	}
}

func TestInstallmentInfoRejectsInvalidBIN(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "12", Price: "100"})
	if err != nil {
		t.Fatalf("InstallmentInfo() error = %v", err)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(doer.calls))
	}
}

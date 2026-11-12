package paytr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

type stubResponse struct {
	body   string
	status int
	err    error
}

// stubTransport answers with canned bodies in call order, repeating the last one,
// and records every request so tests can assert on the exact wire format.
type stubTransport struct {
	queue    []stubResponse
	requests []*http.Request
	bodies   []string
}

func (s *stubTransport) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)

	var next stubResponse
	if len(s.queue) > 0 {
		index := len(s.requests) - 1
		if index >= len(s.queue) {
			index = len(s.queue) - 1
		}
		next = s.queue[index]
	}
	if next.err != nil {
		return nil, next.err
	}
	status := next.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(next.body)),
	}, nil
}

func (s *stubTransport) calls() int { return len(s.requests) }

func (s *stubTransport) body(call int) string {
	if call >= len(s.bodies) {
		return ""
	}
	return s.bodies[call]
}

func (s *stubTransport) path(call int) string {
	if call >= len(s.requests) {
		return ""
	}
	return s.requests[call].URL.Path
}

func jsonBody(value any) stubResponse {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return stubResponse{body: string(raw)}
}

func baseConfig(transport core.Doer) Config {
	return Config{
		ProviderConfig: core.ProviderConfig{
			BaseURL: DefaultBaseURL,
			Doer:    transport,
			Retry:   &core.RetryConfig{Attempts: 3, Delay: time.Millisecond},
		},
		MerchantID:   "123456",
		MerchantKey:  testKey,
		MerchantSalt: testSalt,
	}
}

func newProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func newTestProvider(t *testing.T, transport core.Doer, mode string, testMode bool) *Provider {
	t.Helper()
	cfg := baseConfig(transport)
	cfg.ThreeDSMode = mode
	cfg.TestMode = &testMode
	return newProvider(t, cfg)
}

func parseForm(t *testing.T, body string) map[string]string {
	t.Helper()
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatalf("body is not urlencoded: %v", err)
	}
	fields := make(map[string]string, len(values))
	for key, value := range values {
		fields[key] = value[0]
	}
	return fields
}

func sentForm(t *testing.T, s *stubTransport, call int) map[string]string {
	t.Helper()
	return parseForm(t, s.body(call))
}

var hiddenInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`)

// One HTML attribute decoding pass, not recursive decoding: a literal entity in
// a product name must survive as text.
var htmlEntities = strings.NewReplacer("&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&")

func formFields(t *testing.T, html string) map[string]string {
	t.Helper()
	matches := hiddenInputRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		t.Fatalf("no hidden inputs in form: %s", html)
	}
	fields := make(map[string]string, len(matches))
	for _, match := range matches {
		fields[htmlEntities.Replace(match[1])] = htmlEntities.Replace(match[2])
	}
	return fields
}

func mockBuyer() core.Buyer {
	return core.Buyer{
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
	}
}

func mockAddress(contact string) core.Address {
	return core.Address{
		ContactName: contact,
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

func mockBasket() []core.BasketItem {
	return []core.BasketItem{
		{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: "PHYSICAL", Price: "0.3"},
		{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: "VIRTUAL", Price: "0.5"},
		{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: "PHYSICAL", Price: "0.2"},
	}
}

func mockPaymentRequest() core.PaymentRequest {
	register := true
	return core.PaymentRequest{
		Price:     "1",
		PaidPrice: "1.2",
		Currency:  "TRY",
		BasketID:  "B67832",
		PaymentCard: &core.PaymentCard{
			CardHolderName: "John Doe",
			CardNumber:     "5528790000000008",
			ExpireMonth:    "12",
			ExpireYear:     "2030",
			CVC:            "123",
			RegisterCard:   &register,
		},
		Buyer:           mockBuyer(),
		ShippingAddress: mockAddress("Jane Doe"),
		BillingAddress:  mockAddress("John Doe"),
		BasketItems:     mockBasket(),
		ConversationID:  "123456789",
	}
}

func mockThreeDSRequest() core.ThreeDSPaymentRequest {
	req := core.ThreeDSPaymentRequest{
		PaymentRequest: mockPaymentRequest(),
	}
	req.CallbackURL = "https://example.com/callback"
	return req
}

func TestNewRequiresMerchantCredentials(t *testing.T) {
	for _, field := range []string{"merchantId", "merchantKey", "merchantSalt"} {
		cfg := baseConfig(nil)
		switch field {
		case "merchantId":
			cfg.MerchantID = ""
		case "merchantKey":
			cfg.MerchantKey = ""
		case "merchantSalt":
			cfg.MerchantSalt = ""
		}
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("%s was not required", field)
		}
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("error %q does not name %s", err, field)
		}
		var configErr *core.ConfigurationError
		if !errors.As(err, &configErr) {
			t.Fatalf("error is not a ConfigurationError: %T", err)
		}
	}
}

func TestNewDoesNotRequireAPIKey(t *testing.T) {
	cfg := baseConfig(nil)
	if _, err := New(cfg); err != nil {
		t.Fatalf("New without testMode failed: %v", err)
	}
}

func TestNewRejectsUnknownThreeDSMode(t *testing.T) {
	cfg := baseConfig(nil)
	cfg.ThreeDSMode = "x"
	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), "threeDSMode") {
		t.Fatalf("error = %v, want a threeDSMode error", err)
	}
}

func TestInitThreeDSPaymentIframe(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "TOKEN123"})}}
	p := newTestProvider(t, transport, "", true)

	req := mockThreeDSRequest()
	req.ConversationID = "ORDER1"
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if transport.calls() != 1 {
		t.Fatalf("calls = %d, want 1", transport.calls())
	}
	if path := transport.path(0); path != "/odeme/api/get-token" {
		t.Fatalf("path = %q", path)
	}
	form := sentForm(t, transport, 0)
	for _, secret := range []string{"merchant_key", "merchant_salt"} {
		if _, ok := form[secret]; ok {
			t.Fatalf("secret %s leaked into the request body", secret)
		}
	}
	for key, want := range map[string]string{
		"test_mode":      "1",
		"currency":       "TL",
		"payment_amount": "120",
		"merchant_oid":   "ORDER1",
	} {
		if form[key] != want {
			t.Fatalf("%s = %q, want %q", key, form[key], want)
		}
	}

	raw, err := base64.StdEncoding.DecodeString(form["user_basket"])
	if err != nil {
		t.Fatalf("user_basket is not base64: %v", err)
	}
	var basket [][]any
	if err := json.Unmarshal(raw, &basket); err != nil {
		t.Fatalf("user_basket is not JSON: %v", err)
	}
	want := [][]any{{"Binocular", "0.30", float64(1)}, {"Game code", "0.50", float64(1)}, {"Usb", "0.20", float64(1)}}
	if len(basket) != len(want) {
		t.Fatalf("basket = %v, want %v", basket, want)
	}
	for i := range want {
		for j := range want[i] {
			if basket[i][j] != want[i][j] {
				t.Fatalf("basket = %v, want %v", basket, want)
			}
		}
	}

	token := mustToken(t)(GenerateIframeToken(IframeTokenParams{
		MerchantID:     form["merchant_id"],
		UserIP:         form["user_ip"],
		MerchantOid:    form["merchant_oid"],
		Email:          form["email"],
		PaymentAmount:  form["payment_amount"],
		UserBasket:     form["user_basket"],
		NoInstallment:  form["no_installment"],
		MaxInstallment: form["max_installment"],
		Currency:       form["currency"],
		TestMode:       form["test_mode"],
	}, testSalt, testKey))
	if !core.SafeEqual(form["paytr_token"], token) {
		t.Fatalf("paytr_token = %q, want %q", form["paytr_token"], token)
	}

	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	if result.PaymentID != "ORDER1" {
		t.Fatalf("paymentId = %q", result.PaymentID)
	}
	if want := "https://www.paytr.com/odeme/guvenli/TOKEN123"; result.RedirectURL != want {
		t.Fatalf("redirectUrl = %q, want %q", result.RedirectURL, want)
	}
	if !strings.Contains(result.ThreeDSHTMLContent, "https://www.paytr.com/odeme/guvenli/TOKEN123") {
		t.Fatal("iframe HTML does not contain the payment page URL")
	}
}

func TestInitThreeDSPaymentGeneratesMerchantOid(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "T"})}}
	p := newTestProvider(t, transport, "", true)

	req := mockThreeDSRequest()
	req.ConversationID = ""
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(result.PaymentID) {
		t.Fatalf("generated merchant_oid %q is not alphanumeric", result.PaymentID)
	}
	if got := sentForm(t, transport, 0)["merchant_oid"]; got != result.PaymentID {
		t.Fatalf("sent merchant_oid = %q, want %q", got, result.PaymentID)
	}
}

func TestInitThreeDSPaymentRejectsNonAlphanumericOid(t *testing.T) {
	transport := &stubTransport{}
	p := newTestProvider(t, transport, "", true)

	req := mockThreeDSRequest()
	req.ConversationID = "ORDER-1"
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if transport.calls() != 0 {
		t.Fatalf("PayTR was called %d times for an invalid merchant_oid", transport.calls())
	}
}

func TestInitThreeDSPaymentLiveTestMode(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "T"})}}
	p := newTestProvider(t, transport, "", false)

	req := mockThreeDSRequest()
	if _, err := p.InitThreeDSPayment(context.Background(), &req); err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if got := sentForm(t, transport, 0)["test_mode"]; got != "0" {
		t.Fatalf("test_mode = %q, want 0", got)
	}
}

func TestInitThreeDSPaymentInstallmentOptions(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "token": "T"})}}
	p := newTestProvider(t, transport, "", true)

	cases := []struct {
		installment    int
		noInstallment  string
		maxInstallment string
	}{
		{1, "1", "0"},
		{6, "0", "6"},
	}
	for i, tc := range cases {
		req := mockThreeDSRequest()
		req.Installment = tc.installment
		if _, err := p.InitThreeDSPayment(context.Background(), &req); err != nil {
			t.Fatalf("InitThreeDSPayment: %v", err)
		}
		form := sentForm(t, transport, i)
		if form["no_installment"] != tc.noInstallment || form["max_installment"] != tc.maxInstallment {
			t.Fatalf("installment %d: no_installment=%q max_installment=%q", tc.installment, form["no_installment"], form["max_installment"])
		}
	}
}

func TestInitThreeDSPaymentReturnsPayTRReason(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "failed", "reason": "paytr_token gecersiz"})}}
	p := newTestProvider(t, transport, "", true)

	req := mockThreeDSRequest()
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.ErrorMessage != "paytr_token gecersiz" {
		t.Fatalf("errorMessage = %q", result.ErrorMessage)
	}
}

func TestCompleteThreeDSPaymentAcceptsGenuineNotification(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, "", true)
	result, err := p.CompleteThreeDSPayment(context.Background(), genuineCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if result.PaymentID != "202606234E4E" {
		t.Fatalf("paymentId = %q", result.PaymentID)
	}
}

func TestCompleteThreeDSPaymentRejectsForgedNotification(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, "", true)
	forged := genuineCallback()
	forged.Status = "failed"

	result, err := p.CompleteThreeDSPayment(context.Background(), forged)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.ErrorCode != "INVALID_HASH" {
		t.Fatalf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
	}
	if result.Code != core.CodeInvalidHash {
		t.Fatalf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
}

func TestCompleteThreeDSPaymentAcceptsFormNotification(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, "", true)
	callback := genuineCallback()
	result, err := p.CompleteThreeDSPayment(context.Background(), url.Values{
		"merchant_oid": {callback.MerchantOid},
		"status":       {callback.Status},
		"total_amount": {callback.TotalAmount},
		"hash":         {callback.Hash},
	})
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
}

func refundRequest() *core.RefundRequest {
	return &core.RefundRequest{PaymentID: "ORDER1", Price: "50", Currency: "TRY", IP: "1.1.1.1"}
}

func TestRefundSignsReturnAmountInTL(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "merchant_oid": "ORDER1", "reference_no": "R1"})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.Refund(context.Background(), refundRequest())
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if path := transport.path(0); path != "/odeme/iade" {
		t.Fatalf("path = %q", path)
	}
	form := sentForm(t, transport, 0)
	if form["return_amount"] != "50.00" {
		t.Fatalf("return_amount = %q, want 50.00", form["return_amount"])
	}
	token := mustToken(t)(GenerateRefundToken("123456", "ORDER1", "50.00", testSalt, testKey))
	if !core.SafeEqual(form["paytr_token"], token) {
		t.Fatalf("paytr_token = %q, want %q", form["paytr_token"], token)
	}
	if _, ok := form["merchant_key"]; ok {
		t.Fatal("merchant_key leaked into the refund body")
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if result.RefundID != "R1" {
		t.Fatalf("refundId = %q, want R1", result.RefundID)
	}
}

func TestRefundMapsProviderError(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "error", "err_no": "004", "err_msg": "Tutar hatali"})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.Refund(context.Background(), refundRequest())
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.ErrorCode != "004" || result.ErrorMessage != "Tutar hatali" {
		t.Fatalf("errorCode=%q errorMessage=%q", result.ErrorCode, result.ErrorMessage)
	}
}

// A refund must never be retried: a repeated refund moves money twice.
func TestRefundIsNeverRetried(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{{err: errors.New("connection reset")}}}
	p := newTestProvider(t, transport, "", true)

	if _, err := p.Refund(context.Background(), refundRequest()); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if transport.calls() != 1 {
		t.Fatalf("refund attempts = %d, want 1", transport.calls())
	}
}

func TestCancelFullyRefundsQueriedAmount(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{
		jsonBody(map[string]any{"status": "success", "payment_amount": "10,01", "payment_total": "10,01"}),
		jsonBody(map[string]any{"status": "success", "merchant_oid": "ORDER1"}),
	}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER1", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if path := transport.path(0); path != "/odeme/durum-sorgu" {
		t.Fatalf("first path = %q", path)
	}
	if got := sentForm(t, transport, 1)["return_amount"]; got != "10.01" {
		t.Fatalf("return_amount = %q, want 10.01", got)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
}

func TestGetPaymentQueriesStatus(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "payment_amount": "10.01", "payment_total": "10.01"})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if path := transport.path(0); path != "/odeme/durum-sorgu" {
		t.Fatalf("path = %q", path)
	}
	token := mustToken(t)(GenerateStatusToken("123456", "ORDER1", testSalt, testKey))
	if got := sentForm(t, transport, 0)["paytr_token"]; !core.SafeEqual(got, token) {
		t.Fatalf("paytr_token = %q, want %q", got, token)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
}

func TestGetPaymentIsRetryable(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{{err: errors.New("connection reset")}}}
	cfg := baseConfig(transport)
	cfg.Retry = &core.RetryConfig{Attempts: 2, Delay: time.Millisecond}
	p := newProvider(t, cfg)

	result, err := p.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if transport.calls() != 2 {
		t.Fatalf("status query attempts = %d, want 2", transport.calls())
	}
	if result.Status != core.StatusPending || result.ErrorCode != "NETWORK_ERROR" {
		t.Fatalf("status=%q errorCode=%q, want pending/NETWORK_ERROR", result.Status, result.ErrorCode)
	}
}

func TestGetPaymentReportsFullyRefundedAsCancelled(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{
		"status":         "success",
		"payment_amount": "10.01",
		"returns":        []any{map[string]any{"return_amount": "10.01"}},
	})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if result.Status != core.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", result.Status)
	}
}

func TestGetPaymentMapsQueryError(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "error", "err_no": "001", "err_msg": "Siparis bulunamadi"})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.ErrorMessage != "Siparis bulunamadi" {
		t.Fatalf("errorMessage = %q", result.ErrorMessage)
	}
}

func TestCreatePaymentDirectNon3D(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{"status": "success", "merchant_oid": "ORDER1"})}}
	p := newTestProvider(t, transport, "", true)

	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"
	result, err := p.CreatePayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if path := transport.path(0); path != "/odeme" {
		t.Fatalf("path = %q", path)
	}
	form := sentForm(t, transport, 0)
	for key, want := range map[string]string{
		"non_3d":         "1",
		"sync_mode":      "1",
		"payment_amount": "1.20",
		"payment_type":   "card",
		"expiry_month":   "12",
		"expiry_year":    "30",
	} {
		if form[key] != want {
			t.Fatalf("%s = %q, want %q", key, form[key], want)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
}

func TestCreatePaymentMapsFailedPayment(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{
		"status":             "failed",
		"failed_reason_code": "6",
		"failed_reason_msg":  "Yetersiz bakiye",
	})}}
	p := newTestProvider(t, transport, "", true)

	req := mockPaymentRequest()
	result, err := p.CreatePayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.ErrorMessage != "Yetersiz bakiye" {
		t.Fatalf("errorMessage = %q", result.ErrorMessage)
	}
	if result.Code != core.CodeCancelledByCustomer {
		t.Fatalf("code = %q, want %q", result.Code, core.CodeCancelledByCustomer)
	}
}

// A timed-out payment has an unknown outcome, so it stays PENDING.
func TestCreatePaymentTimeoutIsPending(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{{err: errors.New("Request timed out")}}}
	p := newTestProvider(t, transport, "", true)

	req := mockPaymentRequest()
	result, err := p.CreatePayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	if result.ErrorCode != "NETWORK_ERROR" || result.Code != core.CodeNetworkError {
		t.Fatalf("errorCode=%q code=%q", result.ErrorCode, result.Code)
	}
	if transport.calls() != 1 {
		t.Fatalf("payment attempts = %d, want 1", transport.calls())
	}
}

func TestInitThreeDSPaymentDirectForm(t *testing.T) {
	transport := &stubTransport{}
	p := newTestProvider(t, transport, ModeDirect, true)

	req := mockThreeDSRequest()
	req.ConversationID = "ORDER1"
	req.Installment = 3
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if transport.calls() != 0 {
		t.Fatalf("the browser form must not call PayTR server-side (calls=%d)", transport.calls())
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	if result.PaymentID != "ORDER1" {
		t.Fatalf("paymentId = %q", result.PaymentID)
	}
	html := result.ThreeDSHTMLContent
	if !strings.Contains(html, `action="https://www.paytr.com/odeme"`) {
		t.Fatalf("form action missing: %s", html)
	}
	fields := formFields(t, html)
	for key, want := range map[string]string{
		"merchant_id":       "123456",
		"merchant_oid":      "ORDER1",
		"payment_amount":    "1.20",
		"payment_type":      "card",
		"installment_count": "3",
		"currency":          "TL",
		"test_mode":         "1",
		"non_3d":            "0",
		"cc_owner":          "John Doe",
		"card_number":       "5528790000000008",
		"expiry_month":      "12",
		"expiry_year":       "30",
		"cvv":               "123",
		"merchant_ok_url":   req.CallbackURL,
		"merchant_fail_url": req.CallbackURL,
	} {
		if fields[key] != want {
			t.Fatalf("%s = %q, want %q", key, fields[key], want)
		}
	}
	for _, absent := range []string{"sync_mode", "merchant_key", "merchant_salt"} {
		if _, ok := fields[absent]; ok {
			t.Fatalf("%s must not be in the browser form", absent)
		}
	}
	token := mustToken(t)(GenerateDirectToken(DirectTokenParams{
		MerchantID:       "123456",
		UserIP:           fields["user_ip"],
		MerchantOid:      "ORDER1",
		Email:            fields["email"],
		PaymentAmount:    "1.20",
		PaymentType:      "card",
		InstallmentCount: "3",
		Currency:         "TL",
		TestMode:         "1",
		Non3D:            "0",
	}, testSalt, testKey))
	if !core.SafeEqual(fields["paytr_token"], token) {
		t.Fatalf("paytr_token = %q, want %q", fields["paytr_token"], token)
	}
}

func TestInitThreeDSPaymentDirectSaveCard(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, ModeDirect, true)

	req := mockThreeDSRequest()
	req.SaveCard = &core.SaveCardOption{Enabled: true, CustomerToken: "U1"}
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	fields := formFields(t, result.ThreeDSHTMLContent)
	if fields["store_card"] != "1" || fields["utoken"] != "U1" {
		t.Fatalf("store_card=%q utoken=%q", fields["store_card"], fields["utoken"])
	}
}

func TestInitThreeDSPaymentDirectNeedsCard(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, ModeDirect, true)

	req := mockThreeDSRequest()
	req.PaymentCard = nil
	result, err := p.InitThreeDSPayment(context.Background(), &req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
}

func TestCreatePaymentWithToken(t *testing.T) {
	transport := &stubTransport{}
	p := newTestProvider(t, transport, "", true)

	result, err := p.CreatePaymentWithToken(context.Background(), &TokenPaymentRequest{
		Utoken:         "U1",
		Ctoken:         "C1",
		Price:          "10",
		CallbackURL:    "https://shop/return",
		ConversationID: "ORDER9",
		Buyer: TokenPaymentBuyer{
			Email: "a@b.c", Name: "A", Surname: "B", IP: "1.1.1.1", GSMNumber: "5350000000",
		},
		BasketItems: []BasketItem{{Name: "X", Price: "10", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreatePaymentWithToken: %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	for _, want := range []string{
		`action="https://www.paytr.com/odeme"`,
		`name="utoken" value="U1"`,
		`name="ctoken" value="C1"`,
	} {
		if !strings.Contains(result.ThreeDSHTMLContent, want) {
			t.Fatalf("form is missing %s", want)
		}
	}
	if transport.calls() != 0 {
		t.Fatalf("calls = %d, want 0", transport.calls())
	}
}

func TestBinCheckMapsBinDetail(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{jsonBody(map[string]any{
		"status": "success", "bank": "Garanti", "brand": "bonus", "cardType": "credit", "schema": "MASTERCARD",
	})}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.BinCheck(context.Background(), "415956")
	if err != nil {
		t.Fatalf("BinCheck: %v", err)
	}
	if path := transport.path(0); path != "/odeme/api/bin-detail" {
		t.Fatalf("path = %q", path)
	}
	if result.BankName != "Garanti" || result.CardFamily != "bonus" || result.CardAssociation != "MASTERCARD" {
		t.Fatalf("bin = %+v", result)
	}
}

func TestInstallmentInfoComputesTotals(t *testing.T) {
	transport := &stubTransport{queue: []stubResponse{
		jsonBody(map[string]any{"status": "success", "bank": "Garanti", "brand": "bonus", "cardType": "credit"}),
		jsonBody(map[string]any{"status": "success", "oranlar": map[string]any{
			"bonus": map[string]any{"taksit_2": 2, "taksit_3": "3.5"},
		}}),
	}}
	p := newTestProvider(t, transport, "", true)

	result, err := p.InstallmentInfo(context.Background(), &core.InstallmentInfoRequest{BinNumber: "415956", Price: "100"})
	if err != nil {
		t.Fatalf("InstallmentInfo: %v", err)
	}
	if path := transport.path(1); path != "/odeme/taksit-oranlari" {
		t.Fatalf("second path = %q", path)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if len(result.InstallmentDetails) != 1 {
		t.Fatalf("installmentDetails = %+v", result.InstallmentDetails)
	}
	want := []core.InstallmentDetail{
		{InstallmentNumber: 1, TotalPrice: 100, InstallmentPrice: 100},
		{InstallmentNumber: 2, TotalPrice: 102, InstallmentPrice: 51},
		{InstallmentNumber: 3, TotalPrice: 103.5, InstallmentPrice: 34.5},
	}
	got := result.InstallmentDetails[0].InstallmentPrices
	if len(got) != len(want) {
		t.Fatalf("installmentPrices = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("installmentPrices[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCallbackAcknowledgement(t *testing.T) {
	p := newTestProvider(t, &stubTransport{}, "", true)
	ack := p.CallbackAcknowledgement()
	if ack.Success != "OK" {
		t.Fatalf("success acknowledgement = %q, want OK", ack.Success)
	}
	if ack.Failure == "" {
		t.Fatal("failure acknowledgement is empty")
	}
}

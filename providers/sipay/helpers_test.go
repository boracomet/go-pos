package sipay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Made-up credentials; never use real ones in tests.
const (
	testAppID       = "test-app-id"
	testAppSecret   = "test-app-secret"
	testMerchantKey = "test-merchant-key"
)

type stubCall struct {
	URL    string
	Path   string
	Body   string
	Header http.Header
}

func (c stubCall) authorization() string { return c.Header.Get("Authorization") }

func (c stubCall) jsonBody(t *testing.T) Result {
	t.Helper()
	var body Result
	if err := json.Unmarshal([]byte(c.Body), &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, c.Body)
	}
	return body
}

type stubResponse struct {
	status int
	body   string
	err    error
}

// stubDoer replaces the transport: it records every request and answers per
// endpoint path, consuming queued responses in order. The token endpoint answers
// with a valid token unless a test queues something else.
type stubDoer struct {
	calls     []stubCall
	responses map[string][]stubResponse
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	call := stubCall{URL: req.URL.String(), Path: req.URL.Path, Body: body, Header: req.Header.Clone()}
	s.calls = append(s.calls, call)

	queue := s.responses[call.Path]
	var next stubResponse
	switch {
	case len(queue) > 0:
		next, s.responses[call.Path] = queue[0], queue[1:]
	case call.Path == EndpointToken:
		next = stubResponse{body: tokenResponse("test-token")}
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
		Body:       io.NopCloser(strings.NewReader(next.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func (s *stubDoer) respond(path string, bodies ...string) {
	if s.responses == nil {
		s.responses = map[string][]stubResponse{}
	}
	for _, body := range bodies {
		s.responses[path] = append(s.responses[path], stubResponse{body: body})
	}
}

func (s *stubDoer) respondError(path string, err error) {
	if s.responses == nil {
		s.responses = map[string][]stubResponse{}
	}
	s.responses[path] = append(s.responses[path], stubResponse{err: err})
}

// apiCalls are the calls to an endpoint other than the token endpoint.
func (s *stubDoer) apiCalls() []stubCall {
	out := make([]stubCall, 0, len(s.calls))
	for _, call := range s.calls {
		if call.Path != EndpointToken {
			out = append(out, call)
		}
	}
	return out
}

func (s *stubDoer) tokenCalls() []stubCall {
	out := make([]stubCall, 0, len(s.calls))
	for _, call := range s.calls {
		if call.Path == EndpointToken {
			out = append(out, call)
		}
	}
	return out
}

func timeoutError() error {
	return fmt.Errorf("request timed out: %w", context.DeadlineExceeded)
}

func newTestProvider(t *testing.T, doer core.Doer) *Provider {
	t.Helper()
	return newProviderWith(t, Config{
		ProviderConfig: core.ProviderConfig{
			BaseURL: SandboxURL,
			Doer:    doer,
			Retry:   &core.RetryConfig{Attempts: 3, Delay: time.Millisecond},
		},
		AppID:       testAppID,
		AppSecret:   testAppSecret,
		MerchantKey: testMerchantKey,
	})
}

func newProviderWith(t *testing.T, cfg Config) *Provider {
	t.Helper()
	if cfg.ProviderConfig.Retry == nil {
		cfg.ProviderConfig.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	}
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

func tokenResponse(token string) string {
	return `{"status_code":100,"status_description":"Successfully Generated token",` +
		`"data":{"token":"` + token + `","is_3d":1,"expires_at":"2026-10-11 15:25:29"}}`
}

const unauthenticatedResponse = `{"success":false,"statuscode":101,"message":"Unauthenticated.",` +
	`"status_code":101,"status_description":"Unauthenticated."}`

// paymentResponse is a successful paySmart2D answer; overrides replace data fields.
func paymentResponse(overrides Result) string {
	data := Result{
		"sipay_status":                    1,
		"order_no":                        "VP1791714383141836",
		"order_id":                        "VP1791714383141836",
		"invoice_id":                      "ORDER2D",
		"sipay_payment_method":            1,
		"credit_card_no":                  "450803****4509",
		"transaction_type":                "Auth",
		"payment_status":                  1,
		"payment_method":                  1,
		"error_code":                      100,
		"error":                           "Başarılı",
		"auth_code":                       "679984",
		"installment":                     1,
		"amount":                          1.2,
		"hash_key":                        "c39abe1c58d89dfc:9d75:UX1gBpJ35YlCsGKqyASxWqe__da8fE3pT__i5bJmCa57riW8kAkiiIYF",
		"original_bank_error_code":        "",
		"original_bank_error_description": "",
	}
	for key, value := range overrides {
		data[key] = value
	}
	return `{"status_code":100,"status_description":"Payment process successful","data":` + mustJSON(data) + `}`
}

// paymentDeclined is a paySmart2D answer declined by the bank (insufficient funds).
const paymentDeclined = `{"status_code":41,"status_description":"Limit yetersiz","data":{` +
	`"sipay_status":0,"order_no":"VP17917143852558833","order_id":"VP17917143852558833",` +
	`"invoice_id":"ORDER2D","transaction_type":"Auth","payment_status":0,"error_code":"51",` +
	`"error":"Limit yetersiz","auth_code":null,"original_bank_error_code":"51",` +
	`"original_bank_error_description":"Limit yetersiz"}}`

// threeDSHTML is a paySmart3D answer: a form posting the card to the 3D Secure page.
const threeDSHTML = `<!doctype html>
<html lang="en"><head><title>3DS Page</title></head>
<body>
    <div style="display: none">
        <form id="3dsForm" name="3dsForm" action="https://dummypos.sipay.com.tr/payment/3d" method="POST">
            <input type="hidden" name="invoice_id" value="VP17917144147503468">
<input type="hidden" name="amount" value="1.2">
<input type="hidden" name="return_url" value="https://provisioning.sipay.com.tr/ccpayment/payment/dummy_payment/return3d?brand_order_id=VP17917144147503468">
<input type="hidden" name="language" value="tr">
        </form>
    </div>
    <script type="text/javascript">document.getElementById("3dsForm").submit();</script>
</body></html>`

// threeDSRejectedHTML is a paySmart3D answer posting the error to cancel_url.
const threeDSRejectedHTML = `
        <form id="the-form" method="POST" action="https://example.com/callback"><input type="hidden" name="sipay_status" value="0">
<input type="hidden" name="order_no" value="">
<input type="hidden" name="order_id" value="">
<input type="hidden" name="invoice_id" value="ORDER3D">
<input type="hidden" name="status_code" value="68">
<input type="hidden" name="status_description" value="Invalid hash key">
<input type="hidden" name="payment_status" value="0">
<input type="hidden" name="error_code" value="68">
<input type="hidden" name="error" value="Invalid hash key">
<input type="hidden" name="hash_key" value="71a203309490b625:11c3:jSQERr4u3SVhNT__bw25wOq4D1ELqnDNwK+ZaIyjjMM4=">
</form>`

// callbackFields are the fields posted to return_url after a 3D Secure payment.
func callbackFields(overrides map[string]string) map[string]string {
	fields := map[string]string{
		"sipay_status":                    "1",
		"order_no":                        "VP17917145548423635",
		"order_id":                        "VP17917145548423635",
		"invoice_id":                      "ORDER3D",
		"status_code":                     "100",
		"status_description":              "Payment Successfully Completed",
		"sipay_payment_method":            "1",
		"credit_card_no":                  "450803****4509",
		"transaction_type":                "Auth",
		"payment_status":                  "1",
		"payment_method":                  "1",
		"error_code":                      "100",
		"error":                           "Payment Successfully Completed",
		"auth_code":                       "115923",
		"installment":                     "1",
		"amount":                          "1.2",
		"md_status":                       "1",
		"original_bank_error_code":        "",
		"original_bank_error_description": "",
	}
	for key, value := range overrides {
		fields[key] = value
	}
	return fields
}

// signedCallback signs a callback the way the provider does:
// status|total|invoice_id|order_id|currency.
func signedCallback(t *testing.T, status, total, invoiceID, orderID string, overrides map[string]string) map[string]string {
	t.Helper()
	if status == "" {
		status = "1"
	}
	if total == "" {
		total = "1.20"
	}
	if invoiceID == "" {
		invoiceID = "ORDER3D"
	}
	if orderID == "" {
		orderID = "VP17917145548423635"
	}
	hashKey, err := CreateHashKey([]string{status, total, invoiceID, orderID, "TRY"}, testAppSecret)
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}
	if overrides == nil {
		overrides = map[string]string{}
	}
	overrides["invoice_id"], overrides["order_id"], overrides["hash_key"] = invoiceID, orderID, hashKey
	return callbackFields(overrides)
}

// statusResponse is a checkstatus answer; overrides replace top-level fields.
func statusResponse(overrides Result) string {
	data := Result{
		"status_code":                     100,
		"status_description":              "An order has been taken place for this invoice id: ORDER1",
		"transaction_status":              "Completed",
		"order_id":                        "VP17917145548423635",
		"transaction_id":                  "N3qx0-sjCx-TC10-18309-111026",
		"message":                         "An order has been taken place for this invoice id: ORDER1",
		"reason":                          "",
		"bank_status_code":                "",
		"bank_status_description":         "",
		"invoice_id":                      "ORDER1",
		"total_refunded_amount":           0,
		"product_price":                   "10",
		"transaction_amount":              10,
		"ref_number":                      "",
		"transaction_type":                "Auth",
		"original_bank_error_code":        "",
		"original_bank_error_description": "",
		"cc_no":                           "45080345****4509",
		"refund_info":                     []any{},
		"md_status":                       1,
		"installment":                     1,
		"card_type":                       "CREDIT CARD",
	}
	for key, value := range overrides {
		data[key] = value
	}
	return mustJSON(data)
}

const statusNotFound = `{"status_code":32,"status_description":"Geçersiz fatura kimliği",` +
	`"transaction_status":"Failed","order_id":"","transaction_id":"",` +
	`"message":"Geçersiz fatura kimliği","reason":"No Request found for this invoice id",` +
	`"invoice_id":"MISSING","total_refunded_amount":0,"product_price":"0","transaction_amount":0,` +
	`"original_bank_error_code":"","original_bank_error_description":""}`

const refundResponse = `{"status_code":100,"status_description":"Refund completed successfully",` +
	`"order_no":"VP17917145548423635","invoice_id":"ORDER1","ref_no":"602800085396",` +
	`"ref_number":"602800085396","remaining_refundable_amount":7}`

const posResponse = `{"status_code":100,"status_description":"Successfull","data":[` +
	`{"pos_id":76,"installments_number":3,"card_type":"CREDIT CARD","card_program":"MAXIMUM",` +
	`"card_scheme":"mastercard","is_commercial":"FALSE","payable_amount":105,"amount_to_be_paid":"105.00",` +
	`"currency_code":"TRY","title":"3 installments","card_bank":"T. IS BANKASI A.S.","bank_code":"0064"},` +
	`{"pos_id":76,"installments_number":1,"card_type":"CREDIT CARD","card_program":"MAXIMUM",` +
	`"card_scheme":"mastercard","is_commercial":"FALSE","payable_amount":100,"amount_to_be_paid":"100.00",` +
	`"currency_code":"TRY","title":"Single payment","card_bank":"T. IS BANKASI A.S.","bank_code":"0064"}]}`

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func mockPaymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
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
		},
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
		ShippingAddress: mockAddress("Jane Doe"),
		BillingAddress:  mockAddress("John Doe"),
		BasketItems:     mockBasketItems(),
		ConversationID:  "123456789",
	}
}

func mockBasketItems() []core.BasketItem {
	return []core.BasketItem{
		{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: "PHYSICAL", Price: "0.3"},
		{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: "VIRTUAL", Price: "0.5"},
		{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: "PHYSICAL", Price: "0.2"},
	}
}

func mockAddress(contactName string) core.Address {
	return core.Address{
		ContactName: contactName,
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

func mockThreeDSPaymentRequest() *core.ThreeDSPaymentRequest {
	req := &core.ThreeDSPaymentRequest{
		PaymentRequest: *mockPaymentRequest(),
	}
	req.CallbackURL = "https://example.com/callback"
	return req
}

// hashFields decrypts a hash key of a request body, failing the test when it was
// not signed with the test secret.
func hashFields(t *testing.T, body Result) []string {
	t.Helper()
	hashKey, _ := body["hash_key"].(string)
	fields := DecryptHashKey(hashKey, testAppSecret)
	if fields == nil {
		t.Fatalf("hash_key %q was not signed with the test secret", hashKey)
	}
	return fields
}

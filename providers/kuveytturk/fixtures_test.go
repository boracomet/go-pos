package kuveytturk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const (
	testBaseURL = "https://boatest.kuveytturk.com.tr/boa.virtualpos.services"
	prodBaseURL = "https://sanalpos.kuveytturk.com.tr/ServiceGateWay"
)

func testConfig() Config {
	return Config{
		MerchantID: "12345",
		CustomerID: "67890",
		Username:   "apiuser",
		Password:   "secret123",
	}
}

func testCredentials() HashCredentials {
	return HashCredentials{MerchantID: "12345", Username: "apiuser", Password: "secret123"}
}

// paymentRejectedHTML is the form the bank answers with when Payment is rejected
// before 3D: it posts to failUrl.
const paymentRejectedHTML = `
            <form name="responseForm" action="https://shop.test/payment/fail" method="POST">
                <input type="hidden" name="Success" value="False" />
                <input type="hidden" name="ResponseCode" value="HashDataError" />
                <input type="hidden" name="ResponseMessage" value="Şifrelenen veriler (Hashdata) uyuşmamaktadır." />
                <input type="hidden" name="BusinessKey" value="" />
                <input type="hidden" name="TransactionTime" value="1791604497" />
            </form>
            <script type="text/javascript">document.responseForm.submit();</script>`

// payment3DHTML is the bank's 3D Secure page, returned when Payment is accepted.
const payment3DHTML = `<html><body onload="document.forms[0].submit()">
<form action="https://acs.example-bank.test/challenge" method="POST">
<input type="hidden" name="PaReq" value="eJxVUttugkAQ" />
<input type="hidden" name="TermUrl" value="https://boatest.kuveytturk.com.tr/term" />
<input type="hidden" name="MD" value="abc+/=" />
</form></body></html>`

func threeDSCallback() map[string]any {
	return map[string]any{
		"Success":         "True",
		"ResponseCode":    "00",
		"ResponseMessage": "Kart doğrulandı.",
		"MD":              "mdValue+/=",
		"OrderId":         "334210503",
		"MerchantOrderId": "ORDER3D",
		"BusinessKey":     "202610109999000000002137378",
		"TransactionTime": "1791604509",
	}
}

func ktTransaction(overrides map[string]any) map[string]any {
	tx := map[string]any{
		"MerchantId":          12345,
		"PosTerminalId":       "VP000001",
		"OrderId":             334210503,
		"MerchantOrderId":     "ORDER3D",
		"CardNumber":          "979216****8083",
		"CardType":            "Troy",
		"TransactionTime":     "2026-10-10T13:43:48.133",
		"IsCancellable":       1,
		"IsRefundable":        0,
		"IsPartialRefundable": 0,
		"OrderStatus":         "Satış (1)",
		"LastOrderStatus":     "Satış (1)",
		"OrderType":           "Peşin (1)",
		"TransactionStatus":   "Başarılı (1)",
		"FirstAmount":         120,
		"DrawbackAmount":      0,
		"CancelAmount":        0,
		"ClosedAmount":        0,
		"FEC":                 "TRY (0949)",
		"InstallmentCount":    0,
		"TransactionSecurity": "3d İşlem (3)",
		"ResponseCode":        "00",
		"ResponseExplain":     "",
		"ProvisionNumber":     "123456",
	}
	for key, value := range overrides {
		tx[key] = value
	}
	return tx
}

func ktResponse(overrides map[string]any) map[string]any {
	response := map[string]any{
		"Result":          nil,
		"Success":         true,
		"ResponseCode":    "Successfull",
		"ResponseMessage": "İşleminiz başarıyla gerçekleştirildi.",
		"BusinessKey":     "202610109999000000002129515",
		"TransactionTime": 1791604558,
	}
	for key, value := range overrides {
		response[key] = value
	}
	return response
}

func ktTransactions(transactions ...map[string]any) map[string]any {
	list := make([]any, 0, len(transactions))
	for _, tx := range transactions {
		list = append(list, tx)
	}
	return ktResponse(map[string]any{"Result": map[string]any{"Transactions": list}})
}

func testCard() *core.PaymentCard {
	return &core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "123",
	}
}

func testBuyer() core.Buyer {
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

func testAddress(contactName string) core.Address {
	return core.Address{
		ContactName: contactName,
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

func testBasketItems() []core.BasketItem {
	return []core.BasketItem{
		{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: string(core.BasketItemPhysical), Price: "0.3"},
		{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: string(core.BasketItemVirtual), Price: "0.5"},
		{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: string(core.BasketItemPhysical), Price: "0.2"},
	}
}

func testPaymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:           "1",
		PaidPrice:       "1.2",
		Currency:        string(core.CurrencyTRY),
		BasketID:        "B67832",
		PaymentCard:     testCard(),
		Buyer:           testBuyer(),
		ShippingAddress: testAddress("Jane Doe"),
		BillingAddress:  testAddress("John Doe"),
		BasketItems:     testBasketItems(),
		ConversationID:  "123456789",
	}
}

func testThreeDSRequest() *core.ThreeDSPaymentRequest {
	req := &core.ThreeDSPaymentRequest{
		PaymentRequest: *testPaymentRequest(),
	}
	req.CallbackURL = "https://example.com/callback"
	return req
}

type reply struct {
	status int
	body   string
	err    error
}

// transport records every request and answers with the queued replies, then with
// the fallback reply (or an empty JSON object).
type transport struct {
	once     []reply
	fallback *reply
	calls    int
	paths    []string
	bodies   []string
	headers  []http.Header
}

func (tr *transport) Do(req *http.Request) (*http.Response, error) {
	tr.calls++
	tr.paths = append(tr.paths, req.URL.Path)
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	tr.bodies = append(tr.bodies, body)
	tr.headers = append(tr.headers, req.Header.Clone())

	var next reply
	switch {
	case len(tr.once) > 0:
		next, tr.once = tr.once[0], tr.once[1:]
	case tr.fallback != nil:
		next = *tr.fallback
	default:
		next = reply{body: "{}"}
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
		Header:     http.Header{},
	}, nil
}

func (tr *transport) replyWith(value any) {
	tr.fallback = &reply{body: jsonBody(value)}
}

func (tr *transport) replyOnceWith(value any) {
	tr.once = append(tr.once, reply{body: jsonBody(value)})
}

func jsonBody(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func timeoutReply() reply {
	return reply{err: context.DeadlineExceeded}
}

func newTestProvider(t *testing.T, cfg Config, tr *transport) *Provider {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = testBaseURL
	}
	cfg.Doer = tr
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

func sentBody(t *testing.T, tr *transport, call int) map[string]any {
	t.Helper()
	if call >= len(tr.bodies) {
		t.Fatalf("call %d was not made (%d calls)", call, tr.calls)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(tr.bodies[call]), &body); err != nil {
		t.Fatalf("call %d body is not JSON: %v", call, err)
	}
	return body
}

func wantString(t *testing.T, got any, want, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func wantNoIDs(t *testing.T, paymentID, conversationID string) {
	t.Helper()
	if paymentID != "" || conversationID != "" {
		t.Errorf("ids = (%q, %q), want none", paymentID, conversationID)
	}
}

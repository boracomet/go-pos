package albaraka

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const (
	testBaseURL = "https://epostest.albarakaturk.com.tr"
	prodBaseURL = "https://epos.albarakaturk.com.tr"
)

// MACs computed independently with SHA-256 over the joined MACParams values.
const (
	saleMAC      = "xCjbJqQIpeOedM8oXV+hXi4EBzPAhgzvNTnqasZ5Nhk="
	threeDSMAC   = "kL3uVm4J5E8zZCovFjs2n7ij/Sf0HBUARECmyHdVWig="
	referenceMAC = "BNCLNNvobKR7HEgtmuMW6fPN9JMhy5iE9N5JLK9zpHE="
	inquiryMAC   = "/oCQsEflmYDWrPPy2VvLXlhAqJ8Q3gFQi02vVWKNRY4="
)

func testConfig() Config {
	return Config{
		MerchantID: "6700000067",
		TerminalID: "67000067",
		PosnetID:   "1010",
		EncKey:     "TESTENCKEY",
	}
}

func threeDSCallback() map[string]any {
	return map[string]any{
		"MdStatus":            "1",
		"MdErrorMessage":      "Authenticated",
		"SecureTransactionId": "SECTXN1",
		"CAVV":                "CAVVVALUE=",
		"ECI":                 "05",
		"MD":                  "MDVALUE",
	}
}

func completeRequest() map[string]any {
	data := threeDSCallback()
	data["orderId"] = "ORDER1"
	data["amount"] = "1.20"
	data["currency"] = "TRY"
	return data
}

func albarakaResponse(code string, fields map[string]any) map[string]any {
	response := map[string]any{
		"ServiceResponseData": map[string]any{
			"ResponseCode":        code,
			"ResponseDescription": description(code),
		},
		"AuthCode":        nil,
		"ReferenceCode":   nil,
		"PointDataList":   nil,
		"InstallmentData": nil,
		"MessageData":     nil,
	}
	if code == "00" {
		response["AuthCode"] = "123456"
		response["ReferenceCode"] = "REF123"
	}
	for key, value := range fields {
		response[key] = value
	}
	return response
}

func description(code string) string {
	if code == "00" {
		return "APPROVED"
	}
	return "UNAUTHORIZED REQUEST"
}

func testCard() *core.PaymentCard {
	return &core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5400010000000004",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "000",
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
		ConversationID:  "ORDER1",
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

var formFieldRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

func formFields(html string) map[string]string {
	fields := make(map[string]string)
	for _, match := range formFieldRe.FindAllStringSubmatch(html, -1) {
		fields[match[1]] = match[2]
	}
	return fields
}

func wantString(t *testing.T, got any, want, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func wantNumber(t *testing.T, got any, want float64, label string) {
	t.Helper()
	number, ok := got.(float64)
	if !ok || number != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func wantNoIDs(t *testing.T, paymentID, conversationID string) {
	t.Helper()
	if paymentID != "" || conversationID != "" {
		t.Errorf("ids = (%q, %q), want none", paymentID, conversationID)
	}
}

func nested(t *testing.T, body map[string]any, path string) any {
	t.Helper()
	var current any = body
	for _, key := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q is not an object", path, key)
		}
		current = object[key]
	}
	return current
}

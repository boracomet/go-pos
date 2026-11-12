package est

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func testConfig() Config {
	return Config{
		ProviderConfig: core.ProviderConfig{BaseURL: SandboxURL},
		Bank:           BankIsbank,
		ClientID:       "100100000",
		Username:       "apiuser",
		Password:       "apipass",
		StoreKey:       "TESTSTOREKEY",
	}
}

var testFormFields = map[string]string{
	"clientid":                        "100100000",
	"storetype":                       "3d",
	"amount":                          "1.20",
	"oid":                             "ORDER3D",
	"okUrl":                           "https://example.com/callback",
	"failUrl":                         "https://example.com/callback",
	"TranType":                        "Auth",
	"currency":                        "949",
	"taksit":                          "",
	"rnd":                             "0123456789abcdef0123456789abcdef",
	"lang":                            "tr",
	"hashAlgorithm":                   "ver3",
	"refreshTime":                     "0",
	"Email":                           "john.doe@example.com",
	"pan":                             "5528790000000008",
	"Ecom_Payment_Card_ExpDate_Month": "12",
	"Ecom_Payment_Card_ExpDate_Year":  "30",
	"cv2":                             "123",
	"cardType":                        "mastercard",
	"cardHolderName":                  "John Doe",
}

const testFormHash = "ejx7A/lYNbLnY+LAYDLYwsFN7A7P1UfeHBIM0n/ARKHOBK6DryI6lQHbK7ZsdvtHfuni4/DI5NVTCcjJ69ArWw=="

// test3DCallback is the 3d model: the card is verified, the merchant charges it
// with XML Auth.
func test3DCallback() map[string]string {
	return map[string]string{
		"clientid":       "100100000",
		"oid":            "ORDER3D",
		"amount":         "1.20",
		"currency":       "949",
		"taksit":         "",
		"storetype":      "3d",
		"mdStatus":       "1",
		"md":             "MDVALUE123",
		"xid":            "XIDVALUE=",
		"eci":            "02",
		"cavv":           "CAVVVALUE=",
		"mdErrorMsg":     "Authenticated",
		"hashAlgorithm":  "ver3",
		"rnd":            "0123456789abcdef0123456789abcdef",
		"Response":       "",
		"ProcReturnCode": "",
		"encoding":       "ISO-8859-9",
		"HASH":           "/DO0HeanIuQx/ETIXeB4y6o/3bLMZl4qnQeyQpGnS9mj3tlyHsZ3PP8KE06IsYaEy3PGayXNjVnSusKsTUlaFg==",
	}
}

// test3DPayCallback is the 3d_pay model: the bank charged the card.
func test3DPayCallback() map[string]string {
	return map[string]string{
		"clientid":       "100100000",
		"oid":            "ORDERPAY",
		"amount":         "1.20",
		"currency":       "949",
		"taksit":         "",
		"storetype":      "3d_pay",
		"mdStatus":       "1",
		"md":             "MDVALUE123",
		"xid":            "XIDVALUE=",
		"eci":            "02",
		"cavv":           "CAVVVALUE=",
		"mdErrorMsg":     "Authenticated",
		"hashAlgorithm":  "ver3",
		"rnd":            "0123456789abcdef0123456789abcdef",
		"Response":       "Approved",
		"ProcReturnCode": "00",
		"AuthCode":       "123456",
		"TransId":        "26011AAAA1234",
		"ErrMsg":         "",
		"HASH":           "3udQQXgx3JMKdHwJsnTtHgV1X54DLRRf8KseAe34pLatCK6zsxZD6vnqNfZzDDnAFfruS1/G1qIdGImh1Jp2zQ==",
	}
}

type xmlPair struct {
	name  string
	value string
}

func estXML(fields []xmlPair, extra []xmlPair) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="ISO-8859-9"?><CC5Response>`)
	for _, field := range fields {
		b.WriteString("<" + field.name + ">" + field.value + "</" + field.name + ">")
	}
	if extra != nil {
		b.WriteString("<Extra>")
		for _, field := range extra {
			b.WriteString("<" + field.name + ">" + field.value + "</" + field.name + ">")
		}
		b.WriteString("</Extra>")
	}
	b.WriteString("</CC5Response>")
	return b.String()
}

func pairs(values map[string]string, order ...string) []xmlPair {
	out := make([]xmlPair, 0, len(values))
	for _, name := range order {
		out = append(out, xmlPair{name: name, value: values[name]})
	}
	return out
}

var testApprovedXML = estXML(
	[]xmlPair{
		{"OrderId", "ORDER3D"},
		{"GroupId", "ORDER3D"},
		{"Response", "Approved"},
		{"AuthCode", "123456"},
		{"HostRefNum", "601100000001"},
		{"ProcReturnCode", "00"},
		{"TransId", "26011AAAA1234"},
		{"ErrMsg", ""},
	},
	[]xmlPair{{"SETTLEID", ""}, {"TRXDATE", "20261011 14:00:00"}, {"HOSTMSG", "ONAY"}},
)

func estOrderStatusXML(extra map[string]string) string {
	fields := map[string]string{
		"TRANS_STAT":     "S",
		"CHARGE_TYPE_CD": "S",
		"CAPTURE_AMT":    "120",
	}
	for key, value := range extra {
		fields[key] = value
	}
	return estXML(
		[]xmlPair{
			{"ErrMsg", "Record(s) found for ORDERPAY"},
			{"ProcReturnCode", "00"},
			{"Response", "Approved"},
			{"OrderId", "ORDERPAY"},
			{"TransId", "26011AAAA1234"},
		},
		pairs(fields, "TRANS_STAT", "CHARGE_TYPE_CD", "CAPTURE_AMT"),
	)
}

func testCard() *core.PaymentCard {
	register := true
	return &core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "123",
		RegisterCard:   &register,
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
		{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: "PHYSICAL", Price: "0.3"},
		{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: "VIRTUAL", Price: "0.5"},
		{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: "PHYSICAL", Price: "0.2"},
	}
}

func testPaymentRequest() *core.PaymentRequest {
	return &core.PaymentRequest{
		Price:           "1",
		PaidPrice:       "1.2",
		Currency:        "TRY",
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

func testRefundRequest() *core.RefundRequest {
	return &core.RefundRequest{
		PaymentID:      "12345678",
		Price:          "0.5",
		Currency:       "TRY",
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

func testCancelRequest() *core.CancelRequest {
	return &core.CancelRequest{
		PaymentID:      "12345678",
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

type stubCall struct {
	Method  string
	URL     string
	Body    string
	Headers http.Header
}

// stubDoer records every request and answers with a canned body, or a transport
// error. It never reaches the network.
type stubDoer struct {
	calls    []stubCall
	response string
	status   int
	err      error
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	s.calls = append(s.calls, stubCall{
		Method: req.Method, URL: req.URL.String(), Body: body, Headers: req.Header.Clone(),
	})
	if s.err != nil {
		return nil, s.err
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(s.response)),
		Header:     http.Header{},
	}, nil
}

func newTestProvider(t *testing.T, mutate func(*Config)) (*Provider, *stubDoer) {
	t.Helper()
	doer := &stubDoer{}
	cfg := testConfig()
	cfg.Doer = doer
	if mutate != nil {
		mutate(&cfg)
	}
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider, doer
}

var formInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

func formFieldsOf(html string) map[string]string {
	fields := map[string]string{}
	for _, match := range formInputRe.FindAllStringSubmatch(html, -1) {
		fields[match[1]] = strings.ReplaceAll(match[2], "&amp;", "&")
	}
	return fields
}

// signed re-signs a callback after changing it, like a genuine bank POST.
func signed(t *testing.T, data map[string]string) map[string]string {
	t.Helper()
	rest := map[string]string{}
	for key, value := range data {
		if key != "HASH" {
			rest[key] = value
		}
	}
	hash, err := CreateHash(rest, testConfig().StoreKey)
	if err != nil {
		t.Fatalf("CreateHash() error = %v", err)
	}
	rest["HASH"] = hash
	return rest
}

func sentXML(t *testing.T, doer *stubDoer, call int) string {
	t.Helper()
	if len(doer.calls) <= call {
		t.Fatalf("expected at least %d requests, got %d", call+1, len(doer.calls))
	}
	return doer.calls[call].Body
}

func sentFields(t *testing.T, doer *stubDoer, call int) XMLResponse {
	t.Helper()
	body := sentXML(t, doer, call)
	body = strings.Replace(body, "CC5Request", "CC5Response", 1)
	body = strings.Replace(body, "/CC5Request", "/CC5Response", 1)
	response, err := ParseXMLResponse(body)
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	return response
}

func expectFields(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("field %s = %q, want %q", key, got[key], value)
		}
	}
}

func expectNoIDs(t *testing.T, paymentID, conversationID string) {
	t.Helper()
	if paymentID != "" || conversationID != "" {
		t.Errorf("expected no ids, got paymentId %q conversationId %q", paymentID, conversationID)
	}
}

func expectNoCalls(t *testing.T, doer *stubDoer) {
	t.Helper()
	if len(doer.calls) != 0 {
		t.Errorf("expected no request, got %d", len(doer.calls))
	}
}

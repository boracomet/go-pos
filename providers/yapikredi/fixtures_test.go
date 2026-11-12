package yapikredi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const (
	testBaseURL = "https://setmpos.ykb.com"
	prodBaseURL = "https://posnet.yapikredi.com.tr"
)

// MACs computed independently with SHA-256 over the joined Posnet MAC values.
const (
	ykbMAC     = "A301mJz3V6ewHXJuWUHMpyCCMaIYmq/JG+dBMSmG7m8="
	resolveMAC = "0ITXwphcZczDOx/Fafm8X5OoxmMsDEo2cbU3Kc8xLSg="
	chargeMAC  = "Oyy6eOQW74aps8f/NoY42T5qq9wEvaag+P4fFnAwYfQ="
	guideMAC   = "J/7/Xprj7F/KDf98luVfIGyUPRQzUCqGwpmvz3KT7oQ="
)

func testConfig() Config {
	return Config{
		MerchantID: "6706598320",
		TerminalID: "67005551",
		PosnetID:   "9644",
		EncKey:     "10,10,10,10,10,10,10,10",
	}
}

// field is one fixture element, in the order the bank writes them.
type field struct {
	name  string
	value string
}

type xmlBlock struct {
	name   string
	fields []field
}

func tags(fields []field) string {
	var out strings.Builder
	for _, item := range fields {
		out.WriteString("<" + item.name + ">" + item.value + "</" + item.name + ">")
	}
	return out.String()
}

func ykbXML(fields []field, blocks []xmlBlock) string {
	var out strings.Builder
	out.WriteString("<?xml version='1.0' encoding='iso-8859-9'?><posnetResponse>")
	out.WriteString(tags(fields))
	for _, item := range blocks {
		out.WriteString("<" + item.name + ">" + tags(item.fields) + "</" + item.name + ">")
	}
	out.WriteString("</posnetResponse>")
	return out.String()
}

var oosRequestXML = ykbXML(
	[]field{{"approved", "1"}},
	[]xmlBlock{{"oosRequestDataResponse", []field{
		{"data1", "DATA1VALUE"}, {"data2", "DATA2VALUE"}, {"sign", "SIGNVALUE"},
	}}},
)

var approvedXML = ykbXML([]field{
	{"approved", "1"},
	{"hostlogkey", "021000000000000123"},
	{"authCode", "123456"},
	{"tranDate", "261011144551"},
}, nil)

var declinedXML = ykbXML([]field{
	{"approved", "0"},
	{"respCode", "0225"},
	{"respText", "ONAYLANMADI"},
}, nil)

func ykbResolveXML(data map[string]string) string {
	defaults := []field{
		{"xid", "0000000000000ORDER3D"},
		{"amount", "120"},
		{"currency", "TL"},
		{"installment", "00"},
		{"point", "0"},
		{"pointAmount", "0"},
		{"txStatus", "Y"},
		{"mdStatus", "1"},
		{"mdErrorMessage", "Authenticated"},
	}
	fields := make([]field, 0, len(defaults))
	for _, item := range defaults {
		value := item.value
		if override, ok := data[item.name]; ok {
			value = override
		}
		fields = append(fields, field{item.name, value})
	}
	return ykbXML([]field{{"approved", "1"}}, []xmlBlock{{"oosResolveMerchantDataResponse", fields}})
}

// ykbAgreementXML is an order inquiry answer, as in the XML guide (utf-16 declared).
func ykbAgreementXML(transactions []map[string]string) string {
	defaults := []field{
		{"orderID", "000000000000000000ORDER1"},
		{"ccno", "4506 34** **** 4637"},
		{"amount", "1,20"},
		{"currencyCode", "TL"},
		{"authCode", "504289"},
		{"tranDate", "2026-10-11 11:21:14.281"},
		{"state", "Sale"},
		{"txnStatus", "1"},
		{"hostLogKey", "021450428990000191"},
	}
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="utf-16"?><posnetResponse><approved>1</approved><transactions>`)
	for _, transaction := range transactions {
		fields := make([]field, 0, len(defaults))
		for _, item := range defaults {
			value := item.value
			if override, ok := transaction[item.name]; ok {
				value = override
			}
			fields = append(fields, field{item.name, value})
		}
		out.WriteString("<transaction>" + tags(fields) + "</transaction>")
	}
	out.WriteString("</transactions></posnetResponse>")
	return out.String()
}

func callbackFields() map[string]any {
	return map[string]any{
		"BankPacket":     "BANKPACKET0123456789",
		"MerchantPacket": "MERCHANTPACKET0123456789",
		"Sign":           "SIGN0123456789",
		"CCPrefix":       "450634",
		"TranType":       "Sale",
	}
}

func completeCallback() map[string]any {
	data := callbackFields()
	data["orderId"] = "ORDER3D"
	data["amount"] = "1.20"
	data["currency"] = "TRY"
	return data
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
// the fallback reply (or an empty body).
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
		next = reply{body: ""}
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

func (tr *transport) replyWith(body string) {
	tr.fallback = &reply{body: body}
}

func (tr *transport) replyOnceWith(body string) {
	tr.once = append(tr.once, reply{body: body})
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

// sentXML decodes the xmldata form field of a call. PathUnescape keeps the '+'
// of a base64 MAC, which QueryUnescape would read as a space.
func sentXML(t *testing.T, tr *transport, call int) string {
	t.Helper()
	if call >= len(tr.bodies) {
		t.Fatalf("call %d was not made (%d calls)", call, tr.calls)
	}
	body := tr.bodies[call]
	if !strings.HasPrefix(body, "xmldata=") {
		t.Fatalf("call %d body does not start with xmldata=", call)
	}
	xml, err := url.PathUnescape(strings.TrimPrefix(body, "xmldata="))
	if err != nil {
		t.Fatalf("call %d body is not form-encoded: %v", call, err)
	}
	return xml
}

func leaf(xml, tag string) string {
	match := regexp.MustCompile(`<` + tag + `>([^<]*)</` + tag + `>`).FindStringSubmatch(xml)
	if match == nil {
		return ""
	}
	return match[1]
}

func wantString(t *testing.T, got, want, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

func wantNoIDs(t *testing.T, paymentID, conversationID string) {
	t.Helper()
	if paymentID != "" || conversationID != "" {
		t.Errorf("ids = (%q, %q), want none", paymentID, conversationID)
	}
}

package garanti

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
		MerchantID:     "7000679",
		TerminalID:     "30691297",
		Password:       "123qweASD/",
		StoreKey:       "12345678",
		TestMode:       true,
	}
}

const testSecurityData = "BAF0BF326B0261A4288A7273F18674FF35E9826F"

const testHashParams = "clientid:oid:authcode:procreturncode:response:mdstatus:cavv:eci:md:rnd:"

// test3DCallback is the 3D model: the card is verified, the merchant provisions it.
func test3DCallback() map[string]string {
	return map[string]string{
		"clientid":              "30691297",
		"oid":                   "ORDER3D",
		"orderid":               "ORDER3D",
		"terminalid":            "30691297",
		"terminalmerchantid":    "7000679",
		"authcode":              "",
		"procreturncode":        "",
		"response":              "",
		"mdstatus":              "1",
		"mderrormessage":        "Authenticated",
		"cavv":                  "CAVVVALUE=",
		"eci":                   "02",
		"md":                    "MDVALUE123",
		"xid":                   "XIDVALUE=",
		"rnd":                   "RNDVALUE",
		"txnamount":             "120",
		"txncurrencycode":       "949",
		"txninstallmentcount":   "",
		"txntype":               "sales",
		"customeripaddress":     "85.34.78.112",
		"secure3dsecuritylevel": "3D",
		"hashparams":            testHashParams,
		"hashparamsval":         "30691297ORDER3D1CAVVVALUE=02MDVALUE123RNDVALUE",
		"hash":                  "2E3F98459F27F83DF8F2208C765130FEF3C4F5253605FA258A9112D53D58541CA9AD7EBCD3E719CC6F51AF2118C0799517D0D101B7179C5AB7DD191CFF06AFDF",
	}
}

// test3DPayCallback is the 3D_PAY model: the bank charged the card.
func test3DPayCallback() map[string]string {
	data := test3DCallback()
	data["oid"] = "ORDERPAY"
	data["orderid"] = "ORDERPAY"
	data["authcode"] = "123456"
	data["procreturncode"] = "00"
	data["response"] = "Approved"
	data["secure3dsecuritylevel"] = "3D_PAY"
	data["hashparamsval"] = "30691297ORDERPAY12345600Approved1CAVVVALUE=02MDVALUE123RNDVALUE"
	data["hash"] = "BE6C74AF86901D3689C4A088CECF3597D8D6C696EC0E08652EEAF3A031372AC6F778553D53AC4BA8729D081C0A74B230B84379BC0682993DF3BC247D8D2E2F89"
	return data
}

type xmlPair struct {
	name  string
	value string
}

// mergePairs overrides base values in place and appends the new fields, like a
// spread object literal.
func mergePairs(base, extra []xmlPair) []xmlPair {
	out := append([]xmlPair(nil), base...)
	for _, field := range extra {
		replaced := false
		for i := range out {
			if out[i].name == field.name {
				out[i].value = field.value
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, field)
		}
	}
	return out
}

func tags(fields []xmlPair) string {
	var b strings.Builder
	for _, field := range fields {
		b.WriteString("<" + field.name + ">" + field.value + "</" + field.name + ">")
	}
	return b.String()
}

// garantiXML is a GVPSResponse shaped like the test environment's answers. A nil
// orderInquiry omits OrderInqResult.
func garantiXML(response, transaction, orderInquiry []xmlPair) string {
	order := "<Order><OrderID>ORDER</OrderID><GroupID></GroupID>"
	if orderInquiry != nil {
		order += "<OrderInqResult>" + tags(orderInquiry) + "</OrderInqResult>"
	}
	order += "</Order>"
	return "<GVPSResponse><Mode></Mode><Terminal><ProvUserID>PROVAUT</ProvUserID><ID>30691297</ID></Terminal>" +
		order + "<Transaction><Response>" +
		tags(mergePairs([]xmlPair{
			{"Source", "HOST"}, {"ReasonCode", ""}, {"Message", ""}, {"ErrorMsg", ""}, {"SysErrMsg", ""},
		}, response)) +
		"</Response>" +
		tags(mergePairs([]xmlPair{
			{"RetrefNum", ""}, {"AuthCode", ""}, {"ProvDate", "20261011 14:45:51"},
		}, transaction)) +
		"</Transaction></GVPSResponse>"
}

var testApprovedXML = garantiXML(
	[]xmlPair{{"Code", "00"}, {"ReasonCode", "00"}, {"Message", "Approved"}},
	[]xmlPair{{"RetrefNum", "628408842186"}, {"AuthCode", "405119"}},
	nil,
)

func garantiInquiryXML(order []xmlPair) string {
	return garantiXML(
		[]xmlPair{{"Source", "GVPS"}, {"Code", "00"}, {"Message", "Approved"}},
		nil,
		mergePairs([]xmlPair{
			{"ChargeType", "S"}, {"AuthAmount", "120"}, {"Status", "APPROVED"},
			{"Code", "00"}, {"ReasonCode", "00"}, {"RetrefNum", "628408842186"},
		}, order),
	)
}

var testDeclinedXML = garantiXML(
	[]xmlPair{{"Code", "92"}, {"ReasonCode", "0651"}, {"Message", "Declined"}, {"ErrorMsg", "Kullanıcı şifresi hatalı"}},
	[]xmlPair{{"RetrefNum", ""}},
	nil,
)

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
		PaymentID:      "ORDER1",
		Price:          "0.5",
		Currency:       "TRY",
		IP:             "1.1.1.1",
		ConversationID: "123456789",
	}
}

func testCancelRequest() *core.CancelRequest {
	return &core.CancelRequest{
		PaymentID:      "ORDER1",
		IP:             "1.1.1.1",
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

func leaf(xml, tag string) string {
	match := regexp.MustCompile(`<` + tag + `>([^<]*)</` + tag + `>`).FindStringSubmatch(xml)
	if match == nil {
		return ""
	}
	return match[1]
}

// signed re-signs a callback value with the store key, like a genuine bank POST.
func signed(t *testing.T, value, storeKey string) string {
	t.Helper()
	hash, err := upperHash(core.SHA512, value+storeKey)
	if err != nil {
		t.Fatalf("upperHash() error = %v", err)
	}
	return hash
}

func sentXML(t *testing.T, doer *stubDoer, call int) string {
	t.Helper()
	if len(doer.calls) <= call {
		t.Fatalf("expected at least %d requests, got %d", call+1, len(doer.calls))
	}
	return doer.calls[call].Body
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

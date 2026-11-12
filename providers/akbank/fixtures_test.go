package akbank

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func testConfig() Config {
	return Config{
		ProviderConfig: core.ProviderConfig{BaseURL: "https://apipre.akbank.com/api/v1/payment/virtualpos"},
		MerchantSafeID: "2023090417500272654BD9A49CF07574",
		TerminalSafeID: "2023090417500284633D137A249DBBEB",
		SecretKey:      "3230323330393034313735303032363031353172675f357637355f3273387373745f7233725f73323333383737335f323272383774767276327672323531355f",
		TestMode:       true,
	}
}

const testHashParams = "txnCode+responseCode+responseMessage+hostResponseCode+hostMessage+txnDateTime+" +
	"merchantSafeId+terminalSafeId+orderId+cardHolderName+authCode+rrn+batchNumber+stan+additionalInstallCount+" +
	"deferingMonth+ccbEarnedRewardAmount+ccbBalanceRewardAmount+ccbRewardDesc+pcbEarnedRewardAmount+" +
	"pcbBalanceRewardAmount+xcbEarnedRewardAmount+xcbBalanceRewardAmount"

// test3DPayCallback is the reference 3D_PAY callback of the test terminal.
func test3DPayCallback() map[string]string {
	cfg := testConfig()
	return map[string]string{
		"txnCode":                "1000",
		"responseCode":           "VPS-0000",
		"responseMessage":        "BAŞARILI",
		"hostResponseCode":       "00",
		"hostMessage":            "000 ONAY KODU XXXXXX",
		"txnDateTime":            "2024-04-18T20:27:45.000",
		"merchantSafeId":         cfg.MerchantSafeID,
		"terminalSafeId":         cfg.TerminalSafeID,
		"cardHolderName":         "TD**",
		"orderId":                "2024041811DA",
		"authCode":               "306456",
		"rrn":                    "411024360235",
		"batchNumber":            "43",
		"stan":                   "86",
		"additionalInstallCount": "0",
		"deferingMonth":          "2",
		"ccbEarnedRewardAmount":  "0.01",
		"ccbBalanceRewardAmount": "215.62",
		"ccbRewardDesc":          "CHIP PARA",
		"pcbEarnedRewardAmount":  "0.00",
		"pcbBalanceRewardAmount": "0.00",
		"pcbRewardDesc":          "",
		"xcbEarnedRewardAmount":  "0.00",
		"xcbBalanceRewardAmount": "0.00",
		"xcbRewardDesc":          "",
		"hashParams":             testHashParams,
		"hash":                   "PO/pybfGrY7fesPoAq2U2B1bkpudx659yMyjTnnfP/Cw5MKR1t7mKvRnZdPBxu9nCC7qJFdr3mJSPTdMwYc3SA==",
	}
}

// signedCallback re-signs a callback after changing it, like a genuine bank POST.
func signedCallback(t *testing.T, data map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for key, value := range data {
		out[key] = value
	}
	var value strings.Builder
	for _, name := range strings.Split(out["hashParams"], "+") {
		if name != "" {
			value.WriteString(out[name])
		}
	}
	hash, err := Sign(value.String(), testConfig().SecretKey)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	out["hash"] = hash
	return out
}

// resplitCallback keeps the same signed byte sequence but moves the field
// boundaries, so orderId names another order while the hash still verifies.
func resplitCallback(t *testing.T) map[string]string {
	t.Helper()
	data := test3DPayCallback()
	var signed strings.Builder
	for _, name := range strings.Split(data["hashParams"], "+") {
		if name != "" {
			signed.WriteString(data[name])
		}
	}
	value := signed.String()
	merchantSafeID, terminalSafeID := data["merchantSafeId"], data["terminalSafeId"]
	codeAt := strings.Index(value, "VPS-0000")
	merchantAt := strings.Index(value, merchantSafeID)
	start := strings.Index(value, terminalSafeID) + len(terminalSafeID)
	return map[string]string{
		"before":         value[:codeAt],
		"responseCode":   "VPS-0000",
		"middle":         value[codeAt+len("VPS-0000") : merchantAt],
		"merchantSafeId": merchantSafeID,
		"terminalSafeId": terminalSafeID,
		"orderId":        "2024",
		"after":          value[start+4:],
		"hashParams":     "before+responseCode+middle+merchantSafeId+terminalSafeId+orderId+after",
		"hash":           data["hash"],
	}
}

// historyJSON is an order history answer (txnCode 1010).
func historyJSON(details []map[string]any) string {
	body := map[string]any{"responseCode": "VPS-0000", "txnDetailList": details}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func approvedSale() map[string]any {
	return map[string]any{
		"txnCode": "3000", "responseCode": "VPS-0000", "txnStatus": "N", "orderId": "2024041811DA",
	}
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
		PaymentID:      "ORDER1",
		Price:          "5",
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

// stubDoer records every request and answers with a canned JSON body, or a
// transport error. It never reaches the network.
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

func sentRaw(t *testing.T, doer *stubDoer, call int) string {
	t.Helper()
	if len(doer.calls) <= call {
		t.Fatalf("expected at least %d requests, got %d", call+1, len(doer.calls))
	}
	return doer.calls[call].Body
}

func sentBody(t *testing.T, doer *stubDoer, call int) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(sentRaw(t, doer, call)), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return body
}

func objectAt(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("body has no object %s", key)
	}
	return value
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

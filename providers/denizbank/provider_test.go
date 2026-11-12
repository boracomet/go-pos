package denizbank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

type stubCall struct {
	url  string
	body string
}

type stubReply struct {
	body   string
	status int
	err    error
}

type stubDoer struct {
	calls []stubCall
	queue []stubReply
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	s.calls = append(s.calls, stubCall{url: req.URL.String(), body: body})

	var reply stubReply
	if len(s.queue) > 0 {
		reply, s.queue = s.queue[0], s.queue[1:]
	}
	if reply.err != nil {
		return nil, reply.err
	}
	status := reply.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Header:     make(http.Header),
	}, nil
}

func text(body string) stubReply { return stubReply{body: body} }

func timeoutReply() stubReply {
	return stubReply{err: fmt.Errorf("request timed out: %w", context.DeadlineExceeded)}
}

func newTestProvider(t *testing.T, cfg Config, replies ...stubReply) (*Provider, *stubDoer) {
	t.Helper()
	doer := &stubDoer{queue: replies}
	cfg.Doer = doer
	if cfg.BaseURL == "" {
		cfg.BaseURL = testBaseURL
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, doer
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
			ID: "BY789", Name: "John", Surname: "Doe",
			GSMNumber: "+905350000000", Email: "john.doe@example.com",
			IdentityNumber: "74300864791", IP: "85.34.78.112",
			RegistrationAddress: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
			City:                "Istanbul", Country: "Turkey", ZipCode: "34732",
		},
		ShippingAddress: core.Address{
			ContactName: "Jane Doe", City: "Istanbul", Country: "Turkey",
			Address: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1", ZipCode: "34732",
		},
		BillingAddress: core.Address{
			ContactName: "John Doe", City: "Istanbul", Country: "Turkey",
			Address: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1", ZipCode: "34732",
		},
		BasketItems: []core.BasketItem{
			{ID: "BI101", Name: "Binocular", Category1: "Collectibles", Category2: "Accessories", ItemType: "PHYSICAL", Price: "0.3"},
			{ID: "BI102", Name: "Game code", Category1: "Game", Category2: "Online Game Items", ItemType: "VIRTUAL", Price: "0.5"},
			{ID: "BI103", Name: "Usb", Category1: "Electronics", Category2: "Usb / Cable", ItemType: "PHYSICAL", Price: "0.2"},
		},
		ConversationID: "123456789",
	}
}

func mockThreeDSPaymentRequest() *core.ThreeDSPaymentRequest {
	req := &core.ThreeDSPaymentRequest{
		PaymentRequest: *mockPaymentRequest(),
	}
	req.CallbackURL = "https://example.com/callback"
	return req
}

var hiddenInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

func formFields(html string) map[string]string {
	fields := make(map[string]string)
	for _, m := range hiddenInputRe.FindAllStringSubmatch(html, -1) {
		fields[m[1]] = m[2]
	}
	return fields
}

// sentForm decodes the form body of one recorded call.
func sentForm(t *testing.T, doer *stubDoer, call int) url.Values {
	t.Helper()
	if call >= len(doer.calls) {
		t.Fatalf("call %d was not made (%d calls)", call, len(doer.calls))
	}
	values, err := url.ParseQuery(doer.calls[call].body)
	if err != nil {
		t.Fatalf("form body: %v", err)
	}
	return values
}

func expectForm(t *testing.T, values url.Values, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got := values.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

func expectNoIDs(t *testing.T, res *core.PaymentResponse) {
	t.Helper()
	if res.PaymentID != "" || res.ConversationID != "" {
		t.Errorf("expected no ids, got paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	cases := map[string]func(*Config){
		"shopCode":     func(c *Config) { c.ShopCode = "" },
		"userCode":     func(c *Config) { c.UserCode = "" },
		"userPass":     func(c *Config) { c.UserPass = "" },
		"merchantPass": func(c *Config) { c.MerchantPass = "" },
	}
	for field, clear := range cases {
		t.Run(field, func(t *testing.T) {
			cfg := testConfig()
			cfg.BaseURL = testBaseURL
			clear(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("New error = %v, want it to name %s", err, field)
			}
		})
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	if _, err := New(testConfig()); err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Errorf("New without baseUrl error = %v", err)
	}
}

func TestCreatePaymentSendsNonSecureAuth(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedAnswer()))
	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"

	res, err := p.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if doer.calls[0].url != testBaseURL+Endpoint {
		t.Errorf("url = %q", doer.calls[0].url)
	}
	expectForm(t, sentForm(t, doer, 0), map[string]string{
		"ShopCode":    testShopCode,
		"UserCode":    testUserCode,
		"UserPass":    testUserPass,
		"TxnType":     "Auth",
		"SecureType":  "NonSecure",
		"OrderId":     "ORDER1",
		"PurchAmount": "1.20",
		"Currency":    "949",
		"CardType":    "mastercard",
		"Expiry":      "1230",
		"MOTO":        "0",
		"Lang":        "TR",
	})
	if res.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success (errorCode=%q)", res.Status, res.ErrorCode)
	}
	if res.PaymentID != "ORDER1" || res.ConversationID != "ORDER1" {
		t.Errorf("paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
}

func TestCreatePaymentMapsDecline(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(declinedAnswer()))

	res, err := p.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if res.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", res.Status)
	}
	if res.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", res.Code, core.CodeInsufficientFunds)
	}
}

func TestCreatePaymentIsNotRetried(t *testing.T) {
	cfg := testConfig()
	cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	p, doer := newTestProvider(t, cfg, timeoutReply())

	res, err := p.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if res.Status != core.StatusPending || res.ErrorCode != core.NetworkErrorCode {
		t.Errorf("status=%q errorCode=%q, want pending/NETWORK_ERROR", res.Status, res.ErrorCode)
	}
	if res.Code != core.CodeNetworkError {
		t.Errorf("code = %q", res.Code)
	}
	if len(doer.calls) != 1 {
		t.Errorf("a payment was retried: %d calls", len(doer.calls))
	}
}

func TestInitThreeDSPaymentReturnsSignedForm(t *testing.T) {
	p, doer := newTestProvider(t, testConfig())
	req := mockThreeDSPaymentRequest()
	req.ConversationID = "ORDER3D"

	res, err := p.InitThreeDSPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 0 {
		t.Fatalf("the 3D form must not call the API, got %d calls", len(doer.calls))
	}
	html := res.ThreeDSHTMLContent
	if !strings.Contains(html, `action="`+testBaseURL+Endpoint+`"`) {
		t.Errorf("form action missing from the html")
	}
	fields := formFields(html)
	expectFormValues(t, fields, map[string]string{
		"ShopCode":    testShopCode,
		"SecureType":  "3DModel",
		"TxnType":     "Auth",
		"OrderId":     "ORDER3D",
		"PurchAmount": "1.20",
	})
	if _, sent := fields["UserPass"]; sent {
		t.Error("the API password must not be in the 3D form")
	}
	if strings.Contains(html, testUserPass) {
		t.Error("the API password leaked into the html")
	}
	if strings.Contains(html, testMerchantPass) {
		t.Error("the store key leaked into the html")
	}
	if len(fields["Rnd"]) != 32 {
		t.Errorf("Rnd = %q, want 32 hex characters", fields["Rnd"])
	}
	want := Create3DHash(HashFields{
		ShopCode:     testShopCode,
		OrderID:      "ORDER3D",
		Amount:       "1.20",
		OkURL:        fields["OkUrl"],
		FailURL:      fields["FailUrl"],
		TxnType:      "Auth",
		Installment:  "",
		Rnd:          fields["Rnd"],
		MerchantPass: testMerchantPass,
	})
	if fields["Hash"] != want {
		t.Errorf("Hash = %q, want %q", fields["Hash"], want)
	}
	if res.Status != core.StatusPending || res.PaymentID != "ORDER3D" {
		t.Errorf("status=%q paymentId=%q", res.Status, res.PaymentID)
	}
}

func expectFormValues(t *testing.T, fields map[string]string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("%s = %q, want %q", key, fields[key], value)
		}
	}
}

func TestCompleteThreeDSPaymentChargesWith3DData(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedAnswer()))

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	expectForm(t, sentForm(t, doer, 0), map[string]string{
		"TxnType":                 "Auth",
		"SecureType":              "NonSecure",
		"OrderId":                 "ORDER3D",
		"PurchAmount":             "1.20",
		"Currency":                "949",
		"MD":                      "MDVALUE123",
		"PayerTxnId":              "XIDVALUE=",
		"Eci":                     "05",
		"PayerAuthenticationCode": "CAVVVALUE=",
	})
	if res.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success (errorCode=%q)", res.Status, res.ErrorCode)
	}
	if res.PaymentID != "ORDER3D" || res.ConversationID != "ORDER3D" {
		t.Errorf("paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
}

func TestCompleteThreeDSPaymentRejectsChangedOrderID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedAnswer()))
	data := callbackData()
	data["OrderId"] = "SOMEONE-ELSE"

	res, err := p.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("a forged callback reached the bank: %d calls", len(doer.calls))
	}
	if res.ErrorCode != "INVALID_HASH" || res.Code != core.CodeInvalidHash {
		t.Errorf("errorCode=%q code=%q, want INVALID_HASH", res.ErrorCode, res.Code)
	}
	expectNoIDs(t, res)
}

// The HASH joins the values without separators, so their boundaries can move
// while the HASH stays valid. Such a callback must never succeed on its own: the
// payment is charged with Auth (carrying the 3D session's MD) and the bank decides.
func TestCompleteThreeDSPaymentReSplitCallbackNeedsTheBank(t *testing.T) {
	resplit := callbackData()
	resplit["Currency"] = "949O"
	resplit["OrderId"] = "RDER3D"

	names := []string{"Version3D", "PurchAmount", "TotalAmount", "InstallmentCount", "Currency", "OrderId"}
	var moved, original strings.Builder
	for _, name := range names {
		moved.WriteString(resplit[name])
		original.WriteString(callbackData()[name])
	}
	if moved.String() != original.String() {
		t.Fatal("the re-split fixture no longer hashes to the same value")
	}

	p, doer := newTestProvider(t, testConfig(), text(declinedAnswer()))
	res, err := p.CompleteThreeDSPayment(context.Background(), resplit)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("want exactly 1 bank call, got %d", len(doer.calls))
	}
	expectForm(t, sentForm(t, doer, 0), map[string]string{
		"TxnType": "Auth",
		"OrderId": "RDER3D",
		"MD":      "MDVALUE123",
	})
	if res.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", res.Status)
	}
	expectNoIDs(t, res)
}

func TestNewRejectsThe3DPayModel(t *testing.T) {
	cfg := testConfig()
	cfg.BaseURL = testBaseURL
	cfg.SecureType = "3DPay"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "3DPay is not supported") {
		t.Errorf("New with 3DPay error = %v", err)
	}
	cfg.SecureType = "3DModel"
	if _, err := New(cfg); err != nil {
		t.Errorf("New with 3DModel: %v", err)
	}
}

func TestCompleteThreeDSPaymentBankDecline(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(declinedAnswer()))

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", res.Status)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPaymentTimeoutKeepsOrderID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), timeoutReply())

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", res.Status)
	}
	if res.PaymentID != "ORDER3D" || res.ConversationID != "ORDER3D" {
		t.Errorf("paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
	if len(doer.calls) != 1 {
		t.Errorf("a 3D charge was retried: %d calls", len(doer.calls))
	}
}

func TestRefundAndCancelByOrgOrderID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedAnswer()), text(approvedAnswer()))
	ctx := context.Background()

	refund, err := p.Refund(ctx, &core.RefundRequest{
		PaymentID: "ORDER1", Price: "0.5", Currency: "TRY", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	expectForm(t, sentForm(t, doer, 0), map[string]string{
		"TxnType":     "Refund",
		"orgOrderId":  "ORDER1",
		"PurchAmount": "0.50",
	})
	if refund.Status != core.StatusSuccess || refund.RefundID != "T1" {
		t.Errorf("status=%q refundId=%q", refund.Status, refund.RefundID)
	}

	cancel, err := p.Cancel(ctx, &core.CancelRequest{PaymentID: "ORDER1", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	expectForm(t, sentForm(t, doer, 1), map[string]string{
		"TxnType":    "Void",
		"orgOrderId": "ORDER1",
	})
	if cancel.Status != core.StatusSuccess {
		t.Errorf("cancel status = %q, want success", cancel.Status)
	}
}

func TestGetPaymentIsNotSupported(t *testing.T) {
	p, doer := newTestProvider(t, testConfig())

	_, err := p.GetPayment(context.Background(), "x")
	var baseErr *core.BaseError
	if !errors.As(err, &baseErr) || baseErr.Code != "NOT_SUPPORTED" {
		t.Errorf("GetPayment error = %v, want NOT_SUPPORTED", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("GetPayment called the bank: %d calls", len(doer.calls))
	}
}

func TestBaseURLs(t *testing.T) {
	if SandboxBaseURL != "https://test.inter-vpos.com.tr" {
		t.Errorf("SandboxBaseURL = %q", SandboxBaseURL)
	}
	if ProductionBaseURL != "https://inter-vpos.com.tr" {
		t.Errorf("ProductionBaseURL = %q", ProductionBaseURL)
	}
	if Endpoint != "/mpi/Default.aspx" {
		t.Errorf("Endpoint = %q", Endpoint)
	}
}

func TestOptionalOperationsAreNotSupported(t *testing.T) {
	p, _ := newTestProvider(t, testConfig())
	ctx := context.Background()
	if _, err := p.Authorize(ctx, mockPaymentRequest()); err == nil {
		t.Error("Authorize should not be supported")
	}
	if _, err := p.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDER3D", Amount: "1.20"}); err == nil {
		t.Error("Capture should not be supported")
	}
	if _, err := p.ListCards(ctx, &core.ListCardsRequest{CustomerToken: "t"}); err == nil {
		t.Error("ListCards should not be supported")
	}
	if _, err := p.BinCheck(ctx, "552879"); err == nil {
		t.Error("BinCheck should not be supported")
	}
}

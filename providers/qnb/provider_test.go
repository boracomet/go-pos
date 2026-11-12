package qnb

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

var (
	hiddenInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)
	leafTagRe     = regexp.MustCompile(`<([A-Za-z_][\w.-]*)>([^<]*)</([A-Za-z_][\w.-]*)>`)
)

func formFields(html string) map[string]string {
	fields := make(map[string]string)
	for _, m := range hiddenInputRe.FindAllStringSubmatch(html, -1) {
		fields[m[1]] = m[2]
	}
	return fields
}

func leaf(xml, tag string) string {
	for _, m := range leafTagRe.FindAllStringSubmatch(xml, -1) {
		if m[1] == tag && m[1] == m[3] {
			return m[2]
		}
	}
	return ""
}

func expectNoIDs(t *testing.T, res *core.PaymentResponse) {
	t.Helper()
	if res.PaymentID != "" || res.ConversationID != "" {
		t.Errorf("expected no ids, got paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	cases := map[string]func(*Config){
		"merchantId":   func(c *Config) { c.MerchantID = "" },
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

func TestNewRequiresBaseURLAndKnownSecureType(t *testing.T) {
	cfg := testConfig()
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Errorf("New without baseUrl error = %v", err)
	}
	cfg.BaseURL = testBaseURL
	cfg.SecureType = "3DPay"
	if _, err := New(cfg); err != nil {
		t.Errorf("New with 3DPay: %v", err)
	}
	cfg.SecureType = "3DSomething"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "secureType") {
		t.Errorf("New with unknown secureType error = %v", err)
	}
}

func TestCreatePaymentSendsNonSecureAuth(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))
	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"

	res, err := p.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(doer.calls))
	}
	if doer.calls[0].url != testBaseURL+EndpointAPI {
		t.Errorf("url = %q", doer.calls[0].url)
	}
	xml := doer.calls[0].body
	for tag, want := range map[string]string{
		"MbrId": "5", "UserPass": testUserPass, "SecureType": "NonSecure", "TxnType": "Auth",
		"PurchAmount": "1.20", "Expiry": "1230", "InstallmentCount": "0",
	} {
		if got := leaf(xml, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	if res.Status != core.StatusSuccess {
		t.Errorf("status = %q", res.Status)
	}
	if res.PaymentID != "ORDER1" {
		t.Errorf("paymentId = %q", res.PaymentID)
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
	if !strings.Contains(html, `action="`+testBaseURL+EndpointGate+`"`) {
		t.Errorf("form action missing from %q", html)
	}
	fields := formFields(html)
	for name, want := range map[string]string{
		"MbrId": "5", "MerchantID": testMerchantID, "UserCode": testUserCode,
		"SecureType": "3DModel", "TxnType": "Auth", "InstallmentCount": "0",
		"OrderId": "ORDER3D", "PurchAmount": "1.20", "Pan": "5528790000000008", "Expiry": "1230",
	} {
		if fields[name] != want {
			t.Errorf("form %s = %q, want %q", name, fields[name], want)
		}
	}
	if _, present := fields["UserPass"]; present {
		t.Error("the form carries UserPass")
	}
	if strings.Contains(html, testUserPass) || strings.Contains(html, testMerchantPass) {
		t.Error("the form leaks a password")
	}
	want := Create3DHash(HashFields{
		OrderID: "ORDER3D", Amount: "1.20", OkURL: fields["OkUrl"], FailURL: fields["FailUrl"],
		TxnType: "Auth", Installment: "0", Rnd: fields["Rnd"], MerchantPass: testMerchantPass,
	})
	if fields["Hash"] != want {
		t.Errorf("Hash = %q, want %q", fields["Hash"], want)
	}
	if res.Status != core.StatusPending {
		t.Errorf("status = %q", res.Status)
	}
}

func TestCompleteThreeDSPayment3DModelCharges(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))

	res, err := p.CompleteThreeDSPayment(context.Background(), threeDModelCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	xml := doer.calls[0].body
	for tag, want := range map[string]string{
		"SecureType": "3DModelPayment", "RequestGuid": "1000000128757956", "OrderId": "ORDER3D",
	} {
		if got := leaf(xml, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	if res.Status != core.StatusSuccess || res.PaymentID != "ORDER3D" {
		t.Errorf("status=%q paymentId=%q", res.Status, res.PaymentID)
	}
}

func TestCompleteThreeDSPayment3DModelRejectsChangedOrderID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig())
	data := threeDModelCallback()
	data["OrderId"] = "SOMEONE-ELSE"

	res, err := p.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("a forged callback reached the bank: %d calls", len(doer.calls))
	}
	if res.ErrorCode != "INVALID_HASH" || res.Code != core.CodeInvalidHash {
		t.Errorf("errorCode=%q code=%q", res.ErrorCode, res.Code)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPayment3DModelRejectsOtherMerchant(t *testing.T) {
	p, _ := newTestProvider(t, testConfig())
	data := threeDModelCallback()
	data["MerchantID"] = "1"

	res, err := p.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.ErrorCode != "INVALID_HASH" {
		t.Errorf("errorCode = %q", res.ErrorCode)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPayment3DModelDecline(t *testing.T) {
	declined := qnbXML(Fields{
		{Key: "ProcReturnCode", Value: "51"},
		{Key: "ErrMsg", Value: "Yetersiz bakiye"},
	})
	p, _ := newTestProvider(t, testConfig(), text(declined))

	res, err := p.CompleteThreeDSPayment(context.Background(), threeDModelCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusFailure {
		t.Errorf("status = %q", res.Status)
	}
	if res.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q", res.Code)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPayment3DModelTimeoutKeepsOrderID(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), timeoutReply())

	res, err := p.CompleteThreeDSPayment(context.Background(), threeDModelCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusPending {
		t.Errorf("status = %q", res.Status)
	}
	if res.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q", res.PaymentID)
	}
}

func threeDPayProvider(t *testing.T, replies ...stubReply) (*Provider, *stubDoer) {
	t.Helper()
	cfg := testConfig()
	cfg.SecureType = "3DPay"
	return newTestProvider(t, cfg, replies...)
}

func TestCompleteThreeDSPayment3DPayConfirmsWithInquiry(t *testing.T) {
	cfg := testConfig()
	cfg.SecureType = "3DPay"
	cfg.Retry = &core.RetryConfig{Attempts: 2, Delay: time.Millisecond}
	// The inquiry is read-only, so a transport failure is retried.
	p, doer := newTestProvider(t, cfg, timeoutReply(), text(inquiryXML(nil)))

	res, err := p.CompleteThreeDSPayment(context.Background(), threeDPayCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 2 {
		t.Fatalf("expected the inquiry to be retried once, got %d calls", len(doer.calls))
	}
	xml := doer.calls[1].body
	if got := leaf(xml, "TxnType"); got != "OrderInquiry" {
		t.Errorf("TxnType = %q", got)
	}
	if got := leaf(xml, "OrgOrderId"); got != "ORDERPAY" {
		t.Errorf("OrgOrderId = %q", got)
	}
	if res.Status != core.StatusSuccess {
		t.Errorf("status = %q", res.Status)
	}
}

func TestCompleteThreeDSPayment3DPayRejectsUnconfirmedOrders(t *testing.T) {
	cases := []struct {
		name      string
		overrides Fields
	}{
		{"a voided order", Fields{{Key: "TxnStatus", Value: "V"}, {Key: "IsVoided", Value: "true"}}},
		{"another amount", Fields{{Key: "PurchAmount", Value: "999.00"}}},
		{"an unknown order", Fields{{Key: "ProcReturnCode", Value: "V013"}, {Key: "TxnResult", Value: ""}, {Key: "TxnStatus", Value: "P"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := threeDPayProvider(t, text(inquiryXML(tc.overrides)))
			res, err := p.CompleteThreeDSPayment(context.Background(), threeDPayCallback())
			if err != nil {
				t.Fatalf("CompleteThreeDSPayment: %v", err)
			}
			if res.ErrorCode != "INVALID_HASH" {
				t.Errorf("errorCode = %q", res.ErrorCode)
			}
			expectNoIDs(t, res)
		})
	}
}

func TestRefundAndCancelByOrgOrderID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()), text(approvedXML()))
	ctx := context.Background()

	if _, err := p.Refund(ctx, &core.RefundRequest{
		PaymentID: "ORDER1", Price: "0.5", Currency: "TRY", IP: "1.1.1.1",
	}); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if got := leaf(doer.calls[0].body, "TxnType"); got != "Refund" {
		t.Errorf("TxnType = %q", got)
	}
	if got := leaf(doer.calls[0].body, "OrgOrderId"); got != "ORDER1" {
		t.Errorf("OrgOrderId = %q", got)
	}
	if got := leaf(doer.calls[0].body, "PurchAmount"); got != "0.50" {
		t.Errorf("PurchAmount = %q", got)
	}

	if _, err := p.Cancel(ctx, &core.CancelRequest{PaymentID: "ORDER1", IP: "1.1.1.1"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := leaf(doer.calls[1].body, "TxnType"); got != "Void" {
		t.Errorf("TxnType = %q", got)
	}
}

func TestRefundMapsSameDayRejection(t *testing.T) {
	body := qnbXML(Fields{
		{Key: "ProcReturnCode", Value: "V014"},
		{Key: "ErrMsg", Value: "Bu işlem geri alınamaz, lüften asıl işlemi iptal edin."},
	})
	p, _ := newTestProvider(t, testConfig(), text(body))

	res, err := p.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER1", Price: "1", Currency: "TRY", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if res.Status != core.StatusFailure || res.ErrorCode != "V014" {
		t.Errorf("status=%q errorCode=%q", res.Status, res.ErrorCode)
	}
	if res.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q", res.Code)
	}
}

func TestGetPaymentReturnsOrderStatus(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(),
		text(inquiryXML(Fields{{Key: "TxnStatus", Value: "V"}, {Key: "IsVoided", Value: "true"}})))
	res, err := p.GetPayment(context.Background(), "ORDERPAY")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if res.Status != core.StatusCancelled {
		t.Errorf("status = %q", res.Status)
	}

	missing := qnbXML(Fields{
		{Key: "ProcReturnCode", Value: "V013"},
		{Key: "ErrMsg", Value: "Seçili İşlem Bulunamadı!"},
	})
	p, _ = newTestProvider(t, testConfig(), text(missing))
	res, err = p.GetPayment(context.Background(), "NOPE")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if res.Status != core.StatusFailure || res.ErrorCode != "V013" {
		t.Errorf("status=%q errorCode=%q", res.Status, res.ErrorCode)
	}
}

func TestBaseURLs(t *testing.T) {
	if SandboxBaseURL != "https://vpostest.qnb.com.tr" {
		t.Errorf("SandboxBaseURL = %q", SandboxBaseURL)
	}
	if ProductionBaseURL != "https://vpos.qnb.com.tr" {
		t.Errorf("ProductionBaseURL = %q", ProductionBaseURL)
	}
}

func TestOptionalOperationsAreNotSupported(t *testing.T) {
	p, _ := newTestProvider(t, testConfig())
	ctx := context.Background()
	if _, err := p.Authorize(ctx, mockPaymentRequest()); err == nil {
		t.Error("Authorize should not be supported")
	}
	if _, err := p.SaveCard(ctx, &core.SaveCardRequest{}); err == nil {
		t.Error("SaveCard should not be supported")
	}
	if _, err := p.BinCheck(ctx, "552879"); err == nil {
		t.Error("BinCheck should not be supported")
	}
	if _, err := p.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "552879", Price: "1"}); err == nil {
		t.Error("InstallmentInfo should not be supported")
	}
}

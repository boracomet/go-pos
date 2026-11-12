package vakifbank

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

var leafTagRe = regexp.MustCompile(`<([A-Za-z_][\w.-]*)>([^<]*)</([A-Za-z_][\w.-]*)>`)

func leaf(xml, tag string) string {
	for _, m := range leafTagRe.FindAllStringSubmatch(xml, -1) {
		if m[1] == tag && m[1] == m[3] {
			return m[2]
		}
	}
	return ""
}

func sentXML(t *testing.T, doer *stubDoer, call int) string {
	t.Helper()
	if call >= len(doer.calls) {
		t.Fatalf("call %d was not made (only %d)", call, len(doer.calls))
	}
	body := doer.calls[call].body
	if !strings.HasPrefix(body, "prmstr=") {
		t.Fatalf("body = %q, want a prmstr prefix", body)
	}
	xml, err := url.PathUnescape(strings.TrimPrefix(body, "prmstr="))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return xml
}

func expectNoIDs(t *testing.T, res *core.PaymentResponse) {
	t.Helper()
	if res.PaymentID != "" || res.ConversationID != "" {
		t.Errorf("expected no ids, got paymentId=%q conversationId=%q", res.PaymentID, res.ConversationID)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	cases := map[string]func(*Config){
		"merchantId": func(c *Config) { c.MerchantID = "" },
		"password":   func(c *Config) { c.Password = "" },
		"terminalNo": func(c *Config) { c.TerminalNo = "" },
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
	cfg := testConfig()
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Errorf("New without baseUrl error = %v", err)
	}
}

func TestCreatePaymentSendsNon3DSale(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))
	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"
	req.Installment = 3

	res, err := p.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if doer.calls[0].url != testBaseURL+EndpointVPOS {
		t.Errorf("url = %q", doer.calls[0].url)
	}
	xml := sentXML(t, doer, 0)
	for tag, want := range map[string]string{
		"MerchantId": testMerchantID, "TerminalNo": testTerminalNo, "TransactionType": "Sale",
		"TransactionId": "ORDER1", "CurrencyAmount": "1.20", "Expiry": "203012",
		"NumberOfInstallments": "3", "ClientIp": "85.34.78.112", "TransactionDeviceSource": "0",
	} {
		if got := leaf(xml, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	if strings.Contains(xml, "<ECI>") {
		t.Error("a non-3D sale carries ECI")
	}
	if res.Status != core.StatusSuccess || res.PaymentID != "ORDER1" {
		t.Errorf("status=%q paymentId=%q", res.Status, res.PaymentID)
	}
}

func TestCreatePaymentOmitsSingleInstallment(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))
	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"

	if _, err := p.CreatePayment(context.Background(), req); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if strings.Contains(sentXML(t, doer, 0), "NumberOfInstallments") {
		t.Error("a single payment carries NumberOfInstallments")
	}
}

func TestCreatePaymentMapsDecline(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(declinedXML()))
	req := mockPaymentRequest()
	req.ConversationID = "ORDER1"

	res, err := p.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if res.Status != core.StatusFailure || res.ErrorCode != "0051" {
		t.Errorf("status=%q errorCode=%q", res.Status, res.ErrorCode)
	}
	if res.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q", res.Code)
	}
}

func TestInitThreeDSPaymentEnrollsAndReturnsACSForm(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(enrolledXML))
	req := mockThreeDSPaymentRequest()
	req.ConversationID = "ORDER3D"

	res, err := p.InitThreeDSPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if doer.calls[0].url != MPITestURL {
		t.Errorf("MPI url = %q, want %q", doer.calls[0].url, MPITestURL)
	}
	form, err := url.ParseQuery(doer.calls[0].body)
	if err != nil {
		t.Fatalf("parse form: %v", err)
	}
	for name, want := range map[string]string{
		"MerchantId": testMerchantID, "MerchantPassword": testPassword,
		"VerifyEnrollmentRequestId": "ORDER3D", "Pan": "5528790000000008",
		"ExpiryDate": "3012", "PurchaseAmount": "1.20", "Currency": "949", "BrandName": "200",
		"SuccessUrl": "https://example.com/callback", "FailureUrl": "https://example.com/callback",
		"SessionInfo": "85.34.78.112",
	} {
		if got := form.Get(name); got != want {
			t.Errorf("form %s = %q, want %q", name, got, want)
		}
	}
	if res.Status != core.StatusPending {
		t.Errorf("status = %q", res.Status)
	}
	html := res.ThreeDSHTMLContent
	for _, want := range []string{
		`action="https://acs.example-bank.test/pareq"`,
		`name="PaReq" value="PAREQVALUE"`,
		`name="MD" value="MDVALUE"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if res.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q", res.PaymentID)
	}
}

func TestInitThreeDSPaymentUsesProductionMPI(t *testing.T) {
	cfg := testConfig()
	cfg.TestMode = false
	p, doer := newTestProvider(t, cfg, text(enrolledXML))

	if _, err := p.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest()); err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if doer.calls[0].url != MPIProductionURL {
		t.Errorf("MPI url = %q, want %q", doer.calls[0].url, MPIProductionURL)
	}
}

func TestInitThreeDSPaymentEnrollmentRejected(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(notFoundXML))

	res, err := p.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusFailure || res.ErrorCode != "9093" {
		t.Errorf("status=%q errorCode=%q", res.Status, res.ErrorCode)
	}
	if res.Code != core.CodeProviderError {
		t.Errorf("code = %q", res.Code)
	}
	if res.ThreeDSHTMLContent != "" {
		t.Error("a rejected enrollment returned a form")
	}
}

func TestInitThreeDSPaymentCardNotEnrolled(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(notEnrolledXML))

	res, err := p.InitThreeDSPayment(context.Background(), mockThreeDSPaymentRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusFailure {
		t.Errorf("status = %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "not enrolled") {
		t.Errorf("errorMessage = %q", res.ErrorMessage)
	}
}

func TestCompleteThreeDSPaymentChargesWithMpiTransactionID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	xml := sentXML(t, doer, 0)
	for tag, want := range map[string]string{
		"TransactionType": "Sale", "MpiTransactionId": "ORDER3D", "TransactionId": "ORDER3D",
		"ECI": "02", "CAVV": "CAVVVALUE=", "ClientIp": "85.34.78.112",
	} {
		if got := leaf(xml, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	for _, tag := range []string{"Pan", "Expiry", "CurrencyAmount", "CurrencyCode"} {
		if strings.Contains(xml, "<"+tag+">") {
			t.Errorf("the callback charge carries %s", tag)
		}
	}
	if res.Status != core.StatusSuccess || res.PaymentID != "ORDER3D" || res.ConversationID != "ORDER3D" {
		t.Errorf("status=%q paymentId=%q conversationId=%q", res.Status, res.PaymentID, res.ConversationID)
	}
}

func TestCompleteThreeDSPaymentIgnoresCallbackAmount(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))
	data := callbackData()
	data["PurchAmount"] = "1"

	if _, err := p.CompleteThreeDSPayment(context.Background(), data); err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if strings.Contains(sentXML(t, doer, 0), "CurrencyAmount") {
		t.Error("the callback amount reached VPOS")
	}
}

func TestCompleteThreeDSPaymentSendsCallbackInstallment(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(), text(approvedXML()))
	data := callbackData()
	data["InstallmentCount"] = "6"

	if _, err := p.CompleteThreeDSPayment(context.Background(), data); err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if got := leaf(sentXML(t, doer, 0), "NumberOfInstallments"); got != "6" {
		t.Errorf("NumberOfInstallments = %q", got)
	}
}

func TestCompleteThreeDSPaymentRejectsUnauthenticatedStatus(t *testing.T) {
	for _, status := range []string{"A", "U", "E", "N", ""} {
		name := status
		if name == "" {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			p, doer := newTestProvider(t, testConfig())
			data := callbackData()
			if status == "" {
				delete(data, "Status")
			} else {
				data["Status"] = status
			}
			res, err := p.CompleteThreeDSPayment(context.Background(), data)
			if err != nil {
				t.Fatalf("CompleteThreeDSPayment: %v", err)
			}
			if len(doer.calls) != 0 {
				t.Errorf("an unauthenticated callback reached VPOS: %d calls", len(doer.calls))
			}
			if res.Status != core.StatusFailure || res.Code != core.CodeThreeDSFailed {
				t.Errorf("status=%q code=%q", res.Status, res.Code)
			}
			expectNoIDs(t, res)
		})
	}
}

func TestCompleteThreeDSPaymentRejectsOtherMerchant(t *testing.T) {
	p, doer := newTestProvider(t, testConfig())
	data := callbackData()
	data["MerchantId"] = "999"

	res, err := p.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("a foreign callback reached VPOS: %d calls", len(doer.calls))
	}
	if res.ErrorCode != "INVALID_HASH" {
		t.Errorf("errorCode = %q", res.ErrorCode)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPaymentVPOSDecline(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), text(declinedXML()))

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusFailure || res.ErrorCode != "0051" {
		t.Errorf("status=%q errorCode=%q", res.Status, res.ErrorCode)
	}
	expectNoIDs(t, res)
}

func TestCompleteThreeDSPaymentTimeoutKeepsOrderID(t *testing.T) {
	p, _ := newTestProvider(t, testConfig(), timeoutReply())

	res, err := p.CompleteThreeDSPayment(context.Background(), callbackData())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment: %v", err)
	}
	if res.Status != core.StatusPending || res.PaymentID != "ORDER3D" {
		t.Errorf("status=%q paymentId=%q", res.Status, res.PaymentID)
	}
}

func TestRefundByReferenceTransactionID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(),
		text(vposXML(Fields{{Key: "ResultCode", Value: "0000"}, {Key: "TransactionId", Value: "R1"}})))

	res, err := p.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER3D", Price: "0.5", Currency: "TRY", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	xml := sentXML(t, doer, 0)
	for tag, want := range map[string]string{
		"TransactionType": "Refund", "ReferenceTransactionId": "ORDER3D", "CurrencyAmount": "0.50",
	} {
		if got := leaf(xml, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	if res.Status != core.StatusSuccess || res.RefundID != "R1" {
		t.Errorf("status=%q refundId=%q", res.Status, res.RefundID)
	}
}

func TestCancelByReferenceTransactionID(t *testing.T) {
	p, doer := newTestProvider(t, testConfig(),
		text(vposXML(Fields{{Key: "ResultCode", Value: "0000"}, {Key: "TransactionId", Value: "C1"}})))

	res, err := p.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER3D", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := leaf(sentXML(t, doer, 0), "TransactionType"); got != "Cancel" {
		t.Errorf("TransactionType = %q", got)
	}
	if res.Status != core.StatusSuccess {
		t.Errorf("status = %q", res.Status)
	}
}

// The record search by order id returns the order's approved sale but not the
// cancel or refund records, so it cannot give the current status: GetPayment
// must not report a reversed payment as paid.
func TestGetPaymentDoesNotReportReversedSales(t *testing.T) {
	reversals := []struct {
		name    string
		reverse func(*Provider) error
	}{
		{"a cancel", func(p *Provider) error {
			_, err := p.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER3D", IP: "1.1.1.1"})
			return err
		}},
		{"a full refund", func(p *Provider) error {
			_, err := p.Refund(context.Background(), &core.RefundRequest{
				PaymentID: "ORDER3D", Price: "1.20", Currency: "TRY", IP: "1.1.1.1",
			})
			return err
		}},
	}
	for _, tc := range reversals {
		t.Run(tc.name, func(t *testing.T) {
			p, doer := newTestProvider(t, testConfig(), text(approvedXML()),
				text(vposXML(Fields{{Key: "ResultCode", Value: "0000"}, {Key: "TransactionId", Value: "REV1"}})))
			req := mockPaymentRequest()
			req.ConversationID = "ORDER3D"

			res, err := p.CreatePayment(context.Background(), req)
			if err != nil || res.Status != core.StatusSuccess {
				t.Fatalf("CreatePayment = %v, %v", res, err)
			}
			if err := tc.reverse(p); err != nil {
				t.Fatalf("reverse: %v", err)
			}
			calls := len(doer.calls)

			if _, err := p.GetPayment(context.Background(), "ORDER3D"); err == nil {
				t.Fatal("GetPayment should not be supported")
			} else {
				var baseErr *core.BaseError
				if !errors.As(err, &baseErr) || baseErr.Code != "NOT_SUPPORTED" {
					t.Errorf("GetPayment error = %v, want NOT_SUPPORTED", err)
				}
			}
			if len(doer.calls) != calls {
				t.Errorf("GetPayment called the bank: %d -> %d", calls, len(doer.calls))
			}
		})
	}
}

func TestBaseURLs(t *testing.T) {
	if SandboxBaseURL != "https://onlineodemetest.vakifbank.com.tr:4443" {
		t.Errorf("SandboxBaseURL = %q", SandboxBaseURL)
	}
	if ProductionBaseURL != "https://onlineodeme.vakifbank.com.tr:4443" {
		t.Errorf("ProductionBaseURL = %q", ProductionBaseURL)
	}
	if MPITestURL != "https://3dsecuretest.vakifbank.com.tr:4443/MPIAPI/MPI_Enrollment.aspx" {
		t.Errorf("MPITestURL = %q", MPITestURL)
	}
	if MPIProductionURL != "https://3dsecure.vakifbank.com.tr:4443/MPIAPI/MPI_Enrollment.aspx" {
		t.Errorf("MPIProductionURL = %q", MPIProductionURL)
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

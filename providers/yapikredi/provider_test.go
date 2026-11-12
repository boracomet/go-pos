package yapikredi

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

var generatedOrderIDRe = regexp.MustCompile(`^[A-F0-9]{20}$`)

type methodResult struct {
	status         core.PaymentStatus
	code           core.PaymentErrorCode
	paymentID      string
	conversationID string
	errorCode      string
	errorMessage   string
	html           string
}

func callMethod(t *testing.T, provider *Provider, method string, request *core.ThreeDSPaymentRequest) methodResult {
	t.Helper()
	ctx := context.Background()
	if method == "initThreeDSPayment" {
		response, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		return methodResult{
			status:         response.Status,
			code:           response.Code,
			conversationID: response.ConversationID,
			errorCode:      response.ErrorCode,
			errorMessage:   response.ErrorMessage,
			html:           response.ThreeDSHTMLContent,
		}
	}
	payment := request.PaymentRequest
	response, err := provider.CreatePayment(ctx, &payment)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	return methodResult{
		status:         response.Status,
		code:           response.Code,
		paymentID:      response.PaymentID,
		conversationID: response.ConversationID,
		errorCode:      response.ErrorCode,
		errorMessage:   response.ErrorMessage,
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	for _, name := range []string{"merchantId", "terminalId", "posnetId", "encKey"} {
		cfg := testConfig()
		switch name {
		case "merchantId":
			cfg.MerchantID = ""
		case "terminalId":
			cfg.TerminalID = ""
		case "posnetId":
			cfg.PosnetID = ""
		case "encKey":
			cfg.EncKey = ""
		}
		cfg.BaseURL = testBaseURL
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("New() without %s succeeded, want a configuration error", name)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("New() error = %v, want it to name %s", err, name)
		}
	}
	cfg := testConfig()
	if _, err := New(cfg); err == nil {
		t.Error("New() without baseUrl succeeded, want a configuration error")
	}
}

func TestOrderIDGenerated(t *testing.T) {
	for _, method := range []string{"createPayment", "initThreeDSPayment"} {
		threeDS := method == "initThreeDSPayment"
		tr := &transport{}
		if threeDS {
			tr.replyWith(oosRequestXML)
		} else {
			tr.replyWith(approvedXML)
		}
		provider := newTestProvider(t, testConfig(), tr)
		request := testThreeDSRequest()
		request.ConversationID = ""

		result := callMethod(t, provider, method, request)
		wantStatus := core.StatusSuccess
		if threeDS {
			wantStatus = core.StatusPending
		}
		if result.status != wantStatus {
			t.Fatalf("%s status = %q, want %q", method, result.status, wantStatus)
		}
		if !generatedOrderIDRe.MatchString(result.conversationID) {
			t.Errorf("%s conversationId = %q, want 20 uppercase hex characters", method, result.conversationID)
		}
		tag, want := "orderID", padStart(result.conversationID, 24)
		if threeDS {
			tag, want = "XID", result.conversationID
		}
		wantString(t, leaf(sentXML(t, tr, 0), tag), want, method+" "+tag)

		next := callMethod(t, provider, method, request)
		if next.conversationID == result.conversationID {
			t.Errorf("%s reused the generated order id %q", method, next.conversationID)
		}
	}
}

func TestOrderIDAtLimit(t *testing.T) {
	for _, item := range []struct {
		method string
		length int
	}{
		{"createPayment", 24},
		{"initThreeDSPayment", 20},
	} {
		threeDS := item.method == "initThreeDSPayment"
		tr := &transport{}
		if threeDS {
			tr.replyWith(oosRequestXML)
		} else {
			tr.replyWith(approvedXML)
		}
		provider := newTestProvider(t, testConfig(), tr)
		request := testThreeDSRequest()
		request.ConversationID = strings.Repeat("A", item.length)

		result := callMethod(t, provider, item.method, request)
		wantStatus := core.StatusSuccess
		tag := "orderID"
		if threeDS {
			wantStatus, tag = core.StatusPending, "XID"
		}
		if result.status != wantStatus {
			t.Fatalf("%s status = %q, want %q", item.method, result.status, wantStatus)
		}
		wantString(t, result.conversationID, request.ConversationID, "conversationId")
		wantString(t, leaf(sentXML(t, tr, 0), tag), request.ConversationID, tag)
	}
}

func TestOrderIDOverlong(t *testing.T) {
	for _, item := range []struct {
		method string
		length int
	}{
		{"createPayment", 25},
		{"initThreeDSPayment", 21},
	} {
		tr := &transport{}
		provider := newTestProvider(t, testConfig(), tr)
		request := testThreeDSRequest()
		request.ConversationID = strings.Repeat("A", item.length)

		result := callMethod(t, provider, item.method, request)
		if result.status != core.StatusFailure {
			t.Errorf("%s status = %q, want failure", item.method, result.status)
		}
		if result.code != core.CodeInvalidRequest {
			t.Errorf("%s code = %q, want %q", item.method, result.code, core.CodeInvalidRequest)
		}
		if tr.calls != 0 {
			t.Errorf("%s made %d calls, want none", item.method, tr.calls)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(approvedXML)
	provider := newTestProvider(t, testConfig(), tr)
	request := testPaymentRequest()
	request.ConversationID = "ORDER1"

	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	wantString(t, tr.paths[0], EndpointAPI, "path")
	xml := sentXML(t, tr, 0)
	wantString(t, leaf(xml, "mid"), "6706598320", "mid")
	wantString(t, leaf(xml, "tid"), "67005551", "tid")
	wantString(t, leaf(xml, "amount"), "120", "amount")
	wantString(t, leaf(xml, "currencyCode"), "TL", "currencyCode")
	wantString(t, leaf(xml, "ccno"), "5528790000000008", "ccno")
	wantString(t, leaf(xml, "cvc"), "123", "cvc")
	wantString(t, leaf(xml, "expDate"), "3012", "expDate")
	wantString(t, leaf(xml, "installment"), "00", "installment")
	wantString(t, leaf(xml, "orderID"), "000000000000000000ORDER1", "orderID")
	wantString(t, leaf(xml, "tranDateRequired"), "1", "tranDateRequired")
	if !strings.Contains(xml, "<sale>") {
		t.Error("xml does not hold a sale block")
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "021000000000000123", "paymentId")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestCreatePaymentDecline(t *testing.T) {
	tr := &transport{}
	tr.replyWith(declinedXML)
	provider := newTestProvider(t, testConfig(), tr)
	request := testPaymentRequest()
	request.ConversationID = "ORDER1"

	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "0225", "errorCode")
	wantString(t, result.ErrorMessage, "ONAYLANMADI", "errorMessage")
	wantString(t, result.PaymentID, "", "paymentId")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestCreatePaymentRejectsOrderID(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)
	request := testPaymentRequest()
	request.ConversationID = "ORDER-1"

	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want none", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestCreatePaymentTimeout(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{err: context.DeadlineExceeded}
	provider := newTestProvider(t, testConfig(), tr)
	request := testPaymentRequest()
	request.ConversationID = "ORDER1"

	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	wantString(t, result.ErrorCode, core.NetworkErrorCode, "errorCode")
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestInitThreeDSPayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(oosRequestXML)
	cfg := testConfig()
	provider := newTestProvider(t, cfg, tr)
	request := testThreeDSRequest()
	request.ConversationID = "ORDER3D"

	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	xml := sentXML(t, tr, 0)
	wantString(t, leaf(xml, "posnetid"), cfg.PosnetID, "posnetid")
	wantString(t, leaf(xml, "XID"), "0000000000000ORDER3D", "XID")
	wantString(t, leaf(xml, "tranType"), "Sale", "tranType")
	wantString(t, leaf(xml, "amount"), "120", "amount")
	wantString(t, leaf(xml, "cardHolderName"), "John Doe", "cardHolderName")
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	html := result.ThreeDSHTMLContent
	for _, want := range []string{
		`action="` + testBaseURL + EndpointGate + `"`,
		`name="posnetData" value="DATA1VALUE"`,
		`name="posnetData2" value="DATA2VALUE"`,
		`name="digest" value="SIGNVALUE"`,
		`name="merchantReturnURL" value="https://example.com/callback"`,
		`name="mid" value="` + cfg.MerchantID + `"`,
		`name="posnetID" value="` + cfg.PosnetID + `"`,
		`name="lang" value="tr"`,
		`name="useJokerVadaa" value="1"`,
		`id="yapikredi-3d-form"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html does not contain %s", want)
		}
	}
	if strings.Contains(html, cfg.EncKey) || strings.Contains(html, cfg.TerminalID) {
		t.Error("html leaks the credentials")
	}
	wantString(t, result.ConversationID, "ORDER3D", "conversationId")
}

func TestInitThreeDSPaymentRejected(t *testing.T) {
	tr := &transport{}
	tr.replyWith(ykbXML([]field{
		{"approved", "0"}, {"respCode", "E146"}, {"respText", "Terminal bulunamadi"},
	}, nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "E146", "errorCode")
	wantString(t, result.ThreeDSHTMLContent, "", "threeDSHtmlContent")
}

func TestInitThreeDSPaymentNeedsCallbackURL(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)
	request := testThreeDSRequest()
	request.CallbackURL = ""

	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want none", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestInitThreeDSPaymentLanguage(t *testing.T) {
	tr := &transport{}
	tr.replyWith(oosRequestXML)
	cfg := testConfig()
	cfg.Locale = "en-US"
	provider := newTestProvider(t, cfg, tr)

	result, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if !strings.Contains(result.ThreeDSHTMLContent, `name="lang" value="en"`) {
		t.Error("html does not carry the English language field")
	}
}

func TestCompleteThreeDSPayment(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(nil))
	tr.replyOnceWith(approvedXML)
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	resolve := sentXML(t, tr, 0)
	if !strings.Contains(resolve, "<oosResolveMerchantData>") {
		t.Error("the first call is not oosResolveMerchantData")
	}
	wantString(t, leaf(resolve, "bankData"), "BANKPACKET0123456789", "bankData")
	wantString(t, leaf(resolve, "merchantData"), "MERCHANTPACKET0123456789", "merchantData")
	wantString(t, leaf(resolve, "sign"), "SIGN0123456789", "sign")
	wantString(t, leaf(resolve, "wpAmount"), "0", "wpAmount")
	wantString(t, leaf(resolve, "mac"), ykbMAC, "resolve mac")
	tran := sentXML(t, tr, 1)
	if !strings.Contains(tran, "<oosTranData>") {
		t.Error("the second call is not oosTranData")
	}
	wantString(t, leaf(tran, "mac"), ykbMAC, "charge mac")
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "021000000000000123", "paymentId")
	wantString(t, result.ConversationID, "ORDER3D", "conversationId")
}

func TestCompleteChecksBankMACs(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(map[string]string{"mac": resolveMAC}))
	tr.replyOnceWith(ykbXML([]field{
		{"approved", "1"}, {"hostlogkey", "021000000000000123"}, {"mac", chargeMAC},
	}, nil))
	provider := newTestProvider(t, testConfig(), tr)
	ok, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if ok.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", ok.Status)
	}

	forgedResolve := &transport{}
	forgedResolve.replyOnceWith(ykbResolveXML(map[string]string{"mac": "FORGED="}))
	provider = newTestProvider(t, testConfig(), forgedResolve)
	forged, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if forgedResolve.calls != 1 {
		t.Errorf("calls = %d, want 1 (the charge must not be sent)", forgedResolve.calls)
	}
	wantString(t, forged.ErrorCode, "INVALID_HASH", "errorCode")
	if forged.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", forged.Code, core.CodeInvalidHash)
	}
	wantNoIDs(t, forged.PaymentID, forged.ConversationID)

	forgedCharge := &transport{}
	forgedCharge.replyOnceWith(ykbResolveXML(map[string]string{"mac": resolveMAC}))
	forgedCharge.replyOnceWith(ykbXML([]field{
		{"approved", "1"}, {"hostlogkey", "021000000000000123"}, {"mac", "FORGED="},
	}, nil))
	provider = newTestProvider(t, testConfig(), forgedCharge)
	unknown, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if unknown.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", unknown.Status)
	}
	wantString(t, unknown.ErrorCode, "INVALID_HASH", "errorCode")
	wantString(t, unknown.ConversationID, "ORDER3D", "conversationId")
	wantString(t, unknown.PaymentID, "", "paymentId")
}

func TestCompleteHalfSecure(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(map[string]string{"mdStatus": "2"}))
	provider := newTestProvider(t, testConfig(), tr)

	rejected, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	wantString(t, rejected.ErrorCode, "MD_STATUS_2", "errorCode")
	if rejected.Code != core.CodeThreeDSFailed {
		t.Errorf("code = %q, want %q", rejected.Code, core.CodeThreeDSFailed)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	wantNoIDs(t, rejected.PaymentID, rejected.ConversationID)

	accepted := &transport{}
	accepted.replyOnceWith(ykbResolveXML(map[string]string{"mdStatus": "2"}))
	accepted.replyOnceWith(approvedXML)
	cfg := testConfig()
	cfg.AcceptedMdStatuses = []string{"1", "2"}
	provider = newTestProvider(t, cfg, accepted)
	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success when mdStatus 2 is accepted", result.Status)
	}
}

func TestCompleteNeedsOrder(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), callbackFields())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want none", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "orderId, amount, currency") {
		t.Errorf("errorMessage = %q, want it to ask for the order", result.ErrorMessage)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteMissingPackets(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)
	data := map[string]any{"orderId": "ORDER3D", "amount": "1.20", "currency": "TRY", "Sign": "x"}

	result, err := provider.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want none", tr.calls)
	}
	wantString(t, result.ErrorCode, "INVALID_HASH", "errorCode")
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteRejectsMismatch(t *testing.T) {
	for _, item := range []struct {
		name string
		data map[string]string
	}{
		{"XID", map[string]string{"xid": "0000000000000OTHER01"}},
		{"amount", map[string]string{"amount": "1"}},
		{"currency", map[string]string{"currency": "US"}},
	} {
		tr := &transport{}
		tr.replyOnceWith(ykbResolveXML(item.data))
		provider := newTestProvider(t, testConfig(), tr)

		result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if tr.calls != 1 {
			t.Errorf("%s: calls = %d, want 1", item.name, tr.calls)
		}
		wantString(t, result.ErrorCode, "INVALID_HASH", item.name+" errorCode")
		wantNoIDs(t, result.PaymentID, result.ConversationID)
	}
}

func TestCompleteResolveNotApproved(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbXML([]field{
		{"approved", "0"}, {"respCode", "0148"}, {"respText", "MAC HATASI"},
	}, nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	wantString(t, result.ErrorCode, "INVALID_HASH", "errorCode")
}

func TestCompleteNotAuthenticated(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(map[string]string{
		"mdStatus": "0", "mdErrorMessage": "Not authenticated",
	}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeThreeDSFailed {
		t.Errorf("code = %q, want %q", result.Code, core.CodeThreeDSFailed)
	}
	wantString(t, result.ErrorMessage, "Not authenticated", "errorMessage")
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteChargeDeclined(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(nil))
	tr.replyOnceWith(declinedXML)
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "0225", "errorCode")
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteChargeTimeout(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(nil))
	tr.once = append(tr.once, timeoutReply())
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	wantString(t, result.ConversationID, "ORDER3D", "conversationId")
	wantString(t, result.PaymentID, "", "paymentId")
}

func TestCompleteTypedRequest(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbResolveXML(nil))
	tr.replyOnceWith(approvedXML)
	provider := newTestProvider(t, testConfig(), tr)
	request := CompleteRequest{
		CallbackFields: CallbackFields{
			BankPacket:     "BANKPACKET0123456789",
			MerchantPacket: "MERCHANTPACKET0123456789",
			Sign:           "SIGN0123456789",
		},
		OrderID:  "ORDER3D",
		Amount:   "1.20",
		Currency: "TRY",
	}

	result, err := provider.CompleteThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, leaf(sentXML(t, tr, 0), "mac"), ykbMAC, "mac")
}

func TestRefund(t *testing.T) {
	tr := &transport{}
	tr.replyWith(approvedXML)
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "021000000000000123",
		Price:     "0.5",
		Currency:  string(core.CurrencyTRY),
		IP:        "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	xml := sentXML(t, tr, 0)
	if !strings.Contains(xml, "<return>") {
		t.Error("xml does not hold a return block")
	}
	wantString(t, leaf(xml, "hostLogKey"), "021000000000000123", "hostLogKey")
	wantString(t, leaf(xml, "amount"), "50", "amount")
	wantString(t, leaf(xml, "transaction"), "sale", "transaction")
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, result.RefundID, "021000000000000123", "refundId")
}

func TestCancel(t *testing.T) {
	tr := &transport{}
	tr.replyWith(approvedXML)
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{
		PaymentID: "021000000000000123",
		IP:        "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !strings.Contains(sentXML(t, tr, 0),
		"<reverse><transaction>sale</transaction><hostLogKey>021000000000000123</hostLogKey></reverse>") {
		t.Error("xml does not hold the reverse block")
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, result.TransactionID, "021000000000000123", "transactionId")
}

func TestCancelRejected(t *testing.T) {
	tr := &transport{}
	tr.replyWith(declinedXML)
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "1", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "0225", "errorCode")
	wantString(t, result.TransactionID, "", "transactionId")
}

func TestGetPayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(ykbAgreementXML([]map[string]string{{}}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if !strings.Contains(sentXML(t, tr, 0),
		"<agreement><orderID>000000000000000000ORDER1</orderID></agreement>") {
		t.Error("xml does not hold the agreement block")
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "021450428990000191", "paymentId")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
}

func TestGetPaymentFallsBackToThreeDSOrderID(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ykbXML([]field{
		{"approved", "0"}, {"respCode", "E219"}, {"respText", "KAYIT BULUNAMADI"},
	}, nil))
	tr.replyOnceWith(ykbAgreementXML([]map[string]string{
		{"orderID": "TDS_000000000000ORDER1"},
		{"state": "Sale_Reverse", "hostLogKey": "021450428990000192"},
	}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if !strings.Contains(sentXML(t, tr, 1), "<orderID>TDS_00000000000000ORDER1</orderID>") {
		t.Error("the second call does not use the 3D Secure order id")
	}
	if result.Status != core.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", result.Status)
	}
	wantString(t, result.PaymentID, "021450428990000191", "paymentId")
}

func TestGetPaymentUTF16(t *testing.T) {
	tr := &transport{}
	tr.replyWith(strings.Join(strings.Split(ykbAgreementXML([]map[string]string{{}}), ""), "\x00"))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
}

func TestGetPaymentWithoutSuccessfulSale(t *testing.T) {
	tr := &transport{}
	tr.replyWith(ykbAgreementXML([]map[string]string{{"txnStatus": "0"}}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.PaymentID, "", "paymentId")

	missing := &transport{}
	missing.replyWith(ykbXML([]field{
		{"approved", "0"}, {"respCode", "E219"}, {"respText", "KAYIT BULUNAMADI"},
	}, nil))
	provider = newTestProvider(t, testConfig(), missing)
	result, err = provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "E219", "errorCode")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestGetPaymentApprovedWithoutTransactions(t *testing.T) {
	tr := &transport{}
	tr.replyWith(ykbXML([]field{{"approved", "1"}}, nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "E219", "errorCode")
	wantString(t, result.ErrorMessage, "No transaction found for the order", "errorMessage")
	if tr.calls != 2 {
		t.Errorf("calls = %d, want 2 (both order ids)", tr.calls)
	}
}

func TestGetPaymentOrderStates(t *testing.T) {
	for _, item := range []struct {
		state     string
		txnStatus string
		want      core.PaymentStatus
	}{
		{"Sale", "1", core.StatusSuccess},
		{"vft_sale", "1", core.StatusSuccess},
		{"capture", "1", core.StatusSuccess},
		{"authorization", "1", core.StatusSuccess},
		{"Sale_Reverse", "1", core.StatusCancelled},
		{"return", "1", core.StatusCancelled},
		{"vft_return", "1", core.StatusCancelled},
		{"Sale", "0", core.StatusFailure},
		{"Sale_Reverse", "0", core.StatusFailure},
	} {
		tr := &transport{}
		tr.replyWith(ykbAgreementXML([]map[string]string{
			{"state": item.state, "txnStatus": item.txnStatus},
		}))
		provider := newTestProvider(t, testConfig(), tr)

		result, err := provider.GetPayment(context.Background(), "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != item.want {
			t.Errorf("state %q txnStatus %q: status = %q, want %q",
				item.state, item.txnStatus, result.Status, item.want)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	wantString(t, WithDefaults(testConfig(), true).BaseURL, testBaseURL, "sandbox baseUrl")
	wantString(t, WithDefaults(testConfig(), false).BaseURL, prodBaseURL, "production baseUrl")
	cfg := testConfig()
	cfg.BaseURL = "https://posnet.example.com"
	wantString(t, WithDefaults(cfg, true).BaseURL, cfg.BaseURL, "explicit baseUrl")
}

func TestNeverRetries(t *testing.T) {
	cfg := testConfig()
	cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	tr := &transport{}
	tr.fallback = &reply{err: context.DeadlineExceeded}
	provider := newTestProvider(t, cfg, tr)

	if _, err := provider.CreatePayment(context.Background(), testPaymentRequest()); err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if _, err := provider.GetPayment(context.Background(), "ORDER1"); err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if tr.calls != 2 {
		t.Errorf("calls = %d, want 2 (one per operation)", tr.calls)
	}
}

func TestUnexpectedResponse(t *testing.T) {
	tr := &transport{}
	tr.replyWith("<html/>")
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorMessage, "Yapı Kredi returned an unexpected response", "errorMessage")
}

func TestUnsupportedOperations(t *testing.T) {
	provider := newTestProvider(t, testConfig(), &transport{})
	ctx := context.Background()

	if _, err := provider.Authorize(ctx, testPaymentRequest()); err == nil {
		t.Error("Authorize() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.InitThreeDSAuthorize(ctx, testThreeDSRequest()); err == nil {
		t.Error("InitThreeDSAuthorize() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "1"}); err == nil {
		t.Error("Capture() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: "1"}); err == nil {
		t.Error("VoidAuthorization() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: *testCard()}); err == nil {
		t.Error("SaveCard() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.ListCards(ctx, &core.ListCardsRequest{}); err == nil {
		t.Error("ListCards() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.DeleteCard(ctx, &core.DeleteCardRequest{}); err == nil {
		t.Error("DeleteCard() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.BinCheck(ctx, "552879"); err == nil {
		t.Error("BinCheck() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{}); err == nil {
		t.Error("InstallmentInfo() succeeded, want NOT_SUPPORTED")
	}
}

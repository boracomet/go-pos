package albaraka

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

var generatedOrderIDRe = regexp.MustCompile(`^[A-F0-9]{20}$`)

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"merchantId", "terminalId", "posnetId", "encKey"} {
		cfg := testConfig()
		cfg.BaseURL = testBaseURL
		switch field {
		case "merchantId":
			cfg.MerchantID = ""
		case "terminalId":
			cfg.TerminalID = ""
		case "posnetId":
			cfg.PosnetID = ""
		case "encKey":
			cfg.EncKey = ""
		}
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("New() without %s succeeded", field)
		}
		var configErr *core.ConfigurationError
		if !errors.As(err, &configErr) {
			t.Fatalf("New() without %s error = %T, want a configuration error", field, err)
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("New() without %s error = %q, want it to name the field", field, err)
		}
	}
}

type methodResult struct {
	status         core.PaymentStatus
	code           core.PaymentErrorCode
	conversationID string
	html           string
}

func callMethod(t *testing.T, provider *Provider, method string, request *core.ThreeDSPaymentRequest) methodResult {
	t.Helper()
	ctx := context.Background()
	switch method {
	case "createPayment":
		result, err := provider.CreatePayment(ctx, &request.PaymentRequest)
		if err != nil {
			t.Fatalf("%s() error = %v", method, err)
		}
		return methodResult{status: result.Status, code: result.Code, conversationID: result.ConversationID}
	case "authorize":
		result, err := provider.Authorize(ctx, &request.PaymentRequest)
		if err != nil {
			t.Fatalf("%s() error = %v", method, err)
		}
		return methodResult{status: result.Status, code: result.Code, conversationID: result.ConversationID}
	case "initThreeDSPayment":
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("%s() error = %v", method, err)
		}
		return methodResult{status: result.Status, code: result.Code, conversationID: result.ConversationID, html: result.ThreeDSHTMLContent}
	case "initThreeDSAuthorize":
		result, err := provider.InitThreeDSAuthorize(ctx, request)
		if err != nil {
			t.Fatalf("%s() error = %v", method, err)
		}
		return methodResult{status: result.Status, code: result.Code, conversationID: result.ConversationID, html: result.ThreeDSHTMLContent}
	}
	t.Fatalf("unknown method %q", method)
	return methodResult{}
}

func TestOrderIDs(t *testing.T) {
	for _, method := range []string{"createPayment", "authorize", "initThreeDSPayment", "initThreeDSAuthorize"} {
		threeDS := method == "initThreeDSPayment" || method == "initThreeDSAuthorize"
		wantStatus := core.StatusSuccess
		if threeDS {
			wantStatus = core.StatusPending
		}

		t.Run(method+"/generates an id", func(t *testing.T) {
			tr := &transport{}
			tr.replyWith(albarakaResponse("00", nil))
			provider := newTestProvider(t, testConfig(), tr)

			request := testThreeDSRequest()
			request.ConversationID = ""
			result := callMethod(t, provider, method, request)
			if result.status != wantStatus {
				t.Errorf("status = %q, want %q", result.status, wantStatus)
			}
			if !generatedOrderIDRe.MatchString(result.conversationID) {
				t.Errorf("conversationId = %q, want 20 uppercase hex characters", result.conversationID)
			}
			orderID := result.conversationID
			if threeDS {
				orderID = formFields(result.html)["OrderId"]
			} else {
				wantString(t, sentBody(t, tr, 0)["OrderId"], result.conversationID, "OrderId")
			}
			if orderID != result.conversationID {
				t.Errorf("OrderId = %q, want the conversation id %q", orderID, result.conversationID)
			}

			next := callMethod(t, provider, method, request)
			if next.conversationID == result.conversationID {
				t.Error("the generated order id was reused")
			}
		})

		t.Run(method+"/keeps a 20 character id", func(t *testing.T) {
			tr := &transport{}
			tr.replyWith(albarakaResponse("00", nil))
			provider := newTestProvider(t, testConfig(), tr)

			conversationID := strings.Repeat("A", 20)
			request := testThreeDSRequest()
			request.ConversationID = conversationID
			result := callMethod(t, provider, method, request)
			if result.status != wantStatus {
				t.Errorf("status = %q, want %q", result.status, wantStatus)
			}
			if result.conversationID != conversationID {
				t.Errorf("conversationId = %q, want %q", result.conversationID, conversationID)
			}
			if threeDS {
				wantString(t, formFields(result.html)["OrderId"], conversationID, "OrderId")
			} else {
				wantString(t, sentBody(t, tr, 0)["OrderId"], conversationID, "OrderId")
			}
		})

		t.Run(method+"/rejects an overlong id", func(t *testing.T) {
			tr := &transport{}
			provider := newTestProvider(t, testConfig(), tr)

			request := testThreeDSRequest()
			request.ConversationID = strings.Repeat("A", 21)
			result := callMethod(t, provider, method, request)
			if result.status != core.StatusFailure {
				t.Errorf("status = %q, want failure", result.status)
			}
			if result.code != core.CodeInvalidRequest {
				t.Errorf("code = %q, want %q", result.code, core.CodeInvalidRequest)
			}
			if tr.calls != 0 {
				t.Errorf("calls = %d, want 0", tr.calls)
			}
		})
	}
}

func TestCreatePayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Sale" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Sale")
	}
	if got := tr.headers[0].Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body := sentBody(t, tr, 0)
	for key, want := range map[string]string{
		"ApiType":               "JSON",
		"ApiVersion":            "V100",
		"MerchantNo":            "6700000067",
		"TerminalNo":            "67000067",
		"IsMailOrder":           "N",
		"PaymentInstrumentType": "CARD",
		"CurrencyCode":          "TL",
		"OrderId":               "00000000000000ORDER1",
		"InstallmentType":       "N",
		"MAC":                   saleMAC,
	} {
		wantString(t, body[key], want, key)
	}
	wantString(t, nested(t, body, "CardInformationData.CardNo"), "5400010000000004", "CardInformationData.CardNo")
	wantString(t, nested(t, body, "CardInformationData.ExpireDate"), "3012", "CardInformationData.ExpireDate")
	wantString(t, nested(t, body, "CardInformationData.Cvc2"), "000", "CardInformationData.Cvc2")
	wantNumber(t, body["Amount"], 120, "Amount")
	wantNumber(t, body["InstallmentCount"], 0, "InstallmentCount")
	// The service rejects requests that leave these out
	for _, key := range []string{"IsTDSecureMerchant", "PointAmount", "CipheredData", "ThreeDSecureData"} {
		value, exists := body[key]
		if !exists || value != nil {
			t.Errorf("%s = %v (present: %v), want null", key, value, exists)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "REF123", "paymentId")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestCreatePaymentDecline(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("0148", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "0148", "errorCode")
	wantString(t, result.ErrorMessage, "UNAUTHORIZED REQUEST", "errorMessage")
	wantString(t, result.PaymentID, "", "paymentId")
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
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
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestCreatePaymentTimeout(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{err: context.DeadlineExceeded}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
}

func TestInitThreeDSPayment(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	request := testThreeDSRequest()
	request.Installment = 3
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0: the form is built locally", tr.calls)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	html := result.ThreeDSHTMLContent
	action := `action="` + testBaseURL + EndpointGate + `"`
	if !strings.Contains(html, action) {
		t.Errorf("the form does not post to %s", action)
	}
	fields := formFields(html)
	for key, want := range map[string]string{
		"MerchantNo":        "6700000067",
		"TerminalNo":        "67000067",
		"PosnetID":          "1010",
		"TransactionType":   "Sale",
		"OrderId":           "00000000000000ORDER1",
		"Amount":            "120",
		"CurrencyCode":      "TL",
		"MerchantReturnURL": "https://example.com/callback",
		"InstallmentCount":  "3",
		"InstallmentType":   "Y",
		"CardNo":            "5400010000000004",
		"ExpiredDate":       "3012",
		"Cvv":               "000",
		"UseOOS":            "0",
		"MAC":               saleMAC,
	} {
		wantString(t, fields[key], want, key)
	}
	if strings.Contains(html, testConfig().EncKey) {
		t.Error("the form carries the encryption key")
	}
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestCompleteThreeDSPayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeRequest())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Sale" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Sale")
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["IsTDSecureMerchant"], "Y", "IsTDSecureMerchant")
	wantString(t, nested(t, body, "ThreeDSecureData.SecureTransactionId"), "SECTXN1", "ThreeDSecureData.SecureTransactionId")
	wantString(t, nested(t, body, "ThreeDSecureData.CavvData"), "CAVVVALUE=", "ThreeDSecureData.CavvData")
	wantString(t, nested(t, body, "ThreeDSecureData.Eci"), "05", "ThreeDSecureData.Eci")
	wantNumber(t, nested(t, body, "ThreeDSecureData.MdStatus"), 1, "ThreeDSecureData.MdStatus")
	wantString(t, nested(t, body, "ThreeDSecureData.MD"), "MDVALUE", "ThreeDSecureData.MD")
	wantString(t, body["MACParams"], macParamsThreeDS, "MACParams")
	wantNumber(t, body["Amount"], 120, "Amount")
	wantString(t, body["CurrencyCode"], "TL", "CurrencyCode")
	wantString(t, body["OrderId"], "00000000000000ORDER1", "OrderId")
	wantNumber(t, body["PointAmount"], 0, "PointAmount")
	wantString(t, body["MAC"], threeDSMAC, "MAC")

	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "REF123", "paymentId")
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestCompleteThreeDSPaymentNotAuthenticated(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	data := completeRequest()
	data["MdStatus"] = "0"
	data["MdErrorMessage"] = "Not authenticated"
	result, err := provider.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Code != core.CodeThreeDSFailed {
		t.Errorf("code = %q, want %q", result.Code, core.CodeThreeDSFailed)
	}
	wantString(t, result.ErrorMessage, "Not authenticated", "errorMessage")
	wantString(t, result.ConversationID, "", "conversationId")
}

func TestCompleteThreeDSPaymentHalfSecure(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	data := completeRequest()
	data["MdStatus"] = "2"
	rejected, err := provider.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	wantString(t, rejected.ErrorCode, "MD_STATUS_2", "errorCode")
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}

	cfg := testConfig()
	cfg.AcceptedMdStatuses = []string{"1", "2"}
	accepting := newTestProvider(t, cfg, tr)
	tr.replyWith(albarakaResponse("00", nil))
	accepted, err := accepting.CompleteThreeDSPayment(context.Background(), data)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	wantNumber(t, nested(t, sentBody(t, tr, 0), "ThreeDSecureData.MdStatus"), 2, "ThreeDSecureData.MdStatus")
	if accepted.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", accepted.Status)
	}
}

func TestCompleteThreeDSPaymentNeedsOrder(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
	if !strings.Contains(result.ErrorMessage, "orderId and amount") {
		t.Errorf("errorMessage = %q, want it to name orderId and amount", result.ErrorMessage)
	}
}

func TestCompleteThreeDSPaymentMissingThreeDSFields(t *testing.T) {
	for _, field := range []string{"SecureTransactionId", "CAVV", "ECI", "MD"} {
		tr := &transport{}
		provider := newTestProvider(t, testConfig(), tr)

		data := completeRequest()
		delete(data, field)
		result, err := provider.CompleteThreeDSPayment(context.Background(), data)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() without %s error = %v", field, err)
		}
		if tr.calls != 0 {
			t.Errorf("without %s: calls = %d, want 0", field, tr.calls)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("without %s: status = %q, want failure", field, result.Status)
		}
		if !strings.Contains(result.ErrorMessage, field) {
			t.Errorf("without %s: errorMessage = %q, want it to name the field", field, result.ErrorMessage)
		}
		wantNoIDs(t, result.PaymentID, result.ConversationID)
	}
}

func TestCompleteThreeDSPaymentDecline(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("0148", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeRequest())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentTimeout(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{err: context.DeadlineExceeded}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), completeRequest())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
	wantString(t, result.PaymentID, "", "paymentId")
}

func TestCompleteThreeDSPaymentTypedRequest(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	request := &CompleteRequest{
		MdStatus:            "1",
		SecureTransactionId: "SECTXN1",
		CAVV:                "CAVVVALUE=",
		ECI:                 "05",
		MD:                  "MDVALUE",
		OrderID:             "ORDER1",
		Amount:              "1.20",
		Currency:            "TRY",
	}
	result, err := provider.CompleteThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, sentBody(t, tr, 0)["MAC"], threeDSMAC, "MAC")
}

func TestRefund(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "REF123", Price: "0.5", Currency: "TRY", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Return" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Return")
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["ReferenceCode"], "REF123", "ReferenceCode")
	if value, exists := body["OrderId"]; !exists || value != nil {
		t.Errorf("OrderId = %v (present: %v), want null", value, exists)
	}
	wantNumber(t, body["Amount"], 50, "Amount")
	wantString(t, body["CurrencyCode"], "TL", "CurrencyCode")
	wantString(t, body["MACParams"], macParamsReference, "MACParams")
	wantString(t, body["MAC"], referenceMAC, "MAC")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.RefundID, "REF123", "refundId")
}

func TestCancel(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "REF123", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Reverse" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Reverse")
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["ReferenceCode"], "REF123", "ReferenceCode")
	wantString(t, body["TransactionType"], "Sale", "TransactionType")
	wantString(t, body["MAC"], referenceMAC, "MAC")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestGetPayment(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", map[string]any{"TransactionData": []any{map[string]any{}}}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/TransactionInquiry" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/TransactionInquiry")
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["MACParams"], macParamsInquiry, "MACParams")
	wantString(t, body["OrderId"], "00000000000000ORDER1", "OrderId")
	wantString(t, body["MAC"], inquiryMAC, "MAC")
	// The TransactionData format is not confirmed yet, so a found order stays pending
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	wantString(t, result.ConversationID, "ORDER1", "conversationId")
}

func TestGetPaymentUnknownOrder(t *testing.T) {
	tr := &transport{}
	tr.replyWith(map[string]any{
		"ServiceResponseData": map[string]any{"ResponseCode": "E219", "ResponseDescription": "Kayıt Bulunamadı"},
		"AuthCode":            nil,
		"ReferenceCode":       nil,
		"TransactionData":     nil,
	})
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "E219", "errorCode")
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestOrderStatus(t *testing.T) {
	cases := []struct {
		name string
		data any
		want core.PaymentStatus
	}{
		{"sale", []any{map[string]any{"State": "Sale", "TxnStatus": "1"}}, core.StatusSuccess},
		{"reversed", []any{
			map[string]any{"state": "Sale", "txnStatus": 1},
			map[string]any{"state": "Sale_Reverse", "txnStatus": 1},
		}, core.StatusCancelled},
		{"declined", []any{map[string]any{"State": "Sale", "TxnStatus": "0"}}, core.StatusFailure},
		{"unknown shape", []any{map[string]any{}}, core.StatusPending},
		{"no data", nil, core.StatusPending},
	}
	for _, tc := range cases {
		if got := OrderStatus(tc.data); got != tc.want {
			t.Errorf("%s: OrderStatus() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAuthorize(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Authorize(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Auth" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Auth")
	}
	wantString(t, sentBody(t, tr, 0)["MAC"], saleMAC, "MAC")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestInitThreeDSAuthorize(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	init, err := provider.InitThreeDSAuthorize(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	wantString(t, formFields(init.ThreeDSHTMLContent)["TransactionType"], "Auth", "TransactionType")

	tr.replyWith(albarakaResponse("00", nil))
	data := completeRequest()
	data["transactionType"] = "Auth"
	delete(data, "currency")
	if _, err := provider.CompleteThreeDSPayment(context.Background(), data); err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Auth" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Auth")
	}
}

func TestCapture(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Capture(context.Background(), &core.CaptureRequest{PaymentID: "REF123", Amount: "0.5", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Capture" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Capture")
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["MACParams"], macParamsCapture, "MACParams")
	wantString(t, body["ReferenceCode"], "REF123", "ReferenceCode")
	wantNumber(t, body["Amount"], 50, "Amount")
	wantString(t, body["CurrencyCode"], "TL", "CurrencyCode")
	if value, exists := body["PointAmount"]; !exists || value != nil {
		t.Errorf("PointAmount = %v (present: %v), want null", value, exists)
	}
	wantNumber(t, body["InstallmentCount"], 0, "InstallmentCount")
	wantString(t, body["InstallmentType"], "N", "InstallmentType")
	wantString(t, body["MAC"], referenceMAC, "MAC")
	if _, exists := body["OrderId"]; exists {
		t.Error("Capture must not send OrderId")
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "REF123", "paymentId")
}

func TestVoidAuthorization(t *testing.T) {
	tr := &transport{}
	tr.replyWith(albarakaResponse("00", nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.VoidAuthorization(context.Background(), &core.VoidAuthorizationRequest{PaymentID: "REF123", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if tr.paths[0] != EndpointAPI+"/Reverse" {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointAPI+"/Reverse")
	}
	wantString(t, sentBody(t, tr, 0)["TransactionType"], "Auth", "TransactionType")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestWithDefaults(t *testing.T) {
	if got := WithDefaults(testConfig(), true).BaseURL; got != testBaseURL {
		t.Errorf("sandbox baseUrl = %q, want %q", got, testBaseURL)
	}
	if got := WithDefaults(testConfig(), false).BaseURL; got != prodBaseURL {
		t.Errorf("production baseUrl = %q, want %q", got, prodBaseURL)
	}
}

// No e-POS operation is marked retryable: a charge, refund or reversal must never
// be sent twice, because a timeout may mean the bank already processed it.
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
	tr.fallback = &reply{body: "not json"}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorMessage, "Albaraka returned an unexpected response", "errorMessage")
}

func TestUnsupportedOperations(t *testing.T) {
	provider := newTestProvider(t, testConfig(), &transport{})
	ctx := context.Background()

	if _, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: *testCard()}); err == nil {
		t.Error("SaveCard() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.ListCards(ctx, &core.ListCardsRequest{}); err == nil {
		t.Error("ListCards() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.BinCheck(ctx, "540001"); err == nil {
		t.Error("BinCheck() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{}); err == nil {
		t.Error("InstallmentInfo() succeeded, want NOT_SUPPORTED")
	}
}

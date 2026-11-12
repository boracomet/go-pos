package kuveytturk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func decodeTransaction(t *testing.T, fields map[string]any) *Transaction {
	t.Helper()
	var tx Transaction
	if err := json.Unmarshal([]byte(jsonBody(fields)), &tx); err != nil {
		t.Fatalf("decode transaction: %v", err)
	}
	return &tx
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

func wantNumber(t *testing.T, got any, want float64, label string) {
	t.Helper()
	number, ok := got.(float64)
	if !ok || number != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func wantLookupHash(t *testing.T, body map[string]any) {
	t.Helper()
	hash, err := CreateGetTransactionsHash(testCredentials(), "ORDER3D")
	if err != nil {
		t.Fatalf("CreateGetTransactionsHash() error = %v", err)
	}
	wantString(t, body["hashData"], hash, "hashData")
}

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"merchantId", "customerId", "username", "password"} {
		cfg := testConfig()
		cfg.BaseURL = testBaseURL
		switch field {
		case "merchantId":
			cfg.MerchantID = ""
		case "customerId":
			cfg.CustomerID = ""
		case "username":
			cfg.Username = ""
		case "password":
			cfg.Password = ""
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

func TestCreatePaymentNotSupported(t *testing.T) {
	provider := newTestProvider(t, testConfig(), &transport{})
	_, err := provider.CreatePayment(context.Background(), testPaymentRequest())
	if err == nil {
		t.Fatal("CreatePayment() succeeded, want NOT_SUPPORTED")
	}
	var baseErr *core.BaseError
	if !errors.As(err, &baseErr) || baseErr.Code != "NOT_SUPPORTED" {
		t.Errorf("CreatePayment() error = %v, want code NOT_SUPPORTED", err)
	}
}

func TestInitThreeDSPayment(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{body: payment3DHTML}
	provider := newTestProvider(t, testConfig(), tr)

	request := testThreeDSRequest()
	request.ConversationID = "ORDER3D"
	request.Installment = 3
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}

	if tr.calls != 1 {
		t.Fatalf("calls = %d, want 1", tr.calls)
	}
	if tr.paths[0] != EndpointPayment {
		t.Errorf("path = %q, want %q", tr.paths[0], EndpointPayment)
	}
	if got := tr.headers[0].Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body := sentBody(t, tr, 0)
	wantNumber(t, body["language"], 1, "language")
	wantString(t, body["merchantOrderId"], "ORDER3D", "merchantOrderId")
	wantString(t, body["successUrl"], "https://example.com/callback", "successUrl")
	wantString(t, body["failUrl"], "https://example.com/callback", "failUrl")
	wantString(t, body["merchantId"], "12345", "merchantId")
	wantString(t, body["customerId"], "67890", "customerId")
	wantString(t, body["username"], "apiuser", "username")
	wantString(t, body["amount"], "120", "amount")
	wantString(t, body["currency"], "0949", "currency")
	wantNumber(t, body["installmentCount"], 3, "installmentCount")
	wantNumber(t, body["paymentType"], 1, "paymentType")
	for path, want := range map[string]string{
		"customer.fullName":       "John Doe",
		"customer.cc":             "90",
		"customer.subscriber":     "5350000000",
		"customer.email":          "john.doe@example.com",
		"customer.identityNumber": "74300864791",
		"customer.ipAddress":      "85.34.78.112",
		"card.cardHolderName":     "John Doe",
		"card.cardNumber":         "5528790000000008",
		"card.expireMonth":        "12",
		"card.expireYear":         "30",
		"card.securityCode":       "123",
	} {
		wantString(t, nested(t, body, path), want, path)
	}
	if _, exists := body["password"]; exists {
		t.Error("the request body carries the password")
	}
	hash, err := CreatePaymentHash(testCredentials(), "ORDER3D", "120", "https://example.com/callback", "https://example.com/callback")
	if err != nil {
		t.Fatalf("CreatePaymentHash() error = %v", err)
	}
	wantString(t, body["hashData"], hash, "hashData")

	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	if result.ThreeDSHTMLContent != payment3DHTML {
		t.Error("threeDSHtmlContent is not the bank page")
	}
	wantString(t, result.PaymentID, "ORDER3D", "paymentId")
}

func TestInitThreeDSPaymentFailURLAndPaymentType(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{body: payment3DHTML}
	paymentType := 2
	cfg := testConfig()
	cfg.PaymentType = &paymentType
	provider := newTestProvider(t, cfg, tr)

	if _, err := provider.InitThreeDSPaymentWithFailURL(context.Background(), testThreeDSRequest(), "https://example.com/fail"); err != nil {
		t.Fatalf("InitThreeDSPaymentWithFailURL() error = %v", err)
	}
	body := sentBody(t, tr, 0)
	wantString(t, body["failUrl"], "https://example.com/fail", "failUrl")
	wantNumber(t, body["paymentType"], 2, "paymentType")
}

func TestInitThreeDSPaymentRejectedForm(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{body: paymentRejectedHTML}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "HashDataError", "errorCode")
	wantString(t, result.ErrorMessage, "Şifrelenen veriler (Hashdata) uyuşmamaktadır.", "errorMessage")
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
	if result.ThreeDSHTMLContent != "" {
		t.Error("a rejected Payment must not carry 3D HTML")
	}
}

func TestInitThreeDSPaymentEmptyResponse(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{body: ""}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
}

func TestInitThreeDSPaymentRejectsAmpersandCallback(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	request := testThreeDSRequest()
	request.CallbackURL = "https://example.com/callback?a=1&b=2"
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
}

func TestInitThreeDSPaymentRequiresGSM(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	request := testThreeDSRequest()
	request.Buyer.GSMNumber = ""
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "gsmNumber") {
		t.Errorf("errorMessage = %q, want it to name gsmNumber", result.ErrorMessage)
	}
}

func TestInitThreeDSPaymentRejectsCurrency(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	request := testThreeDSRequest()
	request.Currency = "GBP"
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
}

func TestInitThreeDSPaymentTimeout(t *testing.T) {
	tr := &transport{}
	tr.fallback = &reply{err: context.DeadlineExceeded}
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}

func TestCompleteThreeDSPayment(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	tr.replyOnceWith(ktResponse(map[string]any{"ResponseCode": "00"}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 2 {
		t.Fatalf("calls = %d, want 2", tr.calls)
	}
	if tr.paths[0] != EndpointGetTransactions {
		t.Errorf("lookup path = %q, want %q", tr.paths[0], EndpointGetTransactions)
	}
	lookup := sentBody(t, tr, 0)
	wantString(t, lookup["merchantOrderId"], "ORDER3D", "merchantOrderId")
	wantString(t, lookup["merchantId"], "12345", "merchantId")
	wantLookupHash(t, lookup)

	if tr.paths[1] != EndpointProvision {
		t.Errorf("provision path = %q, want %q", tr.paths[1], EndpointProvision)
	}
	provision := sentBody(t, tr, 1)
	for key, want := range map[string]string{
		"merchantId":      "12345",
		"customerId":      "67890",
		"username":        "apiuser",
		"merchantOrderId": "ORDER3D",
		"orderId":         "334210503",
		"amount":          "120",
		"md":              "mdValue+/=",
	} {
		wantString(t, provision[key], want, key)
	}
	hash, err := CreateProvisionHash(testCredentials(), "ORDER3D", "120")
	if err != nil {
		t.Fatalf("CreateProvisionHash() error = %v", err)
	}
	wantString(t, provision["hashData"], hash, "hashData")

	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "ORDER3D", "paymentId")
	wantString(t, result.ConversationID, "ORDER3D", "conversationId")
}

func TestCompleteThreeDSPaymentUsesBankAmount(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"FirstAmount": 5000})))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	callback := threeDSCallback()
	callback["Amount"] = "1"
	if _, err := provider.CompleteThreeDSPayment(context.Background(), callback); err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	wantString(t, sentBody(t, tr, 1)["amount"], "5000", "amount")
}

func TestCompleteThreeDSPaymentVerificationFailed(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	callback := threeDSCallback()
	callback["Success"] = "False"
	callback["ResponseCode"] = "CardVerificationFailed"
	callback["ResponseMessage"] = "Kart doğrulanamadı."
	result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "CardVerificationFailed", "errorCode")
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentNoIDsFromFailedCallback(t *testing.T) {
	tr := &transport{}
	provider := newTestProvider(t, testConfig(), tr)

	callback := map[string]any{"Success": "False", "MerchantOrderId": "SOMEONE-ELSES-ORDER"}
	result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 0 {
		t.Errorf("calls = %d, want 0", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
	if !reflect.DeepEqual(result.RawResponse, callback) {
		t.Errorf("rawResponse = %v, want the callback", result.RawResponse)
	}
}

func TestCompleteThreeDSPaymentMissingFields(t *testing.T) {
	for _, field := range []string{"MD", "OrderId", "MerchantOrderId"} {
		tr := &transport{}
		provider := newTestProvider(t, testConfig(), tr)

		callback := threeDSCallback()
		delete(callback, field)
		result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() without %s error = %v", field, err)
		}
		if tr.calls != 0 {
			t.Errorf("without %s: calls = %d, want 0", field, tr.calls)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("without %s: status = %q, want failure", field, result.Status)
		}
		wantString(t, result.ErrorCode, "INVALID_HASH", "errorCode")
		wantNoIDs(t, result.PaymentID, result.ConversationID)
	}
}

func TestCompleteThreeDSPaymentOrderIDMismatch(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"OrderId": 999})))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "INVALID_HASH", "errorCode")
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentPicksCallbackOrder(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(
		ktTransaction(map[string]any{"OrderId": 111, "TransactionStatus": "Basarısız (2)", "FirstAmount": 999}),
		ktTransaction(map[string]any{"FirstAmount": 120}),
	))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	provision := sentBody(t, tr, 1)
	wantString(t, provision["orderId"], "334210503", "orderId")
	wantString(t, provision["amount"], "120", "amount")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestCompleteThreeDSPaymentUnknownOrder(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions())
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "INVALID_HASH", "errorCode")
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentNeverPicksAnotherOrder(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"MerchantOrderId": "OTHER"})))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentFailedProvision(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	tr.replyOnceWith(ktResponse(map[string]any{
		"Success":         false,
		"ResponseCode":    "51",
		"ResponseMessage": "Yetersiz bakiye",
	}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "51", "errorCode")
	if result.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentLookupServerError(t *testing.T) {
	tr := &transport{}
	tr.once = append(tr.once, reply{status: http.StatusInternalServerError, body: "{}"})
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("httpStatus = %d, want 500", result.HTTPStatus)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentLookupTimeout(t *testing.T) {
	tr := &transport{}
	tr.once = append(tr.once, timeoutReply())
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	wantNoIDs(t, result.PaymentID, result.ConversationID)
}

func TestCompleteThreeDSPaymentProvisionTimeout(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	tr.once = append(tr.once, timeoutReply())
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want pending", result.Status)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
	wantString(t, result.PaymentID, "ORDER3D", "paymentId")
	wantString(t, result.ConversationID, "ORDER3D", "conversationId")
}

func refundRequest(price string) *core.RefundRequest {
	return &core.RefundRequest{PaymentID: "ORDER3D", Price: price, Currency: "TRY", IP: "1.1.1.1"}
}

func TestRefundFullAmount(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"FirstAmount": 120})))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Refund(context.Background(), refundRequest("1.20"))
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if tr.paths[1] != EndpointSaleReversal {
		t.Errorf("path = %q, want %q", tr.paths[1], EndpointSaleReversal)
	}
	body := sentBody(t, tr, 1)
	for key, want := range map[string]string{
		"merchantOrderId":  "ORDER3D",
		"orderId":          "334210503",
		"saleReversalType": "Drawback",
		"amount":           "120",
	} {
		wantString(t, body[key], want, key)
	}
	hash, err := CreateSaleReversalHash(testCredentials(), "ORDER3D", SaleReversalDrawback, "120")
	if err != nil {
		t.Fatalf("CreateSaleReversalHash() error = %v", err)
	}
	wantString(t, body["hashData"], hash, "hashData")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestRefundPartialAmount(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"FirstAmount": 120})))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	if _, err := provider.Refund(context.Background(), refundRequest("0.50")); err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	body := sentBody(t, tr, 1)
	wantString(t, body["saleReversalType"], "PartialDrawback", "saleReversalType")
	wantString(t, body["amount"], "50", "amount")
	hash, err := CreateSaleReversalHash(testCredentials(), "ORDER3D", SaleReversalPartialDrawback, "50")
	if err != nil {
		t.Fatalf("CreateSaleReversalHash() error = %v", err)
	}
	wantString(t, body["hashData"], hash, "hashData")
}

func TestRefundAfterEarlierPartialRefund(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{"FirstAmount": 120, "DrawbackAmount": 50})))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	if _, err := provider.Refund(context.Background(), refundRequest("1.20")); err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	wantString(t, sentBody(t, tr, 1)["saleReversalType"], "PartialDrawback", "saleReversalType")
}

func TestRefundUnknownOrder(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions())
	provider := newTestProvider(t, testConfig(), tr)

	request := refundRequest("1")
	request.PaymentID = "NOPE"
	result, err := provider.Refund(context.Background(), request)
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if tr.calls != 1 {
		t.Errorf("calls = %d, want 1", tr.calls)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "OrderIdNotFound", "errorCode")
}

func TestRefundRejected(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	tr.replyOnceWith(ktResponse(map[string]any{
		"Success":         false,
		"ResponseCode":    "TechnicalException",
		"ResponseMessage": "Hata",
	}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Refund(context.Background(), refundRequest("1.20"))
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeProviderError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeProviderError)
	}
}

func TestCancel(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	tr.replyOnceWith(ktResponse(nil))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER3D", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	body := sentBody(t, tr, 1)
	wantString(t, body["saleReversalType"], "Cancel", "saleReversalType")
	wantString(t, body["orderId"], "334210503", "orderId")
	wantString(t, body["amount"], "120", "amount")
	hash, err := CreateSaleReversalHash(testCredentials(), "ORDER3D", SaleReversalCancel, "")
	if err != nil {
		t.Fatalf("CreateSaleReversalHash() error = %v", err)
	}
	wantString(t, body["hashData"], hash, "hashData")
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestGetPayment(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER3D")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	wantString(t, result.PaymentID, "ORDER3D", "paymentId")
}

func TestGetPaymentPrefersSuccessfulSale(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(
		ktTransaction(map[string]any{"OrderId": 111, "TransactionStatus": "Basarısız (2)"}),
		ktTransaction(nil),
	))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER3D")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
}

func TestGetPaymentBankError(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktTransactions(ktTransaction(map[string]any{
		"TransactionStatus": "Basarısız (2)",
		"ResponseCode":      "51",
		"ResponseExplain":   "Yetersiz bakiye",
	})))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER3D")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	if result.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
	}
}

func TestGetPaymentAPIError(t *testing.T) {
	tr := &transport{}
	tr.replyOnceWith(ktResponse(map[string]any{
		"Success":         false,
		"ResponseCode":    "HashDataError",
		"ResponseMessage": "Hash",
	}))
	provider := newTestProvider(t, testConfig(), tr)

	result, err := provider.GetPayment(context.Background(), "ORDER3D")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want failure", result.Status)
	}
	wantString(t, result.ErrorCode, "HashDataError", "errorCode")
}

func TestMapTransactionStatus(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		want   core.PaymentStatus
	}{
		{"no transaction", nil, core.StatusPending},
		{"successful sale", ktTransaction(nil), core.StatusSuccess},
		{"failed", ktTransaction(map[string]any{"TransactionStatus": "Basarısız (2)"}), core.StatusFailure},
		{"timed out", ktTransaction(map[string]any{"TransactionStatus": "Zaman Aşımı (3)"}), core.StatusFailure},
		{"cancelled", ktTransaction(map[string]any{"CancelAmount": 120}), core.StatusCancelled},
		{"refunded", ktTransaction(map[string]any{"DrawbackAmount": 120}), core.StatusCancelled},
		{"partly refunded", ktTransaction(map[string]any{"DrawbackAmount": 50}), core.StatusSuccess},
		{"unknown status", ktTransaction(map[string]any{"TransactionStatus": "Bilinmiyor"}), core.StatusPending},
	}
	for _, tc := range cases {
		var tx *Transaction
		if tc.fields != nil {
			tx = decodeTransaction(t, tc.fields)
		}
		if got := MapTransactionStatus(tx); got != tc.want {
			t.Errorf("%s: MapTransactionStatus() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	if got := WithDefaults(testConfig(), true).BaseURL; got != testBaseURL {
		t.Errorf("sandbox baseUrl = %q, want %q", got, testBaseURL)
	}
	if got := WithDefaults(testConfig(), false).BaseURL; got != prodBaseURL {
		t.Errorf("production baseUrl = %q, want %q", got, prodBaseURL)
	}
	cfg := testConfig()
	cfg.BaseURL = "https://custom.test"
	if got := WithDefaults(cfg, true).BaseURL; got != "https://custom.test" {
		t.Errorf("explicit baseUrl = %q, want it kept", got)
	}
}

// Only the read-only lookup is retried: Payment, Provision and SaleReversal must
// never be sent twice, because a timeout may mean the bank already processed them.
func TestRetriesOnlyQueries(t *testing.T) {
	cfg := testConfig()
	cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}

	t.Run("lookup is retried", func(t *testing.T) {
		tr := &transport{}
		tr.fallback = &reply{err: context.DeadlineExceeded}
		provider := newTestProvider(t, cfg, tr)
		result, err := provider.GetPayment(context.Background(), "ORDER3D")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if tr.calls != 3 {
			t.Errorf("calls = %d, want 3", tr.calls)
		}
		if result.Attempts != 3 {
			t.Errorf("attempts = %d, want 3", result.Attempts)
		}
	})

	t.Run("payment is not retried", func(t *testing.T) {
		tr := &transport{}
		tr.fallback = &reply{err: context.DeadlineExceeded}
		provider := newTestProvider(t, cfg, tr)
		if _, err := provider.InitThreeDSPayment(context.Background(), testThreeDSRequest()); err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if tr.calls != 1 {
			t.Errorf("calls = %d, want 1", tr.calls)
		}
	})

	t.Run("provision is not retried", func(t *testing.T) {
		tr := &transport{}
		tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
		tr.fallback = &reply{err: context.DeadlineExceeded}
		provider := newTestProvider(t, cfg, tr)
		if _, err := provider.CompleteThreeDSPayment(context.Background(), threeDSCallback()); err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if tr.calls != 2 {
			t.Errorf("calls = %d, want 2 (lookup once, provision once)", tr.calls)
		}
	})

	t.Run("sale reversal is not retried", func(t *testing.T) {
		tr := &transport{}
		tr.replyOnceWith(ktTransactions(ktTransaction(nil)))
		tr.fallback = &reply{err: context.DeadlineExceeded}
		provider := newTestProvider(t, cfg, tr)
		if _, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER3D", IP: "1.1.1.1"}); err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if tr.calls != 2 {
			t.Errorf("calls = %d, want 2 (lookup once, reversal once)", tr.calls)
		}
	})
}

func TestOptionalOperationsAreNotSupported(t *testing.T) {
	provider := newTestProvider(t, testConfig(), &transport{})
	ctx := context.Background()

	if _, err := provider.Authorize(ctx, testPaymentRequest()); err == nil {
		t.Error("Authorize() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDER3D", Amount: "1"}); err == nil {
		t.Error("Capture() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: *testCard()}); err == nil {
		t.Error("SaveCard() succeeded, want NOT_SUPPORTED")
	}
	if _, err := provider.BinCheck(ctx, "552879"); err == nil {
		t.Error("BinCheck() succeeded, want NOT_SUPPORTED")
	}
}

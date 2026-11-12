package akbank

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func expectString(t *testing.T, object map[string]any, key, want string) {
	t.Helper()
	if got, _ := object[key].(string); got != want {
		t.Errorf("%s = %v, want %q", key, object[key], want)
	}
}

func expectNumber(t *testing.T, object map[string]any, key string, want float64) {
	t.Helper()
	if got, _ := object[key].(float64); got != want {
		t.Errorf("%s = %v, want %v", key, object[key], want)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"merchantSafeId", "terminalSafeId", "secretKey"} {
		t.Run(field, func(t *testing.T) {
			cfg := testConfig()
			switch field {
			case "merchantSafeId":
				cfg.MerchantSafeID = ""
			case "terminalSafeId":
				cfg.TerminalSafeID = ""
			case "secretKey":
				cfg.SecretKey = ""
			}
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("New() error = %v, want a %s error", err, field)
			}
			var configErr *core.ConfigurationError
			if !errors.As(err, &configErr) {
				t.Errorf("New() error = %v, want a configuration error", err)
			}
		})
	}

	t.Run("requires baseUrl", func(t *testing.T) {
		cfg := testConfig()
		cfg.BaseURL = ""
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "baseUrl") {
			t.Fatalf("New() error = %v, want a baseUrl error", err)
		}
	})
}

func TestCreatePayment(t *testing.T) {
	ctx := context.Background()

	t.Run("sends a signed txnCode 1000 request", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000","order":{"orderId":"ORDER1"}}`

		request := testPaymentRequest()
		request.ConversationID = "ORDER1"
		result, err := provider.CreatePayment(ctx, request)
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}

		if len(doer.calls) != 1 {
			t.Fatalf("requests = %d, want 1", len(doer.calls))
		}
		call := doer.calls[0]
		if call.URL != testConfig().BaseURL+EndpointProcess {
			t.Errorf("url = %q", call.URL)
		}
		if call.Method != "POST" {
			t.Errorf("method = %q, want POST", call.Method)
		}
		if got := call.Headers.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		wantHash, err := Sign(call.Body, testConfig().SecretKey)
		if err != nil {
			t.Fatalf("Sign() error = %v", err)
		}
		if got := call.Headers.Get("auth-hash"); got != wantHash {
			t.Errorf("auth-hash = %q, want the signature of the body", got)
		}

		body := sentBody(t, doer, 0)
		cfg := testConfig()
		expectString(t, body, "version", "1.00")
		expectString(t, body, "txnCode", "1000")
		expectString(t, objectAt(t, body, "terminal"), "merchantSafeId", cfg.MerchantSafeID)
		expectString(t, objectAt(t, body, "terminal"), "terminalSafeId", cfg.TerminalSafeID)
		card := objectAt(t, body, "card")
		expectString(t, card, "cardNumber", "5528790000000008")
		expectString(t, card, "cvv2", "123")
		expectString(t, card, "expireDate", "1230")
		transaction := objectAt(t, body, "transaction")
		expectString(t, transaction, "amount", "1.20")
		expectNumber(t, transaction, "currencyCode", 949)
		expectNumber(t, transaction, "motoInd", 0)
		expectNumber(t, transaction, "installCount", 1)
		expectString(t, objectAt(t, body, "customer"), "ipAddress", "85.34.78.112")
		expectString(t, objectAt(t, body, "order"), "orderId", "ORDER1")
		if _, ok := body["subMerchant"]; ok {
			t.Error("subMerchant must be absent without a subMerchantId")
		}

		randomNumber, _ := body["randomNumber"].(string)
		if !regexp.MustCompile(`^[0-9A-F]{128}$`).MatchString(randomNumber) {
			t.Errorf("randomNumber = %q", randomNumber)
		}
		dateTime, _ := body["requestDateTime"].(string)
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.000$`).MatchString(dateTime) {
			t.Errorf("requestDateTime = %q", dateTime)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDER1" {
			t.Errorf("paymentId = %q, want ORDER1", result.PaymentID)
		}
	})

	t.Run("maps declines", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-1073","responseMessage":"Red","hostMessage":"YETERSIZ BAKIYE"}`
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "VPS-1073" {
			t.Errorf("errorCode = %q, want VPS-1073", result.ErrorCode)
		}
		if result.ErrorMessage != "YETERSIZ BAKIYE" {
			t.Errorf("errorMessage = %q", result.ErrorMessage)
		}
	})

	t.Run("prefers the ISO 8583 host response code", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-1073","hostResponseCode":"51","hostMessage":"YETERSIZ BAKIYE"}`
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Code != core.CodeInsufficientFunds {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
		}
	})

	t.Run("reports timeouts as PENDING", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.err = context.DeadlineExceeded
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		if result.Code != core.CodeNetworkError {
			t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
		}
	})

	t.Run("validates the card and the buyer ip", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		request := testPaymentRequest()
		request.PaymentCard = nil
		result, err := provider.CreatePayment(ctx, request)
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure || result.ErrorCode != "VALIDATION_ERROR" {
			t.Errorf("status/errorCode = %q/%q", result.Status, result.ErrorCode)
		}
	})

	t.Run("sends subMerchant when configured", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.SubMerchantID = "SUB1" })
		doer.response = `{"responseCode":"VPS-0000"}`
		if _, err := provider.CreatePayment(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		expectString(t, objectAt(t, sentBody(t, doer, 0), "subMerchant"), "subMerchantId", "SUB1")
	})
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	provider, doer := newTestProvider(t, nil)
	doer.response = `{"responseCode":"VPS-0000"}`
	request := testPaymentRequest()
	request.ConversationID = "ORDERAUTH"
	result, err := provider.Authorize(ctx, request)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	expectString(t, sentBody(t, doer, 0), "txnCode", "1004")
	if result.Status != core.StatusSuccess || result.PaymentID != "ORDERAUTH" {
		t.Errorf("status/paymentId = %q/%q", result.Status, result.PaymentID)
	}
}

func TestInitThreeDSPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("returns a signed 3D_PAY form to the test gateway", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.ConversationID = "ORDER3D"
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		html := result.ThreeDSHTMLContent
		if !strings.Contains(html, `action="`+Gateway3DTest+`"`) {
			t.Errorf("form action missing: %q", html)
		}
		fields := formFieldsOf(html)
		cfg := testConfig()
		expectFields(t, fields, map[string]string{
			"paymentModel":   "3D_PAY",
			"txnCode":        "3000",
			"merchantSafeId": cfg.MerchantSafeID,
			"terminalSafeId": cfg.TerminalSafeID,
			"orderId":        "ORDER3D",
			"lang":           "TR",
			"amount":         "1.20",
			"currencyCode":   "949",
			"installCount":   "1",
			"okUrl":          "https://example.com/callback",
			"failUrl":        "https://example.com/callback",
			"emailAddress":   "john.doe@example.com",
			"creditCard":     "5528790000000008",
			"expiredDate":    "1230",
			"cvv":            "123",
		})
		if !regexp.MustCompile(`^[0-9A-F]{128}$`).MatchString(fields["randomNumber"]) {
			t.Errorf("randomNumber = %q", fields["randomNumber"])
		}
		want, err := Create3DFormHash(fields, cfg.SecretKey)
		if err != nil {
			t.Fatalf("Create3DFormHash() error = %v", err)
		}
		if fields["hash"] != want {
			t.Errorf("hash = %q, want the signature of the form fields", fields["hash"])
		}
		if result.PaymentID != "ORDER3D" {
			t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
		}
	})

	t.Run("uses the production gateway when testMode is off", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) {
			cfg.TestMode = false
			cfg.BaseURL = "https://api.akbank.com/api/v1/payment/virtualpos"
		})
		result, err := provider.InitThreeDSPayment(ctx, testThreeDSRequest())
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if !strings.Contains(result.ThreeDSHTMLContent, Gateway3DProduction) {
			t.Error("the production gateway must be used outside test mode")
		}
	})

	t.Run("uses an explicit gateway3dUrl", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) { cfg.Gateway3DURL = "https://gateway.example/securepay" })
		result, err := provider.InitThreeDSPayment(ctx, testThreeDSRequest())
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if !strings.Contains(result.ThreeDSHTMLContent, `action="https://gateway.example/securepay"`) {
			t.Error("gateway3dUrl must override the default gateway")
		}
	})

	t.Run("signs a pre-authorization form with txnCode 3004", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		result, err := provider.InitThreeDSAuthorize(ctx, testThreeDSRequest())
		if err != nil {
			t.Fatalf("InitThreeDSAuthorize() error = %v", err)
		}
		expectNoCalls(t, doer)
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		if fields["txnCode"] != "3004" || fields["paymentModel"] != "3D_PAY" {
			t.Errorf("txnCode/paymentModel = %q/%q", fields["txnCode"], fields["paymentModel"])
		}
	})

	t.Run("uses failUrl and subMerchantId when given", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) { cfg.SubMerchantID = "SUB1" })
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, testThreeDSRequest(),
			InitOptions{FailURL: "https://example.com/fail"})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		if fields["failUrl"] != "https://example.com/fail" {
			t.Errorf("failUrl = %q", fields["failUrl"])
		}
		if fields["subMerchantId"] != "SUB1" {
			t.Errorf("subMerchantId = %q", fields["subMerchantId"])
		}
	})

	t.Run("requires a callback url", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.CallbackURL = ""
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "VALIDATION_ERROR" {
			t.Errorf("errorCode = %q, want VALIDATION_ERROR", result.ErrorCode)
		}
	})
}

func TestCompleteThreeDSPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts a correctly signed successful callback confirmed by the order history", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = historyJSON([]map[string]any{approvedSale()})
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "2024041811DA" {
			t.Errorf("paymentId = %q, want 2024041811DA", result.PaymentID)
		}
		body := sentBody(t, doer, 0)
		expectString(t, body, "txnCode", "1010")
		expectString(t, objectAt(t, body, "order"), "orderId", "2024041811DA")
	})

	t.Run("rejects a signed success callback that Akbank has no approved payment for", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		failed := approvedSale()
		failed["responseCode"], failed["txnStatus"] = "VPS-1005", "S"
		doer.response = historyJSON([]map[string]any{failed})
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		if result.Code != core.CodeInvalidHash {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
		}
	})

	t.Run("rejects a signed callback whose fields were re-split to name another order", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = historyJSON([]map[string]any{})
		result, err := provider.CompleteThreeDSPayment(ctx, resplitCallback(t))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectString(t, objectAt(t, sentBody(t, doer, 0), "order"), "orderId", "2024")
	})

	t.Run("rejects a signed decline when Akbank reports the order as paid", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		decline := test3DPayCallback()
		decline["responseCode"] = "VPS-1005"
		doer.response = historyJSON([]map[string]any{approvedSale()})
		result, err := provider.CompleteThreeDSPayment(ctx, signedCallback(t, decline))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
	})

	t.Run("returns a confirmed decline as a failure", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		decline := test3DPayCallback()
		decline["responseCode"], decline["hostMessage"] = "VPS-1005", "RED"
		failed := approvedSale()
		failed["responseCode"], failed["txnStatus"] = "VPS-1005", "S"
		doer.response = historyJSON([]map[string]any{failed})

		result, err := provider.CompleteThreeDSPayment(ctx, signedCallback(t, decline))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "VPS-1005" {
			t.Errorf("errorCode = %q, want VPS-1005", result.ErrorCode)
		}
		if result.ErrorMessage != "RED" {
			t.Errorf("errorMessage = %q, want RED", result.ErrorMessage)
		}
	})

	t.Run("is pending when the order history query gets no response", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.err = context.DeadlineExceeded
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		if result.ErrorCode != "NETWORK_ERROR" {
			t.Errorf("errorCode = %q, want NETWORK_ERROR", result.ErrorCode)
		}
	})

	t.Run("rejects an unsigned/forged callback without calling Akbank", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		forged := test3DPayCallback()
		forged["hash"] = "forged"
		result, err := provider.CompleteThreeDSPayment(ctx, forged)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
	})

	t.Run("does not default to success when responseCode is missing", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		rest := test3DPayCallback()
		delete(rest, "responseCode")
		result, err := provider.CompleteThreeDSPayment(ctx, rest)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
	})

	t.Run("rejects callbacks for another terminal", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.TerminalSafeID = "OTHER" })
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "TERMINAL_MISMATCH" {
			t.Errorf("errorCode = %q, want TERMINAL_MISMATCH", result.ErrorCode)
		}
		if result.Code != core.CodeInvalidHash {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
		}
	})
}

func TestRefundCancelCapture(t *testing.T) {
	ctx := context.Background()

	t.Run("refund sends txnCode 1002 with amount", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000","transaction":{"rrn":"R1"}}`
		result, err := provider.Refund(ctx, testRefundRequest())
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		body := sentBody(t, doer, 0)
		expectString(t, body, "txnCode", "1002")
		transaction := objectAt(t, body, "transaction")
		expectString(t, transaction, "amount", "5.00")
		expectNumber(t, transaction, "currencyCode", 949)
		expectString(t, objectAt(t, body, "order"), "orderId", "ORDER1")
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.RefundID != "R1" {
			t.Errorf("refundId = %q, want R1", result.RefundID)
		}
	})

	t.Run("refund falls back to the auth code", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000","transaction":{"authCode":"A1"}}`
		result, err := provider.Refund(ctx, testRefundRequest())
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		if result.RefundID != "A1" {
			t.Errorf("refundId = %q, want A1", result.RefundID)
		}
	})

	t.Run("cancel sends txnCode 1003", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000","transaction":{"rrn":"R2"}}`
		result, err := provider.Cancel(ctx, testCancelRequest())
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		body := sentBody(t, doer, 0)
		expectString(t, body, "txnCode", "1003")
		expectString(t, objectAt(t, body, "order"), "orderId", "ORDER1")
		if _, ok := body["transaction"]; ok {
			t.Error("a void must not carry a transaction amount")
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.TransactionID != "R2" {
			t.Errorf("transactionId = %q, want R2", result.TransactionID)
		}
	})

	t.Run("capture sends txnCode 1005 with the amount and ip", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000"}`
		result, err := provider.Capture(ctx, &core.CaptureRequest{
			PaymentID: "ORDERAUTH", Amount: "1.20", Currency: "TRY", IP: "1.1.1.1",
		})
		if err != nil {
			t.Fatalf("Capture() error = %v", err)
		}
		body := sentBody(t, doer, 0)
		expectString(t, body, "txnCode", "1005")
		expectString(t, objectAt(t, body, "order"), "orderId", "ORDERAUTH")
		expectString(t, objectAt(t, body, "transaction"), "amount", "1.20")
		expectString(t, objectAt(t, body, "customer"), "ipAddress", "1.1.1.1")
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDERAUTH" || result.ConversationID != "ORDERAUTH" {
			t.Errorf("ids = %q/%q", result.PaymentID, result.ConversationID)
		}
	})

	t.Run("capture validates the amount", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		result, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDERAUTH", IP: "1.1.1.1"})
		if err != nil {
			t.Fatalf("Capture() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "VALIDATION_ERROR" {
			t.Errorf("errorCode = %q, want VALIDATION_ERROR", result.ErrorCode)
		}
	})

	t.Run("voidAuthorization voids like a cancel", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-0000"}`
		result, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{
			PaymentID: "ORDERAUTH", IP: "1.1.1.1", ConversationID: "C1",
		})
		if err != nil {
			t.Fatalf("VoidAuthorization() error = %v", err)
		}
		expectString(t, sentBody(t, doer, 0), "txnCode", "1003")
		if result.Status != core.StatusSuccess || result.ConversationID != "C1" {
			t.Errorf("status/conversationId = %q/%q", result.Status, result.ConversationID)
		}
	})
}

func TestGetPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("uses order history (1010)", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = historyJSON([]map[string]any{
			{"txnCode": "1000", "responseCode": "VPS-0000", "txnStatus": "V", "orderId": "ORDER1"},
		})
		result, err := provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		body := sentBody(t, doer, 0)
		expectString(t, body, "txnCode", "1010")
		expectString(t, objectAt(t, body, "order"), "orderId", "ORDER1")
		if result.Status != core.StatusCancelled {
			t.Errorf("status = %q, want cancelled", result.Status)
		}
	})

	t.Run("prefers the sale entry of the history", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = historyJSON([]map[string]any{
			{"txnCode": "1002", "responseCode": "VPS-0000", "txnStatus": "N"},
			{"txnCode": "3000", "responseCode": "VPS-0000", "txnStatus": "N"},
		})
		result, err := provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
	})

	t.Run("maps a rejected query", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = `{"responseCode":"VPS-1005","responseMessage":"Red","hostMessage":"ISLEM YOK"}`
		result, err := provider.GetPayment(ctx, "NOPE")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "VPS-1005" || result.ErrorMessage != "ISLEM YOK" {
			t.Errorf("errorCode/errorMessage = %q/%q", result.ErrorCode, result.ErrorMessage)
		}
	})

	t.Run("is pending without a history entry", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = historyJSON([]map[string]any{})
		result, err := provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
	})
}

func TestMapTxnStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		tx   TxnDetail
		want core.PaymentStatus
	}{
		{"completed", TxnDetail{"responseCode": "VPS-0000", "txnStatus": "N"}, core.StatusSuccess},
		{"refunded", TxnDetail{"responseCode": "VPS-0000", "txnStatus": "R"}, core.StatusCancelled},
		{"voided", TxnDetail{"responseCode": "VPS-0000", "txnStatus": "V"}, core.StatusCancelled},
		{"failed", TxnDetail{"responseCode": "VPS-1005"}, core.StatusFailure},
		{"failed status", TxnDetail{"responseCode": "VPS-0000", "txnStatus": "S"}, core.StatusFailure},
		{"approved without status", TxnDetail{"responseCode": "VPS-0000"}, core.StatusSuccess},
		{"unknown", TxnDetail{}, core.StatusPending},
		{"missing", nil, core.StatusPending},
	} {
		if got := MapTxnStatus(tc.tx); got != tc.want {
			t.Errorf("%s: MapTxnStatus() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnsupportedQueries(t *testing.T) {
	ctx := context.Background()
	provider, doer := newTestProvider(t, nil)

	if _, err := provider.BinCheck(ctx, "415956"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("BinCheck() error = %v", err)
	}
	_, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "415956", Price: "1"})
	if err == nil {
		t.Fatal("InstallmentInfo() error = nil, want NOT_SUPPORTED")
	}
	var baseErr *core.BaseError
	if !errors.As(err, &baseErr) || baseErr.Code != "NOT_SUPPORTED" {
		t.Errorf("InstallmentInfo() error = %v, want code NOT_SUPPORTED", err)
	}
	if !strings.Contains(err.Error(), "no installment-rate query") {
		t.Errorf("InstallmentInfo() message = %q", err.Error())
	}
	if _, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: *testCard()}); err == nil {
		t.Errorf("SaveCard() error = %v", err)
	}
	if _, err := provider.ListCards(ctx, &core.ListCardsRequest{}); err == nil {
		t.Errorf("ListCards() error = %v", err)
	}
	if _, err := provider.DeleteCard(ctx, &core.DeleteCardRequest{CardToken: "T1"}); err == nil {
		t.Errorf("DeleteCard() error = %v", err)
	}
	expectNoCalls(t, doer)
}

func TestRetriesOnlyQueries(t *testing.T) {
	ctx := context.Background()
	retry := func(cfg *Config) {
		cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	}

	t.Run("never retries a payment, refund, cancel or capture", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.CreatePayment(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if _, err := provider.Authorize(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("Authorize() error = %v", err)
		}
		if _, err := provider.Refund(ctx, testRefundRequest()); err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		if _, err := provider.Cancel(ctx, testCancelRequest()); err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if _, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDER1", Amount: "1.20", IP: "1.1.1.1"}); err != nil {
			t.Fatalf("Capture() error = %v", err)
		}
		if len(doer.calls) != 5 {
			t.Errorf("requests = %d, want 5 (one per operation)", len(doer.calls))
		}
	})

	t.Run("retries the order history query", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.GetPayment(ctx, "ORDER1"); err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3", len(doer.calls))
		}
	})

	t.Run("retries the 3D confirmation query", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback()); err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3", len(doer.calls))
		}
	})
}

func TestProviderSatisfiesContract(t *testing.T) {
	provider, _ := newTestProvider(t, nil)
	var _ core.Provider = provider
	if provider.Name() != "akbank" {
		t.Errorf("Name() = %q, want akbank", provider.Name())
	}
}

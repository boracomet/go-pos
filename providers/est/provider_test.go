package est

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"clientId", "username", "password", "storeKey"} {
		t.Run(field, func(t *testing.T) {
			cfg := testConfig()
			switch field {
			case "clientId":
				cfg.ClientID = ""
			case "username":
				cfg.Username = ""
			case "password":
				cfg.Password = ""
			case "storeKey":
				cfg.StoreKey = ""
			}
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("New() error = %v, want it to name %s", err, field)
			}
		})
	}
}

func TestNewRejectsUnknownStoreType(t *testing.T) {
	cfg := testConfig()
	cfg.StoreType = "x"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "storeType") {
		t.Fatalf("New() error = %v, want a storeType error", err)
	}
}

func TestCreatePayment(t *testing.T) {
	ctx := context.Background()

	t.Run("sends a non-3D Auth to the XML API", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		request := testPaymentRequest()
		request.ConversationID = "ORDER1"

		result, err := provider.CreatePayment(ctx, request)
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if len(doer.calls) != 1 {
			t.Fatalf("expected 1 request, got %d", len(doer.calls))
		}
		call := doer.calls[0]
		if call.Method != "POST" || call.URL != SandboxURL+EndpointAPI {
			t.Errorf("request = %s %s", call.Method, call.URL)
		}
		if call.Headers.Get("Content-Type") != "text/xml; charset=ISO-8859-9" {
			t.Errorf("Content-Type = %q", call.Headers.Get("Content-Type"))
		}
		if !strings.HasPrefix(call.Body, `<?xml version="1.0" encoding="ISO-8859-9"?><CC5Request>`) {
			t.Errorf("body = %q", call.Body)
		}
		sent := sentFields(t, doer, 0)
		expectFields(t, sent.Fields, map[string]string{
			"Name": "apiuser", "Password": "apipass", "ClientId": "100100000",
			"Type": "Auth", "IPAddress": "85.34.78.112", "OrderId": "ORDER1",
			"Total": "1.20", "Currency": "949", "Taksit": "",
			"Number": "5528790000000008", "Expires": "12/30", "Cvv2Val": "123", "Mode": "P",
		})
		expectFields(t, sent.Extra, map[string]string{"CARDHOLDERNAME": "John Doe"})
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDER1" {
			t.Errorf("paymentId = %q, want ORDER1", result.PaymentID)
		}
	})

	t.Run("sends the department when configured", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) {
			department := 3
			cfg.Department = &department
		})
		doer.response = testApprovedXML
		if _, err := provider.CreatePayment(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Extra, map[string]string{"BOLUM": "3"})
	})

	t.Run("maps a decline", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estXML([]xmlPair{
			{"Response", "Declined"}, {"ProcReturnCode", "51"}, {"ErrMsg", "Yetersiz bakiye"},
		}, nil)
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || result.ErrorCode != "51" || result.ErrorMessage != "Yetersiz bakiye" {
			t.Errorf("result = %+v", result)
		}
		if result.Code != core.CodeInsufficientFunds {
			t.Errorf("code = %q, want INSUFFICIENT_FUNDS", result.Code)
		}
	})

	t.Run("returns pending on a timeout", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.err = context.DeadlineExceeded
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Status != core.StatusPending || result.Code != core.CodeNetworkError {
			t.Errorf("result = %+v", result)
		}
	})
}

func TestInitThreeDSPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("returns a ver3-signed form to the 3D gate", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.ConversationID = "ORDER3D"
		request.Installment = 3

		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusPending {
			t.Fatalf("status = %q, want pending", result.Status)
		}
		if !strings.Contains(result.ThreeDSHTMLContent, `action="`+SandboxURL+EndpointGate+`"`) {
			t.Errorf("html = %q", result.ThreeDSHTMLContent)
		}
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		expectFields(t, fields, map[string]string{
			"clientid": "100100000", "storetype": "3d", "amount": "1.20", "oid": "ORDER3D",
			"okUrl": "https://example.com/callback", "failUrl": "https://example.com/callback",
			"TranType": "Auth", "currency": "949", "taksit": "3", "lang": "tr", "hashAlgorithm": "ver3",
			"pan": "5528790000000008", "Ecom_Payment_Card_ExpDate_Month": "12",
			"Ecom_Payment_Card_ExpDate_Year": "30", "cv2": "123", "cardType": "mastercard",
			"cardHolderName": "John Doe",
		})
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(fields["rnd"]) {
			t.Errorf("rnd = %q, want 32 hex characters", fields["rnd"])
		}
		hash := fields["hash"]
		delete(fields, "hash")
		expected, err := CreateHash(fields, testConfig().StoreKey)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		if hash != expected {
			t.Errorf("hash = %q, want %q", hash, expected)
		}
		if result.PaymentID != "ORDER3D" {
			t.Errorf("paymentId = %q", result.PaymentID)
		}
	})

	t.Run("sends no card data with a hosted store type", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DHost })
		request := testThreeDSRequest()
		request.PaymentCard = nil
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Fatalf("status = %q, want pending", result.Status)
		}
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		if fields["storetype"] != "3d_host" {
			t.Errorf("storetype = %q", fields["storetype"])
		}
		if _, ok := fields["pan"]; ok {
			t.Error("hosted form carries pan")
		}
		if _, ok := fields["cv2"]; ok {
			t.Error("hosted form carries cv2")
		}
	})

	t.Run("uses failUrl when given", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, testThreeDSRequest(),
			InitOptions{FailURL: "https://example.com/fail"})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if got := formFieldsOf(result.ThreeDSHTMLContent)["failUrl"]; got != "https://example.com/fail" {
			t.Errorf("failUrl = %q", got)
		}
	})

	t.Run("sends Macedonian denars as 807 (Halkbank Macedonia)", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.Currency = "MKD"
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if got := formFieldsOf(result.ThreeDSHTMLContent)["currency"]; got != "807" {
			t.Errorf("currency = %q, want 807", got)
		}
	})

	t.Run("rejects an unsupported currency", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.Currency = "JPY"
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || result.Code != core.CodeInvalidRequest {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("requires a callback url", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		request := testThreeDSRequest()
		request.CallbackURL = ""
		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || !strings.Contains(result.ErrorMessage, "callbackUrl") {
			t.Errorf("result = %+v", result)
		}
	})
}

func TestCompleteThreeDSPayment3D(t *testing.T) {
	ctx := context.Background()

	t.Run("charges a verified callback with XML Auth", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		result, err := provider.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Fields, map[string]string{
			"Type": "Auth", "OrderId": "ORDER3D", "Total": "1.20", "Currency": "949",
			"Number": "MDVALUE123", "PayerTxnId": "XIDVALUE=", "PayerSecurityLevel": "02",
			"PayerAuthenticationCode": "CAVVVALUE=", "Mode": "P",
		})
		if result.Status != core.StatusSuccess || result.PaymentID != "ORDER3D" || result.ConversationID != "ORDER3D" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("rejects a callback with a bad hash as INVALID_HASH", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["amount"] = "0.01"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" || result.Code != core.CodeInvalidHash {
			t.Errorf("result = %+v", result)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a signed callback of another store", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["clientid"] = "999"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("fails without ids when the card was not authenticated", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["mdStatus"] = "0"
		callback["mdErrorMsg"] = "Not authenticated"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure || result.Code != core.CodeThreeDSFailed {
			t.Errorf("result = %+v", result)
		}
		if result.ErrorMessage != "Not authenticated" {
			t.Errorf("errorMessage = %q", result.ErrorMessage)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("fails without ids when the bank declines the Auth", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estXML([]xmlPair{
			{"Response", "Declined"}, {"ProcReturnCode", "05"}, {"ErrMsg", "Red"},
		}, nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || result.ErrorCode != "05" {
			t.Errorf("result = %+v", result)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("fails when the bank answers for another order", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estXML([]xmlPair{
			{"OrderId", "OTHER"}, {"Response", "Approved"}, {"ProcReturnCode", "00"},
		}, nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q", result.Status)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("returns pending with the order id when the Auth times out", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.err = context.DeadlineExceeded
		result, err := provider.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending || result.PaymentID != "ORDER3D" {
			t.Errorf("result = %+v", result)
		}
	})
}

func TestCompleteThreeDSPaymentMdStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects half-secure mdStatus values by default", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["mdStatus"] = "2"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "MD_STATUS_2" {
			t.Errorf("errorCode = %q", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("charges the mdStatus values listed in AcceptedMdStatuses", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) {
			cfg.AcceptedMdStatuses = []string{"1", "2", "3", "4"}
		})
		doer.response = testApprovedXML
		callback := test3DCallback()
		callback["mdStatus"] = "4"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Fields, map[string]string{"Type": "Auth", "Number": "MDVALUE123"})
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q", result.Status)
		}
	})
}

func TestCompleteThreeDSPayment3DHost(t *testing.T) {
	ctx := context.Background()

	// The hosted page only authenticates the card: the callback has md but no
	// ProcReturnCode, and the merchant charges with XML Auth like 3d.
	hostCallback := func(t *testing.T) map[string]string {
		callback := test3DCallback()
		callback["storetype"] = "3d_host"
		delete(callback, "Response")
		delete(callback, "ProcReturnCode")
		return signed(t, callback)
	}

	t.Run("charges the authenticated card with XML Auth", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DHost })
		doer.response = testApprovedXML
		result, err := provider.CompleteThreeDSPayment(ctx, hostCallback(t))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Fields, map[string]string{
			"Type": "Auth", "OrderId": "ORDER3D", "Number": "MDVALUE123",
			"PayerTxnId": "XIDVALUE=", "PayerAuthenticationCode": "CAVVVALUE=",
		})
		if result.Status != core.StatusSuccess || result.PaymentID != "ORDER3D" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("fails without ids when the card was not authenticated", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DHost })
		callback := hostCallback(t)
		callback["mdStatus"] = "0"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q", result.Status)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})
}

func TestCompleteThreeDSPayment3DPay(t *testing.T) {
	ctx := context.Background()
	payProvider := func(t *testing.T) (*Provider, *stubDoer) {
		return newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DPay })
	}

	t.Run("confirms an approved callback with an order status query", func(t *testing.T) {
		provider, doer := payProvider(t)
		doer.response = estOrderStatusXML(nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		sent := sentFields(t, doer, 0)
		expectFields(t, sent.Fields, map[string]string{"OrderId": "ORDERPAY", "Mode": "P"})
		expectFields(t, sent.Extra, map[string]string{"ORDERSTATUS": "SOR"})
		if result.Status != core.StatusSuccess || result.PaymentID != "ORDERPAY" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("rejects an approved callback the bank does not confirm", func(t *testing.T) {
		provider, doer := payProvider(t)
		doer.response = estXML([]xmlPair{
			{"Response", "Declined"}, {"ProcReturnCode", "99"}, {"ErrMsg", "No record found for ORDERPAY"},
		}, nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("fails without ids on a declined callback, without calling the bank", func(t *testing.T) {
		provider, doer := payProvider(t)
		callback := test3DPayCallback()
		callback["Response"] = "Declined"
		callback["ProcReturnCode"] = "51"
		callback["ErrMsg"] = "Red"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure || result.Code != core.CodeInsufficientFunds {
			t.Errorf("result = %+v", result)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("returns pending with the order id when the query times out", func(t *testing.T) {
		provider, doer := payProvider(t)
		doer.err = context.DeadlineExceeded
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending || result.PaymentID != "ORDERPAY" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("treats 3d_pay_hosting the same way", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DPayHosting })
		doer.response = estOrderStatusXML(nil)
		callback := test3DPayCallback()
		callback["storetype"] = "3d_pay_hosting"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Extra, map[string]string{"ORDERSTATUS": "SOR"})
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q", result.Status)
		}
	})
}

func TestGirogate(t *testing.T) {
	ctx := context.Background()
	payProvider := func(t *testing.T) (*Provider, *stubDoer) {
		return newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3DPay })
	}
	withoutCard := func() *core.ThreeDSPaymentRequest {
		request := testThreeDSRequest()
		request.PaymentCard = nil
		return request
	}
	expectSignedForm := func(t *testing.T, html string) map[string]string {
		t.Helper()
		fields := formFieldsOf(html)
		hash := fields["hash"]
		delete(fields, "hash")
		expected, err := CreateHash(fields, testConfig().StoreKey)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		if hash != expected {
			t.Errorf("hash = %q, want %q", hash, expected)
		}
		return fields
	}

	t.Run("sends the method and country instead of card data, covered by the hash", func(t *testing.T) {
		provider, _ := payProvider(t)
		request := withoutCard()
		request.Currency = "EUR"
		request.ConversationID = "ORDERGG"
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, request, InitOptions{
			Girogate: &GirogateRequest{Method: GirogateIDEAL, Country: "nl"},
		})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Fatalf("status = %q", result.Status)
		}
		fields := expectSignedForm(t, result.ThreeDSHTMLContent)
		expectFields(t, fields, map[string]string{
			"storetype": "3d_pay", "paymentType": "GIROGATE_IDEAL", "ccode": "NL",
			"currency": "978", "oid": "ORDERGG",
		})
		for _, name := range []string{"pan", "cv2", "girogateemail"} {
			if _, ok := fields[name]; ok {
				t.Errorf("form carries %s", name)
			}
		}
	})

	for _, tc := range []struct {
		method GirogateMethod
		want   map[string]string
	}{
		{GirogateP24, map[string]string{"girogateemail": "john@example.com"}},
		{GirogateTRUSTLY, map[string]string{"girogateconsumerid": "BY789"}},
		{GirogateGIROPAY, map[string]string{"girogatemobile": "+905350000000"}},
		{GirogateDRAGONPAY, map[string]string{"girogateemail": "john@example.com", "girogatephone": "+905350000000"}},
	} {
		t.Run("adds the customer fields "+string(tc.method)+" needs, from the buyer", func(t *testing.T) {
			provider, _ := payProvider(t)
			request := withoutCard()
			request.Buyer.Email = "john@example.com"
			result, err := provider.InitThreeDSPaymentWithOptions(ctx, request, InitOptions{
				Girogate: &GirogateRequest{Method: tc.method, Country: "PL"},
			})
			if err != nil {
				t.Fatalf("InitThreeDSPayment() error = %v", err)
			}
			fields := expectSignedForm(t, result.ThreeDSHTMLContent)
			expectFields(t, fields, tc.want)
		})
	}

	t.Run("prefers the customer fields given in the request", func(t *testing.T) {
		provider, _ := payProvider(t)
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, withoutCard(), InitOptions{
			Girogate: &GirogateRequest{Method: GirogateP24, Country: "PL", Email: "other@example.com"},
		})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if got := formFieldsOf(result.ThreeDSHTMLContent)["girogateemail"]; got != "other@example.com" {
			t.Errorf("girogateemail = %q", got)
		}
	})

	for _, tc := range []struct {
		girogate *GirogateRequest
		message  string
	}{
		{&GirogateRequest{Method: "NOPE", Country: "NL"}, "Unknown Girogate method"},
		{&GirogateRequest{Method: GirogateIDEAL, Country: "Netherlands"}, "two-letter"},
	} {
		t.Run("rejects an invalid request: "+tc.message, func(t *testing.T) {
			provider, _ := payProvider(t)
			result, err := provider.InitThreeDSPaymentWithOptions(ctx, withoutCard(), InitOptions{Girogate: tc.girogate})
			if err != nil {
				t.Fatalf("InitThreeDSPayment() error = %v", err)
			}
			if result.Status != core.StatusFailure || result.Code != core.CodeInvalidRequest {
				t.Errorf("result = %+v", result)
			}
			if !strings.Contains(result.ErrorMessage, tc.message) {
				t.Errorf("errorMessage = %q, want it to contain %q", result.ErrorMessage, tc.message)
			}
		})
	}

	t.Run("requires the customer field a method needs", func(t *testing.T) {
		provider, _ := payProvider(t)
		request := withoutCard()
		request.Buyer.Email = ""
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, request, InitOptions{
			Girogate: &GirogateRequest{Method: GirogateZIMPLER, Country: "SE"},
		})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || !strings.Contains(result.ErrorMessage, "email") {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("needs a store type where the bank charges", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) { cfg.StoreType = Store3D })
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, withoutCard(), InitOptions{
			Girogate: &GirogateRequest{Method: GirogateIDEAL, Country: "NL"},
		})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || !strings.Contains(result.ErrorMessage, "3d_pay") {
			t.Errorf("result = %+v", result)
		}
	})

	// A Pending callback as the bank signs it, with one more echoed field (oie)
	// that holds another order's id.
	pendingCallback := func(t *testing.T) map[string]string {
		callback := test3DPayCallback()
		callback["Response"] = "Pending"
		callback["ProcReturnCode"] = ""
		callback["paymentType"] = "GIROGATE_SEPA"
		callback["ccode"] = "DE"
		callback["oie"] = "ORDEROTHER"
		return signed(t, callback)
	}

	t.Run("returns pending with the order id when it matches your records", func(t *testing.T) {
		provider, doer := payProvider(t)
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, pendingCallback(t), CompleteContext{OrderID: "ORDERPAY"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusPending || result.PaymentID != "ORDERPAY" || result.ConversationID != "ORDERPAY" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("returns pending without ids when the order is not given", func(t *testing.T) {
		provider, _ := payProvider(t)
		result, err := provider.CompleteThreeDSPayment(ctx, pendingCallback(t))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q", result.Status)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	// The ver3 hash covers the sorted values, not their names: renaming the fields
	// keeps the signed sequence (ORDERPAY, ORDEROTHER) and the HASH, but makes oid
	// another order. Such a callback must not attach a pending result to that order.
	t.Run("does not attach a relabelled Pending callback to another order", func(t *testing.T) {
		provider, doer := payProvider(t)
		callback := pendingCallback(t)
		relabelled := map[string]string{}
		for key, value := range callback {
			if key != "oid" && key != "oie" {
				relabelled[key] = value
			}
		}
		relabelled["oic"] = "ORDERPAY"
		relabelled["oid"] = callback["oie"]
		if !VerifyCallback(relabelled, testConfig().StoreKey) {
			t.Fatal("relabelled callback did not verify")
		}

		unbound, err := provider.CompleteThreeDSPayment(ctx, relabelled)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if unbound.Status != core.StatusPending {
			t.Errorf("status = %q", unbound.Status)
		}
		expectNoIDs(t, unbound.PaymentID, unbound.ConversationID)

		mismatched, err := provider.CompleteThreeDSPaymentWithContext(ctx, relabelled, CompleteContext{OrderID: "ORDERPAY"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if mismatched.Status != core.StatusFailure || mismatched.ErrorCode != "INVALID_HASH" {
			t.Errorf("result = %+v", mismatched)
		}
		expectNoIDs(t, mismatched.PaymentID, mismatched.ConversationID)
		expectNoCalls(t, doer)
	})

	// The order from your records is a separate argument: an orderId added to the
	// body is part of what the HASH must cover, so it breaks the HASH.
	t.Run("rejects an orderId added to the callback body", func(t *testing.T) {
		provider, _ := payProvider(t)
		callback := pendingCallback(t)
		callback["orderId"] = "ORDEROTHER"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("does not let the context change the signed fields", func(t *testing.T) {
		provider, doer := payProvider(t)
		doer.response = estOrderStatusXML(nil)
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DPayCallback(), CompleteContext{OrderID: "ORDERPAY"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q", result.Status)
		}
	})

	t.Run("rejects an unsigned Pending callback", func(t *testing.T) {
		provider, _ := payProvider(t)
		callback := test3DPayCallback()
		callback["Response"] = "Pending"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	// Shaped like the test environment's answer for a store without Girogate.
	t.Run("fails without ids when the store has no Girogate", func(t *testing.T) {
		provider, doer := payProvider(t)
		callback := test3DPayCallback()
		callback["Response"] = "Declined"
		callback["ProcReturnCode"] = "99"
		callback["ErrMsg"] = "Not a girogate merchant: "
		callback["paymentType"] = "GIROGATE_IDEAL"
		callback["ccode"] = "NL"
		result, err := provider.CompleteThreeDSPayment(ctx, signed(t, callback))
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure || result.ErrorCode != "99" {
			t.Errorf("result = %+v", result)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})
}

func TestRefundCancelGetPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("refunds with Credit", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		result, err := provider.Refund(ctx, &core.RefundRequest{
			PaymentID: "ORDER3D", Price: "0.5", Currency: "TRY", IP: "1.1.1.1",
		})
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Fields, map[string]string{
			"Type": "Credit", "OrderId": "ORDER3D", "Total": "0.50", "Currency": "949",
		})
		if result.Status != core.StatusSuccess || result.RefundID != "26011AAAA1234" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("cancels with Void", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		result, err := provider.Cancel(ctx, &core.CancelRequest{PaymentID: "ORDER3D", IP: "1.1.1.1"})
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		expectFields(t, sentFields(t, doer, 0).Fields, map[string]string{"Type": "Void", "OrderId": "ORDER3D"})
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q", result.Status)
		}
	})

	t.Run("maps a rejected refund", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estXML([]xmlPair{
			{"Response", "Error"}, {"ProcReturnCode", "99"}, {"ErrMsg", "Iade tutari fazla"},
		}, nil)
		result, err := provider.Refund(ctx, &core.RefundRequest{
			PaymentID: "ORDER3D", Price: "5", Currency: "TRY", IP: "1.1.1.1",
		})
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		if result.Status != core.StatusFailure || result.ErrorMessage != "Iade tutari fazla" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("returns the order status", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estOrderStatusXML(nil)
		result, err := provider.GetPayment(ctx, "ORDERPAY")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q", result.Status)
		}

		doer.response = estOrderStatusXML(map[string]string{"CAPTURE_AMT": "0"})
		result, err = provider.GetPayment(ctx, "ORDERPAY")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}

		doer.response = estXML([]xmlPair{
			{"Response", "Declined"}, {"ProcReturnCode", "99"}, {"ErrMsg", "No record found for X"},
		}, nil)
		missing, err := provider.GetPayment(ctx, "X")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if missing.Status != core.StatusFailure || missing.ErrorMessage != "No record found for X" {
			t.Errorf("result = %+v", missing)
		}
	})

	t.Run("returns cancelled for a voided order", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estOrderStatusXML(map[string]string{"TRANS_STAT": "V", "CAPTURE_AMT": "100"})
		result, err := provider.GetPayment(ctx, "ORDERPAY")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusCancelled || result.PaymentID != "ORDERPAY" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("returns cancelled for a refunded order", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estOrderStatusXML(map[string]string{"TRANS_STAT": "C", "CHARGE_TYPE_CD": "C", "CAPTURE_AMT": "50"})
		result, err := provider.GetPayment(ctx, "ORDERPAY")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusCancelled {
			t.Errorf("status = %q", result.Status)
		}
	})

	t.Run("returns failure with the bank code for a declined order", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = estXML(
			[]xmlPair{
				{"ErrMsg", "Record(s) found for ORDERPAY"}, {"ProcReturnCode", "12"}, {"Response", "Declined"},
			},
			[]xmlPair{
				{"TRANS_STAT", "D"}, {"CHARGE_TYPE_CD", "S"}, {"CAPTURE_AMT", "100"}, {"PROC_RET_CD", "12"},
			},
		)
		result, err := provider.GetPayment(ctx, "ORDERPAY")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure || result.ErrorCode != "12" || result.ErrorMessage != "Declined by the bank" {
			t.Errorf("result = %+v", result)
		}
	})
}

func TestOrderStatusMapping(t *testing.T) {
	status := func(t *testing.T, extra map[string]string) core.PaymentStatus {
		t.Helper()
		inquiry, err := ParseXMLResponse(estOrderStatusXML(extra))
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		return OrderStatus(inquiry)
	}
	for _, tc := range []struct {
		extra map[string]string
		want  core.PaymentStatus
	}{
		{map[string]string{"TRANS_STAT": "C"}, core.StatusSuccess},
		{map[string]string{"TRANS_STAT": "V"}, core.StatusCancelled},
		{map[string]string{"CHARGE_TYPE_CD": "C"}, core.StatusCancelled},
		{map[string]string{"TRANS_STAT": "D"}, core.StatusFailure},
		{map[string]string{"TRANS_STAT": ""}, core.StatusFailure},
		{map[string]string{"CAPTURE_AMT": "0"}, core.StatusPending},
	} {
		if got := status(t, tc.extra); got != tc.want {
			t.Errorf("OrderStatus(%v) = %q, want %q", tc.extra, got, tc.want)
		}
	}
}

func TestIsCapturedSale(t *testing.T) {
	inquiry := func(t *testing.T, extra map[string]string) XMLResponse {
		t.Helper()
		response, err := ParseXMLResponse(estOrderStatusXML(extra))
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		return response
	}
	for _, tc := range []struct {
		extra map[string]string
		want  bool
	}{
		{map[string]string{}, true},
		{map[string]string{"TRANS_STAT": "C"}, true},
		{map[string]string{"TRANS_STAT": "V"}, false},
		{map[string]string{"CHARGE_TYPE_CD": "C"}, false},
		{map[string]string{"CAPTURE_AMT": "0"}, false},
	} {
		if got := IsCapturedSale(inquiry(t, tc.extra)); got != tc.want {
			t.Errorf("IsCapturedSale(%v) = %v, want %v", tc.extra, got, tc.want)
		}
	}
}

// Only read-only queries may be retried: a payment, refund or cancel that times
// out could otherwise be sent twice.
func TestRetriesOnlyQueries(t *testing.T) {
	ctx := context.Background()
	retry := func(cfg *Config) {
		cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	}

	t.Run("never retries a payment, refund or cancel", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.CreatePayment(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if _, err := provider.Refund(ctx, testRefundRequest()); err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		if _, err := provider.Cancel(ctx, testCancelRequest()); err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3 (one per operation)", len(doer.calls))
		}
	})

	t.Run("retries the order status query", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.GetPayment(ctx, "ORDERPAY"); err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3", len(doer.calls))
		}
	})
}

func TestNewResolvesBankURLs(t *testing.T) {
	banks := []Bank{BankIsbank, BankZiraat, BankHalkbank, BankTEB, BankSekerbank, BankAkbankEST, BankHalkbankMKD}

	t.Run("uses the shared test environment in test mode", func(t *testing.T) {
		for _, bank := range banks {
			cfg := testConfig()
			cfg.Bank, cfg.BaseURL, cfg.TestMode = bank, "", true
			provider, err := New(cfg)
			if err != nil {
				t.Fatalf("New(%s) error = %v", bank, err)
			}
			if provider.Config.BaseURL != SandboxURL {
				t.Errorf("%s baseUrl = %q, want %q", bank, provider.Config.BaseURL, SandboxURL)
			}
		}
	})

	t.Run("keeps a default host per bank", func(t *testing.T) {
		if DefaultURLs[BankSekerbank] != (URLs{Sandbox: SandboxURL, Production: "https://sanalpos.sekerbank.com.tr"}) {
			t.Errorf("sekerbank urls = %+v", DefaultURLs[BankSekerbank])
		}
		if BankAkbankEST == Bank("akbank") {
			t.Error("the EST platform bank must stay distinct from the JSON API bank")
		}
	})

	t.Run("requires baseUrl for an unknown bank", func(t *testing.T) {
		cfg := testConfig()
		cfg.Bank, cfg.BaseURL = "x", ""
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "baseUrl") {
			t.Fatalf("New() error = %v, want a baseUrl error", err)
		}
	})

	t.Run("uses each bank's production host", func(t *testing.T) {
		for _, tc := range []struct {
			bank Bank
			url  string
		}{
			{BankIsbank, "https://sanalpos.isbank.com.tr"},
			{BankZiraat, "https://sanalpos2.ziraatbank.com.tr"},
			{BankHalkbank, "https://sanalpos.halkbank.com.tr"},
			{BankTEB, "https://sanalpos.teb.com.tr"},
			{BankSekerbank, "https://sanalpos.sekerbank.com.tr"},
			{BankAkbankEST, "https://www.sanalakpos.com"},
			{BankHalkbankMKD, "https://epay.halkbank.mk"},
		} {
			cfg := testConfig()
			cfg.Bank, cfg.BaseURL = tc.bank, ""
			provider, err := New(cfg)
			if err != nil {
				t.Fatalf("New(%s) error = %v", tc.bank, err)
			}
			if provider.Config.BaseURL != tc.url {
				t.Errorf("%s baseUrl = %q, want %q", tc.bank, provider.Config.BaseURL, tc.url)
			}
		}
	})

	t.Run("lets baseUrl override the bank host", func(t *testing.T) {
		cfg := testConfig()
		cfg.BaseURL = "https://x.test"
		provider, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if provider.Config.BaseURL != "https://x.test" {
			t.Errorf("baseUrl = %q", provider.Config.BaseURL)
		}
	})
}

func TestOptionalOperationsAreNotSupported(t *testing.T) {
	ctx := context.Background()
	provider, _ := newTestProvider(t, nil)

	if _, err := provider.Authorize(ctx, testPaymentRequest()); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("Authorize() error = %v", err)
	}
	if _, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDER3D", Amount: "1.20", IP: "1.1.1.1"}); err == nil {
		t.Errorf("Capture() error = %v", err)
	}
	if _, err := provider.SaveCard(ctx, &core.SaveCardRequest{Card: *testCard()}); err == nil {
		t.Errorf("SaveCard() error = %v", err)
	}
	if _, err := provider.BinCheck(ctx, "552879"); err == nil {
		t.Errorf("BinCheck() error = %v", err)
	}
	if _, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "552879", Price: "1.2"}); err == nil {
		t.Errorf("InstallmentInfo() error = %v", err)
	}
}

func TestProviderSatisfiesContract(t *testing.T) {
	provider, _ := newTestProvider(t, nil)
	var _ core.Provider = provider
	if provider.Name() != "est" {
		t.Errorf("Name() = %q, want est", provider.Name())
	}
}

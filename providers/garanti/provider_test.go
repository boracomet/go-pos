package garanti

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func TestNewRequiresCredentials(t *testing.T) {
	for _, field := range []string{"merchantId", "terminalId", "password", "storeKey"} {
		t.Run(field, func(t *testing.T) {
			cfg := testConfig()
			switch field {
			case "merchantId":
				cfg.MerchantID = ""
			case "terminalId":
				cfg.TerminalID = ""
			case "password":
				cfg.Password = ""
			case "storeKey":
				cfg.StoreKey = ""
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

	t.Run("rejects an unknown securityLevel", func(t *testing.T) {
		cfg := testConfig()
		cfg.SecurityLevel = "3D_HALF"
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "securityLevel") {
			t.Fatalf("New() error = %v, want a securityLevel error", err)
		}
	})

	t.Run("requires baseUrl", func(t *testing.T) {
		cfg := testConfig()
		cfg.BaseURL = ""
		cfg.TestMode = false
		if _, err := New(cfg); err != nil {
			t.Fatalf("New() error = %v", err)
		}
	})
}

func TestNewResolvesHosts(t *testing.T) {
	t.Run("uses the test host in test mode", func(t *testing.T) {
		cfg := testConfig()
		cfg.BaseURL = ""
		provider, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if provider.Config.BaseURL != SandboxURL {
			t.Errorf("baseUrl = %q, want %q", provider.Config.BaseURL, SandboxURL)
		}
		if !provider.cfg.TestMode {
			t.Error("testMode = false, want true")
		}
	})

	t.Run("uses the production host by default", func(t *testing.T) {
		cfg := testConfig()
		cfg.BaseURL, cfg.TestMode = "", false
		provider, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if provider.Config.BaseURL != ProductionURL {
			t.Errorf("baseUrl = %q, want %q", provider.Config.BaseURL, ProductionURL)
		}
		if provider.cfg.TestMode {
			t.Error("testMode = true, want false")
		}
		if provider.cfg.SecurityLevel != Level3D {
			t.Errorf("securityLevel = %q, want 3D", provider.cfg.SecurityLevel)
		}
	})
}

func TestCreatePayment(t *testing.T) {
	ctx := context.Background()

	t.Run("sends a signed non-3D sale", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML

		request := testPaymentRequest()
		request.ConversationID = "ORDER1"
		result, err := provider.CreatePayment(ctx, request)
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}

		if len(doer.calls) != 1 {
			t.Fatalf("requests = %d, want 1", len(doer.calls))
		}
		if doer.calls[0].URL != SandboxURL+EndpointAPI {
			t.Errorf("url = %q, want %q", doer.calls[0].URL, SandboxURL+EndpointAPI)
		}
		if doer.calls[0].Method != "POST" {
			t.Errorf("method = %q, want POST", doer.calls[0].Method)
		}
		if got := doer.calls[0].Headers.Get("Content-Type"); got != "application/xml" {
			t.Errorf("content-type = %q, want application/xml", got)
		}
		xml := sentXML(t, doer, 0)
		if !strings.HasPrefix(xml, `<?xml version="1.0" encoding="UTF-8"?><GVPSRequest><Mode>TEST</Mode>`) {
			prefix := xml
			if len(prefix) > 90 {
				prefix = prefix[:90]
			}
			t.Errorf("xml prefix = %q", prefix)
		}
		cfg := testConfig()
		for _, tc := range []struct{ tag, want string }{
			{"Version", "512"},
			{"ProvUserID", "PROVAUT"},
			{"MerchantID", cfg.MerchantID},
			{"IPAddress", "85.34.78.112"},
			{"Number", "5528790000000008"},
			{"ExpireDate", "1230"},
			{"Type", "sales"},
			{"Amount", "120"},
			{"CardholderPresentCode", "0"},
		} {
			if got := leaf(xml, tc.tag); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
			}
		}
		want, err := CreateHashData(HashDataFields{
			OrderID:      "ORDER1",
			TerminalID:   cfg.TerminalID,
			CardNumber:   "5528790000000008",
			Amount:       "120",
			CurrencyCode: "949",
			SecurityData: testSecurityData,
		})
		if err != nil {
			t.Fatalf("CreateHashData() error = %v", err)
		}
		if got := leaf(xml, "HashData"); got != want {
			t.Errorf("HashData = %q, want %q", got, want)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDER1" {
			t.Errorf("paymentId = %q, want ORDER1", result.PaymentID)
		}
	})

	t.Run("sends Mode PROD outside test mode", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) { cfg.TestMode = false })
		doer.response = testApprovedXML
		if _, err := provider.CreatePayment(ctx, testPaymentRequest()); err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if got := leaf(sentXML(t, doer, 0), "Mode"); got != "PROD" {
			t.Errorf("Mode = %q, want PROD", got)
		}
	})

	t.Run("maps a bank decline by its reason code", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = garantiXML([]xmlPair{
			{"Code", "51"}, {"ReasonCode", "51"}, {"Message", "Declined"}, {"ErrorMsg", "Bakiye yetersiz"},
		}, nil, nil)
		result, err := provider.CreatePayment(ctx, testPaymentRequest())
		if err != nil {
			t.Fatalf("CreatePayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "51" {
			t.Errorf("errorCode = %q, want 51", result.ErrorCode)
		}
		if result.ErrorMessage != "Bakiye yetersiz" {
			t.Errorf("errorMessage = %q", result.ErrorMessage)
		}
		if result.Code != core.CodeInsufficientFunds {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
		}
	})

	t.Run("returns pending on a timeout", func(t *testing.T) {
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
		if result.Code != core.CodeInvalidRequest {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
		}
	})
}

func TestInitThreeDSPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("returns a signed form to the 3D engine", func(t *testing.T) {
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
			t.Errorf("status = %q, want pending", result.Status)
		}
		html := result.ThreeDSHTMLContent
		if !strings.Contains(html, `action="`+SandboxURL+EndpointGate+`"`) {
			t.Errorf("form action missing: %q", html)
		}
		fields := formFieldsOf(html)
		cfg := testConfig()
		expectFields(t, fields, map[string]string{
			"secure3dsecuritylevel": "3D",
			"mode":                  "TEST",
			"apiversion":            "512",
			"terminalprovuserid":    "PROVAUT",
			"terminalmerchantid":    cfg.MerchantID,
			"terminalid":            cfg.TerminalID,
			"txntype":               "sales",
			"txnamount":             "120",
			"txncurrencycode":       "949",
			"txninstallmentcount":   "3",
			"orderid":               "ORDER3D",
			"successurl":            "https://example.com/callback",
			"errorurl":              "https://example.com/callback",
			"customeripaddress":     "85.34.78.112",
			"cardnumber":            "5528790000000008",
			"cardexpiredatemonth":   "12",
			"cardexpiredateyear":    "30",
			"cardcvv2":              "123",
		})
		want, err := Create3DHash(ThreeDHashFields{
			TerminalID:   cfg.TerminalID,
			OrderID:      "ORDER3D",
			Amount:       "120",
			CurrencyCode: "949",
			SuccessURL:   "https://example.com/callback",
			ErrorURL:     "https://example.com/callback",
			Type:         "sales",
			Installment:  "3",
			StoreKey:     cfg.StoreKey,
			SecurityData: testSecurityData,
		})
		if err != nil {
			t.Fatalf("Create3DHash() error = %v", err)
		}
		if fields["secure3dhash"] != want {
			t.Errorf("secure3dhash = %q, want %q", fields["secure3dhash"], want)
		}
		if result.PaymentID != "ORDER3D" {
			t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
		}
	})

	t.Run("uses the 3D_PAY level and failUrl when configured", func(t *testing.T) {
		provider, _ := newTestProvider(t, func(cfg *Config) { cfg.SecurityLevel = Level3DPay })
		result, err := provider.InitThreeDSPaymentWithOptions(ctx, testThreeDSRequest(),
			InitOptions{FailURL: "https://example.com/fail"})
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		if fields["secure3dsecuritylevel"] != "3D_PAY" {
			t.Errorf("secure3dsecuritylevel = %q, want 3D_PAY", fields["secure3dsecuritylevel"])
		}
		if fields["errorurl"] != "https://example.com/fail" {
			t.Errorf("errorurl = %q", fields["errorurl"])
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

func TestCompleteThreeDSPayment3D(t *testing.T) {
	ctx := context.Background()
	order := CompleteContext{OrderID: "ORDER3D", Amount: "1.20"}

	t.Run("provisions a verified callback for the order from your records", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML

		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DCallback(), order)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}

		xml := sentXML(t, doer, 0)
		for _, tc := range []struct{ tag, want string }{
			{"OrderID", "ORDER3D"},
			{"Type", "sales"},
			{"Amount", "120"},
			{"CardholderPresentCode", "13"},
			{"AuthenticationCode", "CAVVVALUE="},
			{"SecurityLevel", "02"},
			{"TxnID", "XIDVALUE="},
			{"Md", "MDVALUE123"},
		} {
			if got := leaf(xml, tc.tag); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
			}
		}
		if strings.Contains(xml, "<Card>") {
			t.Error("the provision must not send card fields")
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDER3D" || result.ConversationID != "ORDER3D" {
			t.Errorf("ids = %q/%q, want ORDER3D", result.PaymentID, result.ConversationID)
		}
	})

	t.Run("charges the amount and currency from your records, not the callback", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		callback := test3DCallback()
		callback["txnamount"], callback["txncurrencycode"] = "1", "840"
		installment := 3
		contextOrder := order
		contextOrder.Installment = &installment

		if _, err := provider.CompleteThreeDSPaymentWithContext(ctx, callback, contextOrder); err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		xml := sentXML(t, doer, 0)
		for _, tc := range []struct{ tag, want string }{
			{"Amount", "120"}, {"CurrencyCode", "949"}, {"InstallmentCnt", "3"},
		} {
			if got := leaf(xml, tc.tag); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
			}
		}
	})

	t.Run("needs the order from your records before provisioning", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "VALIDATION_ERROR" {
			t.Errorf("errorCode = %q, want VALIDATION_ERROR", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a re-split callback whose hash still verifies", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["clientid"], callback["oid"], callback["orderid"] = "3069129", "7ORDER3D", "7ORDER3D"
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, callback, order)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		if result.Code != core.CodeInvalidHash {
			t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a bad hash as INVALID_HASH", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["hash"] = "X"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a callback whose order id was changed", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["orderid"] = "OTHER"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a callback of another terminal", func(t *testing.T) {
		provider, _ := newTestProvider(t, nil)
		callback := test3DCallback()
		callback["terminalmerchantid"] = "1"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("fails without ids when the card was not authenticated", func(t *testing.T) {
		wrongKey, _ := newTestProvider(t, func(cfg *Config) { cfg.StoreKey = "k" })
		notAuthenticated, err := wrongKey.CompleteThreeDSPayment(ctx, test3DCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if notAuthenticated.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", notAuthenticated.ErrorCode)
		}

		provider, doer := newTestProvider(t, nil)
		value := "30691297ORDER3D0CAVVVALUE=02MDVALUE123RNDVALUE"
		callback := test3DCallback()
		callback["mdstatus"] = "0"
		callback["mderrormessage"] = "Not authenticated"
		callback["hashparamsval"] = value
		callback["hash"] = signed(t, value, testConfig().StoreKey)

		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.Code != core.CodeThreeDSFailed {
			t.Errorf("code = %q, want %q", result.Code, core.CodeThreeDSFailed)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("provisions half-secure mdstatus values only when accepted", func(t *testing.T) {
		value := "30691297ORDER3D2CAVVVALUE=02MDVALUE123RNDVALUE"
		halfSecure := test3DCallback()
		halfSecure["mdstatus"] = "2"
		halfSecure["hashparamsval"] = value
		halfSecure["hash"] = signed(t, value, testConfig().StoreKey)

		provider, doer := newTestProvider(t, nil)
		rejected, err := provider.CompleteThreeDSPayment(ctx, halfSecure)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if rejected.ErrorCode != "MD_STATUS_2" {
			t.Errorf("errorCode = %q, want MD_STATUS_2", rejected.ErrorCode)
		}
		expectNoIDs(t, rejected.PaymentID, rejected.ConversationID)

		lenient, lenientDoer := newTestProvider(t, func(cfg *Config) {
			cfg.AcceptedMdStatuses = []string{"1", "2", "3", "4"}
		})
		lenientDoer.response = testApprovedXML
		accepted, err := lenient.CompleteThreeDSPaymentWithContext(ctx, halfSecure, order)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if len(lenientDoer.calls) != 1 {
			t.Errorf("requests = %d, want 1", len(lenientDoer.calls))
		}
		if accepted.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", accepted.Status)
		}
	})

	t.Run("fails without ids when the provision is declined", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = garantiXML([]xmlPair{
			{"Code", "05"}, {"ReasonCode", "05"}, {"Message", "Declined"}, {"ErrorMsg", "Red"},
		}, nil, nil)
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DCallback(), order)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "05" {
			t.Errorf("errorCode = %q, want 05", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("returns pending with the order id when the provision times out", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.err = context.DeadlineExceeded
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DCallback(), order)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		if result.PaymentID != "ORDER3D" {
			t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
		}
	})
}

func TestGarantiPay(t *testing.T) {
	ctx := context.Background()
	garantiPay := func(cfg *Config) {
		cfg.SecurityLevel = LevelGarantiPay
		cfg.CompanyName = "Test Shop"
	}

	t.Run("returns a signed gpdatarequest form without card fields", func(t *testing.T) {
		provider, doer := newTestProvider(t, garantiPay)
		request := testThreeDSRequest()
		request.PaymentCard = nil
		request.ConversationID = "ORDERGP"

		result, err := provider.InitThreeDSPayment(ctx, request)
		if err != nil {
			t.Fatalf("InitThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		fields := formFieldsOf(result.ThreeDSHTMLContent)
		expectFields(t, fields, map[string]string{
			"secure3dsecuritylevel": "CUSTOM_PAY",
			"txntype":               "gpdatarequest",
			"txnsubtype":            "sales",
			"garantipay":            "Y",
			"bnsuseflag":            "Y",
			"fbbuseflag":            "Y",
			"chequeuseflag":         "Y",
			"mileuseflag":           "Y",
			"companyname":           "Test Shop",
			"lang":                  "tr",
			"orderid":               "ORDERGP",
			"txnamount":             "120",
		})
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T`).MatchString(fields["txntimestamp"]) {
			t.Errorf("txntimestamp = %q", fields["txntimestamp"])
		}
		if _, ok := fields["cardnumber"]; ok {
			t.Error("the GarantiPay form must not carry cardnumber")
		}
		if _, ok := fields["cardcvv2"]; ok {
			t.Error("the GarantiPay form must not carry cardcvv2")
		}
		cfg := testConfig()
		want, err := Create3DHash(ThreeDHashFields{
			TerminalID:   cfg.TerminalID,
			OrderID:      "ORDERGP",
			Amount:       "120",
			CurrencyCode: "949",
			SuccessURL:   "https://example.com/callback",
			ErrorURL:     "https://example.com/callback",
			Type:         "gpdatarequest",
			Installment:  "",
			StoreKey:     cfg.StoreKey,
			SecurityData: testSecurityData,
		})
		if err != nil {
			t.Fatalf("Create3DHash() error = %v", err)
		}
		if fields["secure3dhash"] != want {
			t.Errorf("secure3dhash = %q, want %q", fields["secure3dhash"], want)
		}
	})

	t.Run("confirms an approved callback with an order inquiry", func(t *testing.T) {
		provider, doer := newTestProvider(t, garantiPay)
		doer.response = garantiInquiryXML(nil)
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if got := leaf(sentXML(t, doer, 0), "Type"); got != "orderinq" {
			t.Errorf("Type = %q, want orderinq", got)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDERPAY" {
			t.Errorf("paymentId = %q, want ORDERPAY", result.PaymentID)
		}
	})

	t.Run("rejects an approved callback the bank does not confirm", func(t *testing.T) {
		provider, doer := newTestProvider(t, garantiPay)
		doer.response = garantiInquiryXML([]xmlPair{{"Status", "DECLINED"}})
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	// Shaped like the test environment's answer when the GarantiPay page timed out:
	// the request fields echoed back with an error, and no hash.
	timedOut := func() map[string]string {
		return map[string]string{
			"mdstatus":              "7",
			"procreturncode":        "92",
			"response":              "Error",
			"errmsg":                "İşlem kullanıcıya verilen sürede tamamlanamadı.",
			"txntype":               "gpdatarequest",
			"secure3dsecuritylevel": "CUSTOM_PAY",
			"terminalid":            "30691297",
			"orderid":               "ORDERGP",
			"oid":                   "ORDERGP",
			"txnamount":             "100",
		}
	}

	t.Run("reports an unsigned timeout callback as a failure with its code, without ids", func(t *testing.T) {
		provider, doer := newTestProvider(t, garantiPay)
		result, err := provider.CompleteThreeDSPayment(ctx, timedOut())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "92" {
			t.Errorf("errorCode = %q, want 92", result.ErrorCode)
		}
		if result.ErrorMessage != "İşlem kullanıcıya verilen sürede tamamlanamadı." {
			t.Errorf("errorMessage = %q", result.ErrorMessage)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects an unsigned callback that claims approval", func(t *testing.T) {
		provider, doer := newTestProvider(t, garantiPay)
		callback := timedOut()
		callback["procreturncode"], callback["response"] = "00", "Approved"
		result, err := provider.CompleteThreeDSPayment(ctx, callback)
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})
}

func TestCompleteThreeDSPayment3DPay(t *testing.T) {
	ctx := context.Background()
	pay := func(cfg *Config) { cfg.SecurityLevel = Level3DPay }

	t.Run("confirms an approved callback with an order inquiry", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		doer.response = garantiInquiryXML(nil)

		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		xml := sentXML(t, doer, 0)
		if got := leaf(xml, "Type"); got != "orderinq" {
			t.Errorf("Type = %q, want orderinq", got)
		}
		if got := leaf(xml, "OrderID"); got != "ORDERPAY" {
			t.Errorf("OrderID = %q, want ORDERPAY", got)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.PaymentID != "ORDERPAY" {
			t.Errorf("paymentId = %q, want ORDERPAY", result.PaymentID)
		}
	})

	t.Run("rejects an approved callback the bank does not confirm", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		doer.response = garantiInquiryXML([]xmlPair{{"Status", "DECLINED"}})
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("rejects a callback whose amount differs from the bank record", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		doer.response = garantiInquiryXML([]xmlPair{{"AuthAmount", "99999"}})
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)
	})

	t.Run("checks the bank amount against yours when given", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		doer.response = garantiInquiryXML(nil)
		ok, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DPayCallback(),
			CompleteContext{OrderID: "ORDERPAY", Amount: "1.20"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if ok.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", ok.Status)
		}
		wrong, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DPayCallback(),
			CompleteContext{OrderID: "ORDERPAY", Amount: "99.00"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if wrong.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", wrong.ErrorCode)
		}
		expectNoIDs(t, wrong.PaymentID, wrong.ConversationID)
	})

	t.Run("rejects a callback for another order than yours, without asking the bank", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		result, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DPayCallback(),
			CompleteContext{OrderID: "ORDER2"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		expectNoCalls(t, doer)
		if result.ErrorCode != "INVALID_HASH" {
			t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
		}
	})

	t.Run("returns pending without the callback order id when the inquiry times out", func(t *testing.T) {
		provider, doer := newTestProvider(t, pay)
		doer.err = context.DeadlineExceeded
		result, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback())
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if result.Status != core.StatusPending {
			t.Errorf("status = %q, want pending", result.Status)
		}
		expectNoIDs(t, result.PaymentID, result.ConversationID)

		trusted, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DPayCallback(),
			CompleteContext{OrderID: "ORDERPAY"})
		if err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if trusted.PaymentID != "ORDERPAY" {
			t.Errorf("paymentId = %q, want ORDERPAY", trusted.PaymentID)
		}
	})
}

func TestRefundCancelGetPayment(t *testing.T) {
	ctx := context.Background()

	t.Run("refunds with the PROVRFN user", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		result, err := provider.Refund(ctx, testRefundRequest())
		if err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		xml := sentXML(t, doer, 0)
		for _, tc := range []struct{ tag, want string }{
			{"ProvUserID", "PROVRFN"}, {"Type", "refund"}, {"Amount", "50"},
		} {
			if got := leaf(xml, tc.tag); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.tag, got, tc.want)
			}
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
		if result.RefundID != "628408842186" {
			t.Errorf("refundId = %q, want 628408842186", result.RefundID)
		}
	})

	t.Run("signs refunds with refundPassword when given", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) {
			cfg.RefundPassword = "other"
			cfg.RefundUserID = "RFNUSER"
		})
		doer.response = testApprovedXML
		if _, err := provider.Refund(ctx, testRefundRequest()); err != nil {
			t.Fatalf("Refund() error = %v", err)
		}
		xml := sentXML(t, doer, 0)
		if got := leaf(xml, "ProvUserID"); got != "RFNUSER" {
			t.Errorf("ProvUserID = %q, want RFNUSER", got)
		}
		cfg := testConfig()
		defaultHash, err := CreateHashData(HashDataFields{
			OrderID:      "ORDER1",
			TerminalID:   cfg.TerminalID,
			Amount:       "50",
			CurrencyCode: "949",
			SecurityData: testSecurityData,
		})
		if err != nil {
			t.Fatalf("CreateHashData() error = %v", err)
		}
		if leaf(xml, "HashData") == defaultHash {
			t.Error("HashData must be signed with the refund password")
		}
	})

	t.Run("cancels with void", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testApprovedXML
		result, err := provider.Cancel(ctx, testCancelRequest())
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		xml := sentXML(t, doer, 0)
		if got := leaf(xml, "Type"); got != "void" {
			t.Errorf("Type = %q, want void", got)
		}
		if got := leaf(xml, "ProvUserID"); got != "PROVRFN" {
			t.Errorf("ProvUserID = %q, want PROVRFN", got)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}
	})

	t.Run("maps a rejected cancel", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)
		doer.response = testDeclinedXML
		result, err := provider.Cancel(ctx, testCancelRequest())
		if err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if result.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", result.Status)
		}
		if result.ErrorCode != "0651" {
			t.Errorf("errorCode = %q, want 0651", result.ErrorCode)
		}
		if result.ErrorMessage != "Kullanıcı şifresi hatalı" {
			t.Errorf("errorMessage = %q", result.ErrorMessage)
		}
	})

	t.Run("maps the order inquiry status", func(t *testing.T) {
		provider, doer := newTestProvider(t, nil)

		doer.response = garantiInquiryXML(nil)
		result, err := provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Errorf("status = %q, want success", result.Status)
		}

		doer.response = garantiInquiryXML([]xmlPair{{"Status", "VOID"}, {"AuthAmount", "0"}})
		result, err = provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if result.Status != core.StatusCancelled {
			t.Errorf("status = %q, want cancelled", result.Status)
		}

		doer.response = garantiInquiryXML([]xmlPair{
			{"Status", "DECLINED"}, {"Code", "53"}, {"ReasonCode", "53"}, {"SysErrMsg", "HESAP YOK"},
		})
		declined, err := provider.GetPayment(ctx, "ORDER1")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if declined.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", declined.Status)
		}
		if declined.ErrorCode != "53" {
			t.Errorf("errorCode = %q, want 53", declined.ErrorCode)
		}

		doer.response = garantiXML([]xmlPair{
			{"Source", "GVPS"}, {"Code", "92"}, {"ReasonCode", "0110"},
			{"Message", "Declined"}, {"ErrorMsg", "İşlem bulunamadı"},
		}, nil, nil)
		missing, err := provider.GetPayment(ctx, "NOPE")
		if err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if missing.Status != core.StatusFailure {
			t.Errorf("status = %q, want failure", missing.Status)
		}
		if missing.ErrorMessage != "İşlem bulunamadı" {
			t.Errorf("errorMessage = %q", missing.ErrorMessage)
		}
	})
}

func TestMapOrderStatusAndCapturedSale(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   core.PaymentStatus
	}{
		{"APPROVED", core.StatusSuccess},
		{"DECLINED", core.StatusFailure},
		{"VOID", core.StatusCancelled},
		{"REFUND", core.StatusCancelled},
		{"WAITING", core.StatusPending},
	} {
		if got := MapOrderStatus(tc.status); got != tc.want {
			t.Errorf("MapOrderStatus(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}

	inquiry, err := ParseXMLResponse(garantiInquiryXML(nil))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if !IsCapturedSale(inquiry) {
		t.Error("IsCapturedSale(approved sale) = false, want true")
	}
	preAuth, err := ParseXMLResponse(garantiInquiryXML([]xmlPair{{"ChargeType", "PRE"}}))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if IsCapturedSale(preAuth) {
		t.Error("IsCapturedSale(pre-authorization) = true, want false")
	}
}

func TestRetriesOnlyQueries(t *testing.T) {
	ctx := context.Background()
	retry := func(cfg *Config) {
		cfg.Retry = &core.RetryConfig{Attempts: 3, Delay: time.Millisecond}
	}
	order := CompleteContext{OrderID: "ORDER3D", Amount: "1.20"}

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

	t.Run("never retries the 3D provision", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.CompleteThreeDSPaymentWithContext(ctx, test3DCallback(), order); err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if len(doer.calls) != 1 {
			t.Errorf("requests = %d, want 1", len(doer.calls))
		}
	})

	t.Run("retries the order inquiry", func(t *testing.T) {
		provider, doer := newTestProvider(t, retry)
		doer.err = context.DeadlineExceeded
		if _, err := provider.GetPayment(ctx, "ORDER1"); err != nil {
			t.Fatalf("GetPayment() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3", len(doer.calls))
		}
	})

	t.Run("retries the 3D_PAY confirmation inquiry", func(t *testing.T) {
		provider, doer := newTestProvider(t, func(cfg *Config) {
			retry(cfg)
			cfg.SecurityLevel = Level3DPay
		})
		doer.err = context.DeadlineExceeded
		if _, err := provider.CompleteThreeDSPayment(ctx, test3DPayCallback()); err != nil {
			t.Fatalf("CompleteThreeDSPayment() error = %v", err)
		}
		if len(doer.calls) != 3 {
			t.Errorf("requests = %d, want 3", len(doer.calls))
		}
	})
}

func TestOptionalOperationsAreNotSupported(t *testing.T) {
	ctx := context.Background()
	provider, _ := newTestProvider(t, nil)

	if _, err := provider.Authorize(ctx, testPaymentRequest()); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("Authorize() error = %v", err)
	}
	if _, err := provider.Capture(ctx, &core.CaptureRequest{PaymentID: "ORDER1", Amount: "1.20", IP: "1.1.1.1"}); err == nil {
		t.Errorf("Capture() error = %v", err)
	}
	if _, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{PaymentID: "ORDER1", IP: "1.1.1.1"}); err == nil {
		t.Errorf("VoidAuthorization() error = %v", err)
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
	if provider.Name() != "garanti" {
		t.Errorf("Name() = %q, want garanti", provider.Name())
	}
}

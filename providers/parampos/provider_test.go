package parampos

import (
	"context"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func TestNewRequiresAllCredentials(t *testing.T) {
	_, err := New(Config{
		ProviderConfig: core.ProviderConfig{BaseURL: "https://x"},
		ClientCode:     "1",
		ClientUsername: "",
		ClientPassword: "p",
		GUID:           "g",
	})
	if err == nil || !strings.Contains(err.Error(), "clientUsername") {
		t.Errorf("New() error = %v, want it to name clientUsername", err)
	}
}

func TestCreatePaymentSendsNonSecureSOAP(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_UCD", fields("Sonuc", "1", "Islem_ID", "3007296556", "UCD_HTML", "NONSECURE")))
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.ConversationID = "ORDER1"
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}

	if len(doer.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(doer.calls))
	}
	call := doer.calls[0]
	if got, want := call.soapAction(), Namespace+"TP_WMD_UCD"; got != want {
		t.Errorf("SOAPAction = %q, want %q", got, want)
	}
	if got, want := call.Header.Get("Content-Type"), "text/xml; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	hash, hashErr := GeneratePaymentHash("10738", testGUID, 1, "1,00", "1,20", "ORDER1")
	if hashErr != nil {
		t.Fatalf("GeneratePaymentHash() error = %v", hashErr)
	}
	for _, want := range []string{
		"<Islem_Guvenlik_Tip>NS</Islem_Guvenlik_Tip>",
		"<Islem_Tutar>1,00</Islem_Tutar>",
		"<Toplam_Tutar>1,20</Toplam_Tutar>",
		"<Siparis_ID>ORDER1</Siparis_ID>",
		"<GUID>" + testGUID + "</GUID>",
		"<Islem_Hash>" + hash + "</Islem_Hash>",
	} {
		if !strings.Contains(call.Body, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.PaymentID != "ORDER1" {
		t.Errorf("paymentId = %q, want ORDER1", result.PaymentID)
	}
}

func TestCreatePaymentDeclinesWithoutIslemID(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_UCD", fields("Sonuc", "-1", "Islem_ID", "0", "Sonuc_Str", "Tekrar deneyin")))
	provider := newTestProvider(t, doer)

	result, err := provider.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorMessage != "Tekrar deneyin" {
		t.Errorf("errorMessage = %q, want %q", result.ErrorMessage, "Tekrar deneyin")
	}
}

func TestCreatePaymentRejectsNonTRY(t *testing.T) {
	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	request := mockPaymentRequest()
	request.Currency = "USD"
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0 for a foreign currency", len(doer.calls))
	}
}

func TestInitThreeDSPaymentKeepsPaidPrice(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_UCD", fields("Sonuc", "1", "UCD_HTML", "<form></form>")))
	provider := newTestProvider(t, doer)

	request := mockThreeDSPaymentRequest()
	request.Price = "100"
	request.PaidPrice = "104.50"
	request.Installment = 3
	if _, err := provider.InitThreeDSPayment(context.Background(), request); err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}

	xml := doer.body(0)
	for _, want := range []string{
		"<Taksit>3</Taksit>",
		"<Islem_Tutar>100,00</Islem_Tutar>",
		"<Toplam_Tutar>104,50</Toplam_Tutar>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("request missing %q", want)
		}
	}
}

func TestInitThreeDSPaymentReturnsHTML(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_UCD", fields("Sonuc", "1", "UCD_HTML", `<form action="https://bank">x</form>`)))
	provider := newTestProvider(t, doer)

	request := mockThreeDSPaymentRequest()
	request.ConversationID = "ORDER3D"
	result, err := provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}

	xml := doer.body(0)
	for _, want := range []string{
		"<Islem_Guvenlik_Tip>3D</Islem_Guvenlik_Tip>",
		"<Basarili_URL>https://example.com/callback</Basarili_URL>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if want := `<form action="https://bank">x</form>`; result.ThreeDSHTMLContent != want {
		t.Errorf("threeDSHtmlContent = %q, want %q", result.ThreeDSHTMLContent, want)
	}
	if result.PaymentID != "ORDER3D" {
		t.Errorf("paymentId = %q, want ORDER3D", result.PaymentID)
	}
}

func TestCompleteThreeDSPaymentFinalizes(t *testing.T) {
	callback := referenceCallback()
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_Pay", fields("Sonuc", "1", "Dekont_ID", "3007295376", "Siparis_ID", callback["orderId"])))
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(doer.calls))
	}
	xml := doer.body(0)
	for _, want := range []string{
		`<TP_WMD_Pay xmlns="https://turkpos.com.tr/">`,
		"<UCD_MD>" + callback["md"] + "</UCD_MD>",
		"<Islem_GUID>" + callback["islemGUID"] + "</Islem_GUID>",
		"<Siparis_ID>" + callback["orderId"] + "</Siparis_ID>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if result.PaymentID != callback["orderId"] {
		t.Errorf("paymentId = %q, want %q", result.PaymentID, callback["orderId"])
	}
}

func TestCompleteThreeDSPaymentDeclined(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_Pay", fields("Sonuc", "-100", "Sonuc_Ack", "Hesap bulunamadı.")))
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), referenceCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorMessage != "Hesap bulunamadı." {
		t.Errorf("errorMessage = %q, want %q", result.ErrorMessage, "Hesap bulunamadı.")
	}
}

func TestCompleteThreeDSPaymentMapsBankCode(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_Pay", fields("Sonuc", "-1", "Banka_Sonuc_Kod", "51", "Sonuc_Str", "Yetersiz bakiye")))
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), referenceCallback())
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Code != core.CodeInsufficientFunds {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInsufficientFunds)
	}
	if result.ErrorCode != "-1" {
		t.Errorf("errorCode = %q, want -1", result.ErrorCode)
	}
}

func TestCompleteThreeDSPaymentRejectsForgedCallback(t *testing.T) {
	callback := referenceCallback()
	attackerGUID := "attacker"
	forged := referenceCallback()
	forged["GUID"] = attackerGUID
	forgedHash, hashErr := Generate3DSVerificationHash(
		callback["islemGUID"], callback["md"], "1", callback["orderId"], attackerGUID)
	if hashErr != nil {
		t.Fatalf("Generate3DSVerificationHash() error = %v", hashErr)
	}
	forged["islemHash"] = forgedHash

	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), forged)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "INVALID_HASH" {
		t.Errorf("errorCode = %q, want INVALID_HASH", result.ErrorCode)
	}
	if result.Code != core.CodeInvalidHash {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidHash)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0 for a forged callback", len(doer.calls))
	}
}

func TestCompleteThreeDSPaymentRequiresMDStatusOne(t *testing.T) {
	callback := referenceCallback()
	md0 := referenceCallback()
	md0["mdStatus"] = "0"
	md0Hash, hashErr := Generate3DSVerificationHash(
		callback["islemGUID"], callback["md"], "0", callback["orderId"], testGUID)
	if hashErr != nil {
		t.Fatalf("Generate3DSVerificationHash() error = %v", hashErr)
	}
	md0["islemHash"] = md0Hash

	doer := &stubDoer{}
	provider := newTestProvider(t, doer)

	result, err := provider.CompleteThreeDSPayment(context.Background(), md0)
	if err != nil {
		t.Fatalf("CompleteThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.Code != core.CodeThreeDSFailed {
		t.Errorf("code = %q, want %q", result.Code, core.CodeThreeDSFailed)
	}
	if len(doer.calls) != 0 {
		t.Errorf("calls = %d, want 0 when mdStatus is not 1", len(doer.calls))
	}
}

func TestRefundSendsIADE(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_Islem_Iptal_Iade_Kismi2", fields("Sonuc", "1", "Sonuc_Str", "OK")))
	provider := newTestProvider(t, doer)

	result, err := provider.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "ORDER1", Price: "5.5", Currency: "TRY", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	xml := doer.body(0)
	for _, want := range []string{
		"<Durum>IADE</Durum>",
		"<Siparis_ID>ORDER1</Siparis_ID>",
		"<Tutar>5.50</Tutar>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestCancelLooksUpAmount(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(
		soapResponse("TP_Islem_Sorgulama4", fields("Sonuc", "1", "Durum", "SUCCESS", "Toplam_Tutar", "10.01", "Siparis_ID", "ORDER1")),
		soapResponse("TP_Islem_Iptal_Iade_Kismi2", fields("Sonuc", "1")),
	)
	provider := newTestProvider(t, doer)

	result, err := provider.Cancel(context.Background(), &core.CancelRequest{PaymentID: "ORDER1", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if len(doer.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(doer.calls))
	}
	xml := doer.body(1)
	for _, want := range []string{"<Durum>IPTAL</Durum>", "<Tutar>10.01</Tutar>"} {
		if !strings.Contains(xml, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

func TestGetPaymentMapsDurum(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_Islem_Sorgulama4", fields("Sonuc", "1", "Durum", "REFUND", "Siparis_ID", "ORDER1")))
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if result.Status != core.StatusCancelled {
		t.Errorf("status = %q, want %q", result.Status, core.StatusCancelled)
	}
}

// Read-only queries are marked retryable, so a transport failure is retried.
func TestGetPaymentRetriesTransportFailures(t *testing.T) {
	doer := &stubDoer{queue: []stubResponse{
		{err: timeoutError()},
		{body: soapResponse("TP_Islem_Sorgulama4", fields("Sonuc", "1", "Durum", "SUCCESS", "Siparis_ID", "ORDER1"))},
	}}
	provider := newTestProvider(t, doer)

	result, err := provider.GetPayment(context.Background(), "ORDER1")
	if err != nil {
		t.Fatalf("GetPayment() error = %v", err)
	}
	if len(doer.calls) != 2 {
		t.Errorf("calls = %d, want 2 (the query is retryable)", len(doer.calls))
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
}

// A payment is never retried: a timeout after the service received it would
// charge twice, so the outcome stays PENDING.
func TestCreatePaymentIsNotRetried(t *testing.T) {
	doer := &stubDoer{queue: []stubResponse{{err: timeoutError()}}}
	provider := newTestProvider(t, doer)

	result, err := provider.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if len(doer.calls) != 1 {
		t.Errorf("calls = %d, want 1 (payments are not retryable)", len(doer.calls))
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if result.ErrorCode != core.NetworkErrorCode {
		t.Errorf("errorCode = %q, want %q", result.ErrorCode, core.NetworkErrorCode)
	}
	if result.Code != core.CodeNetworkError {
		t.Errorf("code = %q, want %q", result.Code, core.CodeNetworkError)
	}
}

func TestBinCheck(t *testing.T) {
	doer := &stubDoer{}
	doer.respond(soapResponse("BIN_SanalPos", fields(
		"Sonuc", "1", "BIN", "444676", "Kart_Banka", "ZİRAAT", "Kart_Org", "VISA", "Kart_Tip", "Kredi Kartı")))
	provider := newTestProvider(t, doer)

	result, err := provider.BinCheck(context.Background(), "444676")
	if err != nil {
		t.Fatalf("BinCheck() error = %v", err)
	}
	if !strings.Contains(doer.body(0), "<BIN>444676</BIN>") {
		t.Error("request missing <BIN>444676</BIN>")
	}
	if result.BankName != "ZİRAAT" {
		t.Errorf("bankName = %q, want ZİRAAT", result.BankName)
	}

	doer.respond(soapResponse("BIN_SanalPos", fields("Sonuc", "-1", "Sonuc_Str", "Hata")))
	if _, err := provider.BinCheck(context.Background(), "000000"); err == nil || !strings.Contains(err.Error(), "Hata") {
		t.Errorf("BinCheck() error = %v, want it to surface the service message", err)
	}
}

func TestErrorCodesFromBankCode(t *testing.T) {
	for _, tc := range []struct {
		bankCode string
		want     core.PaymentErrorCode
	}{
		{"51", core.CodeInsufficientFunds},
		{"05", core.CodeCardDeclined},
		{"54", core.CodeExpiredCard},
		{"82", core.CodeInvalidCVC},
		{"5", core.CodeCardDeclined},
	} {
		result := declineWith(t, fields("Sonuc", "-1", "Banka_Sonuc_Kod", tc.bankCode))
		if result.Status != core.StatusFailure {
			t.Errorf("bank %s: status = %q, want %q", tc.bankCode, result.Status, core.StatusFailure)
		}
		if result.Code != tc.want {
			t.Errorf("bank %s: code = %q, want %q", tc.bankCode, result.Code, tc.want)
		}
		if result.ErrorCode != "-1" {
			t.Errorf("bank %s: errorCode = %q, want -1", tc.bankCode, result.ErrorCode)
		}
	}
}

func TestErrorCodesFromSonuc(t *testing.T) {
	for _, tc := range []struct {
		sonuc string
		want  core.PaymentErrorCode
	}{
		{"-105", core.CodeInvalidCVC},
		{"-106", core.CodeExpiredCard},
		{"-119", core.CodeInvalidCard},
		{"-113", core.CodeInvalidRequest},
		{"-101", core.CodeProviderError},
		{"-220", core.CodeCardDeclined},
	} {
		result := declineWith(t, fields("Sonuc", tc.sonuc))
		if result.Code != tc.want {
			t.Errorf("Sonuc %s: code = %q, want %q", tc.sonuc, result.Code, tc.want)
		}
		if result.ErrorCode != tc.sonuc {
			t.Errorf("Sonuc %s: errorCode = %q, want %q", tc.sonuc, result.ErrorCode, tc.sonuc)
		}
	}
}

func TestErrorCodeFallsBackToSonuc(t *testing.T) {
	result := declineWith(t, fields("Sonuc", "-105", "Banka_Sonuc_Kod", "99"))
	if result.Code != core.CodeInvalidCVC {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidCVC)
	}
}

func TestErrorCodeUnknown(t *testing.T) {
	result := declineWith(t, fields("Sonuc", "-999"))
	if result.Code != core.CodeUnknown {
		t.Errorf("code = %q, want %q", result.Code, core.CodeUnknown)
	}
	if result.ErrorCode != "-999" {
		t.Errorf("errorCode = %q, want -999", result.ErrorCode)
	}
}

func declineWith(t *testing.T, extra Fields) *core.PaymentResponse {
	t.Helper()
	doer := &stubDoer{}
	doer.respond(soapResponse("TP_WMD_UCD", append(fields("Islem_ID", "0", "Sonuc_Str", "Hata"), extra...)))
	provider := newTestProvider(t, doer)

	result, err := provider.CreatePayment(context.Background(), mockPaymentRequest())
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	return result
}

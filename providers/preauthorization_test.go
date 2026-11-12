package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/providers/parampos"
	"github.com/boracomet/go-pos/providers/sipay"
	fixtures "github.com/boracomet/go-pos/testing"
)

// preAuthSupport is the verified capability matrix: only these gateways implement
// pre-authorization. Everything else inherits the NOT_SUPPORTED default, and
// PayTR overrides the methods with explicit NOT_SUPPORTED stubs.
var preAuthSupport = map[string]bool{
	"akbank":   true,
	"albaraka": true,
	"iyzico":   true,
	"parampos": true,
	"sipay":    true,
}

// threeDSAuthorizeStatus is the outcome each gateway reports when it hands the
// browser a 3D Secure form (or, for iyzico, an initialized payment).
var threeDSAuthorizeStatus = map[string]core.PaymentStatus{
	"akbank":   core.StatusPending,
	"albaraka": core.StatusPending,
	"iyzico":   core.StatusSuccess,
	"parampos": core.StatusPending,
	"sipay":    core.StatusPending,
}

func TestAuthorizeAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.Authorize(context.Background(), orderRequest())

			if !preAuthSupport[name] {
				wantNotSupported(t, name, "Authorize", err)
				if result != nil {
					t.Errorf("%s: Authorize returned a result alongside NOT_SUPPORTED: %+v", name, result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("%s: Authorize() error = %v", name, err)
			}
			if result.Status != core.StatusSuccess {
				t.Errorf("%s: status = %q, want %q (message %q)", name, result.Status, core.StatusSuccess, result.ErrorMessage)
			}
			if g.count() == 0 {
				t.Errorf("%s: Authorize sent no request to the gateway", name)
			}
		})
	}
}

func TestInitThreeDSAuthorizeAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.InitThreeDSAuthorize(context.Background(), orderThreeDSRequest())

			if !preAuthSupport[name] {
				wantNotSupported(t, name, "InitThreeDSAuthorize", err)
				if result != nil {
					t.Errorf("%s: InitThreeDSAuthorize returned a result alongside NOT_SUPPORTED: %+v", name, result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("%s: InitThreeDSAuthorize() error = %v", name, err)
			}
			if want := threeDSAuthorizeStatus[name]; result.Status != want {
				t.Errorf("%s: status = %q, want %q (message %q)", name, result.Status, want, result.ErrorMessage)
			}
			if name != "iyzico" && result.ThreeDSHTMLContent == "" {
				t.Errorf("%s: threeDSHtmlContent is empty, want the bank form", name)
			}
		})
	}
}

func TestCaptureAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.Capture(context.Background(), captureRequest())

			if !preAuthSupport[name] {
				wantNotSupported(t, name, "Capture", err)
				if result != nil {
					t.Errorf("%s: Capture returned a result alongside NOT_SUPPORTED: %+v", name, result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("%s: Capture() error = %v", name, err)
			}
			if result.Status != core.StatusSuccess {
				t.Errorf("%s: status = %q, want %q (message %q)", name, result.Status, core.StatusSuccess, result.ErrorMessage)
			}
			if g.count() == 0 {
				t.Errorf("%s: Capture sent no request to the gateway", name)
			}
		})
	}
}

func TestVoidAuthorizationAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.VoidAuthorization(context.Background(), voidRequest())

			if !preAuthSupport[name] {
				wantNotSupported(t, name, "VoidAuthorization", err)
				if result != nil {
					t.Errorf("%s: VoidAuthorization returned a result alongside NOT_SUPPORTED: %+v", name, result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("%s: VoidAuthorization() error = %v", name, err)
			}
			if result.Status != core.StatusSuccess {
				t.Errorf("%s: status = %q, want %q (message %q)", name, result.Status, core.StatusSuccess, result.ErrorMessage)
			}
			if g.count() == 0 {
				t.Errorf("%s: VoidAuthorization sent no request to the gateway", name)
			}
		})
	}
}

// TestPreAuthorizationLifecycle walks authorize, capture and void on one gateway
// instance, the way a merchant releases or charges a blocked amount.
func TestPreAuthorizationLifecycle(t *testing.T) {
	for _, name := range supportedNames(preAuthSupport) {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			ctx := context.Background()

			authorized, err := g.provider.Authorize(ctx, orderRequest())
			if err != nil || authorized.Status != core.StatusSuccess {
				t.Fatalf("%s: Authorize() = %+v, %v", name, authorized, err)
			}
			afterAuthorize := g.count()

			captured, err := g.provider.Capture(ctx, captureRequest())
			if err != nil || captured.Status != core.StatusSuccess {
				t.Fatalf("%s: Capture() = %+v, %v", name, captured, err)
			}
			if g.count() <= afterAuthorize {
				t.Errorf("%s: Capture sent no new request", name)
			}
			afterCapture := g.count()

			voided, err := g.provider.VoidAuthorization(ctx, voidRequest())
			if err != nil || voided.Status != core.StatusSuccess {
				t.Fatalf("%s: VoidAuthorization() = %+v, %v", name, voided, err)
			}
			if g.count() <= afterCapture {
				t.Errorf("%s: VoidAuthorization sent no new request", name)
			}
		})
	}
}

// TestPreAuthorizationUnsupportedProvidersAreStubsOrDefaults pins the distinction
// between a deliberate stub and the inherited default: both answer NOT_SUPPORTED,
// and neither may quietly succeed.
func TestPreAuthorizationUnsupportedProvidersAreStubsOrDefaults(t *testing.T) {
	for _, name := range unsupportedNames(preAuthSupport) {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			ctx := context.Background()

			if _, err := g.provider.Authorize(ctx, orderRequest()); err == nil {
				t.Errorf("%s: Authorize succeeded, want NOT_SUPPORTED", name)
			} else {
				wantNotSupported(t, name, "Authorize", err)
			}
			if _, err := g.provider.InitThreeDSAuthorize(ctx, orderThreeDSRequest()); err == nil {
				t.Errorf("%s: InitThreeDSAuthorize succeeded, want NOT_SUPPORTED", name)
			} else {
				wantNotSupported(t, name, "InitThreeDSAuthorize", err)
			}
			if _, err := g.provider.Capture(ctx, captureRequest()); err == nil {
				t.Errorf("%s: Capture succeeded, want NOT_SUPPORTED", name)
			} else {
				wantNotSupported(t, name, "Capture", err)
			}
			if _, err := g.provider.VoidAuthorization(ctx, voidRequest()); err == nil {
				t.Errorf("%s: VoidAuthorization succeeded, want NOT_SUPPORTED", name)
			} else {
				wantNotSupported(t, name, "VoidAuthorization", err)
			}
			wantNoCalls(t, g)
		})
	}
}

func TestPayTRPreAuthorizationStubsExplainWhy(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))
	ctx := context.Background()

	_, err := g.provider.Authorize(ctx, orderRequest())
	wantNotSupported(t, "paytr", "Authorize", err)
	if !strings.Contains(err.Error(), "Direct API") && !strings.Contains(err.Error(), "pre-authorization") {
		t.Errorf("Authorize message = %q, want it to explain that PayTR has no pre-authorization", err)
	}

	_, err = g.provider.Capture(ctx, captureRequest())
	wantNotSupported(t, "paytr", "Capture", err)
	_, err = g.provider.VoidAuthorization(ctx, voidRequest())
	wantNotSupported(t, "paytr", "VoidAuthorization", err)
	_, err = g.provider.InitThreeDSAuthorize(ctx, orderThreeDSRequest())
	wantNotSupported(t, "paytr", "InitThreeDSAuthorize", err)
	wantNoCalls(t, g)
}

func TestParamposAuthorizeSendsNonSecurePreAuth(t *testing.T) {
	g := newProviderGateway(t, "parampos", approve("parampos"))

	result, err := g.provider.Authorize(context.Background(), orderRequest())
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if result.Status != core.StatusSuccess || result.PaymentID != "ORDER1" {
		t.Errorf("Authorize() = %+v, want success for ORDER1", result)
	}

	sent := g.last(t)
	if sent.soapAction() != "TP_Islem_Odeme_OnProv_WMD" {
		t.Errorf("SOAPAction = %q, want TP_Islem_Odeme_OnProv_WMD", sent.Header.Get("SOAPAction"))
	}
	if !strings.Contains(sent.Body, "<Islem_Guvenlik_Tip>NS</Islem_Guvenlik_Tip>") {
		t.Error("a non-3D pre-authorization must send Islem_Guvenlik_Tip NS")
	}
	if strings.Contains(sent.Body, "Basarili_URL") || strings.Contains(sent.Body, "Hata_URL") {
		t.Error("a non-3D pre-authorization must not send callback URLs")
	}

	// Islem_Hash covers CLIENT_CODE+GUID+Islem_Tutar+Toplam_Tutar+Siparis_ID and
	// the two (empty) URLs; the installment is not part of it.
	want, err := parampos.GeneratePreAuthHash("10738", "0c13d406-873b-403b-9c09-a5766840d98c", "1,00", "1,20", "ORDER1", "", "")
	if err != nil {
		t.Fatalf("GeneratePreAuthHash() error = %v", err)
	}
	if !strings.Contains(sent.Body, "<Islem_Hash>"+want+"</Islem_Hash>") {
		t.Errorf("Islem_Hash = %q, want the pre-authorization hash %q", sent.Body, want)
	}
}

func TestParamposPreAuthHashReferenceVector(t *testing.T) {
	got, err := parampos.GeneratePreAuthHash(
		"10738", "0c13d406-873b-403b-9c09-a5766840d98c",
		"1000,25", "1000,25", "order222",
		"https://domain.com/fail", "https://domain.com/success",
	)
	if err != nil {
		t.Fatalf("GeneratePreAuthHash() error = %v", err)
	}
	if want := "LFZ+Sl0mW+ybGvLr1u0ehZoxhxM="; got != want {
		t.Errorf("GeneratePreAuthHash() = %q, want %q", got, want)
	}
}

func TestParamposThreeDSAuthorizeReturnsBankForm(t *testing.T) {
	g := newProviderGateway(t, "parampos", approve("parampos"))

	result, err := g.provider.InitThreeDSAuthorize(context.Background(), orderThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if result.ThreeDSHTMLContent != "<form></form>" {
		t.Errorf("threeDSHtmlContent = %q, want the unescaped bank form", result.ThreeDSHTMLContent)
	}

	sent := g.last(t)
	if !strings.Contains(sent.Body, "<Islem_Guvenlik_Tip>3D</Islem_Guvenlik_Tip>") {
		t.Error("a 3D pre-authorization must send Islem_Guvenlik_Tip 3D")
	}
	if !strings.Contains(sent.Body, "<Basarili_URL>https://example.com/callback</Basarili_URL>") {
		t.Errorf("Basarili_URL missing from the request: %s", sent.Body)
	}
}

func TestParamposCaptureSendsPartialAmount(t *testing.T) {
	g := newProviderGateway(t, "parampos", approve("parampos"))

	result, err := g.provider.Capture(context.Background(), captureRequest())
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}

	sent := g.last(t)
	if sent.soapAction() != "TP_Islem_Odeme_OnProv_Kapa" {
		t.Errorf("SOAPAction = %q, want TP_Islem_Odeme_OnProv_Kapa", sent.Header.Get("SOAPAction"))
	}
	if !strings.Contains(sent.Body, "<Prov_Tutar>0,60</Prov_Tutar>") {
		t.Errorf("Prov_Tutar must carry the partial amount: %s", sent.Body)
	}
	if !strings.Contains(sent.Body, "<Siparis_ID>ORDER1</Siparis_ID>") {
		t.Errorf("Siparis_ID must name the order: %s", sent.Body)
	}
}

func TestParamposVoidAuthorizationSendsIptalOnProv(t *testing.T) {
	g := newProviderGateway(t, "parampos", approve("parampos"))

	result, err := g.provider.VoidAuthorization(context.Background(), voidRequest())
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, core.StatusSuccess)
	}
	if action := g.last(t).soapAction(); action != "TP_Islem_Iptal_OnProv" {
		t.Errorf("SOAPAction = %q, want TP_Islem_Iptal_OnProv", action)
	}
}

func TestParamposDeclinedCaptureIsFailure(t *testing.T) {
	g := newProviderGateway(t, "parampos", func(c call) (int, string) {
		return http.StatusOK, soapEnvelope(c.soapAction(),
			`<Sonuc>-1</Sonuc><Sonuc_Str>Provizyon bulunamadi</Sonuc_Str>`)
	})

	result, err := g.provider.Capture(context.Background(), captureRequest())
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
	if result.ErrorMessage != "Provizyon bulunamadi" {
		t.Errorf("errorMessage = %q, want the bank's reason", result.ErrorMessage)
	}
}

func TestAkbankPreAuthorizationTransactionCodes(t *testing.T) {
	g := newProviderGateway(t, "akbank", approve("akbank"))
	ctx := context.Background()

	authorized, err := g.provider.Authorize(ctx, orderRequest())
	if err != nil || authorized.Status != core.StatusSuccess {
		t.Fatalf("Authorize() = %+v, %v", authorized, err)
	}
	if got := g.at(t, 0).jsonBody(t)["txnCode"]; got != "1004" {
		t.Errorf("authorize txnCode = %v, want 1004", got)
	}
	if authorized.PaymentID != "ORDER1" {
		t.Errorf("paymentId = %q, want ORDER1", authorized.PaymentID)
	}

	captured, err := g.provider.Capture(ctx, captureRequest())
	if err != nil || captured.Status != core.StatusSuccess {
		t.Fatalf("Capture() = %+v, %v", captured, err)
	}
	captureBody := g.at(t, 1).jsonBody(t)
	if captureBody["txnCode"] != "1005" {
		t.Errorf("capture txnCode = %v, want 1005", captureBody["txnCode"])
	}
	if order := captureBody["order"].(map[string]any); order["orderId"] != "ORDER1" {
		t.Errorf("capture orderId = %v, want ORDER1", order["orderId"])
	}
	transaction := captureBody["transaction"].(map[string]any)
	if transaction["amount"] != "0.60" {
		t.Errorf("capture amount = %v, want 0.60", transaction["amount"])
	}
	if transaction["currencyCode"] != float64(949) {
		t.Errorf("capture currencyCode = %v, want 949", transaction["currencyCode"])
	}
	if customer := captureBody["customer"].(map[string]any); customer["ipAddress"] != "1.1.1.1" {
		t.Errorf("capture ipAddress = %v, want 1.1.1.1", customer["ipAddress"])
	}

	voided, err := g.provider.VoidAuthorization(ctx, voidRequest())
	if err != nil || voided.Status != core.StatusSuccess {
		t.Fatalf("VoidAuthorization() = %+v, %v", voided, err)
	}
	voidBody := g.at(t, 2).jsonBody(t)
	if voidBody["txnCode"] != "1003" {
		t.Errorf("void txnCode = %v, want 1003", voidBody["txnCode"])
	}
	if order := voidBody["order"].(map[string]any); order["orderId"] != "ORDER1" {
		t.Errorf("void orderId = %v, want ORDER1", order["orderId"])
	}
}

func TestAkbankThreeDSAuthorizeBuildsPreAuthForm(t *testing.T) {
	g := newProviderGateway(t, "akbank", approve("akbank"))

	result, err := g.provider.InitThreeDSAuthorize(context.Background(), orderThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	for _, want := range []string{`name="txnCode" value="3004"`, `name="paymentModel" value="3D_PAY"`} {
		if !strings.Contains(result.ThreeDSHTMLContent, want) {
			t.Errorf("threeDSHtmlContent is missing %s", want)
		}
	}
	// The form posts the card from the browser: no API call is made.
	wantNoCalls(t, g)
}

func TestIyzicoPreAuthorizationEndpoints(t *testing.T) {
	g := newProviderGateway(t, "iyzico", approve("iyzico"))
	ctx := context.Background()

	authorized, err := g.provider.Authorize(ctx, orderRequest())
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if authorized.Status != core.StatusSuccess || authorized.PaymentID != "42" {
		t.Errorf("Authorize() = %+v, want success with paymentId 42", authorized)
	}
	if path := g.at(t, 0).Path; path != "/payment/preauth" {
		t.Errorf("authorize path = %q, want /payment/preauth", path)
	}

	if _, err := g.provider.InitThreeDSAuthorize(ctx, orderThreeDSRequest()); err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	initCall := g.at(t, 1)
	if initCall.Path != "/payment/3dsecure/initialize/preauth" {
		t.Errorf("3D authorize path = %q, want /payment/3dsecure/initialize/preauth", initCall.Path)
	}
	if got := initCall.jsonBody(t)["callbackUrl"]; got != "https://example.com/callback" {
		t.Errorf("callbackUrl = %v, want the request's callback", got)
	}

	if _, err := g.provider.Capture(ctx, captureRequest()); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	captureCall := g.at(t, 2)
	if captureCall.Path != "/payment/postauth" {
		t.Errorf("capture path = %q, want /payment/postauth", captureCall.Path)
	}
	captureBody := captureCall.jsonBody(t)
	for key, want := range map[string]any{"paymentId": "ORDER1", "paidPrice": "0.60", "currency": "TRY", "ip": "1.1.1.1"} {
		if captureBody[key] != want {
			t.Errorf("capture %s = %v, want %v", key, captureBody[key], want)
		}
	}

	if _, err := g.provider.VoidAuthorization(ctx, voidRequest()); err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if path := g.at(t, 3).Path; path != "/payment/cancel" {
		t.Errorf("void path = %q, want /payment/cancel", path)
	}
}

func TestIyzicoCaptureValidatesBeforeCallingAPI(t *testing.T) {
	g := newProviderGateway(t, "iyzico", approve("iyzico"))
	request := captureRequest()
	request.Amount = "0"

	result, err := g.provider.Capture(context.Background(), request)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	wantNoCalls(t, g)
	if result.Code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", result.Code, core.CodeInvalidRequest)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q", result.Status, core.StatusFailure)
	}
}

func TestIyzicoCaptureDefaultsCurrencyToTRY(t *testing.T) {
	g := newProviderGateway(t, "iyzico", approve("iyzico"))
	request := captureRequest()
	request.Currency = ""

	if _, err := g.provider.Capture(context.Background(), request); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if got := g.last(t).jsonBody(t)["currency"]; got != "TRY" {
		t.Errorf("currency = %v, want TRY", got)
	}
}

func TestSipayPreAuthorizationWireFormat(t *testing.T) {
	g := newProviderGateway(t, "sipay", approve("sipay"))
	ctx := context.Background()

	if _, err := g.provider.Authorize(ctx, orderRequest()); err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	pay := g.last(t)
	if pay.Path != sipay.EndpointPay2D {
		t.Errorf("authorize path = %q, want %q", pay.Path, sipay.EndpointPay2D)
	}
	if got := pay.jsonBody(t)["transaction_type"]; got != "PreAuth" {
		t.Errorf("transaction_type = %v, want PreAuth", got)
	}
	if got := pay.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}

	if _, err := g.provider.Capture(ctx, captureRequest()); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	confirm := g.last(t)
	if confirm.Path != sipay.EndpointConfirmPayment {
		t.Errorf("capture path = %q, want %q", confirm.Path, sipay.EndpointConfirmPayment)
	}
	confirmBody := confirm.jsonBody(t)
	if confirmBody["status"] != float64(sipay.ConfirmStatusCapture) {
		t.Errorf("capture status = %v, want %d", confirmBody["status"], sipay.ConfirmStatusCapture)
	}
	if confirmBody["total"] != 0.6 {
		t.Errorf("capture total = %v, want 0.6", confirmBody["total"])
	}

	if _, err := g.provider.VoidAuthorization(ctx, voidRequest()); err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	release := g.last(t)
	if release.Path != sipay.EndpointConfirmPayment {
		t.Errorf("void path = %q, want %q", release.Path, sipay.EndpointConfirmPayment)
	}
	if got := release.jsonBody(t)["status"]; got != float64(sipay.ConfirmStatusVoid) {
		t.Errorf("void status = %v, want %d", got, sipay.ConfirmStatusVoid)
	}
}

func TestSipayVoidAuthorizationRejectsSettledPayment(t *testing.T) {
	g := newProviderGateway(t, "sipay", func(c call) (int, string) {
		switch c.Path {
		case sipay.EndpointToken:
			return http.StatusOK, `{"status_code":100,"data":{"token":"test-token"}}`
		case sipay.EndpointCheckStatus:
			return http.StatusOK, `{"status_code":100,"transaction_status":"Completed","transaction_amount":1.2}`
		default:
			return http.StatusOK, `{"status_code":100}`
		}
	})

	result, err := g.provider.VoidAuthorization(context.Background(), voidRequest())
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Errorf("status = %q, want %q: a settled payment is not an open pre-authorization", result.Status, core.StatusFailure)
	}
	if result.ErrorCode != "NOT_CANCELLABLE" {
		t.Errorf("errorCode = %q, want NOT_CANCELLABLE", result.ErrorCode)
	}
	for _, c := range g.recorded() {
		if c.Path == sipay.EndpointConfirmPayment {
			t.Error("a settled payment must not be sent to confirmPayment")
		}
	}
}

func TestAlbarakaPreAuthorizationOperations(t *testing.T) {
	g := newProviderGateway(t, "albaraka", approve("albaraka"))
	ctx := context.Background()

	authorized, err := g.provider.Authorize(ctx, orderRequest())
	if err != nil || authorized.Status != core.StatusSuccess {
		t.Fatalf("Authorize() = %+v, %v", authorized, err)
	}
	authCall := g.at(t, 0)
	if authCall.Path != "/ALBMerchantService/MerchantJSONAPI.svc/Auth" {
		t.Errorf("authorize path = %q, want the JSON Auth operation", authCall.Path)
	}
	if authorized.PaymentID != "REF123" {
		t.Errorf("paymentId = %q, want the bank's ReferenceCode", authorized.PaymentID)
	}

	if _, err := g.provider.Capture(ctx, captureRequest()); err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	captureCall := g.at(t, 1)
	if captureCall.Path != "/ALBMerchantService/MerchantJSONAPI.svc/Capture" {
		t.Errorf("capture path = %q, want the JSON Capture operation", captureCall.Path)
	}
	captureBody := captureCall.jsonBody(t)
	if captureBody["ReferenceCode"] != "ORDER1" {
		t.Errorf("capture ReferenceCode = %v, want ORDER1", captureBody["ReferenceCode"])
	}
	if captureBody["Amount"] != float64(60) {
		t.Errorf("capture Amount = %v, want 60 minor units", captureBody["Amount"])
	}
	if captureBody["CurrencyCode"] != "TL" {
		t.Errorf("capture CurrencyCode = %v, want TL", captureBody["CurrencyCode"])
	}

	if _, err := g.provider.VoidAuthorization(ctx, voidRequest()); err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	voidCall := g.at(t, 2)
	if voidCall.Path != "/ALBMerchantService/MerchantJSONAPI.svc/Reverse" {
		t.Errorf("void path = %q, want the JSON Reverse operation", voidCall.Path)
	}
	if got := voidCall.jsonBody(t)["TransactionType"]; got != "Auth" {
		t.Errorf("void TransactionType = %v, want Auth (releasing a pre-authorization, not a sale)", got)
	}
}

func TestAlbarakaThreeDSAuthorizeFormCarriesAuth(t *testing.T) {
	g := newProviderGateway(t, "albaraka", approve("albaraka"))

	result, err := g.provider.InitThreeDSAuthorize(context.Background(), orderThreeDSRequest())
	if err != nil {
		t.Fatalf("InitThreeDSAuthorize() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Errorf("status = %q, want %q", result.Status, core.StatusPending)
	}
	if !strings.Contains(result.ThreeDSHTMLContent, `name="TransactionType" value="Auth"`) {
		t.Errorf("threeDSHtmlContent must post TransactionType Auth: %s", result.ThreeDSHTMLContent)
	}
	wantNoCalls(t, g)
}

// TestMockProviderPreAuthorizationLifecycle checks the in-memory provider used by
// the test fixtures against the same contract: a pre-authorization is either
// captured or released, never both.
func TestMockProviderPreAuthorizationLifecycle(t *testing.T) {
	provider := fixtures.NewMockProvider()
	ctx := context.Background()

	authorize := func() string {
		t.Helper()
		authorized, err := provider.Authorize(ctx, orderRequest())
		if err != nil {
			t.Fatalf("Authorize() error = %v", err)
		}
		if authorized.Status != core.StatusSuccess || authorized.PaymentID == "" {
			t.Fatalf("Authorize() = %+v, want a successful pre-authorization", authorized)
		}
		return authorized.PaymentID
	}

	captured, err := provider.Capture(ctx, &core.CaptureRequest{
		PaymentID: authorize(), Amount: "0.60", IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if captured.Status != core.StatusSuccess {
		t.Errorf("Capture() = %+v, want success", captured)
	}

	voided, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{
		PaymentID: authorize(), IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if voided.Status != core.StatusSuccess && voided.Status != core.StatusCancelled {
		t.Errorf("VoidAuthorization() = %+v, want success or cancelled", voided)
	}

	// A captured pre-authorization can no longer be released.
	released, err := provider.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{
		PaymentID: captured.PaymentID, IP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("VoidAuthorization() error = %v", err)
	}
	if released.Status != core.StatusFailure {
		t.Errorf("VoidAuthorization() after capture = %+v, want failure", released)
	}
}

package providers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/providers/akbank"
	"github.com/boracomet/go-pos/providers/albaraka"
	"github.com/boracomet/go-pos/providers/denizbank"
	"github.com/boracomet/go-pos/providers/est"
	"github.com/boracomet/go-pos/providers/garanti"
	"github.com/boracomet/go-pos/providers/iyzico"
	"github.com/boracomet/go-pos/providers/kuveytturk"
	"github.com/boracomet/go-pos/providers/parampos"
	"github.com/boracomet/go-pos/providers/paytr"
	"github.com/boracomet/go-pos/providers/qnb"
	"github.com/boracomet/go-pos/providers/sipay"
	"github.com/boracomet/go-pos/providers/vakifbank"
	"github.com/boracomet/go-pos/providers/yapikredi"
	fixtures "github.com/boracomet/go-pos/testing"
)

// Made-up PayTR credentials, shared with the signature assertions.
const (
	paytrTestMerchantID = "123456"
	paytrTestKey        = "test-merchant-key"
	paytrTestSalt       = "test-merchant-salt"
)

// The cross-provider tests below run every gateway against a local test server
// with made-up credentials, so no test reaches a real host.

// call is one HTTP request a provider sent to the test server.
type call struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

// jsonBody decodes a JSON request body.
func (c call) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(c.Body), &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, c.Body)
	}
	return body
}

// formBody decodes an urlencoded request body.
func (c call) formBody(t *testing.T) map[string]string {
	t.Helper()
	values, err := url.ParseQuery(c.Body)
	if err != nil {
		t.Fatalf("request body is not urlencoded: %v\n%s", err, c.Body)
	}
	body := make(map[string]string, len(values))
	for key, value := range values {
		body[key] = value[0]
	}
	return body
}

// soapAction is the local name of the SOAPAction header.
func (c call) soapAction() string {
	return strings.Trim(strings.TrimPrefix(c.Header.Get("SOAPAction"), parampos.Namespace), `"`)
}

// responder answers one call with an HTTP status and a body.
type responder func(c call) (int, string)

// gateway is a provider wired to a local test server that records every request.
type gateway struct {
	name     string
	provider core.Provider
	server   *httptest.Server

	mu    sync.Mutex
	calls []call
	reply responder
}

func (g *gateway) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c := call{Method: r.Method, Path: r.URL.Path, Body: string(raw), Header: r.Header.Clone()}

	g.mu.Lock()
	g.calls = append(g.calls, c)
	reply := g.reply
	g.mu.Unlock()

	status, body := http.StatusOK, "{}"
	if reply != nil {
		status, body = reply(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// recorded returns a copy of the calls made so far.
func (g *gateway) recorded() []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]call, len(g.calls))
	copy(out, g.calls)
	return out
}

func (g *gateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// at returns call i, failing the test when it was never made.
func (g *gateway) at(t *testing.T, i int) call {
	t.Helper()
	calls := g.recorded()
	if i >= len(calls) {
		t.Fatalf("%s: call %d was not made (%d calls)", g.name, i, len(calls))
	}
	return calls[i]
}

// last returns the most recent call, failing the test when none was made.
func (g *gateway) last(t *testing.T) call {
	t.Helper()
	calls := g.recorded()
	if len(calls) == 0 {
		t.Fatalf("%s: no request was sent", g.name)
	}
	return calls[len(calls)-1]
}

// newGateway starts the test server and builds the provider against it.
func newGateway(t *testing.T, name string, reply responder, build func(baseURL string) (core.Provider, error)) *gateway {
	t.Helper()
	g := &gateway{name: name, reply: reply}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)

	provider, err := build(g.server.URL)
	if err != nil {
		t.Fatalf("%s: New() error = %v", name, err)
	}
	g.provider = provider
	return g
}

// providerNames lists every gateway, in a stable order.
var providerNames = []string{
	"akbank", "albaraka", "denizbank", "est", "garanti", "iyzico",
	"kuveytturk", "parampos", "paytr", "qnb", "sipay", "vakifbank", "yapikredi",
}

// newProviderGateway builds the named provider with dummy credentials, pointed
// at a local test server.
func newProviderGateway(t *testing.T, name string, reply responder) *gateway {
	t.Helper()
	return newGateway(t, name, reply, func(base string) (core.Provider, error) {
		cfg := core.ProviderConfig{BaseURL: base, Locale: "tr"}
		switch name {
		case "akbank":
			return akbank.New(akbank.Config{
				ProviderConfig: cfg,
				MerchantSafeID: "2023090417500272654BD9A49CF07574",
				TerminalSafeID: "2023090417500284633D137A249DBBEB",
				SecretKey:      "test-secret-key",
				TestMode:       true,
			})
		case "albaraka":
			return albaraka.New(albaraka.Config{
				ProviderConfig: cfg,
				MerchantID:     "6700000067",
				TerminalID:     "67000067",
				PosnetID:       "1010",
				EncKey:         "TESTENCKEY",
			})
		case "denizbank":
			return denizbank.New(denizbank.Config{
				ProviderConfig: cfg,
				ShopCode:       "3123",
				UserCode:       "test-user",
				UserPass:       "test-pass",
				MerchantPass:   "test-merchant-pass",
			})
		case "est":
			return est.New(est.Config{
				ProviderConfig: cfg,
				Bank:           est.BankIsbank,
				ClientID:       "test-client",
				Username:       "test-user",
				Password:       "test-pass",
				StoreKey:       "test-store-key",
			})
		case "garanti":
			return garanti.New(garanti.Config{
				ProviderConfig: cfg,
				MerchantID:     "7000679",
				TerminalID:     "30691297",
				Password:       "test-pass",
				StoreKey:       "test-store-key",
			})
		case "iyzico":
			return iyzico.New(iyzico.Config{
				ProviderConfig: cfg,
				APIKey:         "test-api-key",
				SecretKey:      "test-secret-key",
			})
		case "kuveytturk":
			return kuveytturk.New(kuveytturk.Config{
				ProviderConfig: cfg,
				MerchantID:     "496",
				CustomerID:     "400235",
				Username:       "test-user",
				Password:       "test-pass",
			})
		case "parampos":
			return parampos.New(parampos.Config{
				ProviderConfig: cfg,
				ClientCode:     "10738",
				ClientUsername: "Test",
				ClientPassword: "Test",
				GUID:           "0c13d406-873b-403b-9c09-a5766840d98c",
			})
		case "paytr":
			return paytr.New(paytr.Config{
				ProviderConfig: cfg,
				MerchantID:     paytrTestMerchantID,
				MerchantKey:    paytrTestKey,
				MerchantSalt:   paytrTestSalt,
			})
		case "qnb":
			return qnb.New(qnb.Config{
				ProviderConfig: cfg,
				MerchantID:     "1",
				UserCode:       "test-user",
				UserPass:       "test-pass",
				MerchantPass:   "test-merchant-pass",
			})
		case "sipay":
			return sipay.New(sipay.Config{
				ProviderConfig: cfg,
				AppID:          "test-app-id",
				AppSecret:      "test-app-secret",
				MerchantKey:    "test-merchant-key",
			})
		case "vakifbank":
			return vakifbank.New(vakifbank.Config{
				ProviderConfig: cfg,
				MerchantID:     "000000000000001",
				Password:       "test-pass",
				TerminalNo:     "VP999999",
			})
		case "yapikredi":
			return yapikredi.New(yapikredi.Config{
				ProviderConfig: cfg,
				MerchantID:     "6700000067",
				TerminalID:     "67000067",
				PosnetID:       "1010",
				EncKey:         "TESTENCKEY",
			})
		default:
			t.Fatalf("unknown provider %q", name)
			return nil, nil
		}
	})
}

// unsupportedNames returns every provider outside the given support set.
func unsupportedNames(supported map[string]bool) []string {
	out := make([]string, 0, len(providerNames))
	for _, name := range providerNames {
		if !supported[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// supportedNames returns the keys of a support set, in a stable order.
func supportedNames(supported map[string]bool) []string {
	out := make([]string, 0, len(supported))
	for name, ok := range supported {
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// soapEnvelope wraps a flat SOAP result the way the Param service answers.
func soapEnvelope(action, result string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">` +
		`<soap:Body><` + action + `Response xmlns="` + parampos.Namespace + `">` +
		`<` + action + `Result>` + result + `</` + action + `Result>` +
		`</` + action + `Response></soap:Body></soap:Envelope>`
}

// iyzicoHTML is the Base64 threeDSHtmlContent iyzico sends.
var iyzicoHTML = base64.StdEncoding.EncodeToString([]byte(`<form id="3ds"></form>`))

// approve answers every gateway with an approved result, so a test can assert
// the happy path of an operation without describing each wire format again.
func approve(name string) responder {
	return func(c call) (int, string) {
		switch name {
		case "iyzico":
			switch {
			case c.Path == "/cardstorage/card" && c.Method == http.MethodDelete:
				return http.StatusOK, `{"status":"success"}`
			case c.Path == "/cardstorage/card":
				return http.StatusOK, `{"status":"success","cardUserKey":"U1","cardToken":"C1",` +
					`"binNumber":"552879","lastFourDigits":"0008","cardAlias":"Work","cardBankName":"Halkbank"}`
			case c.Path == "/cardstorage/cards":
				return http.StatusOK, `{"status":"success","cardUserKey":"U1","cardDetails":[` +
					`{"cardToken":"C1","lastFourDigits":"0008","cardBankName":"Halkbank","cardAlias":"Work"}]}`
			case c.Path == "/payment/auth":
				return http.StatusOK, `{"status":"success","paymentId":"42","conversationId":"ORDER1",` +
					`"cardUserKey":"U1","cardToken":"C1"}`
			default:
				return http.StatusOK, `{"status":"success","paymentId":"42","conversationId":"ORDER1",` +
					`"threeDSHtmlContent":"` + iyzicoHTML + `"}`
			}
		case "parampos":
			return http.StatusOK, soapEnvelope(c.soapAction(),
				`<Islem_ID>6005034747</Islem_ID><UCD_HTML>&lt;form&gt;&lt;/form&gt;</UCD_HTML>`+
					`<Sonuc>1</Sonuc><Sonuc_Str>Approved</Sonuc_Str>`)
		case "akbank":
			return http.StatusOK, `{"responseCode":"VPS-0000","transaction":{"rrn":"RRN1"}}`
		case "albaraka":
			return http.StatusOK, `{"ServiceResponseData":{"ResponseCode":"00","ResponseDescription":"APPROVED"},` +
				`"ReferenceCode":"REF123","AuthCode":"123456"}`
		case "sipay":
			switch c.Path {
			case sipay.EndpointToken:
				return http.StatusOK, `{"status_code":100,"data":{"token":"test-token"}}`
			case sipay.EndpointPay3D:
				return http.StatusOK, `<form id="3dsForm" action="https://example.com/3d"></form>`
			case sipay.EndpointCheckStatus:
				return http.StatusOK, `{"status_code":100,"transaction_status":"Pre-authorization",` +
					`"transaction_amount":1.2,"invoice_id":"ORDER1"}`
			default:
				return http.StatusOK, `{"status_code":100,"ref_no":"REF1",` +
					`"data":{"payment_status":1,"order_no":"VP1","invoice_id":"ORDER1"}}`
			}
		case "paytr":
			switch c.Path {
			case "/odeme/capi/list":
				return http.StatusOK, `[{"ctoken":"CT1","last_4":"4358","month":"12","year":"30",` +
					`"c_bank":"Axess","require_cvv":"1","c_brand":"axess","schema":"VISA"}]`
			case "/odeme/capi/delete":
				return http.StatusOK, `{"status":"success"}`
			default:
				return http.StatusOK, `{"status":"success","utoken":"NEWUT"}`
			}
		default:
			return http.StatusOK, `{}`
		}
	}
}

// orderRequest is the shared payment request, with a fixed order id.
func orderRequest() *core.PaymentRequest {
	request := fixtures.FixturePaymentRequest()
	request.ConversationID = "ORDER1"
	return &request
}

// orderThreeDSRequest is the shared 3D Secure request, with a fixed order id.
func orderThreeDSRequest() *core.ThreeDSPaymentRequest {
	request := fixtures.FixtureThreeDSPaymentRequest()
	request.ConversationID = "ORDER1"
	return &request
}

// orderWithoutCard is orderRequest with the card removed, for stored-card payments.
func orderWithoutCard() *core.PaymentRequest {
	request := orderRequest()
	request.PaymentCard = nil
	return request
}

func captureRequest() *core.CaptureRequest {
	return &core.CaptureRequest{PaymentID: "ORDER1", Amount: "0.60", Currency: "TRY", IP: "1.1.1.1"}
}

func voidRequest() *core.VoidAuthorizationRequest {
	return &core.VoidAuthorizationRequest{PaymentID: "ORDER1", IP: "1.1.1.1"}
}

func saveCardRequest() *core.SaveCardRequest {
	card := fixtures.FixtureCard()
	card.RegisterCard = nil
	return &core.SaveCardRequest{
		Email: "john.doe@example.com",
		Alias: "Work",
		Card:  card,
	}
}

func listCardsRequest() *core.ListCardsRequest {
	return &core.ListCardsRequest{CustomerToken: "U1"}
}

func deleteCardRequest() *core.DeleteCardRequest {
	return &core.DeleteCardRequest{CustomerToken: "U1", CardToken: "C1"}
}

func storedCardReference() *core.StoredCardReference {
	return &core.StoredCardReference{CustomerToken: "USERKEY", CardToken: "CARDTOKEN"}
}

// wantNotSupported asserts the NOT_SUPPORTED error the base provider returns for
// a feature a gateway does not implement. A stub and a real implementation are
// both defined methods, so only the returned code tells them apart.
func wantNotSupported(t *testing.T, name, feature string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: %s returned no error, want NOT_SUPPORTED", name, feature)
	}
	var base *core.BaseError
	if !errors.As(err, &base) {
		t.Fatalf("%s: %s error = %T (%v), want *core.BaseError", name, feature, err, err)
	}
	if base.Code != "NOT_SUPPORTED" {
		t.Errorf("%s: %s code = %q, want NOT_SUPPORTED", name, feature, base.Code)
	}
	if base.Provider != name {
		t.Errorf("%s: %s provider = %q, want %q", name, feature, base.Provider, name)
	}
	if !strings.Contains(base.Message, "not supported") {
		t.Errorf("%s: %s message = %q, want it to say the feature is not supported", name, feature, base.Message)
	}
}

// wantNoCalls asserts the provider rejected the request before reaching the bank.
func wantNoCalls(t *testing.T, g *gateway) {
	t.Helper()
	if n := g.count(); n != 0 {
		t.Errorf("%s: %d requests were sent, want none", g.name, n)
	}
}

// wantValidationFailure asserts a request rejected by validation, which the
// providers report as a FAILURE result rather than an error.
func wantValidationFailure(t *testing.T, status core.PaymentStatus, code core.PaymentErrorCode, message, wantField string) {
	t.Helper()
	if status != core.StatusFailure {
		t.Errorf("status = %q, want %q", status, core.StatusFailure)
	}
	if code != core.CodeInvalidRequest {
		t.Errorf("code = %q, want %q", code, core.CodeInvalidRequest)
	}
	if !strings.Contains(message, wantField) {
		t.Errorf("errorMessage = %q, want it to mention %q", message, wantField)
	}
}

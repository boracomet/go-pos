package testing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

// newTestServer starts a server with every action allowed, as an application
// test needs, and collects the callback statuses reported to OnCallback.
func newTestServer(t *testing.T, onCallback func(status string)) *Server {
	t.Helper()
	server, err := NewServer(ServerOptions{
		Mock: NewMockProvider(MockProviderConfig{
			ProviderConfig: core.ProviderConfig{Doer: forbiddenDoer{t}},
		}),
		Handler: core.HandlerOptions{
			ExposeRawResponse: true,
			OnCallback: func(_ context.Context, result *core.PaymentResponse, _ core.CallbackContext) error {
				if onCallback != nil {
					onCallback(string(result.Status))
				}
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, server *Server, method, path string, body any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	return callURL(t, server, method, server.BasePath+"/mock/"+path, body, headers)
}

func callURL(t *testing.T, server *Server, method, requestURL string, body any, headers map[string]string) *core.HandlerResponse {
	t.Helper()
	requestHeaders := map[string]string{"content-type": "application/json"}
	for name, value := range headers {
		requestHeaders[name] = value
	}
	response, err := server.Handler.Handle(context.Background(), &core.HandlerRequest{
		Method:  method,
		URL:     requestURL,
		Headers: requestHeaders,
		Body:    body,
	})
	if err != nil {
		t.Fatalf("%s %s: unexpected error: %v", method, requestURL, err)
	}
	return response
}

// bodyRecord reads a response body as a record, whether the handler returned a
// typed result (ExposeRawResponse) or a map.
func bodyRecord(t *testing.T, response *core.HandlerResponse) map[string]any {
	t.Helper()
	data, err := json.Marshal(response.Body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal body %s: %v", data, err)
	}
	return record
}

func recordOf(t *testing.T, value any) map[string]any {
	t.Helper()
	record, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want a record", value)
	}
	return record
}

func formBody(fields map[string]string) string {
	values := url.Values{}
	for name, value := range fields {
		values.Set(name, value)
	}
	return values.Encode()
}

const formContentType = "application/x-www-form-urlencoded"

func TestServerCheckoutThreeDSCallbackRefund(t *testing.T) {
	var callbacks []string
	server := newTestServer(t, func(status string) { callbacks = append(callbacks, status) })

	init := call(t, server, http.MethodPost, "payment/init-3ds",
		threeDSRequest(withCard(CardSuccess), "https://shop.test/api/pay/mock/payment/complete-3ds"), nil)
	if init.Status != http.StatusOK {
		t.Fatalf("init-3ds status = %d, want 200 (%v)", init.Status, init.Body)
	}
	initBody := bodyRecord(t, init)
	paymentID, _ := initBody["paymentId"].(string)
	html, _ := initBody["threeDSHtmlContent"].(string)
	if paymentID == "" {
		t.Fatal("init-3ds returned no paymentId")
	}
	if !strings.Contains(html, `action="https://shop.test/api/pay/mock/payment/complete-3ds"`) {
		t.Errorf("threeDSHtmlContent does not post to the callback URL: %s", html)
	}

	// The bank page posts the signed result back as a browser would.
	complete := func() *core.HandlerResponse {
		return call(t, server, http.MethodPost, "payment/complete-3ds",
			formBody(formFieldsOfHTML(html)), map[string]string{"content-type": formContentType})
	}
	result := complete()
	if result.Status != http.StatusOK {
		t.Fatalf("complete-3ds status = %d, want 200 (%v)", result.Status, result.Body)
	}
	record := bodyRecord(t, result)
	if record["status"] != "success" || record["paymentId"] != paymentID {
		t.Errorf("complete-3ds body = %v, want success for %s", record, paymentID)
	}

	// A reload of the return page is deduplicated: OnCallback runs once.
	if again := complete(); again.Status != http.StatusOK {
		t.Errorf("replayed complete-3ds status = %d, want 200", again.Status)
	}
	if len(callbacks) != 1 || callbacks[0] != "success" {
		t.Errorf("callbacks = %v, want [success]", callbacks)
	}

	refundBody := map[string]any{"paymentId": paymentID, "price": "25.00", "currency": "TRY", "ip": "1.1.1.1"}
	refund := call(t, server, http.MethodPost, "refund", refundBody, map[string]string{"idempotency-key": "refund-1"})
	if refund.Status != http.StatusOK {
		t.Fatalf("refund status = %d, want 200 (%v)", refund.Status, refund.Body)
	}
	replay := call(t, server, http.MethodPost, "refund", refundBody, map[string]string{"idempotency-key": "refund-1"})
	if replay.Headers["Idempotent-Replayed"] != "true" {
		t.Errorf("replay headers = %v, want Idempotent-Replayed: true", replay.Headers)
	}

	tooMuch := call(t, server, http.MethodPost, "refund",
		map[string]any{"paymentId": paymentID, "price": "75.01", "currency": "TRY", "ip": "1.1.1.1"}, nil)
	if tooMuch.Status != http.StatusUnprocessableEntity {
		t.Errorf("over-refund status = %d, want 422", tooMuch.Status)
	}

	fetched := call(t, server, http.MethodGet, "payment/"+paymentID, nil, nil)
	if fetched.Status != http.StatusOK {
		t.Fatalf("get payment status = %d, want 200", fetched.Status)
	}
	fetchedBody := bodyRecord(t, fetched)
	if fetchedBody["status"] != "success" {
		t.Errorf("get payment status = %v, want success", fetchedBody["status"])
	}
	raw := recordOf(t, fetchedBody["rawResponse"])
	if raw["refundedAmount"] != float64(2500) {
		t.Errorf("rawResponse.refundedAmount = %v, want 2500", raw["refundedAmount"])
	}
	refunds, ok := raw["refunds"].([]any)
	if !ok || len(refunds) != 1 {
		t.Fatalf("rawResponse.refunds = %v, want one refund", raw["refunds"])
	}
	if amount := recordOf(t, refunds[0])["amount"]; amount != float64(2500) {
		t.Errorf("refunds[0].amount = %v, want 2500", amount)
	}
}

func TestServerFailedThreeDSAndForgedCallback(t *testing.T) {
	var callbacks []string
	server := newTestServer(t, func(status string) { callbacks = append(callbacks, status) })

	init := call(t, server, http.MethodPost, "payment/init-3ds",
		threeDSRequest(withCard(CardThreeDSFailed), "https://shop.test/cb"), nil)
	fields := formFieldsOfHTML(bodyRecord(t, init)["threeDSHtmlContent"].(string))
	if fields["mdStatus"] != "0" {
		t.Fatalf("mdStatus = %q, want 0", fields["mdStatus"])
	}

	failed := call(t, server, http.MethodPost, "payment/complete-3ds", fields, nil)
	if failed.Status != http.StatusUnprocessableEntity {
		t.Fatalf("failed callback status = %d, want 422", failed.Status)
	}
	failedBody := bodyRecord(t, failed)
	if failedBody["status"] != "failure" || failedBody["code"] != string(core.CodeThreeDSFailed) {
		t.Errorf("failed callback body = %v, want failure/THREEDS_FAILED", failedBody)
	}

	forged := map[string]any{}
	for name, value := range fields {
		forged[name] = value
	}
	forged["mdStatus"] = "1"
	forged["paymentId"] = "MOCKPAY999999"
	forgedResponse := call(t, server, http.MethodPost, "payment/complete-3ds", forged, nil)
	if body := bodyRecord(t, forgedResponse); body["code"] != string(core.CodeInvalidHash) {
		t.Errorf("forged callback body = %v, want INVALID_HASH", body)
	}
	if len(callbacks) != 1 || callbacks[0] != "failure" {
		t.Errorf("callbacks = %v, want [failure]", callbacks)
	}
}

func TestServerWorksWithTheBrowserClient(t *testing.T) {
	server := newTestServer(t, nil)

	result, err := server.Payments.Mock.CreatePayment(context.Background(), withCard(CardInsufficientFunds))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != core.StatusFailure || result.Code != core.CodeInsufficientFunds {
		t.Errorf("result = %+v, want failure/INSUFFICIENT_FUNDS", result)
	}

	paid, err := server.Payments.Mock.CreatePayment(context.Background(), withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := server.Payments.Mock.GetPayment(context.Background(), paid.PaymentID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.PaymentID != paid.PaymentID || fetched.Status != core.StatusSuccess {
		t.Errorf("fetched = %+v, want the payment %s", fetched, paid.PaymentID)
	}
}

func TestServerServesTheHealthCheckOverHTTP(t *testing.T) {
	server := newTestServer(t, nil)

	response, err := http.Get(server.URL + server.BasePath + "/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read health body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("health body %s: %v", raw, err)
	}
	if body["status"] != "ok" || body["service"] != "go-pos" {
		t.Errorf("health body = %v, want status ok for go-pos", body)
	}
	if body["version"] != core.Version {
		t.Errorf("health version = %v, want %s", body["version"], core.Version)
	}
}

func TestServerRejectsInvalidInputWithA422(t *testing.T) {
	server := newTestServer(t, nil)

	saved := call(t, server, http.MethodPost, "cards/save", map[string]any{}, nil)
	if saved.Status != http.StatusUnprocessableEntity {
		t.Fatalf("cards/save status = %d, want 422", saved.Status)
	}
	if body := bodyRecord(t, saved); body["errorCode"] != "VALIDATION_ERROR" {
		t.Errorf("cards/save body = %v, want VALIDATION_ERROR", body)
	}
}

func TestServerRejectsInvalidJSONAndUnknownRoutes(t *testing.T) {
	server := newTestServer(t, nil)

	bad := call(t, server, http.MethodPost, "payment", "{not json", nil)
	if bad.Status != http.StatusBadRequest {
		t.Errorf("invalid JSON status = %d, want 400", bad.Status)
	}

	unknown := callURL(t, server, http.MethodGet, server.BasePath+"/unknown-provider/payment", nil, nil)
	if unknown.Status != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", unknown.Status)
	}
	if body := bodyRecord(t, unknown); body["error"] != true {
		t.Errorf("unknown provider body = %v, want error true", body)
	}
}

func TestNewServerDefaults(t *testing.T) {
	server, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(server.Close)

	if server.BasePath != adapters.DefaultBasePath {
		t.Errorf("BasePath = %q, want %q", server.BasePath, adapters.DefaultBasePath)
	}
	if server.Mock == nil || server.Handler == nil || server.Payments == nil {
		t.Fatal("NewServer left Mock, Handler or Payments unset")
	}
	provider, err := server.Provider(string(core.ProviderMock))
	if err != nil {
		t.Fatalf("Provider(mock): %v", err)
	}
	if provider != core.Provider(server.Mock) {
		t.Error("Provider(mock) is not the server's mock")
	}
	if _, err := server.Provider("unknown"); err == nil {
		t.Error("Provider(unknown) returned no error")
	}

	// The production defaults stay in place: a privileged action is not reachable.
	refund := call(t, server, http.MethodPost, "refund",
		map[string]any{"paymentId": "MOCKPAY000001", "price": "1.00", "currency": "TRY", "ip": "1.1.1.1"}, nil)
	if refund.Status != http.StatusNotFound {
		t.Errorf("refund status = %d, want 404 (action not enabled)", refund.Status)
	}
}

func TestNewServerServesAdditionalProvidersOnACustomBasePath(t *testing.T) {
	other := NewMockProvider()
	server, err := NewServer(ServerOptions{
		Providers: map[string]core.Provider{"shop": other},
		Handler:   core.HandlerOptions{BasePath: "/checkout"},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(server.Close)

	if server.BasePath != "/checkout" {
		t.Fatalf("BasePath = %q, want /checkout", server.BasePath)
	}
	if provider, err := server.Provider("shop"); err != nil || provider != core.Provider(other) {
		t.Fatalf("Provider(shop) = %v, %v", provider, err)
	}

	paid, err := other.CreatePayment(context.Background(), withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	response := callURL(t, server, http.MethodGet, "/checkout/shop/payment/"+paid.PaymentID, nil, nil)
	if response.Status != http.StatusOK {
		t.Fatalf("get payment status = %d, want 200 (%v)", response.Status, response.Body)
	}
	if body := bodyRecord(t, response); body["paymentId"] != paid.PaymentID {
		t.Errorf("body = %v, want payment %s", body, paid.PaymentID)
	}
}

func TestNewServerReportsConfigurationErrors(t *testing.T) {
	if _, err := NewServer(ServerOptions{Providers: map[string]core.Provider{"shop": nil}}); err == nil {
		t.Error("a nil provider was accepted")
	}
	if _, err := NewServer(ServerOptions{
		Handler: core.HandlerOptions{AllowedActions: core.AllHandlerActions},
	}); err == nil {
		t.Error("privileged actions without an Authorize hook were accepted")
	}
}

func TestServerRunsEveryOperation(t *testing.T) {
	server := newTestServer(t, nil)
	ctx := context.Background()

	paid, err := server.Mock.CreatePayment(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := server.Mock.Authorize(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}
	voided, err := server.Mock.Authorize(ctx, withCard(CardSuccess))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path   string
		body   any
		status int
	}{
		{"payment", withCard(CardSuccess), http.StatusOK},
		{"authorize", withCard(CardSuccess), http.StatusOK},
		{"capture", &core.CaptureRequest{PaymentID: captured.PaymentID, Amount: "100.00", IP: "1.1.1.1"}, http.StatusOK},
		{"void", &core.VoidAuthorizationRequest{PaymentID: voided.PaymentID, IP: "1.1.1.1"}, http.StatusOK},
		{"refund", refundRequest(paid.PaymentID, "10.00"), http.StatusOK},
		{"cancel", &core.CancelRequest{PaymentID: paid.PaymentID, IP: "1.1.1.1"}, http.StatusUnprocessableEntity},
		{"cards/list", &core.ListCardsRequest{CustomerToken: "user-1"}, http.StatusOK},
		{"bin-check", map[string]string{"binNumber": "424242"}, http.StatusOK},
		{"installment", &core.InstallmentInfoRequest{BinNumber: "424242", Price: "100.00"}, http.StatusOK},
	}
	for _, testCase := range cases {
		response := call(t, server, http.MethodPost, testCase.path, testCase.body, nil)
		if response.Status != testCase.status {
			t.Errorf("%s status = %d, want %d (%v)", testCase.path, response.Status, testCase.status, response.Body)
		}
	}

	// A refunded payment cannot be cancelled, and an unknown one is a failure.
	missing := call(t, server, http.MethodPost, "refund", refundRequest("MOCKPAY999999", "1.00"), nil)
	if missing.Status != http.StatusUnprocessableEntity {
		t.Errorf("unknown payment refund status = %d, want 422", missing.Status)
	}

	card := FixtureCard()
	saved := call(t, server, http.MethodPost, "cards/save", &core.SaveCardRequest{
		CustomerToken: "user-1",
		Alias:         "Work",
		Card:          card,
	}, nil)
	if saved.Status != http.StatusOK {
		t.Fatalf("cards/save status = %d, want 200 (%v)", saved.Status, saved.Body)
	}
	stored := recordOf(t, bodyRecord(t, saved)["card"])
	cardToken, _ := stored["cardToken"].(string)
	if cardToken == "" {
		t.Fatal("cards/save returned no cardToken")
	}

	listed := call(t, server, http.MethodPost, "cards/list", &core.ListCardsRequest{CustomerToken: "user-1"}, nil)
	if cards, ok := bodyRecord(t, listed)["cards"].([]any); !ok || len(cards) != 1 {
		t.Errorf("cards/list body = %v, want one card", bodyRecord(t, listed))
	}

	deleted := call(t, server, http.MethodPost, "cards/delete",
		&core.DeleteCardRequest{CustomerToken: "user-1", CardToken: cardToken}, nil)
	if deleted.Status != http.StatusOK {
		t.Errorf("cards/delete status = %d, want 200 (%v)", deleted.Status, deleted.Body)
	}
}

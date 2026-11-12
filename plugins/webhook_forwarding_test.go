package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	webhookForwardingTestURL    = "https://api.example.com/webhooks/payments"
	webhookForwardingTestSecret = "whsec_test_secret_12345"
)

// webhookForwardingTestDoer answers with the given statuses in order, repeating
// the last one, and records every delivery.
func webhookForwardingTestDoer(statuses ...int) *pluginTestDoer {
	doer := &pluginTestDoer{}
	var mu sync.Mutex
	index := 0
	doer.respond = func(*http.Request) (*http.Response, error) {
		mu.Lock()
		status := statuses[min(index, len(statuses)-1)]
		index++
		mu.Unlock()
		return pluginTestResponse(status, "{}"), nil
	}
	return doer
}

func webhookForwardingTestSetup(t *testing.T, options WebhookForwardingOptions) (core.Plugin, *WebhookForwardingMethods, *pluginTestContext) {
	t.Helper()
	plugin, err := WebhookForwarding(options)
	if err != nil {
		t.Fatalf("webhookForwarding() failed: %v", err)
	}
	pctx := newPluginTestContext()
	pctx.logger = &pluginTestLogger{}
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	methods, ok := pluginTestMethods(t, plugin, pctx)["webhooks"].(*WebhookForwardingMethods)
	if !ok {
		t.Fatal("the webhooks member is missing")
	}
	return plugin, methods, pctx
}

func webhookForwardingTestEmit(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, request, result any) {
	t.Helper()
	event := core.EventFor(operation, "mock", request, result)
	if event == nil {
		t.Fatalf("%s produced no event", operation)
	}
	if err := plugin.Events[core.EventAll](context.Background(), event); err != nil {
		t.Fatalf("the event listener failed: %v", err)
	}
}

func webhookForwardingTestCreate(t *testing.T, plugin core.Plugin, provider *pluginTestProvider, request *core.PaymentRequest) *core.PaymentResponse {
	t.Helper()
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	webhookForwardingTestEmit(t, plugin, core.OperationCreatePayment, request, result)
	return result
}

func webhookForwardingTestFlush(t *testing.T, methods *WebhookForwardingMethods) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := methods.Flush(ctx); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
}

func webhookForwardingTestBody(t *testing.T, doer *pluginTestDoer, index int) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(doer.bodyAt(index)), &body); err != nil {
		t.Fatalf("delivery %d is not JSON: %v", index, err)
	}
	return body
}

func TestWebhookForwardingValidatesConfiguration(t *testing.T) {
	cases := []struct {
		name      string
		endpoints []WebhookEndpoint
	}{
		{"no endpoints", nil},
		{"no url", []WebhookEndpoint{{Secret: webhookForwardingTestSecret}}},
		{"no secret", []WebhookEndpoint{{URL: webhookForwardingTestURL}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := WebhookForwarding(WebhookForwardingOptions{Endpoints: testCase.endpoints})
			if err == nil {
				t.Fatal("webhookForwarding() accepted invalid endpoints")
			}
			if _, ok := err.(*core.ConfigurationError); !ok {
				t.Errorf("error is %T, want *core.ConfigurationError", err)
			}
		})
	}
}

func TestWebhookForwardingDeliversSignedWebhooks(t *testing.T) {
	doer := webhookForwardingTestDoer(200)
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		Endpoints: []WebhookEndpoint{{URL: webhookForwardingTestURL, Secret: webhookForwardingTestSecret}},
	})

	result := webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	if result.Status != core.StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	webhookForwardingTestFlush(t, methods)

	if doer.count() != 1 {
		t.Fatalf("delivered %d times, want 1", doer.count())
	}
	request := doer.requestAt(0)
	if request.URL.String() != webhookForwardingTestURL {
		t.Errorf("url = %q", request.URL)
	}
	if got := request.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	signature := request.Header.Get(webhookForwardingSignatureHeader)
	if signature == "" {
		t.Fatal("the delivery carries no signature")
	}
	if got := request.Header.Get(webhookForwardingSignatureAliasHeader); got != signature {
		t.Errorf("alias signature = %q, want %q", got, signature)
	}
	if !VerifyForwardedWebhook(doer.bodyAt(0), signature, webhookForwardingTestSecret) {
		t.Error("the signature does not verify against the raw body")
	}

	body := webhookForwardingTestBody(t, doer, 0)
	if body["event"] != core.EventPaymentSucceeded {
		t.Errorf("event = %v", body["event"])
	}
	if body["provider"] != "mock" {
		t.Errorf("provider = %v", body["provider"])
	}
	if body["paymentId"] != result.PaymentID {
		t.Errorf("paymentId = %v, want %q", body["paymentId"], result.PaymentID)
	}
	if body["conversationId"] != "123456789" {
		t.Errorf("conversationId = %v", body["conversationId"])
	}
	timestamp, ok := body["timestamp"].(float64)
	if !ok || timestamp <= 0 {
		t.Errorf("timestamp = %v, want a number", body["timestamp"])
	}
	if request.Header.Get(webhookForwardingSignatureTimestampHeader) == "" {
		t.Error("the delivery carries no signature timestamp")
	}
}

func TestWebhookForwardingVerifyDetectsTamperingAndWrongSecrets(t *testing.T) {
	doer := webhookForwardingTestDoer(200)
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		Endpoints: []WebhookEndpoint{{URL: webhookForwardingTestURL, Secret: webhookForwardingTestSecret}},
	})
	webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	webhookForwardingTestFlush(t, methods)

	raw := doer.bodyAt(0)
	signature := doer.requestAt(0).Header.Get(webhookForwardingSignatureHeader)

	if !VerifyForwardedWebhook(raw, signature, webhookForwardingTestSecret) {
		t.Error("the correct payload and secret did not verify")
	}
	if !VerifyForwardedWebhook(raw, "sha256="+signature, webhookForwardingTestSecret) {
		t.Error("the sha256= prefix was not accepted")
	}
	if !VerifyForwardedWebhook([]byte(raw), signature, webhookForwardingTestSecret) {
		t.Error("a byte payload did not verify")
	}
	if VerifyForwardedWebhook(raw+"tampered", signature, webhookForwardingTestSecret) {
		t.Error("a tampered payload verified")
	}
	if VerifyForwardedWebhook(raw, signature, "wrong_secret") {
		t.Error("a wrong secret verified")
	}
	if VerifyForwardedWebhook(raw, "", webhookForwardingTestSecret) {
		t.Error("an empty signature verified")
	}
	if VerifyForwardedWebhook(raw, signature, "") {
		t.Error("an empty secret verified")
	}
}

func TestWebhookForwardingFiltersEventsPerEndpoint(t *testing.T) {
	const successURL = "https://api.example.com/webhooks/success-only"
	const failedURL = "https://api.example.com/webhooks/failed-only"
	doer := webhookForwardingTestDoer(200)
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer: doer,
		Endpoints: []WebhookEndpoint{
			{URL: successURL, Secret: webhookForwardingTestSecret, Events: []core.PaymentEventName{core.EventPaymentSucceeded}},
			{URL: failedURL, Secret: webhookForwardingTestSecret, Events: []core.PaymentEventName{core.EventPaymentFailed}},
		},
	})

	webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	webhookForwardingTestFlush(t, methods)
	if doer.count() != 1 {
		t.Fatalf("delivered %d times after the success, want 1", doer.count())
	}
	if got := doer.requestAt(0).URL.String(); got != successURL {
		t.Errorf("url = %q, want the success endpoint", got)
	}
	if body := webhookForwardingTestBody(t, doer, 0); body["event"] != core.EventPaymentSucceeded {
		t.Errorf("event = %v", body["event"])
	}

	declined := pluginTestPaymentRequest()
	declined.PaymentCard.CardNumber = "4000000000009995"
	provider := newPluginTestProvider("mock")
	provider.payment = &core.PaymentResponse{
		Status:       core.StatusFailure,
		Code:         core.CodeInsufficientFunds,
		ErrorCode:    string(core.CodeInsufficientFunds),
		ErrorMessage: "Insufficient funds",
	}
	webhookForwardingTestCreate(t, plugin, provider, declined)
	webhookForwardingTestFlush(t, methods)
	if doer.count() != 2 {
		t.Fatalf("delivered %d times after the failure, want 2", doer.count())
	}
	if got := doer.requestAt(1).URL.String(); got != failedURL {
		t.Errorf("url = %q, want the failed endpoint", got)
	}
	if body := webhookForwardingTestBody(t, doer, 1); body["event"] != core.EventPaymentFailed {
		t.Errorf("event = %v", body["event"])
	}
}

func TestWebhookForwardingRetriesWithBackoff(t *testing.T) {
	doer := webhookForwardingTestDoer(500, 502, 200)
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		Retry:     &WebhookRetry{Attempts: pluginTestPtr(3), DelayMs: pluginTestPtr(10)},
		Endpoints: []WebhookEndpoint{{URL: webhookForwardingTestURL, Secret: webhookForwardingTestSecret}},
	})

	started := time.Now()
	webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	webhookForwardingTestFlush(t, methods)

	if doer.count() != 3 {
		t.Fatalf("delivered %d times, want 3", doer.count())
	}
	for i := 0; i < doer.count(); i++ {
		if got := doer.requestAt(i).URL.String(); got != webhookForwardingTestURL {
			t.Errorf("delivery %d url = %q", i, got)
		}
	}
	// 10ms then 20ms of backoff between the three tries.
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond {
		t.Errorf("the retries took %s, want at least the 30ms backoff", elapsed)
	}
}

func TestWebhookForwardingFailingEndpointDoesNotFailThePayment(t *testing.T) {
	const brokenURL = "https://broken.endpoint.com"
	doer := webhookForwardingTestDoer(500)
	var mu sync.Mutex
	var failures []WebhookEndpoint
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:  doer,
		Retry: &WebhookRetry{Attempts: pluginTestPtr(2), DelayMs: pluginTestPtr(5)},
		OnError: func(_ error, endpoint WebhookEndpoint, _ ForwardedWebhookPayload) {
			mu.Lock()
			defer mu.Unlock()
			failures = append(failures, endpoint)
		},
		Endpoints: []WebhookEndpoint{{URL: brokenURL, Secret: webhookForwardingTestSecret}},
	})

	result := webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	webhookForwardingTestFlush(t, methods)

	if doer.count() != 2 {
		t.Errorf("delivered %d times, want 2", doer.count())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failures) != 1 {
		t.Fatalf("onError was called %d times, want 1", len(failures))
	}
	if failures[0].URL != brokenURL {
		t.Errorf("onError endpoint = %q", failures[0].URL)
	}
}

func TestWebhookForwardingLogsFailuresWithoutOnError(t *testing.T) {
	doer := webhookForwardingTestDoer(500)
	plugin, methods, pctx := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		Retry:     &WebhookRetry{Attempts: pluginTestPtr(1)},
		Endpoints: []WebhookEndpoint{{URL: webhookForwardingTestURL, Secret: webhookForwardingTestSecret}},
	})
	webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	webhookForwardingTestFlush(t, methods)

	logger := pctx.logger.(*pluginTestLogger)
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Message, webhookForwardingTestURL) {
		t.Errorf("log message = %q, want it to name the endpoint", entries[0].Message)
	}
	if entries[0].Meta["event"] != core.EventPaymentSucceeded {
		t.Errorf("log meta = %v", entries[0].Meta)
	}
}

func TestWebhookForwardingSanitizesCardDetails(t *testing.T) {
	doer := webhookForwardingTestDoer(200)
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		Endpoints: []WebhookEndpoint{{URL: webhookForwardingTestURL, Secret: webhookForwardingTestSecret}},
	})
	provider := newPluginTestProvider("mock")
	provider.payment = &core.PaymentResponse{
		Status:    core.StatusSuccess,
		PaymentID: "mock-payment",
		RawResponse: map[string]any{
			"status": "SUCCESS",
			"card": map[string]any{
				"cardNumber":     "5528790000000008",
				"cvv":            "123",
				"securityCode":   "123",
				"expireMonth":    "12",
				"expireYear":     "2030",
				"cardHolderName": "John Doe",
			},
			"cardNumber": "5528790000000008",
			"cvv":        "123",
		},
	}
	webhookForwardingTestCreate(t, plugin, provider, pluginTestPaymentRequest())
	webhookForwardingTestFlush(t, methods)

	if doer.count() != 1 {
		t.Fatalf("delivered %d times, want 1", doer.count())
	}
	raw := doer.bodyAt(0)
	for _, secret := range []string{"5528790000000008", "cardHolderName", "securityCode", "expireMonth", "expireYear", `"cvv"`, `"card"`} {
		if strings.Contains(raw, secret) {
			t.Errorf("the forwarded payload still carries %q", secret)
		}
	}
	body := webhookForwardingTestBody(t, doer, 0)
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v, want an object", body["result"])
	}
	for _, key := range []string{"card", "cardNumber", "cvv"} {
		if _, present := result[key]; present {
			t.Errorf("the forwarded result still carries %q", key)
		}
	}
	nested, ok := result["rawResponse"].(map[string]any)
	if !ok {
		t.Fatalf("rawResponse = %v, want an object", result["rawResponse"])
	}
	if nested["status"] != "SUCCESS" {
		t.Errorf("the sanitizer dropped unrelated data: %v", nested)
	}
}

func TestWebhookForwardingSendsCustomHeadersAndWaitUntil(t *testing.T) {
	doer := webhookForwardingTestDoer(200)
	var waits []<-chan struct{}
	plugin, methods, _ := webhookForwardingTestSetup(t, WebhookForwardingOptions{
		Doer:      doer,
		WaitUntil: func(done <-chan struct{}) { waits = append(waits, done) },
		Endpoints: []WebhookEndpoint{{
			URL:     webhookForwardingTestURL,
			Secret:  webhookForwardingTestSecret,
			Headers: map[string]string{"x-tenant": "acme", "content-type": "application/vnd.acme+json"},
		}},
	})
	webhookForwardingTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	if len(waits) != 1 || waits[0] == nil {
		t.Fatalf("waitUntil received %v, want one pending delivery", waits)
	}
	webhookForwardingTestFlush(t, methods)

	request := doer.requestAt(0)
	if got := request.Header.Get("x-tenant"); got != "acme" {
		t.Errorf("x-tenant = %q", got)
	}
	if got := request.Header.Get("content-type"); got != "application/vnd.acme+json" {
		t.Errorf("content-type = %q, want the endpoint override", got)
	}
}

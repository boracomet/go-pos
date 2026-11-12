package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// The headers a forwarded webhook carries. The signature is sent twice, under
// the generic name and under a library-specific alias.
const (
	webhookForwardingSignatureHeader          = "x-signature"
	webhookForwardingSignatureAliasHeader     = "x-go-pos-signature"
	webhookForwardingSignatureTimestampHeader = "x-signature-timestamp"
)

// WebhookEndpoint is one delivery target of the webhook forwarding plugin.
type WebhookEndpoint struct {
	// URL is the target webhook URL the event is delivered to.
	URL string
	// Secret signs the payload with HMAC-SHA256.
	Secret string
	// Events are the event types forwarded to this endpoint. Use "*" for all
	// events. Default: all.
	Events []core.PaymentEventName
	// Headers are extra headers sent with the POST. They override the defaults.
	Headers map[string]string
	// Retry overrides the default retry policy for this endpoint.
	Retry *WebhookRetry
}

// WebhookRetry is the delivery retry policy. A nil field keeps the default.
type WebhookRetry struct {
	// Attempts is the total number of delivery tries. Default: 3.
	Attempts *int
	// DelayMs is the first backoff delay in milliseconds; it doubles per attempt.
	// Default: 1000.
	DelayMs *int
}

// ForwardedWebhookPayload is the signed JSON body a forwarded webhook carries.
// Result is stripped of sensitive cardholder details.
type ForwardedWebhookPayload struct {
	Event          core.PaymentEventType `json:"event"`
	Timestamp      int64                 `json:"timestamp"`
	Provider       string                `json:"provider"`
	Operation      core.PaymentOperation `json:"operation"`
	PaymentID      string                `json:"paymentId,omitempty"`
	ConversationID string                `json:"conversationId,omitempty"`
	Amount         string                `json:"amount,omitempty"`
	Currency       string                `json:"currency,omitempty"`
	Result         any                   `json:"result"`
}

// WebhookForwardingOptions configures the webhook forwarding plugin.
type WebhookForwardingOptions struct {
	// Endpoints are the delivery targets. At least one is required.
	Endpoints []WebhookEndpoint
	// Retry is the default retry policy for failed deliveries.
	Retry *WebhookRetry
	// TimeoutMs is the request timeout in milliseconds. Default: 10000.
	TimeoutMs int
	// Doer is the transport used for deliveries. Default: http.DefaultClient.
	Doer core.Doer
	// OnError is called when an endpoint delivery fails. Default: the payment
	// logger's Error.
	OnError func(err error, endpoint WebhookEndpoint, payload ForwardedWebhookPayload)
	// WaitUntil keeps serverless runtimes alive until deliveries finish. It
	// receives the channel closed when the work is done.
	WaitUntil func(done <-chan struct{})
}

// webhookForwardingSensitiveKeys are stripped from the forwarded result. The
// match is case-sensitive, as it is on the wire.
var webhookForwardingSensitiveKeys = map[string]struct{}{
	"card":           {},
	"cardNumber":     {},
	"cvv":            {},
	"securityCode":   {},
	"expireMonth":    {},
	"expireYear":     {},
	"cardHolderName": {},
}

func webhookForwardingSanitize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if _, sensitive := webhookForwardingSensitiveKeys[key]; sensitive {
				continue
			}
			clean[key] = webhookForwardingSanitize(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, item := range typed {
			clean[i] = webhookForwardingSanitize(item)
		}
		return clean
	default:
		return value
	}
}

// VerifyForwardedWebhook verifies a forwarded webhook HMAC-SHA256 signature
// against the raw payload and the shared secret. Payload is the raw body (string
// or bytes) or a value serialized to JSON; signature is the x-signature header,
// with an optional "sha256=" prefix. An empty signature or secret never matches.
func VerifyForwardedWebhook(payload any, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	cleanSignature := strings.TrimPrefix(signature, "sha256=")
	var raw []byte
	switch typed := payload.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	case json.RawMessage:
		raw = typed
	default:
		encoded, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		raw = encoded
	}
	expected, err := core.HMAC(core.SHA256, []byte(secret), raw)
	if err != nil {
		return false
	}
	return core.SafeEqual(strings.ToLower(cleanSignature), strings.ToLower(core.ToHex(expected)))
}

type webhookForwardingRuntime struct {
	options      WebhookForwardingOptions
	timeout      time.Duration
	defaultTries int
	defaultDelay int
	doer         core.Doer
	group        pluginTaskGroup
	mu           sync.Mutex
	logger       core.Logger
}

func (r *webhookForwardingRuntime) setLogger(logger core.Logger) {
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
}

func (r *webhookForwardingRuntime) log() core.Logger {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logger
}

func webhookForwardingStatusText(response *http.Response) string {
	if response.Status == "" {
		return http.StatusText(response.StatusCode)
	}
	return strings.TrimSpace(strings.TrimPrefix(response.Status, strconv.Itoa(response.StatusCode)))
}

func (r *webhookForwardingRuntime) deliver(endpoint WebhookEndpoint, payload ForwardedWebhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	mac, err := core.HMAC(core.SHA256, []byte(endpoint.Secret), body)
	if err != nil {
		return err
	}
	signature := core.ToHex(mac)

	attempts, baseDelay := r.defaultTries, r.defaultDelay
	if endpoint.Retry != nil {
		if endpoint.Retry.Attempts != nil {
			attempts = *endpoint.Retry.Attempts
		}
		if endpoint.Retry.DelayMs != nil {
			baseDelay = *endpoint.Retry.DelayMs
		}
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		err := r.post(endpoint, payload, body, signature)
		if err == nil {
			return nil
		}
		if attempt >= attempts {
			return err
		}
		if waitErr := pluginSleep(context.Background(), webhookForwardingBackoff(baseDelay, attempt)); waitErr != nil {
			return err
		}
	}
	return nil
}

// webhookForwardingBackoff doubles the base delay for every attempt already made.
func webhookForwardingBackoff(baseDelay, attempt int) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return time.Duration(delay) * time.Millisecond
}

func (r *webhookForwardingRuntime) post(endpoint WebhookEndpoint, payload ForwardedWebhookPayload, body []byte, signature string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set(webhookForwardingSignatureHeader, signature)
	request.Header.Set(webhookForwardingSignatureAliasHeader, signature)
	request.Header.Set(webhookForwardingSignatureTimestampHeader, strconv.FormatInt(payload.Timestamp, 10))
	for name, value := range endpoint.Headers {
		request.Header.Set(name, value)
	}
	response, err := r.doer.Do(request)
	if err != nil {
		return err
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("HTTP %d %s", response.StatusCode, webhookForwardingStatusText(response))
	}
	return nil
}

func (r *webhookForwardingRuntime) report(err error, endpoint WebhookEndpoint, payload ForwardedWebhookPayload) {
	// A failing error callback must not affect the main runtime.
	_ = pluginCall(func() error {
		if r.options.OnError != nil {
			r.options.OnError(err, endpoint, payload)
			return nil
		}
		if logger := r.log(); logger != nil {
			logger.Error(
				fmt.Sprintf("webhookForwarding: delivery to %s failed", endpoint.URL),
				err,
				map[string]any{"event": string(payload.Event), "paymentId": payload.PaymentID},
			)
		}
		return nil
	})
}

func (r *webhookForwardingRuntime) forward(event *core.PaymentEvent) error {
	endpoints := make([]WebhookEndpoint, 0, len(r.options.Endpoints))
	for _, endpoint := range r.options.Endpoints {
		events := endpoint.Events
		if len(events) == 0 {
			events = []core.PaymentEventName{core.EventAll}
		}
		for _, name := range events {
			if name == core.EventAll || string(name) == string(event.Type) {
				endpoints = append(endpoints, endpoint)
				break
			}
		}
	}
	if len(endpoints) == 0 {
		return nil
	}

	payload := ForwardedWebhookPayload{
		Event:          event.Type,
		Timestamp:      pluginNowMs(),
		Provider:       event.Provider,
		Operation:      event.Operation,
		PaymentID:      event.PaymentID,
		ConversationID: event.ConversationID,
		Amount:         event.Amount,
		Currency:       event.Currency,
		Result:         webhookForwardingSanitize(pluginGeneric(event.Result)),
	}

	var waiting sync.WaitGroup
	for _, endpoint := range endpoints {
		waiting.Add(1)
		go func(endpoint WebhookEndpoint) {
			defer waiting.Done()
			if err := r.deliver(endpoint, payload); err != nil {
				r.report(err, endpoint, payload)
			}
		}(endpoint)
	}
	waiting.Wait()
	return nil
}

// WebhookForwardingMethods is the "webhooks" member the plugin adds to the facade.
type WebhookForwardingMethods struct {
	runtime *webhookForwardingRuntime
}

// Flush waits for every pending webhook delivery.
func (m *WebhookForwardingMethods) Flush(ctx context.Context) error {
	return m.runtime.group.flush(ctx)
}

// WebhookForwarding forwards payment events to external HTTP endpoints as signed
// JSON webhooks with HMAC-SHA256 signatures, retry with exponential backoff and
// card data sanitization. Slow or failing endpoints run in the background and
// never block or fail the payment.
func WebhookForwarding(options WebhookForwardingOptions) (core.Plugin, error) {
	if len(options.Endpoints) == 0 {
		return core.Plugin{}, core.NewConfigurationError("webhookForwarding: endpoints must be a non-empty array", "")
	}
	for _, endpoint := range options.Endpoints {
		if endpoint.URL == "" {
			return core.Plugin{}, core.NewConfigurationError("webhookForwarding: each endpoint must have a valid url string", "")
		}
		if endpoint.Secret == "" {
			return core.Plugin{}, core.NewConfigurationError("webhookForwarding: each endpoint must have a secret string", "")
		}
	}

	timeoutMs := options.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 10000
	}
	attempts, delay := 3, 1000
	if options.Retry != nil {
		if options.Retry.Attempts != nil {
			attempts = *options.Retry.Attempts
		}
		if options.Retry.DelayMs != nil {
			delay = *options.Retry.DelayMs
		}
	}
	runtime := &webhookForwardingRuntime{
		options:      options,
		timeout:      time.Duration(timeoutMs) * time.Millisecond,
		defaultTries: attempts,
		defaultDelay: delay,
		doer:         pluginDoer(options.Doer),
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDWebhookForwarding,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			runtime.setLogger(pctx.Logger())
			return nil
		},
		Events: core.PaymentEventListeners{
			core.EventAll: func(_ context.Context, event *core.PaymentEvent) error {
				done := runtime.group.start(func() {
					if err := pluginCall(func() error { return runtime.forward(event) }); err != nil {
						if logger := runtime.log(); logger != nil {
							logger.Error("webhookForwarding: unexpected event handler error", err, nil)
						}
					}
				})
				if options.WaitUntil != nil {
					options.WaitUntil(done)
				}
				return nil
			},
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"webhooks": &WebhookForwardingMethods{runtime: runtime}}, nil
		},
	}), nil
}

package adapters_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
	postest "github.com/boracomet/go-pos/testing"
)

const (
	formContentType = "application/x-www-form-urlencoded"
	jsonContentType = "application/json"
)

// notificationProvider stands in for a provider that answers server-to-server
// notifications with plain text. It verifies an HMAC over the notification
// fields, so a forged notification is rejected without any network access.
type notificationProvider struct {
	core.BaseProvider
	key  string
	salt string
}

func newNotificationProvider() *notificationProvider {
	return &notificationProvider{
		BaseProvider: core.NewBaseProvider("paytr", core.ProviderConfig{}),
		key:          "KEY",
		salt:         "SALT",
	}
}

// CallbackAcknowledgement is the plain-text body the provider re-sends until it
// receives "OK".
func (p *notificationProvider) CallbackAcknowledgement() core.CallbackAcknowledgement {
	return core.CallbackAcknowledgement{Success: "OK", Failure: "bad hash"}
}

func (p *notificationProvider) CompleteThreeDSPayment(_ context.Context, callbackData any) (*core.PaymentResponse, error) {
	fields := notificationFields(callbackData)
	orderID, status, total := fields["merchant_oid"], fields["status"], fields["total_amount"]
	signature, err := core.HMAC(core.SHA256, []byte(p.key), []byte(orderID+p.salt+status+total))
	if err != nil {
		return nil, err
	}
	if !core.SafeEqual(core.ToBase64(signature), fields["hash"]) {
		out := &core.PaymentResponse{PaymentID: orderID}
		out.SetFailure(core.Failure{
			Status:       core.StatusFailure,
			Code:         core.CodeInvalidHash,
			ErrorCode:    "INVALID_HASH",
			ErrorMessage: "Invalid notification signature",
		})
		return out, nil
	}
	return &core.PaymentResponse{PaymentID: orderID, Status: core.StatusSuccess}, nil
}

func (p *notificationProvider) CreatePayment(context.Context, *core.PaymentRequest) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("Direct payments")
}

func (p *notificationProvider) InitThreeDSPayment(context.Context, *core.ThreeDSPaymentRequest) (*core.ThreeDSInitResponse, error) {
	return nil, p.NotSupported("3D Secure")
}

func (p *notificationProvider) Refund(context.Context, *core.RefundRequest) (*core.RefundResponse, error) {
	return nil, p.NotSupported("Refunds")
}

func (p *notificationProvider) Cancel(context.Context, *core.CancelRequest) (*core.CancelResponse, error) {
	return nil, p.NotSupported("Cancellations")
}

func (p *notificationProvider) GetPayment(context.Context, string) (*core.PaymentResponse, error) {
	return nil, p.NotSupported("Payment queries")
}

func notificationFields(callbackData any) map[string]string {
	fields := map[string]string{}
	switch data := callbackData.(type) {
	case map[string]string:
		for name, value := range data {
			fields[name] = value
		}
	case map[string]any:
		for name, value := range data {
			fields[name], _ = value.(string)
		}
	case url.Values:
		for name := range data {
			fields[name] = data.Get(name)
		}
	}
	return fields
}

// newPaymentHandler serves the in-memory mock provider and the notification
// provider, with every action allowed and a browser redirect after a callback.
func newPaymentHandler(t *testing.T) *core.Handler {
	t.Helper()
	server, err := postest.NewServer(postest.ServerOptions{
		Providers: map[string]core.Provider{"paytr": newNotificationProvider()},
		Handler: core.HandlerOptions{
			CallbackRedirect: func(_ context.Context, result *core.PaymentResponse, _ core.CallbackContext) (string, error) {
				return "/orders/" + result.PaymentID + "?payment=" + string(result.Status), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(server.Close)
	return server.Handler
}

// facade is a payment facade exposing its handler, the shape ResolveHandler
// accepts besides a bare handler.
type facade struct{ handler *core.Handler }

func (f facade) Handler() *core.Handler { return f.handler }

type reply struct {
	status  int
	headers http.Header
	text    string
}

func (r reply) contentType() string { return r.headers.Get("Content-Type") }

func (r reply) record(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(r.text), &body); err != nil {
		t.Fatalf("body %q: %v", r.text, err)
	}
	return body
}

func send(t *testing.T, handler http.Handler, method, path, body, contentType string) reply {
	t.Helper()
	request := requestWithBody(method, path, body, contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return readReply(t, recorder)
}

func requestWithBody(method, path, body, contentType string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}

func readReply(t *testing.T, recorder *httptest.ResponseRecorder) reply {
	t.Helper()
	response := recorder.Result()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return reply{status: response.StatusCode, headers: response.Header, text: string(raw)}
}

var formFieldRe = regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)

func formFieldsOfHTML(html string) map[string]string {
	fields := map[string]string{}
	for _, match := range formFieldRe.FindAllStringSubmatch(html, -1) {
		fields[match[1]] = match[2]
	}
	return fields
}

func formBody(fields map[string]string) string {
	values := url.Values{}
	for name, value := range fields {
		values.Set(name, value)
	}
	return values.Encode()
}

func threeDSBody(t *testing.T) string {
	t.Helper()
	request := postest.FixturePaymentRequest()
	request.CallbackURL = "https://shop.test/return"
	data, err := json.Marshal(&core.ThreeDSPaymentRequest{PaymentRequest: request})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(data)
}

func notificationBody(t *testing.T, hash string) string {
	t.Helper()
	if hash == "" {
		signature, err := core.HMAC(core.SHA256, []byte("KEY"), []byte("ORDER1SALTsuccess10000"))
		if err != nil {
			t.Fatalf("sign notification: %v", err)
		}
		hash = core.ToBase64(signature)
	}
	return formBody(map[string]string{
		"merchant_oid": "ORDER1",
		"status":       "success",
		"total_amount": "10000",
		"hash":         hash,
	})
}

// adapterVariants are the ways a Go application mounts the handler. They all
// speak net/http, so one adapter serves stdlib, chi and any router built on it.
func adapterVariants() map[string]func(*testing.T, *core.Handler) http.Handler {
	return map[string]func(*testing.T, *core.Handler) http.Handler{
		"handler": func(_ *testing.T, handler *core.Handler) http.Handler {
			return adapters.HTTPHandler(handler)
		},
		"mounted on a mux": func(_ *testing.T, handler *core.Handler) http.Handler {
			mux := http.NewServeMux()
			mux.Handle("/api/pay/", adapters.HTTPHandler(handler))
			return mux
		},
		"middleware": func(_ *testing.T, handler *core.Handler) http.Handler {
			return adapters.Middleware("/api/pay", handler)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "outside the payment routes", http.StatusNotFound)
			}))
		},
		"middleware on the root": func(_ *testing.T, handler *core.Handler) http.Handler {
			return adapters.Middleware("/", handler)(http.NotFoundHandler())
		},
		"facade": func(_ *testing.T, handler *core.Handler) http.Handler {
			return adapters.HTTPHandler(facade{handler: handler})
		},
		"lazy source": func(t *testing.T, handler *core.Handler) http.Handler {
			calls := 0
			t.Cleanup(func() {
				if calls == 0 {
					t.Error("the lazy source was never resolved")
				}
			})
			return adapters.HTTPHandler(func() (adapters.HandlerSource, error) {
				calls++
				return handler, nil
			})
		},
	}
}

func TestAdapterThreeDSecureFlow(t *testing.T) {
	for name, mount := range adapterVariants() {
		t.Run(name, func(t *testing.T) {
			handler := mount(t, newPaymentHandler(t))

			init := send(t, handler, http.MethodPost, "/api/pay/mock/payment/init-3ds", threeDSBody(t), jsonContentType)
			if init.status != http.StatusOK {
				t.Fatalf("init-3ds status = %d, want 200 (%s)", init.status, init.text)
			}
			if !strings.Contains(init.contentType(), jsonContentType) {
				t.Errorf("init-3ds content type = %q, want JSON", init.contentType())
			}
			body := init.record(t)
			if body["status"] != "pending" {
				t.Errorf("init-3ds status = %v, want pending", body["status"])
			}
			paymentID, _ := body["paymentId"].(string)
			html, _ := body["threeDSHtmlContent"].(string)
			if paymentID == "" || html == "" {
				t.Fatalf("init-3ds body = %v, want a paymentId and HTML", body)
			}

			// The bank posts the form back: 303 to the redirect, no body.
			complete := send(t, handler, http.MethodPost, "/api/pay/mock/payment/complete-3ds",
				formBody(formFieldsOfHTML(html)), formContentType)
			if complete.status != http.StatusSeeOther {
				t.Fatalf("complete-3ds status = %d, want 303 (%s)", complete.status, complete.text)
			}
			if location := complete.headers.Get("Location"); location != "/orders/"+paymentID+"?payment=success" {
				t.Errorf("Location = %q, want /orders/%s?payment=success", location, paymentID)
			}
			if complete.text != "" {
				t.Errorf("complete-3ds body = %q, want empty", complete.text)
			}

			fetched := send(t, handler, http.MethodGet, "/api/pay/mock/payment/"+paymentID, "", "")
			if fetched.status != http.StatusOK {
				t.Fatalf("get payment status = %d, want 200", fetched.status)
			}
			if record := fetched.record(t); record["status"] != "success" || record["paymentId"] != paymentID {
				t.Errorf("get payment body = %v, want success for %s", record, paymentID)
			}
		})
	}
}

func TestAdapterPlainTextNotification(t *testing.T) {
	for name, mount := range adapterVariants() {
		t.Run(name, func(t *testing.T) {
			handler := mount(t, newPaymentHandler(t))

			ok := send(t, handler, http.MethodPost, "/api/pay/paytr/callback", notificationBody(t, ""), formContentType)
			if ok.status != http.StatusOK {
				t.Fatalf("notification status = %d, want 200 (%s)", ok.status, ok.text)
			}
			if ok.text != "OK" {
				t.Errorf("notification body = %q, want OK", ok.text)
			}
			if !strings.Contains(ok.contentType(), "text/plain") {
				t.Errorf("notification content type = %q, want text/plain", ok.contentType())
			}

			forged := send(t, handler, http.MethodPost, "/api/pay/paytr/callback", notificationBody(t, "forged"), formContentType)
			if forged.status != http.StatusBadRequest {
				t.Errorf("forged notification status = %d, want 400", forged.status)
			}
		})
	}
}

func TestAdapterRejectsAnOversizedBody(t *testing.T) {
	for name, mount := range adapterVariants() {
		t.Run(name, func(t *testing.T) {
			handler := mount(t, newPaymentHandler(t))
			response := send(t, handler, http.MethodPost, "/api/pay/paytr/callback",
				"a="+strings.Repeat("x", 2*1024*1024), formContentType)
			if response.status != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", response.status)
			}
			if body := response.record(t); body["error"] != true {
				t.Errorf("body = %v, want error true", body)
			}
		})
	}
}

func TestAdapterRejectsInvalidJSONAndServesHealth(t *testing.T) {
	for name, mount := range adapterVariants() {
		t.Run(name, func(t *testing.T) {
			handler := mount(t, newPaymentHandler(t))

			bad := send(t, handler, http.MethodPost, "/api/pay/mock/payment", "{not json", jsonContentType)
			if bad.status != http.StatusBadRequest {
				t.Errorf("invalid JSON status = %d, want 400", bad.status)
			}

			health := send(t, handler, http.MethodGet, "/api/pay/health", "", "")
			if health.status != http.StatusOK {
				t.Fatalf("health status = %d, want 200", health.status)
			}
			if body := health.record(t); body["status"] != "ok" {
				t.Errorf("health body = %v, want status ok", body)
			}
		})
	}
}

func TestAdapterAnswers404ForAnUnknownProvider(t *testing.T) {
	for name, mount := range adapterVariants() {
		t.Run(name, func(t *testing.T) {
			handler := mount(t, newPaymentHandler(t))
			response := send(t, handler, http.MethodGet, "/api/pay/unknown-provider/payment", "", "")
			if response.status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", response.status)
			}
			if body := response.record(t); body["error"] != true {
				t.Errorf("body = %v, want error true", body)
			}
		})
	}
}

func TestMiddlewarePassesOtherRequestsThrough(t *testing.T) {
	handler := newPaymentHandler(t)
	middleware := adapters.Middleware("/api/pay", handler)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	if response := send(t, middleware, http.MethodGet, "/shop/orders", "", ""); response.status != http.StatusTeapot {
		t.Errorf("outside the base path status = %d, want 418", response.status)
	}
	if response := send(t, middleware, http.MethodGet, "/api/pay/health", "", ""); response.status != http.StatusOK {
		t.Errorf("health status = %d, want 200", response.status)
	}
	// The base path itself belongs to the handler, which answers 404 for it.
	if response := send(t, middleware, http.MethodGet, "/api/pay", "", ""); response.status != http.StatusNotFound {
		t.Errorf("base path status = %d, want the handler's 404", response.status)
	}

	defaulted := adapters.Middleware("", handler)(http.NotFoundHandler())
	if response := send(t, defaulted, http.MethodGet, "/api/pay/health", "", ""); response.status != http.StatusOK {
		t.Errorf("default base path status = %d, want 200", response.status)
	}
}

// chunkedReader hands out the body in pieces, the way a socket does.
type chunkedReader struct {
	chunks [][]byte
	index  int
	read   int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	n := copy(p, chunk)
	r.read += n
	if n == len(chunk) {
		r.index++
	} else {
		r.chunks[r.index] = chunk[n:]
	}
	return n, nil
}

func streamedRequest(path string, chunks [][]byte, contentType string) (*http.Request, *chunkedReader) {
	reader := &chunkedReader{chunks: chunks}
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Header.Set("Content-Type", contentType)
	request.Body = io.NopCloser(reader)
	request.ContentLength = -1
	return request, reader
}

func TestBodySizeLimitStopsReadingAStreamedBody(t *testing.T) {
	handler := newPaymentHandler(t)
	adapter := adapters.HTTPHandler(handler, adapters.AdapterOptions{MaxBodySize: 100 * 1024})

	chunks := make([][]byte, 160)
	for i := range chunks {
		chunks[i] = []byte(strings.Repeat("a", 64*1024))
	}
	request, reader := streamedRequest("/api/pay/paytr/callback", chunks, formContentType)

	recorder := httptest.NewRecorder()
	adapter.ServeHTTP(recorder, request)
	if response := readReply(t, recorder); response.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.status)
	}
	if reader.read > 100*1024+1 {
		t.Errorf("read %d bytes, want at most maxBodySize+1", reader.read)
	}
}

func TestBodySizeLimitAcceptsTheExactLimit(t *testing.T) {
	handler := newPaymentHandler(t)
	adapter := adapters.HTTPHandler(handler, adapters.AdapterOptions{MaxBodySize: 10})

	if response := send(t, adapter, http.MethodPost, "/api/pay/mock/payment", `{"a":1}`, jsonContentType); response.status == http.StatusRequestEntityTooLarge {
		t.Errorf("a 7 byte body was rejected with 413")
	}
	if response := send(t, adapter, http.MethodPost, "/api/pay/mock/payment", "0123456789", jsonContentType); response.status == http.StatusRequestEntityTooLarge {
		t.Errorf("a body of exactly maxBodySize was rejected with 413")
	}
	if response := send(t, adapter, http.MethodPost, "/api/pay/mock/payment", "01234567890", jsonContentType); response.status != http.StatusRequestEntityTooLarge {
		t.Errorf("an 11 byte body status = %d, want 413", response.status)
	}
}

func TestBodySizeLimitAppliesToEveryAdapter(t *testing.T) {
	handler := newPaymentHandler(t)
	for name, mount := range map[string]func() http.Handler{
		"handler": func() http.Handler {
			return adapters.HTTPHandler(handler, adapters.AdapterOptions{MaxBodySize: 1024})
		},
		"middleware": func() http.Handler {
			return adapters.Middleware("/api/pay", handler, adapters.AdapterOptions{MaxBodySize: 1024})(http.NotFoundHandler())
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := send(t, mount(), http.MethodPost, "/api/pay/paytr/callback",
				"a="+strings.Repeat("x", 4096), formContentType)
			if response.status != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413", response.status)
			}
		})
	}
}

func TestDefaultMaxBodySize(t *testing.T) {
	if adapters.DefaultMaxBodySize != 1024*1024 {
		t.Errorf("DefaultMaxBodySize = %d, want 1 MiB", adapters.DefaultMaxBodySize)
	}
	handler := newPaymentHandler(t)
	adapter := adapters.HTTPHandler(handler)
	body := "a=" + strings.Repeat("x", 1024*1024)
	if response := send(t, adapter, http.MethodPost, "/api/pay/paytr/callback", body, formContentType); response.status != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over 1 MiB status = %d, want 413", response.status)
	}
}

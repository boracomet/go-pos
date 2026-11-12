package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// recordingDoer captures the requests a client sends and answers with a canned
// response, so no test touches the network.
type recordingDoer struct {
	requests []*http.Request
	bodies   []string

	status     int
	statusText string
	body       string
	err        error
}

func (d *recordingDoer) Do(r *http.Request) (*http.Response, error) {
	if d.err != nil {
		return nil, d.err
	}
	raw := ""
	if r.Body != nil {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		raw = string(data)
	}
	d.requests = append(d.requests, r)
	d.bodies = append(d.bodies, raw)

	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	text := d.statusText
	if text == "" {
		text = http.StatusText(status)
	}
	body := d.body
	if body == "" {
		body = `{"status":"success"}`
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, text),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (d *recordingDoer) lastURL() string {
	if len(d.requests) == 0 {
		return ""
	}
	return d.requests[len(d.requests)-1].URL.String()
}

func (d *recordingDoer) lastMethod() string {
	if len(d.requests) == 0 {
		return ""
	}
	return d.requests[len(d.requests)-1].Method
}

func testClient(doer *recordingDoer, baseURL string) *Client {
	return New(Config{BaseURL: baseURL, Doer: doer})
}

func TestURLBuilding(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(*Client) error
		want string
	}{
		{"iyzico payment", func(c *Client) error {
			_, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{})
			return err
		}, "http://localhost/api/pay/iyzico/payment"},
		{"paytr 3DS init", func(c *Client) error {
			_, err := c.PayTR.InitThreeDSPayment(ctx, &core.ThreeDSPaymentRequest{})
			return err
		}, "http://localhost/api/pay/paytr/payment/init-3ds"},
		{"parampos refund", func(c *Client) error {
			_, err := c.ParamPOS.Refund(ctx, &core.RefundRequest{})
			return err
		}, "http://localhost/api/pay/parampos/refund"},
		{"getPayment with id", func(c *Client) error {
			_, err := c.Iyzico.GetPayment(ctx, "PMT-123")
			return err
		}, "http://localhost/api/pay/iyzico/payment/PMT-123"},
		{"binCheck", func(c *Client) error {
			_, err := c.Iyzico.BinCheck(ctx, "454360")
			return err
		}, "http://localhost/api/pay/iyzico/bin-check"},
		{"installmentInfo", func(c *Client) error {
			_, err := c.Iyzico.InstallmentInfo(ctx, &core.InstallmentInfoRequest{BinNumber: "454360", Price: "100.00"})
			return err
		}, "http://localhost/api/pay/iyzico/installment"},
		{"complete 3DS", func(c *Client) error {
			_, err := c.Mock.CompleteThreeDSPayment(ctx, map[string]any{"paymentId": "P1"})
			return err
		}, "http://localhost/api/pay/mock/payment/complete-3ds"},
		{"authorize", func(c *Client) error {
			_, err := c.Akbank.Authorize(ctx, &core.PaymentRequest{})
			return err
		}, "http://localhost/api/pay/akbank/authorize"},
		{"authorize 3DS init", func(c *Client) error {
			_, err := c.Akbank.InitThreeDSAuthorize(ctx, &core.ThreeDSPaymentRequest{})
			return err
		}, "http://localhost/api/pay/akbank/authorize/init-3ds"},
		{"capture", func(c *Client) error {
			_, err := c.Garanti.Capture(ctx, &core.CaptureRequest{})
			return err
		}, "http://localhost/api/pay/garanti/capture"},
		{"void", func(c *Client) error {
			_, err := c.Garanti.VoidAuthorization(ctx, &core.VoidAuthorizationRequest{})
			return err
		}, "http://localhost/api/pay/garanti/void"},
		{"cancel", func(c *Client) error {
			_, err := c.Sipay.Cancel(ctx, &core.CancelRequest{})
			return err
		}, "http://localhost/api/pay/sipay/cancel"},
		{"save card", func(c *Client) error {
			_, err := c.Iyzico.SaveCard(ctx, &core.SaveCardRequest{})
			return err
		}, "http://localhost/api/pay/iyzico/cards/save"},
		{"list cards", func(c *Client) error {
			_, err := c.Iyzico.ListCards(ctx, &core.ListCardsRequest{})
			return err
		}, "http://localhost/api/pay/iyzico/cards/list"},
		{"delete card", func(c *Client) error {
			_, err := c.Iyzico.DeleteCard(ctx, &core.DeleteCardRequest{})
			return err
		}, "http://localhost/api/pay/iyzico/cards/delete"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &recordingDoer{}
			if err := tc.call(testClient(doer, "/api/pay")); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if got := doer.lastURL(); got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestURLBuildingStripsTrailingSlash(t *testing.T) {
	doer := &recordingDoer{}
	c := testClient(doer, "/api/pay/")
	if _, err := c.Akbank.CreatePayment(context.Background(), &core.PaymentRequest{}); err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	if got, want := doer.lastURL(), "http://localhost/api/pay/akbank/payment"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestURLBuildingKeepsAbsoluteBaseURL(t *testing.T) {
	doer := &recordingDoer{}
	c := testClient(doer, "https://api.example.com/pay/")
	if _, err := c.Iyzico.CreatePayment(context.Background(), &core.PaymentRequest{}); err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	if got, want := doer.lastURL(), "https://api.example.com/pay/iyzico/payment"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestRequestOptions(t *testing.T) {
	ctx := context.Background()

	t.Run("sets Content-Type to application/json", func(t *testing.T) {
		doer := &recordingDoer{}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); err != nil {
			t.Fatalf("createPayment failed: %v", err)
		}
		if got := doer.requests[0].Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
	})

	t.Run("merges custom headers", func(t *testing.T) {
		doer := &recordingDoer{}
		c := New(Config{BaseURL: "/api/pay", Doer: doer, Headers: map[string]string{"X-Custom": "test-value"}})
		if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); err != nil {
			t.Fatalf("createPayment failed: %v", err)
		}
		if got := doer.requests[0].Header.Get("X-Custom"); got != "test-value" {
			t.Errorf("X-Custom = %q, want test-value", got)
		}
	})

	t.Run("POSTs the serialized body", func(t *testing.T) {
		doer := &recordingDoer{}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{Price: "100.00"}); err != nil {
			t.Fatalf("createPayment failed: %v", err)
		}
		if got := doer.lastMethod(); got != http.MethodPost {
			t.Errorf("method = %q, want POST", got)
		}
		if got := doer.bodies[0]; !strings.Contains(got, `"price":"100.00"`) {
			t.Errorf("body = %q, want the serialized request", got)
		}
	})

	t.Run("GETs without a body", func(t *testing.T) {
		doer := &recordingDoer{}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.GetPayment(ctx, "PMT-1"); err != nil {
			t.Fatalf("getPayment failed: %v", err)
		}
		if got := doer.lastMethod(); got != http.MethodGet {
			t.Errorf("method = %q, want GET", got)
		}
		if doer.requests[0].Body != nil {
			t.Error("getPayment sent a body")
		}
		if doer.bodies[0] != "" {
			t.Errorf("body = %q, want empty", doer.bodies[0])
		}
		if got := doer.requests[0].Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want none on a GET", got)
		}
	})

	t.Run("binCheck posts the bin number", func(t *testing.T) {
		doer := &recordingDoer{}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.BinCheck(ctx, "454360"); err != nil {
			t.Fatalf("binCheck failed: %v", err)
		}
		if got, want := doer.bodies[0], `{"binNumber":"454360"}`; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}

func TestErrorHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("returns the server message", func(t *testing.T) {
		doer := &recordingDoer{status: http.StatusBadRequest, body: `{"message":"Payment failed"}`}
		c := testClient(doer, "/api/pay")
		_, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{})
		if err == nil || err.Error() != "Payment failed" {
			t.Fatalf("error = %v, want 'Payment failed'", err)
		}
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Status != http.StatusBadRequest {
			t.Errorf("error = %v, want a RequestError with status 400", err)
		}
	})

	t.Run("falls back to the HTTP status when the body is not JSON", func(t *testing.T) {
		doer := &recordingDoer{status: http.StatusInternalServerError, statusText: "Internal Server Error", body: "not json"}
		c := testClient(doer, "/api/pay")
		_, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{})
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("error = %v, want it to contain 'HTTP 500'", err)
		}
	})

	t.Run("returns a 422 result instead of an error", func(t *testing.T) {
		doer := &recordingDoer{
			status: http.StatusUnprocessableEntity,
			body:   `{"status":"failure","code":"INSUFFICIENT_FUNDS","errorCode":"INSUFFICIENT_FUNDS"}`,
		}
		c := testClient(doer, "/api/pay")
		result, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{})
		if err != nil {
			t.Fatalf("createPayment failed: %v", err)
		}
		if result.Status != core.StatusFailure || result.Code != core.CodeInsufficientFunds {
			t.Errorf("result = %+v, want a failure with INSUFFICIENT_FUNDS", result)
		}
	})

	t.Run("rejects a 422 without a result", func(t *testing.T) {
		doer := &recordingDoer{status: http.StatusUnprocessableEntity, body: `{"error":true,"message":"Invalid request"}`}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); err == nil {
			t.Fatal("want an error for a 422 without a result")
		}
	})

	t.Run("propagates a transport error", func(t *testing.T) {
		want := errors.New("connection refused")
		doer := &recordingDoer{err: want}
		c := testClient(doer, "/api/pay")
		if _, err := c.Iyzico.CreatePayment(ctx, &core.PaymentRequest{}); !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}

func TestHealth(t *testing.T) {
	ctx := context.Background()

	t.Run("calls the health endpoint", func(t *testing.T) {
		doer := &recordingDoer{body: `{"status":"ok","service":"go-pos","version":"1.0.0","timestamp":"2024-01-01T00:00:00.000Z"}`}
		c := testClient(doer, "/api/pay")
		health, err := c.Health(ctx)
		if err != nil {
			t.Fatalf("health failed: %v", err)
		}
		if got, want := doer.lastURL(), "http://localhost/api/pay/health"; got != want {
			t.Errorf("url = %q, want %q", got, want)
		}
		if got := doer.lastMethod(); got != http.MethodGet {
			t.Errorf("method = %q, want GET", got)
		}
		if health.Status != "ok" || health.Service != "go-pos" {
			t.Errorf("health = %+v, want status ok", health)
		}
	})

	t.Run("fails when the endpoint is unavailable", func(t *testing.T) {
		doer := &recordingDoer{status: http.StatusServiceUnavailable, statusText: "Service Unavailable", body: `{}`}
		c := testClient(doer, "/api/pay")
		_, err := c.Health(ctx)
		if err == nil || !strings.Contains(err.Error(), "Health check failed") {
			t.Fatalf("error = %v, want it to contain 'Health check failed'", err)
		}
	})
}

func TestNewExposesEveryProvider(t *testing.T) {
	c := New(Config{BaseURL: "/api/pay"})
	providers := map[string]*ProviderClient{
		"iyzico": c.Iyzico, "paytr": c.PayTR, "akbank": c.Akbank, "parampos": c.ParamPOS,
		"kuveytturk": c.KuveytTurk, "sipay": c.Sipay, "denizbank": c.DenizBank, "albaraka": c.Albaraka,
		"vakifbank": c.VakifBank, "yapikredi": c.YapiKredi, "qnb": c.QNB, "qnbpay": c.QNBPAY,
		"garanti": c.Garanti, "mock": c.Mock,
	}
	for id, provider := range providers {
		if provider == nil {
			t.Fatalf("provider %q is nil", id)
		}
		if got := provider.Provider(); got != id {
			t.Errorf("provider id = %q, want %q", got, id)
		}
	}
	if got := c.Use("custom").Provider(); got != "custom" {
		t.Errorf("Use(custom) = %q, want custom", got)
	}
}

func TestNewCopiesTheConfiguration(t *testing.T) {
	headers := map[string]string{"X-Custom": "first"}
	doer := &recordingDoer{}
	c := New(Config{BaseURL: "/api/pay", Doer: doer, Headers: headers})
	headers["X-Custom"] = "second"

	if _, err := c.Iyzico.CreatePayment(context.Background(), &core.PaymentRequest{}); err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	if got := doer.requests[0].Header.Get("X-Custom"); got != "first" {
		t.Errorf("X-Custom = %q, want first", got)
	}
}

func TestEncodeURIComponent(t *testing.T) {
	cases := map[string]string{
		"PMT-123":   "PMT-123",
		"a b/c":     "a%20b%2Fc",
		"!'()*-._~": "!'()*-._~",
		"é":         "%C3%A9",
		"+":         "%2B",
		"a&b=c":     "a%26b%3Dc",
		"":          "",
	}
	for input, want := range cases {
		if got := encodeURIComponent(input); got != want {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGetPaymentEscapesTheID(t *testing.T) {
	doer := &recordingDoer{}
	c := testClient(doer, "/api/pay")
	if _, err := c.Iyzico.GetPayment(context.Background(), "PMT/1 2"); err != nil {
		t.Fatalf("getPayment failed: %v", err)
	}
	if got, want := doer.lastURL(), "http://localhost/api/pay/iyzico/payment/PMT%2F1%202"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type httpTestCall struct {
	Method      string
	URL         string
	Body        string
	Header      http.Header
	HasDeadline bool
	Ctx         context.Context
}

type httpTestDoer struct {
	calls   []httpTestCall
	respond func(call httpTestCall) (*http.Response, error)
}

func (d *httpTestDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	_, deadline := req.Context().Deadline()
	call := httpTestCall{
		Method:      req.Method,
		URL:         req.URL.String(),
		Body:        body,
		Header:      req.Header.Clone(),
		HasDeadline: deadline,
		Ctx:         req.Context(),
	}
	d.calls = append(d.calls, call)
	return d.respond(call)
}

func httpTestAnswer(status int, body string) func(httpTestCall) (*http.Response, error) {
	return func(httpTestCall) (*http.Response, error) {
		return httpTestResponse(status, body), nil
	}
}

func httpTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func httpTestNewClient(doer Doer, mutate func(*HTTPClientOptions)) *HTTPClient {
	opts := HTTPClientOptions{
		Provider: "test",
		BaseURL:  "https://api.example.com/",
		Timeout:  time.Second,
		Doer:     doer,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return NewHTTPClient(opts)
}

type httpTestLogEntry struct {
	Level   string
	Message string
	Err     error
	Meta    map[string]any
}

type httpTestLogger struct {
	entries []httpTestLogEntry
}

func (l *httpTestLogger) record(level, message string, err error, meta map[string]any) {
	l.entries = append(l.entries, httpTestLogEntry{Level: level, Message: message, Err: err, Meta: meta})
}

func (l *httpTestLogger) Debug(message string, meta map[string]any) {
	l.record("debug", message, nil, meta)
}

func (l *httpTestLogger) Info(message string, meta map[string]any) {
	l.record("info", message, nil, meta)
}

func (l *httpTestLogger) Error(message string, err error, meta map[string]any) {
	l.record("error", message, err, meta)
}

func (l *httpTestLogger) messages(level string) []string {
	out := make([]string, 0, len(l.entries))
	for _, entry := range l.entries {
		if entry.Level == level {
			out = append(out, entry.Message)
		}
	}
	return out
}

func (l *httpTestLogger) json(t *testing.T) string {
	t.Helper()
	payload := make([]map[string]any, 0, len(l.entries))
	for _, entry := range l.entries {
		payload = append(payload, map[string]any{
			"level":   entry.Level,
			"message": entry.Message,
			"meta":    entry.Meta,
			"err":     entry.Err,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal log: %v", err)
	}
	return string(data)
}

func httpTestError(t *testing.T, err error) *HTTPError {
	t.Helper()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want *HTTPError", err, err)
	}
	return httpErr
}

func httpTestWantStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestHTTPClientRequestShape(t *testing.T) {
	t.Run("joins baseURL and path, merges headers and sends the body as is", func(t *testing.T) {
		doer := &httpTestDoer{respond: httpTestAnswer(http.StatusOK, `{"a":1}`)}
		client := httpTestNewClient(doer, func(opts *HTTPClientOptions) {
			opts.Headers = map[string]string{"Content-Type": "application/json", "X-A": "1"}
		})

		res, err := client.Post(context.Background(), "/payment/auth", `{"x":1}`, RequestConfig{
			Headers: map[string]string{"X-B": "2"},
		})
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		if len(doer.calls) != 1 {
			t.Fatalf("calls = %d, want 1", len(doer.calls))
		}
		call := doer.calls[0]
		if call.URL != "https://api.example.com/payment/auth" {
			t.Errorf("url = %q", call.URL)
		}
		if call.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", call.Method)
		}
		if call.Body != `{"x":1}` {
			t.Errorf("body = %q, want the serialized body unchanged", call.Body)
		}
		want := http.Header{"Content-Type": {"application/json"}, "X-A": {"1"}, "X-B": {"2"}}
		if len(call.Header) != len(want) {
			t.Errorf("headers = %v, want %v", call.Header, want)
		}
		for name, values := range want {
			if got := call.Header.Get(name); got != values[0] {
				t.Errorf("header %s = %q, want %q", name, got, values[0])
			}
		}
		if !call.HasDeadline {
			t.Error("request context has no deadline, want the configured timeout")
		}
		if res.Status != http.StatusOK {
			t.Errorf("status = %d, want 200", res.Status)
		}
		record, ok := res.Data.(map[string]any)
		if !ok || record["a"] != float64(1) {
			t.Errorf("data = %#v, want {a:1}", res.Data)
		}
	})

	t.Run("posts to the baseURL itself for an empty path and keeps absolute URLs", func(t *testing.T) {
		doer := &httpTestDoer{respond: httpTestAnswer(http.StatusOK, "ok")}
		client := httpTestNewClient(doer, nil)
		if _, err := client.Post(context.Background(), "", "<xml/>", RequestConfig{}); err != nil {
			t.Fatalf("Post: %v", err)
		}
		if _, err := client.Get(context.Background(), "https://other.example.com/x", RequestConfig{}); err != nil {
			t.Fatalf("Get: %v", err)
		}
		httpTestWantStrings(t, "urls", []string{doer.calls[0].URL, doer.calls[1].URL}, []string{
			"https://api.example.com/",
			"https://other.example.com/x",
		})
		if doer.calls[1].Body != "" {
			t.Errorf("GET body = %q, want none", doer.calls[1].Body)
		}
	})

	t.Run("returns non-JSON bodies as text and raw text for responseType text", func(t *testing.T) {
		plain := &httpTestDoer{respond: httpTestAnswer(http.StatusOK, "OK")}
		res, err := httpTestNewClient(plain, nil).Get(context.Background(), "/", RequestConfig{})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if res.Data != "OK" {
			t.Errorf("data = %#v, want the raw text", res.Data)
		}

		raw := &httpTestDoer{respond: httpTestAnswer(http.StatusOK, `{"a":1}`)}
		res, err = httpTestNewClient(raw, nil).Get(context.Background(), "/", RequestConfig{ResponseType: "text"})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if res.Data != `{"a":1}` {
			t.Errorf("data = %#v, want the unparsed body", res.Data)
		}
		if res.Text != `{"a":1}` {
			t.Errorf("text = %q", res.Text)
		}
	})
}

func TestHTTPClientFailures(t *testing.T) {
	t.Run("returns an HTTPError with the parsed response for non-2xx statuses", func(t *testing.T) {
		doer := &httpTestDoer{respond: httpTestAnswer(http.StatusBadRequest, `{"errorCode":"5001","errorMessage":"Bad"}`)}
		_, err := httpTestNewClient(doer, nil).Post(context.Background(), "/x", "{}", RequestConfig{})
		httpErr := httpTestError(t, err)
		if httpErr.IsNetworkError {
			t.Error("IsNetworkError = true, want false for an answered request")
		}
		if httpErr.Response == nil || httpErr.Response.Status != http.StatusBadRequest {
			t.Fatalf("Response = %#v, want status 400", httpErr.Response)
		}
		record, ok := httpErr.Response.Data.(map[string]any)
		if !ok || record["errorCode"] != "5001" {
			t.Errorf("Response.Data = %#v", httpErr.Response.Data)
		}
		if IsNetworkError(err) {
			t.Error("IsNetworkError(err) = true, want false")
		}

		failure := BuildFailure("Test", err, "failed")
		if failure.Status != StatusFailure {
			t.Errorf("status = %q, want failure", failure.Status)
		}
		if failure.ErrorCode != "5001" {
			t.Errorf("errorCode = %q, want 5001", failure.ErrorCode)
		}
		if failure.ErrorMessage != "Bad" {
			t.Errorf("errorMessage = %q, want Bad", failure.ErrorMessage)
		}
	})

	t.Run("resolves statuses accepted by validateStatus", func(t *testing.T) {
		doer := &httpTestDoer{respond: httpTestAnswer(http.StatusUnprocessableEntity, `{"code":"X"}`)}
		res, err := httpTestNewClient(doer, nil).Post(context.Background(), "/x", "{}", RequestConfig{
			ValidateStatus: func(status int) bool { return status < 500 },
		})
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		if res.Status != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", res.Status)
		}
		record, ok := res.Data.(map[string]any)
		if !ok || record["code"] != "X" {
			t.Errorf("data = %#v", res.Data)
		}
	})

	t.Run("reports transport errors as network errors with the underlying message", func(t *testing.T) {
		doer := &httpTestDoer{respond: func(httpTestCall) (*http.Response, error) {
			return nil, errors.New("read ECONNRESET")
		}}
		_, err := httpTestNewClient(doer, nil).Post(context.Background(), "/x", "{}", RequestConfig{})
		httpErr := httpTestError(t, err)
		if !httpErr.IsNetworkError {
			t.Error("IsNetworkError = false, want true")
		}
		if httpErr.Message != "read ECONNRESET" {
			t.Errorf("message = %q, want the transport message", httpErr.Message)
		}
		if !IsNetworkError(err) {
			t.Error("IsNetworkError(err) = false, want true")
		}

		failure := BuildFailure("iyzico", err, "failed")
		if failure.Status != StatusPending {
			t.Errorf("status = %q, want pending: the provider may have processed the payment", failure.Status)
		}
		if failure.ErrorCode != NetworkErrorCode {
			t.Errorf("errorCode = %q, want NETWORK_ERROR", failure.ErrorCode)
		}
		if !strings.Contains(failure.ErrorMessage, "No response from iyzico (read ECONNRESET)") {
			t.Errorf("errorMessage = %q", failure.ErrorMessage)
		}
	})

	t.Run("times out with ETIMEDOUT", func(t *testing.T) {
		doer := &httpTestDoer{respond: func(call httpTestCall) (*http.Response, error) {
			<-call.Ctx.Done()
			return nil, call.Ctx.Err()
		}}
		client := httpTestNewClient(doer, func(opts *HTTPClientOptions) { opts.Timeout = 20 * time.Millisecond })
		_, err := client.Get(context.Background(), "/slow", RequestConfig{})
		httpErr := httpTestError(t, err)
		if !httpErr.IsNetworkError {
			t.Error("IsNetworkError = false, want true")
		}
		if httpErr.Code != "ETIMEDOUT" {
			t.Errorf("code = %q, want ETIMEDOUT", httpErr.Code)
		}
		if httpErr.Message != "Request timed out" {
			t.Errorf("message = %q, want Request timed out", httpErr.Message)
		}
	})
}

func TestBuildFailureCopiesHTTPStatusAndAttempts(t *testing.T) {
	answered := &HTTPError{
		Message:  "Payment declined at transport level",
		Config:   RequestConfig{Method: http.MethodPost, URL: "/pay"},
		Response: &Response{Status: http.StatusPaymentRequired, Data: map[string]any{"errorMessage": "No"}, Headers: map[string]string{}},
		Attempts: 2,
	}
	failure := BuildFailure("Test", answered, "failed")
	if failure.Status != StatusFailure {
		t.Errorf("status = %q, want failure", failure.Status)
	}
	if failure.HTTPStatus != http.StatusPaymentRequired {
		t.Errorf("httpStatus = %d, want 402", failure.HTTPStatus)
	}
	if failure.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", failure.Attempts)
	}

	lost := &HTTPError{
		Message:        "Request timed out",
		IsNetworkError: true,
		Code:           "ETIMEDOUT",
		Config:         RequestConfig{Method: http.MethodPost, URL: "/pay"},
		Attempts:       3,
	}
	pending := BuildFailure("Test", lost, "failed")
	if pending.Status != StatusPending {
		t.Errorf("status = %q, want pending", pending.Status)
	}
	if pending.ErrorCode != NetworkErrorCode {
		t.Errorf("errorCode = %q, want NETWORK_ERROR", pending.ErrorCode)
	}
	if pending.Code != CodeNetworkError {
		t.Errorf("code = %q, want NETWORK_ERROR", pending.Code)
	}
	if pending.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", pending.Attempts)
	}
	if pending.HTTPStatus != 0 {
		t.Errorf("httpStatus = %d, want none for a transport error", pending.HTTPStatus)
	}

	result := ApplyFailure("Test", lost, "failed", &PaymentResponse{})
	if result.Status != StatusPending || result.Attempts != 3 {
		t.Errorf("ApplyFailure = %+v, want the failure written into the response", result)
	}
}

func TestHTTPClientAttemptsOnGiveUp(t *testing.T) {
	respond := httpTestAnswer(http.StatusServiceUnavailable, "{}")
	retry := func(opts *HTTPClientOptions) {
		opts.Retry = &RetryConfig{Attempts: 3, Delay: time.Millisecond, StatusCodes: []int{503}}
	}

	retried := &httpTestDoer{respond: respond}
	_, err := httpTestNewClient(retried, retry).Get(context.Background(), "/x", RequestConfig{})
	if got := httpTestError(t, err).Attempts; got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
	if len(retried.calls) != 3 {
		t.Errorf("calls = %d, want 3", len(retried.calls))
	}

	single := &httpTestDoer{respond: respond}
	_, err = httpTestNewClient(single, retry).Post(context.Background(), "/pay", "{}", RequestConfig{})
	if got := httpTestError(t, err).Attempts; got != 1 {
		t.Errorf("attempts = %d, want 1: payments are never retried", got)
	}
	if len(single.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(single.calls))
	}
}

func TestHTTPClientLogging(t *testing.T) {
	t.Run("logs method, URL and status without bodies", func(t *testing.T) {
		logger := &httpTestLogger{}
		ok := &httpTestDoer{respond: httpTestAnswer(http.StatusOK, "{}")}
		client := httpTestNewClient(ok, func(opts *HTTPClientOptions) { opts.Logger = logger })
		if _, err := client.Post(context.Background(), "/pay", `{"cardNumber":"5528790000000008"}`, RequestConfig{}); err != nil {
			t.Fatalf("Post: %v", err)
		}
		httpTestWantStrings(t, "debug", logger.messages("debug"), []string{
			"[test] POST https://api.example.com/pay",
			"[test] 200 https://api.example.com/pay",
		})
		if strings.Contains(logger.json(t), "5528") {
			t.Error("the log carries card data")
		}

		logger = &httpTestLogger{}
		bad := &httpTestDoer{respond: httpTestAnswer(http.StatusInternalServerError, "{}")}
		client = httpTestNewClient(bad, func(opts *HTTPClientOptions) { opts.Logger = logger })
		if _, err := client.Get(context.Background(), "/x", RequestConfig{}); err == nil {
			t.Fatal("Get succeeded, want an error")
		}
		httpTestWantStrings(t, "error", logger.messages("error"), []string{
			"[test] Request failed: Request failed with status code 500",
		})
		entry := logger.entries[len(logger.entries)-1]
		if _, ok := entry.Err.(*HTTPError); !ok {
			t.Errorf("logged err = %T, want *HTTPError", entry.Err)
		}
		if entry.Meta["status"] != http.StatusInternalServerError {
			t.Errorf("meta = %#v, want status 500", entry.Meta)
		}
	})

	t.Run("does not keep the request body or headers on HTTPError", func(t *testing.T) {
		failing := []*httpTestDoer{
			{respond: httpTestAnswer(http.StatusInternalServerError, "{}")},
			{respond: func(httpTestCall) (*http.Response, error) { return nil, errors.New("fetch failed") }},
		}
		for _, doer := range failing {
			logger := &httpTestLogger{}
			client := httpTestNewClient(doer, func(opts *HTTPClientOptions) { opts.Logger = logger })
			_, err := client.Post(context.Background(), "/pay", `{"cardNumber":"5528790000000008","cvc":"123"}`, RequestConfig{
				Headers: map[string]string{"Authorization": "IYZWSv2 secret"},
			})
			httpErr := httpTestError(t, err)
			if httpErr.Config.Method != http.MethodPost || httpErr.Config.URL != "/pay" {
				t.Errorf("config = %+v, want method POST and url /pay", httpErr.Config)
			}
			if httpErr.Config.Data != "" {
				t.Errorf("config.Data = %q, want the body stripped", httpErr.Config.Data)
			}
			if httpErr.Config.Headers != nil {
				t.Errorf("config.Headers = %v, want the headers stripped", httpErr.Config.Headers)
			}
			logged := logger.json(t)
			if strings.Contains(logged, "5528790000000008") {
				t.Error("the log carries the card number")
			}
			if strings.Contains(logged, "IYZWSv2 secret") {
				t.Error("the log carries the Authorization header")
			}
		}
	})
}

package adapters_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

// echoHandler records what the adapter passed to the handler and answers with
// the request body, so a test can see the normalized request.
type echoHandler struct {
	requests []*core.HandlerRequest
	response *core.HandlerResponse
	err      error
}

func (h *echoHandler) Handle(_ context.Context, request *core.HandlerRequest) (*core.HandlerResponse, error) {
	h.requests = append(h.requests, request)
	if h.err != nil {
		return nil, h.err
	}
	if h.response != nil {
		return h.response, nil
	}
	return &core.HandlerResponse{Status: http.StatusOK, Headers: map[string]string{}, Body: request.Body}, nil
}

func (h *echoHandler) lastRequest(t *testing.T) *core.HandlerRequest {
	t.Helper()
	if len(h.requests) == 0 {
		t.Fatal("the handler was never called")
	}
	return h.requests[len(h.requests)-1]
}

// failingReader stands in for a request stream that breaks mid-body.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func serve(adapter http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	adapter.ServeHTTP(recorder, request)
	return recorder
}

func TestOnErrorForwardsAHandlerErrorUnchanged(t *testing.T) {
	failure := errors.New("handler failed")
	handler := &echoHandler{err: failure}
	var forwarded error
	adapter := adapters.HTTPHandler(handler, adapters.AdapterOptions{
		OnError: func(_ http.ResponseWriter, _ *http.Request, err error) { forwarded = err },
	})

	recorder := serve(adapter, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "raw", jsonContentType))
	if forwarded != failure {
		t.Errorf("OnError received %v, want the handler's error unchanged", forwarded)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("the response was written: status %d, body %q", recorder.Code, recorder.Body.String())
	}
}

func TestOnErrorForwardsARequestStreamErrorUnchanged(t *testing.T) {
	failure := errors.New("request stream failed")
	handler := &echoHandler{}
	var forwarded error
	adapter := adapters.HTTPHandler(handler, adapters.AdapterOptions{
		OnError: func(_ http.ResponseWriter, _ *http.Request, err error) { forwarded = err },
	})

	request := httptest.NewRequest(http.MethodPost, "/api/pay/mock/callback", nil)
	request.Body = io.NopCloser(failingReader{err: failure})
	request.ContentLength = -1
	recorder := serve(adapter, request)

	if forwarded != failure {
		t.Errorf("OnError received %v, want the stream's error unchanged", forwarded)
	}
	if len(handler.requests) != 0 {
		t.Error("the handler was called with an unreadable body")
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("the response was written: status %d, body %q", recorder.Code, recorder.Body.String())
	}
}

func TestWithoutOnErrorAFailureIsAnEmpty500(t *testing.T) {
	failure := errors.New("private failure detail")
	adapter := adapters.HTTPHandler(&echoHandler{err: failure})

	recorder := serve(adapter, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "raw", jsonContentType))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", recorder.Body.String())
	}
	for name, values := range recorder.Header() {
		if strings.Contains(name+strings.Join(values, ""), failure.Error()) {
			t.Errorf("the error message leaked into header %s", name)
		}
	}
}

func TestGetAndHeadDoNotReadTheBody(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			handler := &echoHandler{}
			request := httptest.NewRequest(method, "/api/pay/health", nil)
			request.Body = io.NopCloser(failingReader{err: errors.New("the stream must not be read")})

			recorder := serve(adapters.HTTPHandler(handler), request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if body := handler.lastRequest(t).Body; body != nil {
				t.Errorf("body = %v, want nil", body)
			}
		})
	}
}

func TestTheBodyLimitIsMeasuredInBytes(t *testing.T) {
	exact := &echoHandler{}
	recorder := serve(
		adapters.HTTPHandler(exact, adapters.AdapterOptions{MaxBodySize: 4}),
		requestWithBody(http.MethodPost, "/api/pay/mock/callback", "éé", jsonContentType),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("a body of exactly 4 bytes status = %d, want 200", recorder.Code)
	}
	if body := exact.lastRequest(t).Body; body != "éé" {
		t.Errorf("body = %v, want éé", body)
	}

	oversized := &echoHandler{}
	recorder = serve(
		adapters.HTTPHandler(oversized, adapters.AdapterOptions{MaxBodySize: 4}),
		requestWithBody(http.MethodPost, "/api/pay/mock/callback", "ééa", jsonContentType),
	)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 5 byte body status = %d, want 413", recorder.Code)
	}
	if len(oversized.requests) != 0 {
		t.Error("the handler was called with an oversized body")
	}
}

func TestUTF8CharactersSplitAcrossChunksAreReassembled(t *testing.T) {
	data := []byte("A€B")
	request, _ := streamedRequest("/api/pay/mock/callback", [][]byte{data[:2], data[2:3], data[3:]}, jsonContentType)
	handler := &echoHandler{}

	if recorder := serve(adapters.HTTPHandler(handler), request); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body := handler.lastRequest(t).Body; body != "A€B" {
		t.Errorf("body = %v, want A€B", body)
	}
}

func TestRawBodiesAreReassembledFromChunks(t *testing.T) {
	cases := []struct {
		name        string
		chunks      []string
		contentType string
		want        string
	}{
		{"form-urlencoded", []string{"merchant_oid=ORDER1", "&status=success"}, formContentType, "merchant_oid=ORDER1&status=success"},
		{"json", []string{`{"a":`, `1}`}, jsonContentType, `{"a":1}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			chunks := make([][]byte, len(testCase.chunks))
			for i, chunk := range testCase.chunks {
				chunks[i] = []byte(chunk)
			}
			request, _ := streamedRequest("/api/pay/mock/callback", chunks, testCase.contentType)
			handler := &echoHandler{}
			serve(adapters.HTTPHandler(handler), request)
			if body := handler.lastRequest(t).Body; body != testCase.want {
				t.Errorf("body = %v, want %q", body, testCase.want)
			}
		})
	}
}

func TestFromRequestNormalizesTheRequest(t *testing.T) {
	request := httptest.NewRequest("post", "/api/pay/mock/callback?x=1", strings.NewReader(""))
	request.Header["Accept"] = []string{"application/json", "text/plain"}
	request.Header.Set("X-Payment", "ok")

	converted, err := adapters.FromRequest(request)
	if err != nil {
		t.Fatalf("FromRequest: %v", err)
	}
	if converted.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", converted.Method)
	}
	if converted.URL != "/api/pay/mock/callback?x=1" {
		t.Errorf("url = %q, want the full path and query", converted.URL)
	}
	if converted.Headers["accept"] != "application/json, text/plain" {
		t.Errorf("accept = %q, want the repeated values joined", converted.Headers["accept"])
	}
	if converted.Headers["x-payment"] != "ok" {
		t.Errorf("headers = %v, want lowercased names", converted.Headers)
	}
	if converted.Body != nil {
		t.Errorf("body = %v, want nil for an empty body", converted.Body)
	}
}

func TestFromRequestDefaultsTheMethodAndURL(t *testing.T) {
	converted, err := adapters.FromRequest(&http.Request{Method: ""})
	if err != nil {
		t.Fatalf("FromRequest: %v", err)
	}
	if converted.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", converted.Method)
	}
	if converted.URL != "/" {
		t.Errorf("url = %q, want /", converted.URL)
	}
}

func TestFromRequestReportsAnOversizedBody(t *testing.T) {
	request := requestWithBody(http.MethodPost, "/api/pay/mock/callback", "0123456789", jsonContentType)
	_, err := adapters.FromRequest(request, adapters.AdapterOptions{MaxBodySize: 4})
	var tooLarge *adapters.BodyTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v, want a BodyTooLargeError", err)
	}
	if tooLarge.Error() != "Request body too large" {
		t.Errorf("message = %q, want Request body too large", tooLarge.Error())
	}
}

func TestResolveHandler(t *testing.T) {
	handler := &echoHandler{}
	failure := errors.New("no facade yet")

	cases := []struct {
		name   string
		source adapters.HandlerSource
		want   adapters.Handler
	}{
		{"handler", handler, handler},
		{"facade", facadeOf{handler: handler}, handler},
		{"function", func() adapters.Handler { return handler }, handler},
		{"function returning a source", func() adapters.HandlerSource { return handler }, handler},
		{"fallible function", func() (adapters.Handler, error) { return handler, nil }, handler},
		{"fallible source function", func() (adapters.HandlerSource, error) { return handler, nil }, handler},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, err := adapters.ResolveHandler(testCase.source)
			if err != nil {
				t.Fatalf("ResolveHandler: %v", err)
			}
			if resolved != testCase.want {
				t.Errorf("resolved %v, want the handler", resolved)
			}
		})
	}

	for _, source := range []adapters.HandlerSource{
		nil,
		42,
		func() (adapters.Handler, error) { return nil, failure },
		func() (adapters.HandlerSource, error) { return nil, failure },
	} {
		if _, err := adapters.ResolveHandler(source); err == nil {
			t.Errorf("ResolveHandler(%T) returned no error", source)
		}
	}

	if _, err := adapters.ResolveHandler(func() (adapters.Handler, error) { return nil, failure }); err != failure {
		t.Errorf("a source error was changed: %v", err)
	}
	var configuration *core.ConfigurationError
	if _, err := adapters.ResolveHandler(42); !errors.As(err, &configuration) {
		t.Errorf("an unsupported source error = %v, want a ConfigurationError", err)
	}
}

// facadeOf exposes a handler the way a payment facade does.
type facadeOf struct{ handler adapters.Handler }

func (f facadeOf) Handler() adapters.Handler { return f.handler }

func TestHandlerFuncAndHandlerProvider(t *testing.T) {
	calls := 0
	handler := adapters.HandlerFunc(func(_ context.Context, request *core.HandlerRequest) (*core.HandlerResponse, error) {
		calls++
		return &core.HandlerResponse{Status: http.StatusOK, Body: request.Body}, nil
	})
	resolved, err := adapters.ResolveHandler(facadeOf{handler: handler})
	if err != nil {
		t.Fatalf("ResolveHandler: %v", err)
	}
	response, err := resolved.Handle(context.Background(), &core.HandlerRequest{Method: http.MethodPost, Body: "raw"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls != 1 || response.Body != "raw" {
		t.Errorf("calls = %d, body = %v", calls, response.Body)
	}
}

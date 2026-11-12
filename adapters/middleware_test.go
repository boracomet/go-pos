package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

// passthrough answers 418 so a test can tell which handler served a request.
func passthrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// TestMiddlewareMatchesOnlyItsOwnPathPrefix checks the boundary of the mount
// point: a path that merely starts with the same letters belongs to the app, not
// to the payment handler.
func TestMiddlewareMatchesOnlyItsOwnPathPrefix(t *testing.T) {
	middleware := adapters.Middleware("/api/pay", newPaymentHandler(t))(passthrough())

	cases := []struct {
		path string
		want int
	}{
		{"/api/pay/health", http.StatusOK},
		{"/api/pay", http.StatusNotFound},
		{"/api/pay/", http.StatusNotFound},
		{"/api/payment/health", http.StatusTeapot},
		{"/api/payments", http.StatusTeapot},
		{"/api/pa", http.StatusTeapot},
		{"/API/pay/health", http.StatusTeapot},
		{"/shop/api/pay/health", http.StatusTeapot},
	}
	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			if got := send(t, middleware, http.MethodGet, testCase.path, "", "").status; got != testCase.want {
				t.Errorf("status = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestMiddlewareNormalizesTheBasePath checks that the mount point is read the
// same way whichever slashes the caller writes around it.
func TestMiddlewareNormalizesTheBasePath(t *testing.T) {
	handler := newPaymentHandler(t)
	for _, basePath := range []string{"/api/pay", "/api/pay/", "api/pay", "/api/pay//", "api/pay/"} {
		t.Run(basePath, func(t *testing.T) {
			middleware := adapters.Middleware(basePath, handler)(passthrough())
			if got := send(t, middleware, http.MethodGet, "/api/pay/health", "", "").status; got != http.StatusOK {
				t.Errorf("health status = %d, want 200", got)
			}
			if got := send(t, middleware, http.MethodGet, "/shop/orders", "", "").status; got != http.StatusTeapot {
				t.Errorf("outside status = %d, want 418", got)
			}
		})
	}
}

// TestMiddlewareWithRootBasePathServesEveryPath checks the "/" mount: nothing is
// passed through, so the payment handler answers every request itself.
func TestMiddlewareWithRootBasePathServesEveryPath(t *testing.T) {
	middleware := adapters.Middleware("/", newPaymentHandler(t))(passthrough())

	if got := send(t, middleware, http.MethodGet, "/api/pay/health", "", "").status; got != http.StatusOK {
		t.Errorf("health status = %d, want 200", got)
	}
	// The handler's own 404, not the app's 418.
	if got := send(t, middleware, http.MethodGet, "/shop/orders", "", "").status; got != http.StatusNotFound {
		t.Errorf("unknown route status = %d, want the handler's 404", got)
	}
}

// contextKey is the request-scoped value the handler below reads back.
type contextKey struct{}

// TestHTTPHandlerPassesTheRequestContextToTheHandler checks that request-scoped
// values (a tenant, a trace, a deadline) reach the payment handler.
func TestHTTPHandlerPassesTheRequestContextToTheHandler(t *testing.T) {
	var seen any
	handler := adapters.HandlerFunc(func(ctx context.Context, _ *core.HandlerRequest) (*core.HandlerResponse, error) {
		seen = ctx.Value(contextKey{})
		return &core.HandlerResponse{Status: http.StatusOK}, nil
	})

	request := requestWithBody(http.MethodPost, "/api/pay/mock/payment", "{}", jsonContentType)
	request = request.WithContext(context.WithValue(request.Context(), contextKey{}, "tenant-1"))
	recorder := serve(adapters.HTTPHandler(handler), request)

	if seen != "tenant-1" {
		t.Errorf("the handler saw %v, want the request's context value", seen)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

// TestHTTPHandlerReportsAnUnresolvableSource checks the failure path when the
// adapter was given something that is not a payment handler: the reason stays
// server-side.
func TestHTTPHandlerReportsAnUnresolvableSource(t *testing.T) {
	request := func() *http.Request {
		return requestWithBody(http.MethodPost, "/api/pay/mock/payment", "{}", jsonContentType)
	}

	for _, source := range []adapters.HandlerSource{nil, 42, "not a handler"} {
		recorder := serve(adapters.HTTPHandler(source), request())
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("source %T: status = %d, want 500", source, recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("source %T: body = %q, want empty", source, recorder.Body.String())
		}
	}

	var forwarded error
	adapter := adapters.HTTPHandler(42, adapters.AdapterOptions{
		OnError: func(_ http.ResponseWriter, _ *http.Request, err error) { forwarded = err },
	})
	recorder := serve(adapter, request())

	var configuration *core.ConfigurationError
	if !errors.As(forwarded, &configuration) {
		t.Fatalf("OnError received %v, want a ConfigurationError", forwarded)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("the response was written: status %d, body %q", recorder.Code, recorder.Body.String())
	}
}

// TestHTTPHandlerResolvesALazySourcePerRequest checks that a source function is
// called for every request, so a facade created later is picked up.
func TestHTTPHandlerResolvesALazySourcePerRequest(t *testing.T) {
	handler := &echoHandler{}
	calls := 0
	source := func() adapters.Handler {
		calls++
		return handler
	}
	adapter := adapters.HTTPHandler(source)

	for i := 0; i < 2; i++ {
		recorder := serve(adapter, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "raw", jsonContentType))
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, recorder.Code)
		}
	}
	if calls != 2 {
		t.Errorf("the source was called %d times, want once per request", calls)
	}
	if len(handler.requests) != 2 {
		t.Errorf("the handler served %d requests, want 2", len(handler.requests))
	}
}

// TestAdapterOptionsMerge checks that several option structs combine: a limit
// set in one survives alongside a hook set in another, and a later limit wins.
func TestAdapterOptionsMerge(t *testing.T) {
	failure := errors.New("handler failed")
	handler := &echoHandler{err: failure}
	var forwarded error
	adapter := adapters.HTTPHandler(handler,
		adapters.AdapterOptions{MaxBodySize: 4},
		adapters.AdapterOptions{OnError: func(_ http.ResponseWriter, _ *http.Request, err error) { forwarded = err }},
	)

	// The limit from the first struct still applies, and 413 never reaches OnError.
	recorder := serve(adapter, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "12345", jsonContentType))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized status = %d, want 413", recorder.Code)
	}
	if forwarded != nil {
		t.Errorf("OnError received %v, want nothing for an oversized body", forwarded)
	}

	// The hook from the second struct still applies.
	recorder = serve(adapter, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "{}", jsonContentType))
	if forwarded != failure {
		t.Errorf("OnError received %v, want the handler's error", forwarded)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("the response was written: status %d, body %q", recorder.Code, recorder.Body.String())
	}

	// A later limit replaces an earlier one.
	relaxed := adapters.HTTPHandler(&echoHandler{},
		adapters.AdapterOptions{MaxBodySize: 4},
		adapters.AdapterOptions{MaxBodySize: 1024},
	)
	recorder = serve(relaxed, requestWithBody(http.MethodPost, "/api/pay/mock/callback", "12345", jsonContentType))
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want the later limit to allow the body", recorder.Code)
	}
}

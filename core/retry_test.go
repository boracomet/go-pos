package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func retryTestClient(retry *RetryConfig, doer Doer) *HTTPClient {
	return NewHTTPClient(HTTPClientOptions{
		Provider: "test",
		BaseURL:  "https://api.example.com",
		Timeout:  time.Second,
		Retry:    retry,
		Doer:     doer,
	})
}

func retryTestNetworkError() *httpTestDoer {
	return &httpTestDoer{respond: func(httpTestCall) (*http.Response, error) {
		return nil, errors.New("socket hang up")
	}}
}

func TestHTTPClientRetry(t *testing.T) {
	retry := &RetryConfig{Attempts: 3, Delay: time.Millisecond}

	t.Run("never retries POST requests (payments, refunds)", func(t *testing.T) {
		doer := retryTestNetworkError()
		_, err := retryTestClient(retry, doer).Post(context.Background(), "/payment", "{}", RequestConfig{})
		if err == nil {
			t.Fatal("Post succeeded, want a transport error")
		}
		if len(doer.calls) != 1 {
			t.Errorf("calls = %d, want 1: a payment must never be sent twice", len(doer.calls))
		}
	})

	t.Run("retries GET requests on network errors", func(t *testing.T) {
		doer := retryTestNetworkError()
		_, err := retryTestClient(retry, doer).Get(context.Background(), "/status", RequestConfig{})
		if err == nil {
			t.Fatal("Get succeeded, want a transport error")
		}
		if len(doer.calls) != 3 {
			t.Errorf("calls = %d, want 3", len(doer.calls))
		}
	})

	t.Run("retries POST requests explicitly marked retryable (read-only queries)", func(t *testing.T) {
		doer := retryTestNetworkError()
		_, err := retryTestClient(retry, doer).Post(context.Background(), "/query", "{}", RequestConfig{Retryable: true})
		if err == nil {
			t.Fatal("Post succeeded, want a transport error")
		}
		if len(doer.calls) != 3 {
			t.Errorf("calls = %d, want 3", len(doer.calls))
		}
	})

	t.Run("retries on configured status codes only", func(t *testing.T) {
		doer := &httpTestDoer{}
		doer.respond = func(call httpTestCall) (*http.Response, error) {
			status := http.StatusBadRequest
			if len(doer.calls) == 1 {
				status = http.StatusServiceUnavailable
			}
			return httpTestResponse(status, "{}"), nil
		}
		client := retryTestClient(&RetryConfig{Attempts: 5, Delay: time.Millisecond, StatusCodes: []int{503}}, doer)
		_, err := client.Get(context.Background(), "/status", RequestConfig{})
		if err == nil {
			t.Fatal("Get succeeded, want an error")
		}
		if !strings.Contains(err.Error(), "400") {
			t.Errorf("error = %q, want the 400 status", err.Error())
		}
		if len(doer.calls) != 2 {
			t.Errorf("calls = %d, want 2", len(doer.calls))
		}
	})

	t.Run("returns the response of a successful retry", func(t *testing.T) {
		doer := &httpTestDoer{}
		doer.respond = func(call httpTestCall) (*http.Response, error) {
			if len(doer.calls) == 1 {
				return nil, errors.New("fetch failed")
			}
			return httpTestResponse(http.StatusOK, `{"ok":true}`), nil
		}
		res, err := retryTestClient(retry, doer).Get(context.Background(), "/status", RequestConfig{})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		record, ok := res.Data.(map[string]any)
		if !ok || record["ok"] != true {
			t.Errorf("data = %#v, want {ok:true}", res.Data)
		}
		if len(doer.calls) != 2 {
			t.Errorf("calls = %d, want 2", len(doer.calls))
		}
	})

	t.Run("does nothing when retry is not configured", func(t *testing.T) {
		doer := retryTestNetworkError()
		_, err := retryTestClient(nil, doer).Get(context.Background(), "/status", RequestConfig{})
		if err == nil {
			t.Fatal("Get succeeded, want a transport error")
		}
		if len(doer.calls) != 1 {
			t.Errorf("calls = %d, want 1", len(doer.calls))
		}
	})
}

func TestRetryConfigDefaults(t *testing.T) {
	if got := (RetryConfig{}).delay(); got != time.Second {
		t.Errorf("delay() = %v, want 1s", got)
	}
	if got := (RetryConfig{Delay: 250 * time.Millisecond}).delay(); got != 250*time.Millisecond {
		t.Errorf("delay() = %v, want 250ms", got)
	}
	config := RetryConfig{StatusCodes: []int{502, 503}}
	if !config.retriesStatus(503) {
		t.Error("retriesStatus(503) = false, want true")
	}
	if config.retriesStatus(500) {
		t.Error("retriesStatus(500) = true, want false")
	}
	if (RetryConfig{}).retriesStatus(503) {
		t.Error("an empty StatusCodes list must not retry any status")
	}
}

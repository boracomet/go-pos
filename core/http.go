package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Doer is the transport used for provider calls. *http.Client satisfies it;
// tests can substitute a stub.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// RequestConfig describes one provider call.
type RequestConfig struct {
	Method string
	// URL is relative to the client's BaseURL, or absolute.
	URL string
	// Data is the already-serialized request body.
	Data    string
	Headers map[string]string
	// Retryable marks a read-only query sent with POST as safe to retry.
	// Payment, refund and cancel requests are never retried without it.
	Retryable bool
	// ResponseType is "json" (default, falls back to text) or "text".
	ResponseType string
	// ValidateStatus decides which statuses resolve instead of failing. Default: 2xx.
	ValidateStatus func(int) bool
	// Timeout overrides the client's timeout.
	Timeout time.Duration
}

// Response is a provider answer. Data holds parsed JSON (or the raw text when
// the body is not JSON); Text always holds the raw body.
type Response struct {
	Data    any
	Text    string
	Status  int
	Headers map[string]string
}

// HTTPError is returned by HTTPClient.
//
// Response is set when the server answered with a status that failed
// ValidateStatus. IsNetworkError is set when no response arrived (timeout,
// connection reset, DNS failure) — the provider may still have processed the
// request. Config never carries the body or headers: they hold card data and
// credentials, and the error is passed to the logger.
type HTTPError struct {
	Message        string
	IsNetworkError bool
	Code           string
	Response       *Response
	Config         RequestConfig
	Attempts       int
	Cause          error
}

func (e *HTTPError) Error() string { return e.Message }
func (e *HTTPError) Unwrap() error { return e.Cause }

// HTTPClientOptions configures an HTTPClient.
type HTTPClientOptions struct {
	Provider string
	BaseURL  string
	Timeout  time.Duration
	Headers  map[string]string
	Doer     Doer
	Logger   Logger
	Retry    *RetryConfig
}

// HTTPClient retries only idempotent requests: GET/HEAD/OPTIONS or requests
// marked Retryable. Payment, refund and cancel requests are never retried,
// because a timeout after the provider received the request would otherwise
// charge or refund twice.
type HTTPClient struct {
	opts HTTPClientOptions
}

// NewHTTPClient builds a client. A nil Doer defaults to http.DefaultClient and a
// nil Logger to NopLogger.
func NewHTTPClient(opts HTTPClientOptions) *HTTPClient {
	if opts.Doer == nil {
		opts.Doer = &http.Client{}
	}
	if opts.Logger == nil {
		opts.Logger = NopLogger{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	return &HTTPClient{opts: opts}
}

var idempotentMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true}

func joinURL(baseURL, url string) string {
	if strings.HasPrefix(strings.ToLower(url), "http://") || strings.HasPrefix(strings.ToLower(url), "https://") || baseURL == "" {
		return url
	}
	if url == "" {
		return baseURL
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(url, "/")
}

// Get issues a GET request.
func (c *HTTPClient) Get(ctx context.Context, url string, cfg RequestConfig) (*Response, error) {
	cfg.Method, cfg.URL = http.MethodGet, url
	return c.Request(ctx, cfg)
}

// Post issues a POST request with an already-serialized body.
func (c *HTTPClient) Post(ctx context.Context, url, data string, cfg RequestConfig) (*Response, error) {
	cfg.Method, cfg.URL, cfg.Data = http.MethodPost, url, data
	return c.Request(ctx, cfg)
}

// Request issues a call, retrying only when the request is safe to retry.
func (c *HTTPClient) Request(ctx context.Context, cfg RequestConfig) (*Response, error) {
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	safe := cfg.Retryable || idempotentMethods[cfg.Method]
	attempts := 1
	if safe && c.opts.Retry != nil && c.opts.Retry.Attempts > 1 {
		attempts = c.opts.Retry.Attempts
	}

	for attempt := 1; ; attempt++ {
		resp, err := c.send(ctx, cfg)
		if err == nil {
			return resp, nil
		}
		var httpErr *HTTPError
		retryable := errors.As(err, &httpErr) &&
			(httpErr.IsNetworkError ||
				(httpErr.Response != nil && c.opts.Retry != nil && c.opts.Retry.retriesStatus(httpErr.Response.Status)))
		if !retryable || attempt >= attempts {
			if httpErr != nil {
				httpErr.Attempts = attempt
			}
			return nil, err
		}
		delay := time.Second
		if c.opts.Retry != nil {
			delay = c.opts.Retry.delay()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (c *HTTPClient) send(ctx context.Context, cfg RequestConfig) (*Response, error) {
	url := joinURL(c.opts.BaseURL, cfg.URL)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = c.opts.Timeout
	}

	c.opts.Logger.Debug(fmt.Sprintf("[%s] %s %s", c.opts.Provider, cfg.Method, url), map[string]any{
		"provider": c.opts.Provider, "method": cfg.Method, "url": url,
	})

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if cfg.Method != http.MethodGet && cfg.Method != http.MethodHead && cfg.Data != "" {
		body = strings.NewReader(cfg.Data)
	}
	req, err := http.NewRequestWithContext(reqCtx, cfg.Method, url, body)
	if err != nil {
		return nil, c.transportError(cfg, err)
	}
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	res, err := c.opts.Doer.Do(req)
	if err != nil {
		httpErr := c.transportError(cfg, err)
		c.opts.Logger.Error(fmt.Sprintf("[%s] Request failed: %s", c.opts.Provider, httpErr.Message), httpErr, map[string]any{
			"provider": c.opts.Provider, "url": url,
		})
		return nil, httpErr
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		httpErr := c.transportError(cfg, err)
		c.opts.Logger.Error(fmt.Sprintf("[%s] Request failed: %s", c.opts.Provider, httpErr.Message), httpErr, map[string]any{
			"provider": c.opts.Provider, "url": url,
		})
		return nil, httpErr
	}

	text := string(raw)
	headers := make(map[string]string, len(res.Header))
	for k := range res.Header {
		headers[k] = res.Header.Get(k)
	}
	result := &Response{Data: parseBody(text, cfg.ResponseType), Text: text, Status: res.StatusCode, Headers: headers}

	validate := cfg.ValidateStatus
	if validate == nil {
		validate = func(status int) bool { return status >= 200 && status < 300 }
	}
	if !validate(res.StatusCode) {
		httpErr := &HTTPError{
			Message:  fmt.Sprintf("Request failed with status code %d", res.StatusCode),
			Response: result,
			Config:   sanitizeConfig(cfg),
		}
		c.opts.Logger.Error(fmt.Sprintf("[%s] Request failed: %s", c.opts.Provider, httpErr.Message), httpErr, map[string]any{
			"provider": c.opts.Provider, "url": url, "status": res.StatusCode,
		})
		return nil, httpErr
	}

	c.opts.Logger.Debug(fmt.Sprintf("[%s] %d %s", c.opts.Provider, res.StatusCode, url), map[string]any{
		"provider": c.opts.Provider, "status": res.StatusCode, "url": url,
	})
	return result, nil
}

func (c *HTTPClient) transportError(cfg RequestConfig, err error) *HTTPError {
	code, message := "", err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		code, message = "ETIMEDOUT", "Request timed out"
	} else if message == "" {
		message = "Network error"
	}
	return &HTTPError{
		Message:        message,
		IsNetworkError: true,
		Code:           code,
		Config:         sanitizeConfig(cfg),
		Cause:          err,
	}
}

// sanitizeConfig strips the body and headers, which carry card data and credentials.
func sanitizeConfig(cfg RequestConfig) RequestConfig {
	cfg.Data, cfg.Headers = "", nil
	return cfg
}

func parseBody(text, responseType string) any {
	if responseType == "text" || text == "" {
		return text
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return text
	}
	return parsed
}

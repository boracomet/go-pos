// Package adapters serves the payment handler over net/http.
//
// The handler in core is framework-agnostic: an adapter only converts an
// incoming request, calls Handle and writes the response. One adapter covers
// every Go web framework, because they all speak net/http:
//
//	http.Handle("/api/pay/", adapters.HTTPHandler(payment))        // stdlib
//	r.Mount("/api/pay", adapters.HTTPHandler(payment))             // chi
//	r.Use(adapters.Middleware(payment))                            // chi, net/http
//	e.Any("/api/pay/*", echo.WrapHandler(adapters.HTTPHandler(p))) // echo
//	r.Any("/api/pay/*", gin.WrapH(adapters.HTTPHandler(p)))        // gin
//
// The request body is read as raw text, so form-urlencoded bank callbacks reach
// the handler untouched and JSON bodies are parsed by the handler itself. No
// body-parsing middleware is needed on the payment routes.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/boracomet/go-pos/core"
)

// Handler serves a payment request. *core.Handler satisfies it.
type Handler interface {
	Handle(ctx context.Context, request *core.HandlerRequest) (*core.HandlerResponse, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, request *core.HandlerRequest) (*core.HandlerResponse, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, request *core.HandlerRequest) (*core.HandlerResponse, error) {
	return f(ctx, request)
}

// HandlerProvider is a payment facade exposing its handler.
type HandlerProvider interface {
	Handler() Handler
}

// HandlerSource is where an adapter gets the handler from: a Handler, a value
// exposing Handler() (the payment facade), or a function returning either. A
// function is called on every request, so the facade can be created lazily.
type HandlerSource any

// ResolveHandler resolves a HandlerSource. It returns a ConfigurationError for a
// source that does not provide a handler.
func ResolveHandler(source HandlerSource) (Handler, error) {
	switch value := source.(type) {
	case nil:
		return nil, core.NewConfigurationError("The adapter needs a payment handler", "")
	case Handler:
		return value, nil
	case HandlerProvider:
		return value.Handler(), nil
	case interface{ Handler() *core.Handler }:
		return value.Handler(), nil
	case func() Handler:
		return ResolveHandler(value())
	case func() *core.Handler:
		return ResolveHandler(value())
	case func() (Handler, error):
		handler, err := value()
		if err != nil {
			return nil, err
		}
		return ResolveHandler(handler)
	case func() HandlerSource:
		return ResolveHandler(value())
	case func() (HandlerSource, error):
		next, err := value()
		if err != nil {
			return nil, err
		}
		return ResolveHandler(next)
	default:
		return nil, core.NewConfigurationError(
			fmt.Sprintf("Unsupported handler source %T: pass a handler, a facade or a function returning one", source), "")
	}
}

// DefaultMaxBodySize is the default limit for request bodies read by the
// adapters: 1 MiB. Bank callbacks are a few kB.
const DefaultMaxBodySize int64 = 1024 * 1024

// AdapterOptions configures an adapter.
type AdapterOptions struct {
	// MaxBodySize is the largest request body read, in bytes. Larger requests get
	// 413. Zero or less means DefaultMaxBodySize.
	MaxBodySize int64
	// OnError receives unexpected failures (a body that could not be read, a
	// handler that returned an error) instead of the default empty 500 response.
	// It is the equivalent of Express's next(error): the response is left
	// untouched, so the error must not be written to the client by the caller
	// either. It never receives a BodyTooLargeError.
	OnError func(w http.ResponseWriter, r *http.Request, err error)
}

func (o AdapterOptions) maxBodySize() int64 {
	if o.MaxBodySize <= 0 {
		return DefaultMaxBodySize
	}
	return o.MaxBodySize
}

func (o AdapterOptions) fail(w http.ResponseWriter, r *http.Request, err error) {
	if o.OnError != nil {
		o.OnError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
}

func mergeOptions(options []AdapterOptions) AdapterOptions {
	merged := AdapterOptions{}
	for _, option := range options {
		if option.MaxBodySize != 0 {
			merged.MaxBodySize = option.MaxBodySize
		}
		if option.OnError != nil {
			merged.OnError = option.OnError
		}
	}
	return merged
}

// BodyTooLargeError reports a request body larger than AdapterOptions.MaxBodySize.
type BodyTooLargeError struct{}

func (e *BodyTooLargeError) Error() string { return "Request body too large" }

// PayloadTooLarge is the 413 response the adapters send for an oversized body.
func PayloadTooLarge() *core.HandlerResponse {
	return &core.HandlerResponse{
		Status:  http.StatusRequestEntityTooLarge,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]any{"error": true, "message": "Request body too large"},
	}
}

// isBodyTooLarge reports whether err is a BodyTooLargeError.
func isBodyTooLarge(err error) bool {
	var tooLarge *BodyTooLargeError
	return errors.As(err, &tooLarge)
}

package adapters

import (
	"net/http"
	"strings"
)

// DefaultBasePath is the path prefix the handler is mounted on when
// core.HandlerOptions.BasePath is empty.
const DefaultBasePath = "/api/pay"

// HTTPHandler serves the payment routes as an http.Handler. Mount it on the
// handler's base path (default "/api/pay"); requests outside it are answered
// with the handler's 404.
//
// The body is read raw, so both JSON requests and form-urlencoded provider
// callbacks work without a body parser. A body larger than MaxBodySize gets
// 413; an unexpected failure gets an empty 500 (or AdapterOptions.OnError), so
// no internal message reaches the client.
func HTTPHandler(source HandlerSource, options ...AdapterOptions) http.Handler {
	opts := mergeOptions(options)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := FromRequest(r, opts)
		if err != nil {
			if isBodyTooLarge(err) {
				_ = WriteResponse(w, PayloadTooLarge())
				return
			}
			opts.fail(w, r, err)
			return
		}
		handler, err := ResolveHandler(source)
		if err != nil {
			opts.fail(w, r, err)
			return
		}
		response, err := handler.Handle(r.Context(), request)
		if err != nil {
			opts.fail(w, r, err)
			return
		}
		_ = WriteResponse(w, response)
	})
}

// Middleware serves the payment routes under basePath and passes every other
// request to next. It is the standard net/http middleware shape, so it works
// with chi (r.Use) and any router built on net/http. An empty basePath means
// "/api/pay".
func Middleware(basePath string, source HandlerSource, options ...AdapterOptions) func(http.Handler) http.Handler {
	handler := HTTPHandler(source, options...)
	if basePath == "" {
		basePath = DefaultBasePath
	}
	prefix := "/" + strings.Trim(basePath, "/")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimRight(r.URL.Path, "/")
			if prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/") {
				handler.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

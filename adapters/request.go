package adapters

import (
	"io"
	"net/http"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// FromRequest converts an incoming request for the handler. The body is passed
// as raw text (nil when empty), so form-urlencoded callbacks reach the handler
// untouched. It returns a BodyTooLargeError when the body is larger than
// AdapterOptions.MaxBodySize.
func FromRequest(r *http.Request, options ...AdapterOptions) (*core.HandlerRequest, error) {
	opts := mergeOptions(options)
	method := strings.ToUpper(r.Method)
	if method == "" {
		method = http.MethodGet
	}

	var body any
	if method != http.MethodGet && method != http.MethodHead {
		text, err := readBody(r, opts.maxBodySize())
		if err != nil {
			return nil, err
		}
		if text != "" {
			body = text
		}
	}

	return &core.HandlerRequest{
		Method:  method,
		URL:     requestURL(r),
		Headers: flattenHeaders(r.Header),
		Body:    body,
	}, nil
}

// requestURL keeps the path and query, including a request target that is
// already absolute (a proxied request).
func requestURL(r *http.Request) string {
	if r.URL == nil {
		return "/"
	}
	return r.URL.RequestURI()
}

// flattenHeaders lowercases header names and joins repeated values with ", ",
// the way a web Request and a Node request expose them.
func flattenHeaders(header http.Header) map[string]string {
	flat := make(map[string]string, len(header))
	for name, values := range header {
		flat[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return flat
}

// readBody reads at most limit bytes, stopping as soon as the body grows past
// it instead of buffering an oversized request.
func readBody(r *http.Request, limit int64) (string, error) {
	if r.ContentLength > limit {
		return "", &BodyTooLargeError{}
	}
	if r.Body == nil {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > limit {
		return "", &BodyTooLargeError{}
	}
	return string(raw), nil
}

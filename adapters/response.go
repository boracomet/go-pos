package adapters

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// SerializedResponse is a handler response ready for the wire. Body is nil when
// the response carries no body (a redirect, 204, or an absent body).
type SerializedResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// noBodyStatuses are the statuses a response body must not be sent with.
var noBodyStatuses = map[int]bool{
	http.StatusNoContent:         true,
	http.StatusResetContent:      true,
	http.StatusMovedPermanently:  true,
	http.StatusFound:             true,
	http.StatusSeeOther:          true,
	http.StatusNotModified:       true,
	http.StatusTemporaryRedirect: true,
	http.StatusPermanentRedirect: true,
}

// SerializeResponse serializes a handler response: text bodies (a provider's
// "OK" acknowledgement) as they are, redirects without a body but keeping
// Location, everything else as JSON. The response's headers are copied, so a
// stored response can be replayed unchanged.
func SerializeResponse(response *core.HandlerResponse) (SerializedResponse, error) {
	out := SerializedResponse{Headers: map[string]string{}}
	if response == nil {
		return out, nil
	}
	out.Status = response.Status
	for name, value := range response.Headers {
		out.Headers[name] = value
	}
	if noBodyStatuses[response.Status] || response.Body == nil {
		return out, nil
	}
	if text, ok := response.Body.(string); ok {
		out.Body = bodyBytes(text)
		return out, nil
	}
	if !hasContentType(out.Headers) {
		out.Headers["Content-Type"] = "application/json"
	}
	data, err := marshalJSON(response.Body)
	if err != nil {
		return out, err
	}
	out.Body = data
	return out, nil
}

// WriteResponse serializes a handler response and writes it to w.
func WriteResponse(w http.ResponseWriter, response *core.HandlerResponse) error {
	serialized, err := SerializeResponse(response)
	if err != nil {
		return err
	}
	return WriteSerialized(w, serialized)
}

// WriteSerialized writes an already serialized response to w.
func WriteSerialized(w http.ResponseWriter, response SerializedResponse) error {
	for name, value := range response.Headers {
		w.Header().Set(name, value)
	}
	if response.Status > 0 {
		w.WriteHeader(response.Status)
	}
	if len(response.Body) == 0 {
		return nil
	}
	_, err := w.Write(response.Body)
	return err
}

func hasContentType(headers map[string]string) bool {
	for name := range headers {
		if strings.EqualFold(name, "content-type") {
			return true
		}
	}
	return false
}

// marshalJSON serializes without HTML escaping and without a trailing newline,
// so the bytes match a plain JSON serializer.
func marshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// bodyBytes keeps an empty text body distinguishable from no body at all.
func bodyBytes(text string) []byte {
	data := []byte(text)
	if data == nil {
		return []byte{}
	}
	return data
}

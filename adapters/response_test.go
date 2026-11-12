package adapters_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

func writeResponse(t *testing.T, response *core.HandlerResponse) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	if err := adapters.WriteResponse(recorder, response); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	return recorder
}

func TestWriteResponseCopiesTheStatusHeadersAndJSONBody(t *testing.T) {
	recorder := writeResponse(t, &core.HandlerResponse{
		Status:  http.StatusCreated,
		Headers: map[string]string{"X-Payment": "ok"},
		Body:    map[string]any{"paymentId": "P1"},
	})

	if recorder.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", recorder.Code)
	}
	if recorder.Header().Get("X-Payment") != "ok" {
		t.Errorf("X-Payment = %q, want ok", recorder.Header().Get("X-Payment"))
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q, want JSON", recorder.Header().Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", recorder.Body.String(), err)
	}
	if body["paymentId"] != "P1" {
		t.Errorf("body = %v, want paymentId P1", body)
	}
}

func TestWriteResponseKeepsARedirectLocationWithoutABody(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		recorder := writeResponse(t, &core.HandlerResponse{
			Status:  status,
			Headers: map[string]string{"Location": "/orders/P1"},
			Body:    "",
		})
		if recorder.Code != status {
			t.Errorf("status = %d, want %d", recorder.Code, status)
		}
		if recorder.Header().Get("Location") != "/orders/P1" {
			t.Errorf("Location = %q, want /orders/P1", recorder.Header().Get("Location"))
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("status %d body = %q, want empty", status, recorder.Body.String())
		}
	}
}

func TestWriteResponseSendsPlainTextWithoutAJSONContentType(t *testing.T) {
	recorder := writeResponse(t, &core.HandlerResponse{
		Status:  http.StatusOK,
		Headers: map[string]string{"Content-Type": "text/plain"},
		Body:    "OK",
	})
	if recorder.Body.String() != "OK" {
		t.Errorf("body = %q, want OK", recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", recorder.Header().Get("Content-Type"))
	}
}

func TestWriteResponseKeepsAnExistingContentType(t *testing.T) {
	recorder := writeResponse(t, &core.HandlerResponse{
		Status:  http.StatusOK,
		Headers: map[string]string{"content-type": "application/problem+json"},
		Body:    map[string]any{"error": true},
	})
	if recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("Content-Type = %q, want the handler's value", recorder.Header().Get("Content-Type"))
	}
	if strings.Contains(recorder.Body.String(), "\n") {
		t.Errorf("body = %q, want no trailing newline", recorder.Body.String())
	}
}

func TestWriteResponseCopiesA404Verbatim(t *testing.T) {
	recorder := writeResponse(t, &core.HandlerResponse{
		Status:  http.StatusNotFound,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]any{"error": true, "message": "Route not found"},
	})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", recorder.Body.String(), err)
	}
	if body["error"] != true || body["message"] != "Route not found" {
		t.Errorf("body = %v, want the handler's 404 unchanged", body)
	}
}

func TestWriteResponseOmitsTheBodyForNoBodyStatuses(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusResetContent, http.StatusNotModified} {
		recorder := writeResponse(t, &core.HandlerResponse{
			Status: status,
			Body:   map[string]any{"ignored": true},
		})
		if recorder.Code != status {
			t.Errorf("status = %d, want %d", recorder.Code, status)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("status %d body = %q, want empty", status, recorder.Body.String())
		}
	}
}

func TestSerializeResponseDoesNotEscapeHTML(t *testing.T) {
	serialized, err := adapters.SerializeResponse(&core.HandlerResponse{
		Status: http.StatusOK,
		Body:   map[string]any{"html": `<a href="https://shop.test?a=1&b=2">&</a>`},
	})
	if err != nil {
		t.Fatalf("SerializeResponse: %v", err)
	}
	if !strings.Contains(string(serialized.Body), "&b=2") {
		t.Errorf("body = %s, want unescaped HTML", serialized.Body)
	}
	if strings.HasSuffix(string(serialized.Body), "\n") {
		t.Errorf("body = %s, want no trailing newline", serialized.Body)
	}
}

func TestSerializeResponseCopiesTheHeaders(t *testing.T) {
	response := &core.HandlerResponse{
		Status:  http.StatusOK,
		Headers: map[string]string{"X-Payment": "ok"},
		Body:    "OK",
	}
	serialized, err := adapters.SerializeResponse(response)
	if err != nil {
		t.Fatalf("SerializeResponse: %v", err)
	}
	serialized.Headers["X-Payment"] = "changed"
	if response.Headers["X-Payment"] != "ok" {
		t.Error("serializing changed the handler's headers")
	}
	if string(serialized.Body) != "OK" {
		t.Errorf("body = %q, want OK", serialized.Body)
	}
}

func TestSerializeResponseHandlesAnAbsentResponse(t *testing.T) {
	serialized, err := adapters.SerializeResponse(nil)
	if err != nil {
		t.Fatalf("SerializeResponse: %v", err)
	}
	if serialized.Status != 0 || len(serialized.Body) != 0 {
		t.Errorf("serialized = %+v, want an empty response", serialized)
	}

	recorder := httptest.NewRecorder()
	if err := adapters.WriteSerialized(recorder, serialized); err != nil {
		t.Fatalf("WriteSerialized: %v", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("status %d body %q, want the recorder's default 200 and no body", recorder.Code, recorder.Body.String())
	}
}

func TestSerializeResponseReportsAMarshalError(t *testing.T) {
	response := &core.HandlerResponse{Status: http.StatusOK, Body: map[string]any{"bad": make(chan int)}}
	if _, err := adapters.SerializeResponse(response); err == nil {
		t.Fatal("an unserializable body was accepted")
	}
	recorder := httptest.NewRecorder()
	if err := adapters.WriteResponse(recorder, response); err == nil {
		t.Fatal("WriteResponse hid the marshal error")
	}
}

func TestPayloadTooLarge(t *testing.T) {
	response := adapters.PayloadTooLarge()
	if response.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Status)
	}
	recorder := writeResponse(t, response)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", recorder.Body.String(), err)
	}
	if body["error"] != true || body["message"] != "Request body too large" {
		t.Errorf("body = %v, want error true and Request body too large", body)
	}
}

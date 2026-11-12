package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFailureNetworkErrorIsPending(t *testing.T) {
	err := &HTTPError{
		Message:        "Request timed out",
		IsNetworkError: true,
		Code:           "ETIMEDOUT",
		Attempts:       3,
	}
	got := BuildFailure("test-pos", err, "fallback")

	if got.Status != StatusPending {
		t.Errorf("Status = %q, want %q", got.Status, StatusPending)
	}
	if got.ErrorCode != NetworkErrorCode {
		t.Errorf("ErrorCode = %q, want %q", got.ErrorCode, NetworkErrorCode)
	}
	if got.Code != CodeNetworkError {
		t.Errorf("Code = %q, want %q", got.Code, CodeNetworkError)
	}
	if got.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", got.Attempts)
	}
	if got.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0: no response arrived", got.HTTPStatus)
	}
	for _, want := range []string{"test-pos", "ETIMEDOUT", "Request timed out", "getPayment()"} {
		if !strings.Contains(got.ErrorMessage, want) {
			t.Errorf("ErrorMessage = %q, want it to contain %q", got.ErrorMessage, want)
		}
	}
}

func TestBuildFailureNetworkErrorWithoutACause(t *testing.T) {
	got := BuildFailure("test-pos", &HTTPError{IsNetworkError: true}, "fallback")
	if got.Status != StatusPending || got.Code != CodeNetworkError || got.ErrorCode != NetworkErrorCode {
		t.Errorf("failure = %+v, want a pending NETWORK_ERROR", got)
	}
	if strings.Contains(got.ErrorMessage, " (") {
		t.Errorf("ErrorMessage = %q, want no cause parenthesis", got.ErrorMessage)
	}
	if !strings.Contains(got.ErrorMessage, "No response from test-pos.") {
		t.Errorf("ErrorMessage = %q, want it to name the provider", got.ErrorMessage)
	}
}

func TestBuildFailureValidationError(t *testing.T) {
	err := NewValidationError("Invalid request: price is required", "test-pos", []ValidationIssue{
		{Path: "price", Message: "is required"},
	})
	got := BuildFailure("test-pos", err, "fallback")

	if got.Status != StatusFailure {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailure)
	}
	if got.ErrorCode != "VALIDATION_ERROR" {
		t.Errorf("ErrorCode = %q, want VALIDATION_ERROR", got.ErrorCode)
	}
	if got.Code != CodeInvalidRequest {
		t.Errorf("Code = %q, want %q", got.Code, CodeInvalidRequest)
	}
	if got.ErrorMessage != err.Message {
		t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, err.Message)
	}
}

func TestBuildFailureWrappedValidationError(t *testing.T) {
	err := fmt.Errorf("before the call: %w", NewValidationError("price is required", "test-pos", nil))
	got := BuildFailure("test-pos", err, "fallback")
	if got.ErrorCode != "VALIDATION_ERROR" || got.Code != CodeInvalidRequest {
		t.Errorf("failure = %+v, want VALIDATION_ERROR / INVALID_REQUEST", got)
	}
}

func TestBuildFailureGenericError(t *testing.T) {
	got := BuildFailure("test-pos", errors.New("boom"), "fallback")
	if got.Status != StatusFailure {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailure)
	}
	if got.Code != CodeUnknown {
		t.Errorf("Code = %q, want %q", got.Code, CodeUnknown)
	}
	if got.ErrorMessage != "boom" {
		t.Errorf("ErrorMessage = %q, want boom", got.ErrorMessage)
	}
}

func TestBuildFailureFallsBackWhenTheErrorHasNoMessage(t *testing.T) {
	got := BuildFailure("test-pos", errors.New(""), "Payment failed")
	if got.ErrorMessage != "Payment failed" {
		t.Errorf("ErrorMessage = %q, want the fallback", got.ErrorMessage)
	}
	got = BuildFailure("test-pos", nil, "Payment failed")
	if got.ErrorMessage != "Payment failed" || got.Status != StatusFailure || got.Code != CodeUnknown {
		t.Errorf("failure = %+v, want a failure with the fallback message", got)
	}
}

func TestBuildFailureUsesTheJSONErrorBody(t *testing.T) {
	body := map[string]any{
		"errorCode":    "10051",
		"errorMessage": "Kart limiti yetersiz",
		"status":       "failure",
	}
	err := &HTTPError{
		Message:  "Request failed with status code 400",
		Response: &Response{Data: body, Status: 400},
		Attempts: 2,
	}
	got := BuildFailure("test-pos", err, "fallback")

	if got.Status != StatusFailure {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailure)
	}
	if got.ErrorCode != "10051" {
		t.Errorf("ErrorCode = %q, want 10051", got.ErrorCode)
	}
	if got.Code != CodeUnknown {
		t.Errorf("Code = %q, want %q without a provider table", got.Code, CodeUnknown)
	}
	if got.ErrorMessage != "Kart limiti yetersiz" {
		t.Errorf("ErrorMessage = %q, want the provider message", got.ErrorMessage)
	}
	if got.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", got.HTTPStatus)
	}
	if got.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", got.Attempts)
	}
	if !reflect.DeepEqual(got.RawResponse, body) {
		t.Errorf("RawResponse = %v, want the response body", got.RawResponse)
	}
}

func TestBuildFailureKeepsTheHTTPMessageWithoutABodyCode(t *testing.T) {
	err := &HTTPError{
		Message:  "Request failed with status code 502",
		Response: &Response{Data: "bad gateway", Status: 502},
	}
	got := BuildFailure("test-pos", err, "fallback")
	if got.ErrorCode != "" {
		t.Errorf("ErrorCode = %q, want an empty raw code", got.ErrorCode)
	}
	if got.Code != CodeUnknown {
		t.Errorf("Code = %q, want %q", got.Code, CodeUnknown)
	}
	if got.ErrorMessage != err.Message {
		t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, err.Message)
	}
	if got.HTTPStatus != 502 {
		t.Errorf("HTTPStatus = %d, want 502", got.HTTPStatus)
	}
}

func TestBuildFailureAlwaysSetsANormalizedCode(t *testing.T) {
	branches := []struct {
		name string
		err  error
	}{
		{"network", &HTTPError{IsNetworkError: true, Code: "ECONNRESET"}},
		{"validation", NewValidationError("price is required", "test-pos", nil)},
		{"generic", errors.New("boom")},
		{"http body", &HTTPError{Response: &Response{Status: 400, Data: map[string]any{"errorCode": "51"}}}},
	}
	for _, branch := range branches {
		t.Run(branch.name, func(t *testing.T) {
			got := BuildFailure("test-pos", branch.err, "fallback")
			if got.Code == "" {
				t.Error("Code is empty: every failure carries a normalized code")
			}
			if got.Status != StatusPending && got.Status != StatusFailure {
				t.Errorf("Status = %q, want pending or failure", got.Status)
			}
			if got.ErrorMessage == "" {
				t.Error("ErrorMessage is empty")
			}
			if got.Code != ResolveErrorCode(got.ErrorCode, nil) {
				t.Errorf("Code = %q, want the resolution of ErrorCode %q", got.Code, got.ErrorCode)
			}
		})
	}
}

func TestApplyFailurePopulatesEveryResultType(t *testing.T) {
	results := []struct {
		name string
		out  func() FailureResult
	}{
		{"PaymentResponse", func() FailureResult { return &PaymentResponse{} }},
		{"ThreeDSInitResponse", func() FailureResult { return &ThreeDSInitResponse{} }},
		{"RefundResponse", func() FailureResult { return &RefundResponse{} }},
		{"CancelResponse", func() FailureResult { return &CancelResponse{} }},
		{"SaveCardResponse", func() FailureResult { return &SaveCardResponse{} }},
		{"ListCardsResponse", func() FailureResult { return &ListCardsResponse{} }},
		{"DeleteCardResponse", func() FailureResult { return &DeleteCardResponse{} }},
		{"InstallmentInfoResponse", func() FailureResult { return &InstallmentInfoResponse{} }},
		{"CheckoutFormInitResponse", func() FailureResult { return &CheckoutFormInitResponse{} }},
		{"CheckoutFormRetrieveResponse", func() FailureResult { return &CheckoutFormRetrieveResponse{} }},
		{"PWIPaymentInitResponse", func() FailureResult { return &PWIPaymentInitResponse{} }},
		{"PWIPaymentRetrieveResponse", func() FailureResult { return &PWIPaymentRetrieveResponse{} }},
	}
	branches := []struct {
		name      string
		err       error
		status    PaymentStatus
		errorCode string
		code      PaymentErrorCode
	}{
		{"network", &HTTPError{IsNetworkError: true}, StatusPending, NetworkErrorCode, CodeNetworkError},
		{"validation", NewValidationError("price is required", "test-pos", nil), StatusFailure, "VALIDATION_ERROR", CodeInvalidRequest},
		{"generic", errors.New("boom"), StatusFailure, "", CodeUnknown},
	}
	for _, branch := range branches {
		for _, result := range results {
			t.Run(branch.name+"/"+result.name, func(t *testing.T) {
				out := result.out()
				returned := ApplyFailure("test-pos", branch.err, "fallback", out)
				if returned != out {
					t.Fatal("ApplyFailure must return the result it was given")
				}
				status, code, errorCode := failureTestFields(t, out)
				if status != branch.status {
					t.Errorf("Status = %q, want %q", status, branch.status)
				}
				if errorCode != branch.errorCode {
					t.Errorf("ErrorCode = %q, want %q", errorCode, branch.errorCode)
				}
				if code != branch.code {
					t.Errorf("Code = %q, want %q", code, branch.code)
				}
				if message := failureTestField(t, out, "ErrorMessage"); message == "" {
					t.Error("ErrorMessage is empty")
				}
			})
		}
	}
}

func TestApplyFailureKeepsHTTPMetadata(t *testing.T) {
	err := &HTTPError{
		Message:  "Request failed with status code 500",
		Response: &Response{Status: 500, Data: map[string]any{"errorCode": "96"}},
		Attempts: 4,
	}
	out := ApplyFailure("test-pos", err, "fallback", &PaymentResponse{})
	if out.HTTPStatus != 500 {
		t.Errorf("HTTPStatus = %d, want 500", out.HTTPStatus)
	}
	if out.Attempts != 4 {
		t.Errorf("Attempts = %d, want 4", out.Attempts)
	}
	if out.ErrorCode != "96" || out.Code != CodeUnknown {
		t.Errorf("codes = %q/%q, want 96/UNKNOWN", out.ErrorCode, out.Code)
	}
	if out.RawResponse == nil {
		t.Error("RawResponse is empty, want the provider body")
	}
}

func TestSetFailureOverwritesPreviousValues(t *testing.T) {
	out := &PaymentResponse{
		Status:       StatusSuccess,
		PaymentID:    "P1",
		Code:         CodeCardDeclined,
		ErrorCode:    "05",
		ErrorMessage: "old",
		HTTPStatus:   200,
		Attempts:     1,
	}
	out.SetFailure(Failure{Status: StatusFailure, Code: CodeUnknown, ErrorCode: "", ErrorMessage: "new"})
	if out.Status != StatusFailure || out.Code != CodeUnknown || out.ErrorCode != "" || out.ErrorMessage != "new" {
		t.Errorf("result = %+v, want the failure to replace every field", out)
	}
	if out.HTTPStatus != 0 || out.Attempts != 0 {
		t.Errorf("HTTP metadata = %d/%d, want it cleared", out.HTTPStatus, out.Attempts)
	}
	if out.PaymentID != "P1" {
		t.Errorf("PaymentID = %q, want it kept", out.PaymentID)
	}
}

func TestResponseDataOf(t *testing.T) {
	body := map[string]any{"errorCode": "51"}
	cases := []struct {
		name string
		err  error
		want any
	}{
		{"nil", nil, nil},
		{"plain error", errors.New("boom"), nil},
		{"http error without a response", &HTTPError{Message: "timeout"}, nil},
		{"http error with a body", &HTTPError{Response: &Response{Data: body}}, body},
		{"wrapped", fmt.Errorf("wrapped: %w", &HTTPError{Response: &Response{Data: body}}), body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResponseDataOf(tc.err); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResponseDataOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNetworkCause(t *testing.T) {
	cases := []struct {
		name string
		err  *HTTPError
		want string
	}{
		{"code and message", &HTTPError{Code: "ECONNRESET", Message: "socket hang up"}, " (ECONNRESET: socket hang up)"},
		{"code only", &HTTPError{Code: "ETIMEDOUT"}, " (ETIMEDOUT)"},
		{"message only", &HTTPError{Message: "timeout"}, " (timeout)"},
		{"neither", &HTTPError{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := networkCause(tc.err); got != tc.want {
				t.Errorf("networkCause = %q, want %q", got, tc.want)
			}
		})
	}
}

// failureTestFields reads the three failure fields every result type carries.
func failureTestFields(t *testing.T, out any) (PaymentStatus, PaymentErrorCode, string) {
	t.Helper()
	return PaymentStatus(failureTestField(t, out, "Status")),
		PaymentErrorCode(failureTestField(t, out, "Code")),
		failureTestField(t, out, "ErrorCode")
}

func failureTestField(t *testing.T, out any, name string) string {
	t.Helper()
	value := reflect.ValueOf(out)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		t.Fatalf("result must be a non-nil pointer, got %T", out)
	}
	field := value.Elem().FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		t.Fatalf("%T has no string field %s", out, name)
	}
	return field.String()
}

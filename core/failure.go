package core

import (
	"errors"
	"fmt"
)

// NetworkErrorCode is the raw errorCode used when no response arrived.
const NetworkErrorCode = "NETWORK_ERROR"

// Failure is the common shape of a provider result that represents a failure.
// HTTPStatus is set only when the provider answered with an HTTP error; it is
// absent for transport errors and for failure bodies returned with a 2xx status.
// Attempts counts the HTTP attempts made, including the first (1 = no retry).
type Failure struct {
	Status          PaymentStatus
	Code            PaymentErrorCode
	ErrorCode       string
	ErrorMessage    string
	ProviderMessage string
	RawResponse     any
	HTTPStatus      int
	Attempts        int
}

// FailureResult is implemented by every response type that can carry a failure.
type FailureResult interface {
	SetFailure(Failure)
}

// BuildFailure builds the result returned when a provider call fails.
//
//   - Transport errors (timeout, connection reset, no response) become PENDING
//     with NETWORK_ERROR: the provider may still have processed the request.
//   - A ValidationError (bad input caught before calling the provider) becomes
//     FAILURE with VALIDATION_ERROR.
//   - Any other error becomes FAILURE, using errorCode/errorMessage from a JSON
//     error body when present and attaching the body as RawResponse.
func BuildFailure(providerName string, err error, fallback string) Failure {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		f := Failure{}
		if httpErr.Response != nil {
			f.HTTPStatus = httpErr.Response.Status
		}
		if httpErr.Attempts > 0 {
			f.Attempts = httpErr.Attempts
		}
		if httpErr.IsNetworkError {
			f.Status = StatusPending
			f.ErrorCode = NetworkErrorCode
			f.Code = ResolveErrorCode(NetworkErrorCode, nil)
			f.ErrorMessage = fmt.Sprintf(
				"No response from %s%s. The transaction may have been processed; verify it with getPayment() before retrying.",
				providerName, networkCause(httpErr))
			return f
		}
	}

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return Failure{
			Status:       StatusFailure,
			ErrorCode:    "VALIDATION_ERROR",
			Code:         ResolveErrorCode("VALIDATION_ERROR", nil),
			ErrorMessage: validationErr.Message,
		}
	}

	f := Failure{Status: StatusFailure}
	if httpErr != nil {
		if httpErr.Response != nil {
			f.HTTPStatus = httpErr.Response.Status
		}
		if httpErr.Attempts > 0 {
			f.Attempts = httpErr.Attempts
		}
		data := ResponseDataOf(err)
		f.RawResponse = data
		if body, ok := data.(map[string]any); ok {
			if code, ok := body["errorCode"].(string); ok {
				f.ErrorCode = code
			}
			if msg, ok := body["errorMessage"].(string); ok && msg != "" {
				f.ErrorMessage = msg
			}
		}
	}
	if f.ErrorMessage == "" {
		f.ErrorMessage = ErrorMessage(err, fallback)
	}
	f.Code = ResolveErrorCode(f.ErrorCode, nil)
	return f
}

// ApplyFailure builds a failure and writes it into out, returning out.
func ApplyFailure[T FailureResult](providerName string, err error, fallback string, out T) T {
	out.SetFailure(BuildFailure(providerName, err, fallback))
	return out
}

// ResponseDataOf returns the HTTP response body attached to an HTTP error, if any.
func ResponseDataOf(err error) any {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Response != nil {
		return httpErr.Response.Data
	}
	return nil
}

// networkCause renders " (ECONNRESET: socket hang up)" for transport errors.
func networkCause(err *HTTPError) string {
	if err.Code == "" && err.Message == "" {
		return ""
	}
	parts := make([]string, 0, 2)
	if err.Code != "" {
		parts = append(parts, err.Code)
	}
	if err.Message != "" {
		parts = append(parts, err.Message)
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ": "
		}
		out += p
	}
	return " (" + out + ")"
}

func (r *PaymentResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
	r.HTTPStatus, r.Attempts = f.HTTPStatus, f.Attempts
}

func (r *ThreeDSInitResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
	r.HTTPStatus, r.Attempts = f.HTTPStatus, f.Attempts
}

func (r *RefundResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
	r.HTTPStatus, r.Attempts = f.HTTPStatus, f.Attempts
}

func (r *CancelResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
	r.HTTPStatus, r.Attempts = f.HTTPStatus, f.Attempts
}

func (r *SaveCardResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *ListCardsResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *DeleteCardResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *InstallmentInfoResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *CheckoutFormInitResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *CheckoutFormRetrieveResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *PWIPaymentInitResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

func (r *PWIPaymentRetrieveResponse) SetFailure(f Failure) {
	r.Status, r.Code, r.ErrorCode, r.ErrorMessage = f.Status, f.Code, f.ErrorCode, f.ErrorMessage
	r.ProviderMessage, r.RawResponse = f.ProviderMessage, f.RawResponse
}

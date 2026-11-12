package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// eventsTestPointerResult embeds a response by pointer, as a provider wrapper may.
type eventsTestPointerResult struct {
	*PaymentResponse
	Extra string `json:"extra,omitempty"`
}

// eventsTestValueResult embeds a response by value.
type eventsTestValueResult struct {
	PaymentResponse
}

func TestEventForPaymentOperations(t *testing.T) {
	cases := []struct {
		operation PaymentOperation
		status    PaymentStatus
		want      PaymentEventType
	}{
		{OperationCreatePayment, StatusSuccess, EventPaymentSucceeded},
		{OperationCreatePayment, StatusPending, EventPaymentPending},
		{OperationCreatePayment, StatusFailure, EventPaymentFailed},
		{OperationCreatePayment, StatusCancelled, ""},
		{OperationCompleteThreeDSPayment, StatusSuccess, EventPaymentSucceeded},
		{OperationCompleteThreeDSPayment, StatusPending, EventPaymentPending},
		{OperationCompleteThreeDSPayment, StatusFailure, EventPaymentFailed},
		{OperationAuthorize, StatusSuccess, EventPaymentAuthorized},
		{OperationAuthorize, StatusPending, EventPaymentPending},
		{OperationAuthorize, StatusFailure, EventPaymentFailed},
		{OperationCapture, StatusSuccess, EventPaymentSucceeded},
		{OperationCapture, StatusPending, EventPaymentPending},
		{OperationCapture, StatusFailure, EventPaymentFailed},
		{OperationInitThreeDSPayment, StatusFailure, EventPaymentFailed},
		{OperationInitThreeDSPayment, StatusSuccess, ""},
		{OperationInitThreeDSPayment, StatusPending, ""},
		{OperationInitThreeDSAuthorize, StatusFailure, EventPaymentFailed},
		{OperationInitThreeDSAuthorize, StatusPending, ""},
		{OperationCancel, StatusSuccess, EventPaymentCancelled},
		{OperationCancel, StatusFailure, ""},
		{OperationCancel, StatusPending, ""},
		{OperationVoidAuthorization, StatusSuccess, EventPaymentCancelled},
		{OperationVoidAuthorization, StatusFailure, ""},
		{OperationRefund, StatusSuccess, EventRefundSucceeded},
		{OperationRefund, StatusFailure, EventRefundFailed},
		{OperationRefund, StatusPending, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.operation)+"/"+string(tc.status), func(t *testing.T) {
			result := &PaymentResponse{Status: tc.status}
			event := EventFor(tc.operation, "test-pos", &PaymentRequest{}, result)
			if tc.want == "" {
				if event != nil {
					t.Fatalf("event = %+v, want none", event)
				}
				return
			}
			if event == nil {
				t.Fatalf("event = nil, want %q", tc.want)
			}
			if event.Type != tc.want {
				t.Errorf("Type = %q, want %q", event.Type, tc.want)
			}
			if event.Operation != tc.operation {
				t.Errorf("Operation = %q, want %q", event.Operation, tc.operation)
			}
			if event.Provider != "test-pos" {
				t.Errorf("Provider = %q, want test-pos", event.Provider)
			}
			if event.Result != result {
				t.Error("Result must be the result the caller received")
			}
		})
	}
}

func TestEventForQueriesEmitNothing(t *testing.T) {
	operations := []PaymentOperation{
		OperationGetPayment,
		OperationSaveCard,
		OperationListCards,
		OperationDeleteCard,
		OperationBinCheck,
		OperationInstallmentInfo,
	}
	for _, operation := range operations {
		for _, status := range []PaymentStatus{StatusSuccess, StatusFailure, StatusPending} {
			if event := EventFor(operation, "test-pos", nil, &PaymentResponse{Status: status}); event != nil {
				t.Errorf("%s/%s emitted %+v, want no event", operation, status, event)
			}
		}
	}
}

func TestEventForUnknownOperationEmitsNothing(t *testing.T) {
	if event := EventFor(PaymentOperation("unknown"), "test-pos", nil, &PaymentResponse{Status: StatusSuccess}); event != nil {
		t.Errorf("event = %+v, want none", event)
	}
}

func TestEventForIgnoresForgedCallbacks(t *testing.T) {
	operations := []PaymentOperation{
		OperationCreatePayment,
		OperationCompleteThreeDSPayment,
		OperationAuthorize,
		OperationCapture,
		OperationRefund,
		OperationCancel,
		OperationInitThreeDSPayment,
	}
	for _, operation := range operations {
		for _, status := range []PaymentStatus{StatusSuccess, StatusFailure, StatusPending} {
			result := &PaymentResponse{Status: status, ErrorCode: "INVALID_HASH", Code: CodeInvalidHash}
			if event := EventFor(operation, "test-pos", nil, result); event != nil {
				t.Errorf("%s/%s with INVALID_HASH emitted %+v, want no event", operation, status, event)
			}
		}
	}
}

func TestEventForHandlesEmbeddedPointerResults(t *testing.T) {
	cases := []struct {
		name   string
		result any
		want   PaymentEventType
	}{
		{
			name:   "embedded pointer",
			result: eventsTestPointerResult{PaymentResponse: &PaymentResponse{Status: StatusSuccess, PaymentID: "P1"}},
			want:   EventPaymentSucceeded,
		},
		{
			name:   "pointer to embedded pointer",
			result: &eventsTestPointerResult{PaymentResponse: &PaymentResponse{Status: StatusFailure, Code: CodeCardDeclined}},
			want:   EventPaymentFailed,
		},
		{
			name:   "nil embedded pointer",
			result: eventsTestPointerResult{},
			want:   "",
		},
		{
			name:   "pointer with a nil embedded pointer",
			result: &eventsTestPointerResult{},
			want:   "",
		},
		{
			name:   "embedded value",
			result: eventsTestValueResult{PaymentResponse: PaymentResponse{Status: StatusPending}},
			want:   EventPaymentPending,
		},
		{
			name:   "plain map",
			result: map[string]any{"status": "success", "paymentId": "P9"},
			want:   EventPaymentSucceeded,
		},
		{
			name:   "nil result",
			result: nil,
			want:   "",
		},
		{
			name:   "typed nil result",
			result: (*PaymentResponse)(nil),
			want:   "",
		},
		{
			name:   "result without a status",
			result: &PaymentResponse{},
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := EventFor(OperationCreatePayment, "test-pos", &PaymentRequest{Price: "1"}, tc.result)
			if tc.want == "" {
				if event != nil {
					t.Fatalf("event = %+v, want none", event)
				}
				return
			}
			if event == nil {
				t.Fatalf("event = nil, want %q", tc.want)
			}
			if event.Type != tc.want {
				t.Errorf("Type = %q, want %q", event.Type, tc.want)
			}
		})
	}
}

func TestEventForForgedEmbeddedPointerResultEmitsNothing(t *testing.T) {
	result := eventsTestPointerResult{PaymentResponse: &PaymentResponse{
		Status:    StatusSuccess,
		ErrorCode: "INVALID_HASH",
	}}
	if event := EventFor(OperationCompleteThreeDSPayment, "test-pos", nil, result); event != nil {
		t.Errorf("event = %+v, want none for a forged callback", event)
	}
}

func TestEventForCarriesTheRequestAmountAndIds(t *testing.T) {
	request := &PaymentRequest{
		Price:          "10.00",
		PaidPrice:      "12.00",
		Currency:       "TRY",
		ConversationID: "ORDER-1",
	}
	result := &PaymentResponse{Status: StatusSuccess, PaymentID: "P1", ConversationID: "ORDER-2"}
	event := EventFor(OperationCreatePayment, "test-pos", request, result)
	if event == nil {
		t.Fatal("event = nil, want payment.succeeded")
	}
	if event.Amount != "12.00" {
		t.Errorf("Amount = %q, want the paid price 12.00", event.Amount)
	}
	if event.Currency != "TRY" {
		t.Errorf("Currency = %q, want TRY", event.Currency)
	}
	if event.PaymentID != "P1" {
		t.Errorf("PaymentID = %q, want P1", event.PaymentID)
	}
	if event.ConversationID != "ORDER-2" {
		t.Errorf("ConversationID = %q, want the result's ORDER-2", event.ConversationID)
	}
	if event.Request != request {
		t.Error("Request must be the request the caller passed")
	}
}

func TestEventForFallsBackToTheRequest(t *testing.T) {
	request := &PaymentRequest{Price: "10.00", ConversationID: "ORDER-1", Currency: "TRY"}
	result := &PaymentResponse{Status: StatusSuccess}
	event := EventFor(OperationCreatePayment, "test-pos", request, result)
	if event == nil {
		t.Fatal("event = nil, want payment.succeeded")
	}
	if event.Amount != "10.00" {
		t.Errorf("Amount = %q, want the price 10.00 when there is no paid price", event.Amount)
	}
	if event.ConversationID != "ORDER-1" {
		t.Errorf("ConversationID = %q, want the request's ORDER-1", event.ConversationID)
	}
	if event.PaymentID != "" {
		t.Errorf("PaymentID = %q, want an empty id", event.PaymentID)
	}
}

func TestEventForCaptureUsesTheCaptureAmount(t *testing.T) {
	request := &CaptureRequest{PaymentID: "P1", Amount: "5.00", Currency: "TRY"}
	result := &PaymentResponse{Status: StatusSuccess, PaymentID: "P1"}
	event := EventFor(OperationCapture, "test-pos", request, result)
	if event == nil {
		t.Fatal("event = nil, want payment.succeeded")
	}
	if event.Amount != "5.00" {
		t.Errorf("Amount = %q, want the captured amount 5.00", event.Amount)
	}
}

func TestEventForRefundUsesTheRefundPrice(t *testing.T) {
	request := &RefundRequest{PaymentID: "P1", Price: "1.00", Currency: "TRY", ConversationID: "ORDER-1"}
	result := &RefundResponse{Status: StatusSuccess, ConversationID: "ORDER-1"}
	event := EventFor(OperationRefund, "test-pos", request, result)
	if event == nil {
		t.Fatal("event = nil, want refund.succeeded")
	}
	if event.Amount != "1.00" {
		t.Errorf("Amount = %q, want 1.00", event.Amount)
	}
	if event.PaymentID != "P1" {
		t.Errorf("PaymentID = %q, want the refunded payment P1", event.PaymentID)
	}
}

func TestEventForCarriesTheNormalizedCode(t *testing.T) {
	result := &PaymentResponse{Status: StatusFailure, ErrorCode: "51", Code: CodeInsufficientFunds}
	event := EventFor(OperationCreatePayment, "test-pos", &PaymentRequest{}, result)
	if event == nil {
		t.Fatal("event = nil, want payment.failed")
	}
	if event.Code != CodeInsufficientFunds {
		t.Errorf("Code = %q, want %q", event.Code, CodeInsufficientFunds)
	}
}

func TestEventForThreeDSCompletionUsesTheCallbackData(t *testing.T) {
	callback := map[string]any{"paymentId": "P1", "conversationId": "ORDER-1", "mdStatus": "1"}
	result := &PaymentResponse{Status: StatusSuccess, PaymentID: "P1"}
	event := EventFor(OperationCompleteThreeDSPayment, "test-pos", callback, result)
	if event == nil {
		t.Fatal("event = nil, want payment.succeeded")
	}
	if event.ConversationID != "ORDER-1" {
		t.Errorf("ConversationID = %q, want the callback's ORDER-1", event.ConversationID)
	}
	if event.Amount != "" {
		t.Errorf("Amount = %q, want an empty amount: a callback carries none", event.Amount)
	}
}

func TestPaymentEventTypesAreCompleteAndUnique(t *testing.T) {
	want := []PaymentEventType{
		EventPaymentSucceeded,
		EventPaymentAuthorized,
		EventPaymentPending,
		EventPaymentFailed,
		EventPaymentCancelled,
		EventPaymentExpired,
		EventPaymentUnresolved,
		EventRefundSucceeded,
		EventRefundFailed,
	}
	if len(PaymentEventTypes) != len(want) {
		t.Fatalf("PaymentEventTypes has %d entries, want %d", len(PaymentEventTypes), len(want))
	}
	seen := make(map[PaymentEventType]bool, len(want))
	for i, eventType := range want {
		if PaymentEventTypes[i] != eventType {
			t.Errorf("PaymentEventTypes[%d] = %q, want %q", i, PaymentEventTypes[i], eventType)
		}
		if seen[eventType] {
			t.Errorf("PaymentEventTypes lists %q twice", eventType)
		}
		seen[eventType] = true
	}
}

func TestPaymentEventTypeValues(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{EventPaymentSucceeded, "payment.succeeded"},
		{EventPaymentAuthorized, "payment.authorized"},
		{EventPaymentPending, "payment.pending"},
		{EventPaymentFailed, "payment.failed"},
		{EventPaymentCancelled, "payment.cancelled"},
		{EventPaymentExpired, "payment.expired"},
		{EventPaymentUnresolved, "payment.unresolved"},
		{EventRefundSucceeded, "refund.succeeded"},
		{EventRefundFailed, "refund.failed"},
		{string(EventAll), "*"},
		{ResolvedFromPending, "pending"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("event type = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestNewEventListenerError(t *testing.T) {
	cause := errors.New("database down")
	result := &PaymentResponse{Status: StatusSuccess, PaymentID: "P1"}
	event := &PaymentEvent{Type: EventPaymentSucceeded, Provider: "test-pos", Result: result}

	err := NewEventListenerError(event, result, cause)
	if err.Event != event {
		t.Error("Event must be the event that failed")
	}
	if err.Result != result {
		t.Error("Result must be the operation's result, so the payment is not lost")
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap must expose the listener's error")
	}
	if err.Code != "EVENT_LISTENER_FAILED" {
		t.Errorf("Code = %q, want EVENT_LISTENER_FAILED", err.Code)
	}
	if err.Provider != "test-pos" {
		t.Errorf("Provider = %q, want test-pos", err.Provider)
	}
	for _, want := range []string{EventPaymentSucceeded, "database down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestEventListenerErrorWithoutACause(t *testing.T) {
	event := &PaymentEvent{Type: EventPaymentFailed, Provider: "test-pos"}
	err := NewEventListenerError(event, nil, nil)
	if err.Error() == "" {
		t.Error("Error() is empty")
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap must be nil without a cause")
	}
}

func TestPaymentEventListenerSignature(t *testing.T) {
	// A listener receives the context and the event, and its error fails the call.
	var seen *PaymentEvent
	var listener PaymentEventListener = func(ctx context.Context, event *PaymentEvent) error {
		if ctx == nil {
			t.Error("listener got a nil context")
		}
		seen = event
		return nil
	}
	event := &PaymentEvent{Type: EventPaymentSucceeded}
	if err := listener(context.Background(), event); err != nil {
		t.Fatalf("listener: %v", err)
	}
	if seen != event {
		t.Error("listener did not receive the event")
	}
}

package core

import (
	"context"
	"fmt"
)

// PaymentEventType names a payment event. Events are emitted after an operation
// returns a result, and only after the provider verified it: a forged callback
// emits nothing.
//
//	payment.succeeded   createPayment, completeThreeDSPayment, capture: success
//	payment.authorized  authorize: success (the amount is blocked)
//	payment.pending     createPayment, completeThreeDSPayment, authorize, capture: pending
//	payment.failed      the payment operations and the 3D Secure inits: failure
//	payment.cancelled   cancel, voidAuthorization: success
//	refund.succeeded    refund: success
//	refund.failed       refund: failure
type PaymentEventType string

// The payment event types. EventPaymentExpired and EventPaymentUnresolved are
// emitted by pending-payment resolution, not by EventFor.
const (
	EventPaymentSucceeded  = "payment.succeeded"
	EventPaymentAuthorized = "payment.authorized"
	EventPaymentPending    = "payment.pending"
	EventPaymentFailed     = "payment.failed"
	EventPaymentCancelled  = "payment.cancelled"
	EventPaymentExpired    = "payment.expired"
	EventPaymentUnresolved = "payment.unresolved"
	EventRefundSucceeded   = "refund.succeeded"
	EventRefundFailed      = "refund.failed"
)

// PaymentEventTypes lists every event type, in declaration order.
var PaymentEventTypes = []PaymentEventType{
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

// ResolvedFromPending marks an event whose pending payment was resolved later.
const ResolvedFromPending = "pending"

// PaymentEvent is emitted after an operation returns a result.
type PaymentEvent struct {
	Type PaymentEventType `json:"type"`
	// Provider is the id of the provider that handled the operation.
	Provider  string           `json:"provider"`
	Operation PaymentOperation `json:"operation"`
	// PaymentID is the provider's payment id, when known.
	PaymentID string `json:"paymentId,omitempty"`
	// ConversationID is the merchant's order id, when known.
	ConversationID string `json:"conversationId,omitempty"`
	// Amount is the amount charged or refunded, taken from the request, when known.
	Amount   string `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
	// Code is the normalized error code of a failed result.
	Code PaymentErrorCode `json:"code,omitempty"`
	// Result is the operation's result as returned to the caller.
	Result any `json:"result"`
	// Request is the operation's request (callback data for 3D Secure completions).
	Request any `json:"request"`
	// ResolvedFrom is ResolvedFromPending when a pending payment was resolved.
	ResolvedFrom string `json:"resolvedFrom,omitempty"`
}

// PaymentEventName is a PaymentEventType, or EventAll to receive every event.
type PaymentEventName string

// EventAll subscribes a listener to every payment event.
const EventAll PaymentEventName = "*"

// PaymentEventListener handles a payment event. Returning an error fails the
// operation with an EventListenerError that still carries the result.
type PaymentEventListener func(ctx context.Context, event *PaymentEvent) error

// PaymentEventListeners maps event names (event types or EventAll) to a listener.
type PaymentEventListeners map[PaymentEventName]PaymentEventListener

// EventListenerError reports a failing event listener. The operation itself
// completed: Result is its result, so the payment is not lost.
type EventListenerError struct {
	BaseError
	Event  *PaymentEvent
	Result any
	Cause  error
}

// NewEventListenerError wraps the error a listener returned.
func NewEventListenerError(event *PaymentEvent, result any, cause error) *EventListenerError {
	return &EventListenerError{
		BaseError: BaseError{
			Message:  fmt.Sprintf("A '%s' event listener failed: %s", event.Type, ErrorMessage(cause, "")),
			Code:     "EVENT_LISTENER_FAILED",
			Provider: event.Provider,
		},
		Event:  event,
		Result: result,
		Cause:  cause,
	}
}

// Unwrap returns the error the listener returned.
func (e *EventListenerError) Unwrap() error { return e.Cause }

// eventPaymentOperations are the operations whose success is a payment outcome.
var eventPaymentOperations = map[PaymentOperation]bool{
	OperationCreatePayment:          true,
	OperationCompleteThreeDSPayment: true,
	OperationAuthorize:              true,
	OperationCapture:                true,
}

// EventFor returns the event an operation result triggers, or nil when it
// triggers none. A result whose errorCode is INVALID_HASH is a forged callback,
// not a payment outcome, and never produces an event.
func EventFor(operation PaymentOperation, provider string, request, result any) *PaymentEvent {
	status := PaymentStatus(valueAt(result, "status"))
	if valueAt(result, "errorCode") == "INVALID_HASH" {
		return nil
	}

	var eventType PaymentEventType
	switch {
	case eventPaymentOperations[operation]:
		switch status {
		case StatusSuccess:
			eventType = EventPaymentSucceeded
			if operation == OperationAuthorize {
				eventType = EventPaymentAuthorized
			}
		case StatusPending:
			eventType = EventPaymentPending
		case StatusFailure:
			eventType = EventPaymentFailed
		}
	case operation == OperationInitThreeDSPayment || operation == OperationInitThreeDSAuthorize:
		if status == StatusFailure {
			eventType = EventPaymentFailed
		}
	case operation == OperationCancel || operation == OperationVoidAuthorization:
		if status == StatusSuccess {
			eventType = EventPaymentCancelled
		}
	case operation == OperationRefund:
		switch status {
		case StatusSuccess:
			eventType = EventRefundSucceeded
		case StatusFailure:
			eventType = EventRefundFailed
		}
	}
	if eventType == "" {
		return nil
	}

	paymentID := valueAt(result, "paymentId")
	if paymentID == "" {
		paymentID = valueAt(request, "paymentId")
	}
	conversationID := valueAt(result, "conversationId")
	if conversationID == "" {
		conversationID = valueAt(request, "conversationId")
	}
	amount := valueAt(request, "paidPrice")
	if amount == "" {
		amount = valueAt(request, "price")
	}
	if operation == OperationCapture {
		amount = valueAt(request, "amount")
	}

	return &PaymentEvent{
		Type:           eventType,
		Provider:       provider,
		Operation:      operation,
		PaymentID:      paymentID,
		ConversationID: conversationID,
		Amount:         amount,
		Currency:       valueAt(request, "currency"),
		Code:           PaymentErrorCode(valueAt(result, "code")),
		Result:         result,
		Request:        request,
	}
}

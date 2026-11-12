package testing

import "github.com/boracomet/go-pos/core"

// MockOperation is an operation whose next outcome can be forced with
// MockProvider.FailNext or MockProvider.NetworkErrorNext.
type MockOperation string

// The operations a mock outcome can be queued for.
const (
	OperationPayment         MockOperation = "payment"
	OperationThreeDSInit     MockOperation = "threeDSInit"
	OperationThreeDSComplete MockOperation = "threeDSComplete"
	OperationRefund          MockOperation = "refund"
	OperationCancel          MockOperation = "cancel"
	OperationCapture         MockOperation = "capture"
	OperationVoid            MockOperation = "void"
)

// MockPaymentState is the state of a payment as the mock bank sees it.
type MockPaymentState string

// The payment states of the mock bank.
const (
	StatePending3DS MockPaymentState = "pending_3ds"
	StateSucceeded  MockPaymentState = "succeeded"
	StateAuthorized MockPaymentState = "authorized"
	StateFailed     MockPaymentState = "failed"
	StateCancelled  MockPaymentState = "cancelled"
	StateVoided     MockPaymentState = "voided"
)

// MockRefund is one refund of a payment, in minor units.
type MockRefund struct {
	RefundID string `json:"refundId"`
	Amount   int64  `json:"amount"`
}

// MockPaymentRecord is a payment as the mock bank sees it: the value returned by
// MockProvider.GetRecord and carried in a result's RawResponse. Amounts are in
// minor units (kuruş/cents).
type MockPaymentRecord struct {
	PaymentID      string           `json:"paymentId"`
	ConversationID string           `json:"conversationId,omitempty"`
	State          MockPaymentState `json:"state"`
	Amount         int64            `json:"amount"`
	// CapturedAmount is the captured amount of a pre-authorization; for a direct
	// payment it equals Amount.
	CapturedAmount int64                 `json:"capturedAmount"`
	RefundedAmount int64                 `json:"refundedAmount"`
	Currency       string                `json:"currency"`
	Installment    int                   `json:"installment"`
	CardLastFour   string                `json:"cardLastFour,omitempty"`
	PreAuth        bool                  `json:"preAuth"`
	ErrorCode      core.PaymentErrorCode `json:"errorCode,omitempty"`
	Refunds        []MockRefund          `json:"refunds"`
	CreatedAt      string                `json:"createdAt"`
}

// paymentRecord is the internal record: the public one plus the card data and
// the outcome the mock bank will apply.
type paymentRecord struct {
	MockPaymentRecord
	scenario       scenario
	saveCard       *core.SaveCardOption
	cardNumber     string
	cardHolderName string
	expireMonth    string
	expireYear     string
}

// savedCard is a stored card plus the outcome it produces when paid with.
type savedCard struct {
	core.StoredCard
	scenario scenario
}

// queuedOutcome is a forced outcome waiting for its operation.
type queuedOutcome struct {
	operation MockOperation
	scenario  scenario
}

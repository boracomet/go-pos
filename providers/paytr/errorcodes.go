package paytr

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps PayTR failed_reason_code values to normalized codes. Code 0
// carries a free-text reason in failed_reason_msg and is deliberately unmapped.
var ErrorCodes = map[string]core.PaymentErrorCode{
	"1":  core.CodeThreeDSFailed,
	"2":  core.CodeThreeDSFailed,
	"3":  core.CodeFraudSuspected,
	"6":  core.CodeCancelledByCustomer,
	"8":  core.CodeCardDeclined,
	"9":  core.CodeCardDeclined,
	"10": core.CodeCardDeclined,
	"11": core.CodeFraudSuspected,
	"99": core.CodeProviderError,
}

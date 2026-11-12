package core

import "strings"

// PaymentErrorCode is a provider-independent failure code. Every failure result
// carries Code next to ErrorCode (the provider's raw code): branch on Code, log
// ErrorCode.
type PaymentErrorCode string

const (
	// CodeInsufficientFunds means not enough balance or credit limit.
	CodeInsufficientFunds PaymentErrorCode = "INSUFFICIENT_FUNDS"
	// CodeCardDeclined means the issuer declined without a more specific reason.
	CodeCardDeclined PaymentErrorCode = "CARD_DECLINED"
	// CodeInvalidCard means the card number, BIN or issuer is not valid.
	CodeInvalidCard PaymentErrorCode = "INVALID_CARD"
	// CodeExpiredCard means the card is expired or the expiry date is wrong.
	CodeExpiredCard PaymentErrorCode = "EXPIRED_CARD"
	// CodeInvalidCVC means the CVC/CVV is wrong.
	CodeInvalidCVC PaymentErrorCode = "INVALID_CVC"
	// CodeThreeDSFailed means 3D Secure authentication failed or was not completed.
	CodeThreeDSFailed PaymentErrorCode = "THREEDS_FAILED"
	// CodeFraudSuspected means suspected fraud, or a lost/stolen card.
	CodeFraudSuspected PaymentErrorCode = "FRAUD_SUSPECTED"
	// CodeLimitExceeded means an amount or transaction-count limit was exceeded.
	CodeLimitExceeded PaymentErrorCode = "LIMIT_EXCEEDED"
	// CodeDuplicateOrder means the order id was already used.
	CodeDuplicateOrder PaymentErrorCode = "DUPLICATE_ORDER"
	// CodeCancelledByCustomer means the customer left the payment page.
	CodeCancelledByCustomer PaymentErrorCode = "CANCELLED_BY_CUSTOMER"
	// CodeInvalidRequest means the request was rejected before reaching the provider.
	CodeInvalidRequest PaymentErrorCode = "INVALID_REQUEST"
	// CodeNetworkError means no response arrived; the outcome is unknown (pending).
	CodeNetworkError PaymentErrorCode = "NETWORK_ERROR"
	// CodeInvalidHash means a callback signature did not verify; treat it as forged.
	CodeInvalidHash PaymentErrorCode = "INVALID_HASH"
	// CodeProviderError means a provider, bank or merchant configuration problem.
	CodeProviderError PaymentErrorCode = "PROVIDER_ERROR"
	// CodeUnknown means the raw code is not mapped yet.
	CodeUnknown PaymentErrorCode = "UNKNOWN"
)

// internalCodes are set by the library itself, not by a provider.
var internalCodes = map[string]PaymentErrorCode{
	"NETWORK_ERROR":    CodeNetworkError,
	"INVALID_HASH":     CodeInvalidHash,
	"INVALID_CALLBACK": CodeThreeDSFailed,
	"VALIDATION_ERROR": CodeInvalidRequest,
}

// ISO8583ErrorCodes maps the host response codes returned by Turkish bank
// virtual POS systems.
var ISO8583ErrorCodes = map[string]PaymentErrorCode{
	"01": CodeCardDeclined,      // refer to card issuer
	"02": CodeCardDeclined,      // refer to card issuer, special condition
	"03": CodeProviderError,     // invalid merchant
	"04": CodeFraudSuspected,    // pick up card
	"05": CodeCardDeclined,      // do not honour
	"07": CodeFraudSuspected,    // pick up card, special condition
	"12": CodeCardDeclined,      // invalid transaction
	"13": CodeInvalidRequest,    // invalid amount
	"14": CodeInvalidCard,       // invalid card number
	"15": CodeInvalidCard,       // no such issuer
	"33": CodeExpiredCard,       // expired card, pick up
	"41": CodeFraudSuspected,    // lost card
	"43": CodeFraudSuspected,    // stolen card
	"51": CodeInsufficientFunds, // not sufficient funds
	"54": CodeExpiredCard,       // expired card
	"57": CodeCardDeclined,      // transaction not permitted to cardholder
	"58": CodeProviderError,     // transaction not permitted to terminal
	"59": CodeFraudSuspected,    // suspected fraud
	"61": CodeLimitExceeded,     // exceeds amount limit
	"62": CodeCardDeclined,      // restricted card
	"65": CodeLimitExceeded,     // exceeds frequency limit
	"82": CodeInvalidCVC,        // CVV verification failed
	"91": CodeProviderError,     // issuer or switch inoperative
	"94": CodeDuplicateOrder,    // duplicate transmission
	"96": CodeProviderError,     // system malfunction
}

// ResolveErrorCode resolves the normalized code for a raw error code: internal
// codes first, then the provider's table, otherwise UNKNOWN.
func ResolveErrorCode(raw string, providerCodes map[string]PaymentErrorCode) PaymentErrorCode {
	key := strings.TrimSpace(raw)
	if key == "" {
		return CodeUnknown
	}
	if strings.HasPrefix(key, "MD_STATUS_") {
		return CodeThreeDSFailed
	}
	if code, ok := internalCodes[key]; ok {
		return code
	}
	if code, ok := providerCodes[key]; ok {
		return code
	}
	return CodeUnknown
}

package iyzico

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps the documented iyzico payment error codes to normalized codes.
// API validation codes are not listed: they resolve to UNKNOWN and keep the raw
// code in ErrorCode.
var ErrorCodes = map[string]core.PaymentErrorCode{
	"10051": core.CodeInsufficientFunds, // not sufficient funds
	"10005": core.CodeCardDeclined,      // do not honour
	"10012": core.CodeCardDeclined,      // invalid transaction
	"10041": core.CodeFraudSuspected,    // lost card
	"10043": core.CodeFraudSuspected,    // stolen card
	"10054": core.CodeExpiredCard,       // expired card
	"10084": core.CodeInvalidCVC,        // invalid CVC2
	"10057": core.CodeCardDeclined,      // not permitted to cardholder
	"10058": core.CodeProviderError,     // not permitted to terminal
	"10034": core.CodeFraudSuspected,    // fraud suspect
	"10093": core.CodeCardDeclined,      // card blocked for online payments
	"10201": core.CodeCardDeclined,      // card not permitted
	"10204": core.CodeProviderError,     // general payment error
	"10206": core.CodeInvalidCVC,        // invalid CVC length
	"10207": core.CodeCardDeclined,      // refer to issuer
	"10208": core.CodeProviderError,     // invalid merchant category code
	"10209": core.CodeCardDeclined,      // blocked card
	"10210": core.CodeThreeDSFailed,     // invalid CAVV
	"10211": core.CodeThreeDSFailed,     // invalid ECI
	"10213": core.CodeInvalidCard,       // BIN not found
	"10214": core.CodeProviderError,     // communication or system error
	"10215": core.CodeInvalidCard,       // invalid card number
	"10216": core.CodeInvalidCard,       // no such issuer
	"10217": core.CodeCardDeclined,      // debit cards require 3D Secure
	"10219": core.CodeProviderError,     // request to the bank timed out
	"10222": core.CodeProviderError,     // terminal not configured for installments
	"10223": core.CodeProviderError,     // end of day processing required
	"10225": core.CodeCardDeclined,      // restricted card
	"10226": core.CodeLimitExceeded,     // PIN tries exceeded
	"10227": core.CodeCardDeclined,      // invalid PIN
	"10228": core.CodeProviderError,     // issuer or switch inoperative
	"10229": core.CodeExpiredCard,       // invalid expiry date
	"10232": core.CodeInvalidRequest,    // invalid amount
}

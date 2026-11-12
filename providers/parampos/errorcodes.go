package parampos

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps Param's Sonuc values for failed requests. Successful results
// have a positive Sonuc; failures are negative. A plain -1 means the bank
// declined: its ISO 8583 code in Banka_Sonuc_Kod decides the normalized code.
var ErrorCodes = map[string]core.PaymentErrorCode{
	"-100": core.CodeProviderError,  // account not found
	"-101": core.CodeProviderError,  // security error (credentials)
	"-102": core.CodeProviderError,  // invalid request hash
	"-103": core.CodeProviderError,  // invalid GUID length
	"-104": core.CodeInvalidRequest, // order id longer than 36 characters
	"-105": core.CodeInvalidCVC,     // invalid CVC length
	"-106": core.CodeExpiredCard,    // invalid expiry year
	"-107": core.CodeExpiredCard,    // invalid expiry month
	"-108": core.CodeInvalidRequest, // invalid GSM number
	"-109": core.CodeProviderError,  // invalid SanalPOS_ID length
	"-110": core.CodeInvalidRequest, // invalid installment
	"-111": core.CodeInvalidRequest, // invalid IP address
	"-112": core.CodeInvalidRequest, // invalid amount format
	"-113": core.CodeInvalidRequest, // amount must be greater than zero
	"-114": core.CodeProviderError,  // test user cannot make transactions
	"-115": core.CodeInvalidRequest, // amount needs two decimals
	"-116": core.CodeInvalidRequest, // success or error URL is empty
	"-117": core.CodeInvalidRequest, // GSM number must be numeric
	"-118": core.CodeInvalidCard,    // invalid card number length
	"-119": core.CodeInvalidCard,    // invalid card number
	"-120": core.CodeInvalidRequest, // invalid date format
	"-121": core.CodeInvalidRequest, // cardholder details missing
	"-200": core.CodeProviderError,  // commission settings not found
	"-201": core.CodeInvalidRequest, // installment not allowed for this POS
	"-202": core.CodeInvalidRequest, // wrong commission added to the total
	"-203": core.CodeProviderError,  // commission calculation error
	"-204": core.CodeProviderError,  // invalid SanalPOS type
	"-205": core.CodeProviderError,  // payment could not be saved, retry
	"-206": core.CodeProviderError,  // virtual POS transaction not saved
	"-207": core.CodeProviderError,  // system error
	"-208": core.CodeInvalidCard,    // SanalPOS type or card number not found
	"-209": core.CodeInvalidRequest, // transaction not found
	"-210": core.CodeInvalidRequest, // nothing to cancel or refund
	"-211": core.CodeInvalidRequest, // already cancelled
	"-212": core.CodeInvalidRequest, // already refunded
	"-213": core.CodeInvalidRequest, // refundable transaction cannot be cancelled
	"-214": core.CodeInvalidRequest, // cancellable transaction cannot be refunded
	"-215": core.CodeInvalidCard,    // card BIN does not match SanalPOS_ID
	"-216": core.CodeProviderError,  // GUID does not match the security object
	"-217": core.CodeInvalidRequest, // refund or cancel amount above the payment
	"-218": core.CodeInvalidRequest, // invalid cancel amount
	"-219": core.CodeInvalidRequest, // Durum must be IPTAL or IADE
	"-220": core.CodeCardDeclined,   // debit cards cannot pay in installments
	"-221": core.CodeInvalidRequest, // refund above the refundable balance
	"-222": core.CodeInvalidRequest, // date range longer than 7 days
	"-223": core.CodeInvalidCard,    // test card used in production
	"-300": core.CodeProviderError,  // card could not be stored
	"-301": core.CodeProviderError,  // card could not be decrypted
}

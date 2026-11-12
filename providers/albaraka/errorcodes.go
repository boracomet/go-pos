package albaraka

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps the documented e-POS response codes to normalized codes, on
// top of the bank host response codes.
var ErrorCodes = buildErrorCodes()

func buildErrorCodes() map[string]core.PaymentErrorCode {
	codes := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes)+4)
	for raw, code := range core.ISO8583ErrorCodes {
		codes[raw] = code
	}
	// Codes seen on the test host
	codes["0148"] = core.CodeProviderError  // UNAUTHORIZED REQUEST
	codes["0355"] = core.CodeInvalidRequest // a MACParams value is missing
	codes["E219"] = core.CodeInvalidRequest // record not found
	codes["E999"] = core.CodeProviderError  // general error
	return codes
}

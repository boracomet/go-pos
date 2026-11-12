package kuveytturk

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps the documented KT Pay Gate response codes to normalized codes,
// on top of the bank host response codes.
var ErrorCodes = buildErrorCodes()

func buildErrorCodes() map[string]core.PaymentErrorCode {
	codes := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes)+7)
	for raw, code := range core.ISO8583ErrorCodes {
		codes[raw] = code
	}
	codes["RequestValidationError"] = core.CodeInvalidRequest
	codes["InvalidTransaction"] = core.CodeInvalidRequest
	codes["OrderIdNotFound"] = core.CodeInvalidRequest
	codes["MetaDataNotFound"] = core.CodeThreeDSFailed
	// A request signed with the wrong API password, not a forged callback
	codes["HashDataError"] = core.CodeProviderError
	codes["ApiUserNotDefined"] = core.CodeProviderError
	codes["TechnicalException"] = core.CodeProviderError
	return codes
}

package yapikredi

import "github.com/boracomet/go-pos/core"

// ErrorCodes maps the Posnet host response codes to normalized codes.
var ErrorCodes = buildErrorCodes()

func buildErrorCodes() map[string]core.PaymentErrorCode {
	codes := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes))
	for raw, code := range core.ISO8583ErrorCodes {
		codes[raw] = code
	}
	return codes
}

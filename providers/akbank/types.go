package akbank

import "github.com/boracomet/go-pos/core"

// APIVersion is the version field of every request.
const APIVersion = "1.00"

// SuccessCode is the responseCode of an approved transaction.
const SuccessCode = "VPS-0000"

// EndpointProcess is the JSON API endpoint, relative to the base URL.
const EndpointProcess = "/transaction/process"

// Transaction codes of the Sanal POS API.
const (
	TxnSale          = "1000"
	TxnRefund        = "1002"
	TxnVoid          = "1003"
	TxnPreAuth       = "1004"
	TxnPostAuth      = "1005"
	TxnOrderHistory  = "1010"
	TxnSecureSale    = "3000"
	TxnSecurePreAuth = "3004"
)

// PaymentTxnCodes are the codes of a sale or pre-authorization, as they appear in
// an order history answer.
var PaymentTxnCodes = []string{TxnSale, TxnSecureSale, TxnPreAuth, TxnSecurePreAuth}

// Hosts of the 3D Secure gateway (securepay), where the customer's browser posts
// the form.
const (
	Gateway3DTest       = "https://virtualpospaymentgatewaypre.akbank.com/securepay"
	Gateway3DProduction = "https://virtualpospaymentgateway.akbank.com/securepay"
)

// Config holds the Akbank Sanal POS credentials from the merchant panel:
// MerchantSafeID and TerminalSafeID identify the terminal, SecretKey signs every
// request and callback (HMAC-SHA512).
type Config struct {
	core.ProviderConfig
	MerchantSafeID string
	TerminalSafeID string
	SecretKey      string
	// SubMerchantID is sent as subMerchant when the terminal belongs to a
	// marketplace.
	SubMerchantID string
	// TestMode selects the test 3D gateway when Gateway3DURL is empty.
	TestMode bool
	// Gateway3DURL overrides the 3D gateway URL (securepay).
	Gateway3DURL string
}

// InitOptions carries the optional fields of a 3D Secure initialization.
type InitOptions struct {
	// FailURL defaults to the callback URL.
	FailURL string
}

// Terminal is the terminal echoed back by the API.
type Terminal struct {
	MerchantSafeID string
	TerminalSafeID string
}

// APIResponse is the common response envelope. The API answers with a loose JSON
// object, so unknown fields are kept as they arrive.
type APIResponse map[string]any

// TxnDetail is one entry of txnDetailList in an order history answer.
type TxnDetail map[string]any

// CallbackData holds the fields Akbank POSTs to okUrl / failUrl after a 3D_PAY
// transaction. hash covers the fields listed in hashParams ("+"-separated).
type CallbackData = map[string]string

// FormField is one hidden input of the 3D Secure form, in the order it is sent.
type FormField struct {
	Name  string
	Value string
}

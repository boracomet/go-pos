package sipay

import "github.com/boracomet/go-pos/core"

// Brand is the payment institution running the ccpayment API on its own hosts.
type Brand string

const (
	// BrandSipay is the default institution.
	BrandSipay Brand = "sipay"
	// BrandQNBpay runs the same API on the QNBpay hosts.
	BrandQNBpay Brand = "qnbpay"
)

// Default base URLs per brand and environment.
const (
	SandboxURL          = "https://provisioning.sipay.com.tr"
	ProductionURL       = "https://app.sipay.com.tr"
	QNBpaySandboxURL    = "https://test.qnbpay.com.tr"
	QNBpayProductionURL = "https://portal.qnbpay.com.tr"
)

const (
	providerName       = "sipay"
	qnbpayProviderName = "qnbpay"
	sipayDisplayName   = "Sipay"
	qnbpayDisplayName  = "QNBpay"
)

// Config holds the merchant credentials from the integration settings page.
type Config struct {
	core.ProviderConfig
	// Brand selects the institution, its provider id and its default URLs.
	// Defaults to BrandSipay.
	Brand Brand
	// AppID and AppSecret obtain the Bearer token and sign every hash key.
	AppID     string
	AppSecret string
	// MerchantKey is sent with every payment request.
	MerchantKey string
	// SaleWebhookKey is sent as sale_web_hook_key when set, so the sale webhook
	// registered for that key is notified.
	SaleWebhookKey string
}

// ThreeDSRequest is a 3D Secure request with the URL failures are posted to.
// The core request type has no fail URL; use it with
// InitThreeDSPaymentWithFailURL / InitThreeDSAuthorizeWithFailURL.
type ThreeDSRequest struct {
	core.ThreeDSPaymentRequest
	// FailURL becomes cancel_url. Defaults to CallbackURL when empty.
	FailURL string
}

// Result is a raw Sipay response object. Fields are read by name because Sipay
// sends numbers and strings interchangeably.
type Result = map[string]any

// VerifiedHash is the decrypted content of a Sipay hash key: the only part of a
// 3D Secure callback that can be trusted.
type VerifiedHash struct {
	// Status is "1" for an approved payment, "0" for a failed one.
	Status    string `json:"status"`
	Total     string `json:"total"`
	InvoiceID string `json:"invoiceId"`
	OrderID   string `json:"orderId"`
	Currency  string `json:"currency"`
}

// Item is one line of the items array sent with a payment.
type Item struct {
	Name        string `json:"name"`
	Price       string `json:"price"`
	Quantity    int    `json:"quantity"`
	Description string `json:"description"`
}

// tokenError reports that no token was issued (wrong app id or secret).
type tokenError struct {
	core.BaseError
	statusCode string
	response   any
}

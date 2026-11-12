package garanti

import "github.com/boracomet/go-pos/core"

// APIVersion is the GVPS request version this provider signs for.
const APIVersion = "512"

// Endpoints of the Garanti BBVA virtual POS, relative to the base URL.
const (
	// EndpointAPI is the XML API (GVPSRequest).
	EndpointAPI = "/VPServlet"
	// EndpointGate is the 3D Secure form target (the customer's browser).
	EndpointGate = "/servlet/gt3dengine"
)

// Hosts of the Garanti BBVA virtual POS.
const (
	SandboxURL    = "https://sanalposprovtest.garantibbva.com.tr"
	ProductionURL = "https://sanalposprov.garanti.com.tr"
)

// SecurityLevel is the payment model (secure3dsecuritylevel).
type SecurityLevel string

// Payment models: with 3D the bank verifies the card and the merchant provisions
// it after the callback; with 3D_PAY the bank verifies and charges it;
// GARANTI_PAY (CUSTOM_PAY) opens Garanti's page, where the customer pays with the
// mobile app without card details.
const (
	Level3D         SecurityLevel = "3D"
	Level3DPay      SecurityLevel = "3D_PAY"
	LevelGarantiPay SecurityLevel = "GARANTI_PAY"
)

// SecurityLevels lists the supported payment models.
var SecurityLevels = []SecurityLevel{Level3D, Level3DPay, LevelGarantiPay}

// Config holds the Garanti BBVA virtual POS credentials from the merchant panel:
// MerchantID and TerminalID are the üye işyeri and terminal numbers, Password the
// provision user's password, StoreKey the 3D Secure store key.
type Config struct {
	core.ProviderConfig
	MerchantID string
	TerminalID string
	Password   string
	StoreKey   string
	// ProvUserID is the provision user. Default: PROVAUT.
	ProvUserID string
	// RefundUserID is the cancel and refund user. Default: PROVRFN.
	RefundUserID string
	// RefundPassword is the cancel/refund user's password. Default: Password.
	RefundPassword string
	SecurityLevel  SecurityLevel
	// CompanyName is shown on the GarantiPay page (companyname).
	CompanyName string
	// AcceptedMdStatuses are the mdstatus values provisioned in the 3D model.
	// Default: ["1"] (full 3D Secure authentication). 2, 3 and 4 are half-secure;
	// accepting them can leave chargeback liability with the merchant.
	AcceptedMdStatuses []string
	// TestMode sends Mode TEST and selects the test host when BaseURL is empty.
	TestMode bool
}

// CompleteContext is the order from the merchant's own records, passed separately
// and never read from the callback body: the callback's hash does not pin each
// value to its field.
type CompleteContext struct {
	// OrderID is the order that was started; required for the 3D model.
	OrderID string
	// Amount is the order amount ("100.00"); required for 3D, checked against the
	// bank for 3D_PAY.
	Amount string
	// Currency defaults to TRY.
	Currency string
	// Installment defaults to the callback's count.
	Installment *int
}

// InitOptions carries the optional fields of a 3D Secure initialization.
type InitOptions struct {
	// FailURL defaults to the callback URL.
	FailURL string
}

// CallbackData holds the fields Garanti POSTs to successurl / errorurl. hash
// covers the fields named in hashparams.
type CallbackData = map[string]string

// Expiry is a card expiry as two-digit month and year.
type Expiry struct {
	Month string
	Year  string
}

// HashDataFields are the inputs of the XML API request hash.
type HashDataFields struct {
	OrderID      string
	TerminalID   string
	CardNumber   string
	Amount       string
	CurrencyCode string
	SecurityData string
}

// ThreeDHashFields are the inputs of the 3D form hash (secure3dhash).
type ThreeDHashFields struct {
	TerminalID   string
	OrderID      string
	Amount       string
	CurrencyCode string
	SuccessURL   string
	ErrorURL     string
	Type         string
	Installment  string
	StoreKey     string
	SecurityData string
}

// XMLField is one element of a GVPSRequest, in the order it is sent. A non-nil
// Children makes it a nested element and Value is ignored.
type XMLField struct {
	Name     string
	Value    string
	Children []XMLField
}

// XMLResponse is a parsed GVPSResponse.
type XMLResponse struct {
	// Response holds Transaction/Response: Source, Code, ReasonCode, Message,
	// ErrorMsg, SysErrMsg.
	Response map[string]string
	// Transaction holds the transaction fields: RetrefNum, AuthCode, ProvDate, ...
	Transaction map[string]string
	// OrderInquiry holds Order/OrderInqResult: Status, AuthAmount, ChargeType, ...
	OrderInquiry map[string]string
}

// FormField is one hidden input of the 3D Secure form, in the order it is sent.
type FormField struct {
	Name  string
	Value string
}

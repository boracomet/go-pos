package est

import "github.com/boracomet/go-pos/core"

// Bank is a bank on the EST platform. Each has its own production host; all of
// them share the test host.
type Bank string

// Banks supported by the EST platform.
const (
	BankIsbank      Bank = "isbank"
	BankZiraat      Bank = "ziraat"
	BankHalkbank    Bank = "halkbank"
	BankTEB         Bank = "teb"
	BankSekerbank   Bank = "sekerbank"
	BankAkbankEST   Bank = "akbankest"
	BankHalkbankMKD Bank = "halkbankmkd"
)

// Endpoints of the EST platform, relative to the base URL.
const (
	// EndpointGate is the 3D Secure form target (the customer's browser).
	EndpointGate = "/fim/est3Dgate"
	// EndpointAPI is the XML API (CC5Request).
	EndpointAPI = "/fim/api"
)

// SandboxURL is the shared EST test host.
const SandboxURL = "https://entegrasyon.asseco-see.com.tr"

// URLs holds a bank's test and production hosts.
type URLs struct {
	Sandbox    string
	Production string
}

// DefaultURLs maps every supported bank to its hosts. Config.BaseURL overrides them.
var DefaultURLs = map[Bank]URLs{
	BankIsbank:      {Sandbox: SandboxURL, Production: "https://sanalpos.isbank.com.tr"},
	BankZiraat:      {Sandbox: SandboxURL, Production: "https://sanalpos2.ziraatbank.com.tr"},
	BankHalkbank:    {Sandbox: SandboxURL, Production: "https://sanalpos.halkbank.com.tr"},
	BankTEB:         {Sandbox: SandboxURL, Production: "https://sanalpos.teb.com.tr"},
	BankSekerbank:   {Sandbox: SandboxURL, Production: "https://sanalpos.sekerbank.com.tr"},
	BankAkbankEST:   {Sandbox: SandboxURL, Production: "https://www.sanalakpos.com"},
	BankHalkbankMKD: {Sandbox: SandboxURL, Production: "https://epay.halkbank.mk"},
}

// StoreType is the 3D model of the store (`storetype`).
type StoreType string

// Store types: with 3d the bank only verifies the card and the merchant charges
// it with the XML API after the callback; with 3d_pay the bank verifies and
// charges it; 3d_pay_hosting and 3d_host are the same on the bank's card page.
const (
	Store3D           StoreType = "3d"
	Store3DPay        StoreType = "3d_pay"
	Store3DPayHosting StoreType = "3d_pay_hosting"
	Store3DHost       StoreType = "3d_host"
)

// StoreTypes lists the supported store types.
var StoreTypes = []StoreType{Store3D, Store3DPay, Store3DPayHosting, Store3DHost}

// Config holds the EST credentials of one bank, from its merchant panel:
// ClientID is the merchant id, Username and Password the API user of the XML
// API, StoreKey the 3D Secure store key used for the hash.
type Config struct {
	core.ProviderConfig
	// Bank selects the production host. BaseURL overrides it.
	Bank       Bank
	ClientID   string
	Username   string
	Password   string
	StoreKey   string
	StoreType  StoreType
	Department *int
	// AcceptedMdStatuses are the mdStatus values charged in the 3d and 3d_host
	// models. Default: ["1"] (full 3D Secure authentication). 2, 3 and 4 are
	// half-secure; accepting them can leave chargeback liability with the merchant.
	AcceptedMdStatuses []string
	// TestMode selects the shared test host when BaseURL is empty.
	TestMode bool
}

// CompleteContext is the order from the merchant's own records, never from the
// callback: the bank's POST alone does not pin which order a result belongs to.
type CompleteContext struct {
	// OrderID is the order from the merchant's records; a Girogate Pending result
	// carries ids only when it matches.
	OrderID string
}

// GirogateRequest pays with an alternative payment method instead of a card,
// with the 3d_pay or 3d_pay_hosting store type.
type GirogateRequest struct {
	// Method is the payment method, e.g. "IDEAL", "SOFORT", "P24".
	Method GirogateMethod
	// Country is the customer's country, ISO 3166 alpha-2 (e.g. "NL").
	Country string
	// Email defaults to buyer.email (P24, Zimpler and the Asian methods).
	Email string
	// Phone defaults to buyer.gsmNumber (giropay, Dragonpay, eNETS, SingPost, Thai banks).
	Phone string
	// ConsumerID defaults to buyer.id (Entercash, Trustly, AstroPay).
	ConsumerID string
}

// InitOptions carries the optional fields of a 3D Secure initialization.
type InitOptions struct {
	// FailURL defaults to the callback URL.
	FailURL  string
	Girogate *GirogateRequest
}

// CallbackData holds the fields EST POSTs to okUrl / failUrl. HASH (ver3) covers
// every other field.
type CallbackData = map[string]string

// GirogateMethod is an alternative payment method through the EST gate, enabled
// per store by the bank. It is sent as paymentType GIROGATE_<method>.
type GirogateMethod string

// Supported Girogate methods.
const (
	GirogateEPS                GirogateMethod = "EPS"
	GirogateGIROPAY            GirogateMethod = "GIROPAY"
	GirogateIDEAL              GirogateMethod = "IDEAL"
	GirogateALIPAY             GirogateMethod = "ALIPAY"
	GirogateMYBANK             GirogateMethod = "MYBANK"
	GirogateENTERCASH          GirogateMethod = "ENTERCASH"
	GirogateBANCONTACT         GirogateMethod = "BANCONTACT"
	GirogateSOFORT             GirogateMethod = "SOFORT"
	GirogateMULTIBANCO         GirogateMethod = "MULTIBANCO"
	GirogateSAFETYPAY          GirogateMethod = "SAFETYPAY"
	GirogateP24                GirogateMethod = "P24"
	GirogateBOLETO             GirogateMethod = "BOLETO"
	GirogateFINNISHONLINEBANK  GirogateMethod = "FINNISHONLINEBANKING"
	GirogateSEPA               GirogateMethod = "SEPA"
	GirogateWECHATPAY          GirogateMethod = "WECHATPAY"
	GirogateSINGPOST           GirogateMethod = "SINGPOST"
	GirogateDRAGONPAY          GirogateMethod = "DRAGONPAY"
	GirogateENETS              GirogateMethod = "ENETS"
	GirogatePAYU               GirogateMethod = "PAYU"
	GirogatePOLI               GirogateMethod = "POLI"
	GirogateTRUSTLY            GirogateMethod = "TRUSTLY"
	GirogateASTROPAYCARD       GirogateMethod = "ASTROPAYCARD"
	GirogateZIMPLER            GirogateMethod = "ZIMPLER"
	GirogateASTROPAYDIRECT     GirogateMethod = "ASTROPAYDIRECT"
	GirogateKRUNGTHAIBANK      GirogateMethod = "KRUNGTHAIBANK"
	GirogateKRUNGSIBANK        GirogateMethod = "KRUNGSIBANK"
	GirogateSIAMCOMMERCIALBANK GirogateMethod = "SIAMCOMMERCIALBANK"
	GirogateBANGKOKBANK        GirogateMethod = "BANGKOKBANK"
)

// GirogateMethods lists the supported Girogate methods.
var GirogateMethods = []GirogateMethod{
	GirogateEPS, GirogateGIROPAY, GirogateIDEAL, GirogateALIPAY, GirogateMYBANK,
	GirogateENTERCASH, GirogateBANCONTACT, GirogateSOFORT, GirogateMULTIBANCO,
	GirogateSAFETYPAY, GirogateP24, GirogateBOLETO, GirogateFINNISHONLINEBANK,
	GirogateSEPA, GirogateWECHATPAY, GirogateSINGPOST, GirogateDRAGONPAY,
	GirogateENETS, GirogatePAYU, GirogatePOLI, GirogateTRUSTLY, GirogateASTROPAYCARD,
	GirogateZIMPLER, GirogateASTROPAYDIRECT, GirogateKRUNGTHAIBANK, GirogateKRUNGSIBANK,
	GirogateSIAMCOMMERCIALBANK, GirogateBANGKOKBANK,
}

// GirogateOptions are the customer fields of a Girogate payment.
type GirogateOptions struct {
	Method     string
	Country    string
	Email      string
	Phone      string
	ConsumerID string
}

// Expiry is a card expiry as two-digit month and year.
type Expiry struct {
	Month string
	Year  string
}

// XMLField is one element of a CC5Request, in the order it is sent. A non-nil
// Children makes it a nested element and Value is ignored.
type XMLField struct {
	Name     string
	Value    string
	Children []XMLField
}

// XMLResponse is a parsed CC5Response: the top-level fields (OrderId,
// ProcReturnCode, Response, ErrMsg, ...) and the fields inside <Extra>.
type XMLResponse struct {
	Fields map[string]string
	Extra  map[string]string
}

// FormField is one hidden input of the 3D Secure form, in the order it is sent.
type FormField struct {
	Name  string
	Value string
}

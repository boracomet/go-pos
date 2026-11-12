package pos

import (
	"fmt"

	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/providers/akbank"
	"github.com/boracomet/go-pos/providers/albaraka"
	"github.com/boracomet/go-pos/providers/denizbank"
	"github.com/boracomet/go-pos/providers/est"
	"github.com/boracomet/go-pos/providers/garanti"
	"github.com/boracomet/go-pos/providers/iyzico"
	"github.com/boracomet/go-pos/providers/kuveytturk"
	"github.com/boracomet/go-pos/providers/parampos"
	"github.com/boracomet/go-pos/providers/paytr"
	"github.com/boracomet/go-pos/providers/qnb"
	"github.com/boracomet/go-pos/providers/sipay"
	"github.com/boracomet/go-pos/providers/vakifbank"
	"github.com/boracomet/go-pos/providers/yapikredi"
)

// ProviderFactory builds a provider from its own configuration and the shared
// settings. Config.Providers accepts a configuration value for every id in
// ProviderFactories.
type ProviderFactory func(config any, setup Setup) (core.Provider, error)

// providerFactories maps a provider id to its constructor. The EST banks share
// one implementation, and QNBpay runs on Sipay's platform.
var providerFactories = map[string]ProviderFactory{
	string(core.ProviderIyzico):      newIyzico,
	string(core.ProviderPayTR):       newPayTR,
	string(core.ProviderParamPOS):    newParamPOS,
	string(core.ProviderSipay):       newSipay,
	string(core.ProviderQNBPAY):      newSipay,
	string(core.ProviderAkbank):      newAkbank,
	string(core.ProviderKuveytTurk):  newKuveytTurk,
	string(core.ProviderAlbaraka):    newAlbaraka,
	string(core.ProviderYapiKredi):   newYapiKredi,
	string(core.ProviderQNB):         newQNB,
	string(core.ProviderVakifBank):   newVakifBank,
	string(core.ProviderDenizBank):   newDenizBank,
	string(core.ProviderGaranti):     newGaranti,
	string(core.ProviderIsbank):      newEST,
	string(core.ProviderZiraat):      newEST,
	string(core.ProviderHalkbank):    newEST,
	string(core.ProviderTEB):         newEST,
	string(core.ProviderSekerbank):   newEST,
	string(core.ProviderAkbankEST):   newEST,
	string(core.ProviderHalkbankMKD): newEST,
	"est":                            newEST,
}

// ProviderFactories returns a copy of the provider registry: every built-in id
// mapped to its constructor.
func ProviderFactories() map[string]ProviderFactory {
	factories := make(map[string]ProviderFactory, len(providerFactories))
	for id, factory := range providerFactories {
		factories[id] = factory
	}
	return factories
}

// NewProvider builds the provider registered under id from its own configuration
// (for example IyzicoConfig) and the shared settings. It reports a
// ConfigurationError for an unknown id or a configuration of another provider.
func NewProvider(id string, config any, setup Setup) (core.Provider, error) {
	factory, ok := providerFactories[id]
	if !ok {
		return nil, core.NewConfigurationError(fmt.Sprintf("Unknown provider '%s'", id), id)
	}
	return factory(config, setup)
}

// providerDefinition binds a built-in configuration to its provider id. Payment
// creates it through the registry so every shared setting, locale included,
// applies; Create is the path for a core.Options consumer.
type providerDefinition struct {
	id     string
	config any
}

func (d providerDefinition) Create(setup core.ProviderSetupContext) (core.Provider, error) {
	return NewProvider(d.id, d.config, Setup{ProviderSetupContext: setup})
}

func definition(id string, config any) core.ProviderDefinition {
	return providerDefinition{id: id, config: config}
}

// configOf copies the configuration a provider was given, accepting a value or a
// pointer to it.
func configOf[T any](config any, id string) (T, error) {
	var cfg T
	switch value := config.(type) {
	case T:
		return value, nil
	case *T:
		if value == nil {
			return cfg, core.NewConfigurationError(fmt.Sprintf("providers.%s is nil", id), id)
		}
		return *value, nil
	default:
		return cfg, core.NewConfigurationError(
			fmt.Sprintf("providers.%s expects %T, got %T", id, cfg, config), id)
	}
}

// withDefaults fills the shared settings the provider's own configuration leaves
// out. The base URL is resolved by each factory from the provider's own hosts.
func withDefaults(providerType core.ProviderType, cfg core.ProviderConfig, setup Setup) core.ProviderConfig {
	cfg = core.WithProviderDefaults(providerType, cfg, setup.ProviderSetupContext)
	if cfg.Locale == "" {
		cfg.Locale = setup.locale()
	}
	return cfg
}

// hostFor picks a provider's test or live host.
func hostFor(sandbox bool, sandboxURL, productionURL string) string {
	if sandbox {
		return sandboxURL
	}
	return productionURL
}

func newIyzico(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[IyzicoConfig](config, string(core.ProviderIyzico))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), iyzico.SandboxURL, iyzico.ProductionURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderIyzico, cfg.ProviderConfig, setup)
	return iyzico.New(cfg)
}

func newPayTR(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[PayTRConfig](config, string(core.ProviderPayTR))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = paytr.DefaultBaseURL
	}
	if cfg.TestMode == nil {
		testMode := setup.sandbox()
		cfg.TestMode = &testMode
	}
	cfg.ProviderConfig = withDefaults(core.ProviderPayTR, cfg.ProviderConfig, setup)
	return paytr.New(cfg)
}

func newParamPOS(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[ParamPOSConfig](config, string(core.ProviderParamPOS))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), parampos.SandboxURL, parampos.ProductionURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderParamPOS, cfg.ProviderConfig, setup)
	return parampos.New(cfg)
}

// newSipay builds Sipay or QNBpay. The brand of the configuration wins; without
// one, the id selects it.
func newSipay(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[SipayConfig](config, string(core.ProviderSipay))
	if err != nil {
		return nil, err
	}
	if cfg.Brand == "" {
		cfg.Brand = sipay.BrandSipay
		if setup.ID == string(core.ProviderQNBPAY) {
			cfg.Brand = sipay.BrandQNBpay
		}
	}
	providerType := core.ProviderSipay
	sandboxURL, productionURL := sipay.SandboxURL, sipay.ProductionURL
	if cfg.Brand == sipay.BrandQNBpay {
		providerType = core.ProviderQNBPAY
		sandboxURL, productionURL = sipay.QNBpaySandboxURL, sipay.QNBpayProductionURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), sandboxURL, productionURL)
	}
	cfg.ProviderConfig = withDefaults(providerType, cfg.ProviderConfig, setup)
	return sipay.New(cfg)
}

func newAkbank(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[AkbankConfig](config, string(core.ProviderAkbank))
	if err != nil {
		return nil, err
	}
	// Akbank publishes no API host constant; the shared table carries both.
	cfg.ProviderConfig = withDefaults(core.ProviderAkbank, cfg.ProviderConfig, setup)
	if setup.sandbox() {
		cfg.TestMode = true
	}
	return akbank.New(cfg)
}

func newKuveytTurk(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[KuveytTurkConfig](config, string(core.ProviderKuveytTurk))
	if err != nil {
		return nil, err
	}
	cfg = kuveytturk.WithDefaults(cfg, setup.sandbox())
	cfg.ProviderConfig = withDefaults(core.ProviderKuveytTurk, cfg.ProviderConfig, setup)
	return kuveytturk.New(cfg)
}

func newAlbaraka(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[AlbarakaConfig](config, string(core.ProviderAlbaraka))
	if err != nil {
		return nil, err
	}
	cfg = albaraka.WithDefaults(cfg, setup.sandbox())
	cfg.ProviderConfig = withDefaults(core.ProviderAlbaraka, cfg.ProviderConfig, setup)
	return albaraka.New(cfg)
}

func newYapiKredi(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[YapiKrediConfig](config, string(core.ProviderYapiKredi))
	if err != nil {
		return nil, err
	}
	cfg = yapikredi.WithDefaults(cfg, setup.sandbox())
	cfg.ProviderConfig = withDefaults(core.ProviderYapiKredi, cfg.ProviderConfig, setup)
	return yapikredi.New(cfg)
}

func newQNB(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[QNBConfig](config, string(core.ProviderQNB))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), qnb.SandboxBaseURL, qnb.ProductionBaseURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderQNB, cfg.ProviderConfig, setup)
	return qnb.New(cfg)
}

func newVakifBank(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[VakifBankConfig](config, string(core.ProviderVakifBank))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), vakifbank.SandboxBaseURL, vakifbank.ProductionBaseURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderVakifBank, cfg.ProviderConfig, setup)
	if setup.sandbox() {
		cfg.TestMode = true
	}
	return vakifbank.New(cfg)
}

func newDenizBank(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[DenizBankConfig](config, string(core.ProviderDenizBank))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), denizbank.SandboxBaseURL, denizbank.ProductionBaseURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderDenizBank, cfg.ProviderConfig, setup)
	return denizbank.New(cfg)
}

func newGaranti(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[GarantiConfig](config, string(core.ProviderGaranti))
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hostFor(setup.sandbox(), garanti.SandboxURL, garanti.ProductionURL)
	}
	cfg.ProviderConfig = withDefaults(core.ProviderGaranti, cfg.ProviderConfig, setup)
	if setup.sandbox() {
		cfg.TestMode = true
	}
	return garanti.New(cfg)
}

// estProviderTypes maps an EST bank to the provider id carrying its hosts.
var estProviderTypes = map[est.Bank]core.ProviderType{
	est.BankIsbank:      core.ProviderIsbank,
	est.BankZiraat:      core.ProviderZiraat,
	est.BankHalkbank:    core.ProviderHalkbank,
	est.BankTEB:         core.ProviderTEB,
	est.BankSekerbank:   core.ProviderSekerbank,
	est.BankAkbankEST:   core.ProviderAkbankEST,
	est.BankHalkbankMKD: core.ProviderHalkbankMKD,
}

// newEST builds the EST provider of one bank. The bank of the configuration wins;
// without one, the id selects it. An unknown bank needs an explicit base URL.
func newEST(config any, setup Setup) (core.Provider, error) {
	cfg, err := configOf[EstConfig](config, "est")
	if err != nil {
		return nil, err
	}
	if cfg.Bank == "" {
		if _, ok := estProviderTypes[est.Bank(setup.ID)]; ok {
			cfg.Bank = est.Bank(setup.ID)
		}
	}
	if cfg.BaseURL == "" {
		if urls, ok := est.DefaultURLs[cfg.Bank]; ok {
			cfg.BaseURL = hostFor(setup.sandbox(), urls.Sandbox, urls.Production)
		}
	}
	cfg.ProviderConfig = withDefaults(estProviderTypes[cfg.Bank], cfg.ProviderConfig, setup)
	return est.New(cfg)
}

// The built-in providers and their configurations.
type (
	// IyzicoProvider calls the iyzico API.
	IyzicoProvider = iyzico.Provider
	// IyzicoConfig holds the iyzico credentials.
	IyzicoConfig = iyzico.Config
	// IyzicoCallbackData is the iyzico 3D Secure callback.
	IyzicoCallbackData = iyzico.ThreeDSCallbackData

	// PayTRProvider calls the PayTR API.
	PayTRProvider = paytr.Provider
	// PayTRConfig holds the PayTR merchant credentials.
	PayTRConfig = paytr.Config
	// PayTRCallbackData is PayTR's server-to-server notification.
	PayTRCallbackData = paytr.CallbackData
	// PayTRTokenPaymentRequest starts a payment with a stored card token.
	PayTRTokenPaymentRequest = paytr.TokenPaymentRequest

	// ParamPOSProvider calls the Param (TurkPOS) SOAP API.
	ParamPOSProvider = parampos.Provider
	// ParamPOSConfig holds the Param merchant credentials.
	ParamPOSConfig = parampos.Config
	// ParamPOSCallbackData is the Param 3D Secure callback.
	ParamPOSCallbackData = parampos.CallbackData

	// SipayProvider calls the Sipay ccpayment API, also used by QNBpay.
	SipayProvider = sipay.Provider
	// SipayConfig holds the Sipay merchant credentials.
	SipayConfig = sipay.Config
	// SipayBrand is the institution running the ccpayment API.
	SipayBrand = sipay.Brand

	// AkbankProvider calls the Akbank Sanal POS JSON API.
	AkbankProvider = akbank.Provider
	// AkbankConfig holds the Akbank Sanal POS credentials.
	AkbankConfig = akbank.Config
	// AkbankCallbackData is the Akbank 3D Secure callback.
	AkbankCallbackData = akbank.CallbackData

	// KuveytTurkProvider calls the Kuveyt Türk KT Pay Gate API.
	KuveytTurkProvider = kuveytturk.Provider
	// KuveytTurkConfig holds the KT Pay Gate credentials.
	KuveytTurkConfig = kuveytturk.Config
	// KuveytTurkCallbackData is the Kuveyt Türk 3D Secure callback.
	KuveytTurkCallbackData = kuveytturk.ThreeDSCallbackData

	// AlbarakaProvider calls the Albaraka Türk e-POS API.
	AlbarakaProvider = albaraka.Provider
	// AlbarakaConfig holds the e-POS credentials.
	AlbarakaConfig = albaraka.Config
	// AlbarakaCompleteRequest is the callback plus the merchant's own order.
	AlbarakaCompleteRequest = albaraka.CompleteRequest

	// YapiKrediProvider calls the Yapı Kredi Posnet API.
	YapiKrediProvider = yapikredi.Provider
	// YapiKrediConfig holds the Posnet credentials.
	YapiKrediConfig = yapikredi.Config
	// YapiKrediCallbackFields are the encrypted Yapı Kredi callback packets.
	YapiKrediCallbackFields = yapikredi.CallbackFields
	// YapiKrediCompleteRequest is the callback plus the merchant's own order.
	YapiKrediCompleteRequest = yapikredi.CompleteRequest

	// QNBProvider calls the QNB Finansbank PayFor API.
	QNBProvider = qnb.Provider
	// QNBConfig holds the QNB virtual POS credentials.
	QNBConfig = qnb.Config
	// QNBCallbackData is the QNB 3D Secure callback.
	QNBCallbackData = qnb.CallbackData

	// VakifBankProvider calls the VakıfBank VPOS 7/24 API.
	VakifBankProvider = vakifbank.Provider
	// VakifBankConfig holds the VakıfBank virtual POS credentials.
	VakifBankConfig = vakifbank.Config
	// VakifBankCallbackData is the VakıfBank MPI callback.
	VakifBankCallbackData = vakifbank.CallbackData

	// DenizBankProvider calls the Denizbank InterVPos API.
	DenizBankProvider = denizbank.Provider
	// DenizBankConfig holds the Denizbank virtual POS credentials.
	DenizBankConfig = denizbank.Config
	// DenizBankCallbackData is the Denizbank 3D Secure callback.
	DenizBankCallbackData = denizbank.CallbackData

	// GarantiProvider calls the Garanti BBVA virtual POS API.
	GarantiProvider = garanti.Provider
	// GarantiConfig holds the Garanti BBVA virtual POS credentials.
	GarantiConfig = garanti.Config
	// GarantiCallbackData is the Garanti BBVA 3D Secure callback.
	GarantiCallbackData = garanti.CallbackData
	// GarantiCompleteContext is the merchant's own order of a 3D Secure payment.
	GarantiCompleteContext = garanti.CompleteContext
	// GarantiSecurityLevel is the Garanti BBVA payment model.
	GarantiSecurityLevel = garanti.SecurityLevel

	// EstProvider calls the EST (Asseco) virtual POS of one bank.
	EstProvider = est.Provider
	// EstConfig holds the EST credentials of one bank.
	EstConfig = est.Config
	// EstBank is a bank on the EST platform.
	EstBank = est.Bank
	// EstStoreType is the EST 3D model.
	EstStoreType = est.StoreType
	// EstCallbackData is the EST 3D Secure callback.
	EstCallbackData = est.CallbackData
	// EstCompleteContext is the merchant's own order of a 3D Secure payment.
	EstCompleteContext = est.CompleteContext
	// EstGirogateRequest pays with an alternative payment method.
	EstGirogateRequest = est.GirogateRequest
	// EstGirogateMethod is an alternative payment method through the EST gate.
	EstGirogateMethod = est.GirogateMethod
)

// The EST banks and the Sipay brands.
const (
	EstBankIsbank      = est.BankIsbank
	EstBankZiraat      = est.BankZiraat
	EstBankHalkbank    = est.BankHalkbank
	EstBankTEB         = est.BankTEB
	EstBankSekerbank   = est.BankSekerbank
	EstBankAkbankEST   = est.BankAkbankEST
	EstBankHalkbankMKD = est.BankHalkbankMKD

	SipayBrandSipay  = sipay.BrandSipay
	SipayBrandQNBpay = sipay.BrandQNBpay
)

// EstGirogateMethods lists the alternative payment methods of the EST gate.
var EstGirogateMethods = est.GirogateMethods

// Iyzico returns the iyzico provider for Config.Providers.
func Iyzico(cfg IyzicoConfig) core.ProviderDefinition {
	return definition(string(core.ProviderIyzico), cfg)
}

// PayTR returns the PayTR provider for Config.Providers.
func PayTR(cfg PayTRConfig) core.ProviderDefinition {
	return definition(string(core.ProviderPayTR), cfg)
}

// ParamPOS returns the Param (TurkPOS) provider for Config.Providers.
func ParamPOS(cfg ParamPOSConfig) core.ProviderDefinition {
	return definition(string(core.ProviderParamPOS), cfg)
}

// Sipay returns the Sipay provider for Config.Providers. Set cfg.Brand to
// SipayBrandQNBpay for QNBpay.
func Sipay(cfg SipayConfig) core.ProviderDefinition {
	return definition(string(core.ProviderSipay), cfg)
}

// QNBpay returns QNBpay, which runs on Sipay's platform.
func QNBpay(cfg SipayConfig) core.ProviderDefinition {
	cfg.Brand = sipay.BrandQNBpay
	return definition(string(core.ProviderQNBPAY), cfg)
}

// Akbank returns the Akbank Sanal POS provider for Config.Providers.
func Akbank(cfg AkbankConfig) core.ProviderDefinition {
	return definition(string(core.ProviderAkbank), cfg)
}

// KuveytTurk returns the Kuveyt Türk provider for Config.Providers.
func KuveytTurk(cfg KuveytTurkConfig) core.ProviderDefinition {
	return definition(string(core.ProviderKuveytTurk), cfg)
}

// Albaraka returns the Albaraka Türk provider for Config.Providers.
func Albaraka(cfg AlbarakaConfig) core.ProviderDefinition {
	return definition(string(core.ProviderAlbaraka), cfg)
}

// YapiKredi returns the Yapı Kredi provider for Config.Providers.
func YapiKredi(cfg YapiKrediConfig) core.ProviderDefinition {
	return definition(string(core.ProviderYapiKredi), cfg)
}

// QNB returns the QNB Finansbank provider for Config.Providers.
func QNB(cfg QNBConfig) core.ProviderDefinition {
	return definition(string(core.ProviderQNB), cfg)
}

// VakifBank returns the VakıfBank provider for Config.Providers.
func VakifBank(cfg VakifBankConfig) core.ProviderDefinition {
	return definition(string(core.ProviderVakifBank), cfg)
}

// DenizBank returns the Denizbank provider for Config.Providers.
func DenizBank(cfg DenizBankConfig) core.ProviderDefinition {
	return definition(string(core.ProviderDenizBank), cfg)
}

// Garanti returns the Garanti BBVA provider for Config.Providers.
func Garanti(cfg GarantiConfig) core.ProviderDefinition {
	return definition(string(core.ProviderGaranti), cfg)
}

// EST returns the EST (Asseco) provider of cfg.Bank for Config.Providers. The
// bank may also be left out and selected by the provider id ("isbank", "ziraat",
// "halkbank", "teb", "sekerbank", "akbankest", "halkbankmkd").
func EST(cfg EstConfig) core.ProviderDefinition {
	return definition("est", cfg)
}

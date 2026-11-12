package core

// Mode selects the environment: sandbox uses test URLs and turns on provider test
// modes, production uses live URLs.
type Mode string

const (
	ModeSandbox    Mode = "sandbox"
	ModeProduction Mode = "production"
)

// ProviderType is the id of a built-in provider. Ids are the usual keys of
// Options.Providers, but any key works: the key is the provider's id in
// PluginContext.Use and in handler URLs.
type ProviderType string

const (
	ProviderIyzico     ProviderType = "iyzico"
	ProviderPayTR      ProviderType = "paytr"
	ProviderAkbank     ProviderType = "akbank"
	ProviderParamPOS   ProviderType = "parampos"
	ProviderKuveytTurk ProviderType = "kuveytturk"
	ProviderSipay      ProviderType = "sipay"
	ProviderAlbaraka   ProviderType = "albaraka"
	// ProviderQNBPAY is QNBpay, on Sipay's platform.
	ProviderQNBPAY ProviderType = "qnbpay"
	// ProviderIsbank is an EST (Asseco) bank; all of them share one implementation.
	ProviderIsbank    ProviderType = "isbank"
	ProviderZiraat    ProviderType = "ziraat"
	ProviderHalkbank  ProviderType = "halkbank"
	ProviderTEB       ProviderType = "teb"
	ProviderSekerbank ProviderType = "sekerbank"
	// ProviderAkbankEST is Akbank's older EST virtual POS; ProviderAkbank is its JSON API.
	ProviderAkbankEST ProviderType = "akbankest"
	// ProviderHalkbankMKD is Halkbank Macedonia (Halk Banka AD Skopje).
	ProviderHalkbankMKD ProviderType = "halkbankmkd"
	ProviderDenizBank   ProviderType = "denizbank"
	ProviderVakifBank   ProviderType = "vakifbank"
	ProviderYapiKredi   ProviderType = "yapikredi"
	ProviderQNB         ProviderType = "qnb"
	ProviderGaranti     ProviderType = "garanti"
	// ProviderMock is the in-memory provider for tests.
	ProviderMock ProviderType = "mock"
)

// AllProviderTypes lists the built-in provider ids. The handler accepts a route
// for any of them, even before the provider is configured, so that an unknown
// provider reports "not enabled" instead of "route not found".
var AllProviderTypes = []ProviderType{
	ProviderIyzico,
	ProviderPayTR,
	ProviderAkbank,
	ProviderParamPOS,
	ProviderKuveytTurk,
	ProviderSipay,
	ProviderAlbaraka,
	ProviderQNBPAY,
	ProviderIsbank,
	ProviderZiraat,
	ProviderHalkbank,
	ProviderTEB,
	ProviderSekerbank,
	ProviderAkbankEST,
	ProviderHalkbankMKD,
	ProviderDenizBank,
	ProviderVakifBank,
	ProviderYapiKredi,
	ProviderQNB,
	ProviderGaranti,
	ProviderMock,
}

// ProviderSetupContext are the shared settings passed to a ProviderDefinition
// once they are known.
type ProviderSetupContext struct {
	// ID is the provider's key in Options.Providers.
	ID     string
	Mode   Mode
	Logger Logger
	Retry  *RetryConfig
	// Doer overrides the transport used for API calls.
	Doer Doer
	// Validate requests before calling the provider. Nil means the default (true).
	Validate *bool
}

// ProviderDefinition creates a provider once the shared settings are known.
type ProviderDefinition interface {
	Create(setup ProviderSetupContext) (Provider, error)
}

// ProviderEntry is a provider instance or a definition that creates one. When a
// value satisfies both, the instance wins.
type ProviderEntry any

type providerDefinitionFunc func(ProviderSetupContext) (Provider, error)

func (f providerDefinitionFunc) Create(setup ProviderSetupContext) (Provider, error) {
	return f(setup)
}

// DefineProvider wraps a function that creates a custom provider from the shared
// settings.
func DefineProvider(create func(setup ProviderSetupContext) (Provider, error)) ProviderDefinition {
	return providerDefinitionFunc(create)
}

// Options configures the payment facade.
type Options struct {
	// Providers by id, e.g. {"iyzico": ..., "paytr": ...}. A custom provider can be
	// passed as a Provider instance.
	Providers map[string]ProviderEntry
	// DefaultProvider is used by the facade's own operations. Empty defaults to the
	// only provider, when there is exactly one.
	DefaultProvider string
	// Mode defaults to ModeProduction.
	Mode   Mode
	Logger Logger
	Retry  *RetryConfig
	// Doer overrides the transport for provider API calls (a proxy, or tests).
	Doer Doer
	// Validate requests before calling providers. Nil means true. A provider's own
	// config can override it.
	Validate *bool
	// Handler configures the HTTP handler.
	Handler *HandlerOptions
	// Plugins are applied in order.
	Plugins []Plugin
}

// ProviderURLs are the API hosts of a provider in both environments.
type ProviderURLs struct {
	Sandbox    string
	Production string
}

// For returns the host for a mode; anything but ModeSandbox means production.
func (u ProviderURLs) For(mode Mode) string {
	if mode == ModeSandbox {
		return u.Sandbox
	}
	return u.Production
}

// ProviderDefaultURLs holds the default hosts of the providers that call a remote
// API (every built-in provider except the in-memory mock).
var ProviderDefaultURLs = map[ProviderType]ProviderURLs{
	ProviderIyzico: {
		Sandbox:    "https://sandbox-api.iyzipay.com",
		Production: "https://api.iyzipay.com",
	},
	// A single host: test transactions are selected with test_mode=1.
	ProviderPayTR: {
		Sandbox:    "https://www.paytr.com",
		Production: "https://www.paytr.com",
	},
	ProviderAkbank: {
		Sandbox:    "https://apipre.akbank.com/api/v1/payment/virtualpos",
		Production: "https://api.akbank.com/api/v1/payment/virtualpos",
	},
	ProviderParamPOS: {
		Sandbox:    "https://test-dmz.param.com.tr/turkpos.ws/service_turkpos_test.asmx",
		Production: "https://posws.param.com.tr/turkpos.ws/service_turkpos_prod.asmx",
	},
	ProviderKuveytTurk: {
		Sandbox:    "https://boatest.kuveytturk.com.tr/boa.virtualpos.services",
		Production: "https://sanalpos.kuveytturk.com.tr/ServiceGateWay",
	},
	ProviderSipay: {
		Sandbox:    "https://provisioning.sipay.com.tr",
		Production: "https://app.sipay.com.tr",
	},
	ProviderAlbaraka: {
		Sandbox:    "https://epostest.albarakaturk.com.tr",
		Production: "https://epos.albarakaturk.com.tr",
	},
	ProviderQNBPAY: {
		Sandbox:    "https://test.qnbpay.com.tr",
		Production: "https://portal.qnbpay.com.tr",
	},
	// The EST banks share one test host.
	ProviderIsbank: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://sanalpos.isbank.com.tr",
	},
	ProviderZiraat: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://sanalpos2.ziraatbank.com.tr",
	},
	ProviderHalkbank: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://sanalpos.halkbank.com.tr",
	},
	ProviderTEB: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://sanalpos.teb.com.tr",
	},
	ProviderSekerbank: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://sanalpos.sekerbank.com.tr",
	},
	ProviderAkbankEST: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://www.sanalakpos.com",
	},
	ProviderHalkbankMKD: {
		Sandbox:    "https://entegrasyon.asseco-see.com.tr",
		Production: "https://epay.halkbank.mk",
	},
	ProviderDenizBank: {
		Sandbox:    "https://test.inter-vpos.com.tr",
		Production: "https://inter-vpos.com.tr",
	},
	ProviderVakifBank: {
		Sandbox:    "https://onlineodemetest.vakifbank.com.tr:4443",
		Production: "https://onlineodeme.vakifbank.com.tr:4443",
	},
	ProviderYapiKredi: {
		Sandbox:    "https://setmpos.ykb.com",
		Production: "https://posnet.yapikredi.com.tr",
	},
	ProviderQNB: {
		Sandbox:    "https://vpostest.qnb.com.tr",
		Production: "https://vpos.qnb.com.tr",
	},
	ProviderGaranti: {
		Sandbox:    "https://sanalposprovtest.garantibbva.com.tr",
		Production: "https://sanalposprov.garanti.com.tr",
	},
}

// WithProviderDefaults fills the base URL for the mode and the shared settings
// the provider's own config leaves out. Settings already set on config win.
func WithProviderDefaults(providerType ProviderType, config ProviderConfig, setup ProviderSetupContext) ProviderConfig {
	if config.BaseURL == "" {
		config.BaseURL = ProviderDefaultURLs[providerType].For(setup.Mode)
	}
	if config.Logger == nil {
		config.Logger = setup.Logger
	}
	if config.Retry == nil {
		config.Retry = setup.Retry
	}
	if config.Doer == nil {
		config.Doer = setup.Doer
	}
	if config.Validate == nil {
		config.Validate = setup.Validate
	}
	return config
}

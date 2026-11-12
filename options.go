package pos

import (
	"fmt"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// Config configures a Payment. Providers is the only field without a default.
type Config struct {
	// Providers are the enabled providers by id, e.g.
	// {"iyzico": IyzicoConfig{...}, "paytr": PayTRConfig{...}}. A value is a
	// provider's own configuration, a core.ProviderDefinition returned by Iyzico
	// and the other factories, or a ready core.Provider instance. The key is the
	// provider's id in Use, in the handler routes and in plugins; any key works.
	// An id that is not present here reports a ProviderNotEnabledError.
	Providers map[string]core.ProviderEntry
	// DefaultProvider is the provider the operations on Payment use. Empty
	// defaults to the only provider, when exactly one is configured.
	DefaultProvider string
	// Mode defaults to core.ModeProduction. core.ModeSandbox selects the test
	// hosts and turns on the provider test modes (PayTR test_mode, the Akbank,
	// Garanti BBVA and VakıfBank test gateways).
	Mode core.Mode
	// Locale is the language of provider pages and messages, "tr" (default) or
	// "en". A provider's own Locale wins.
	Locale string
	// Logger receives diagnostic messages. Implementations must never log
	// request bodies or headers: they carry card data and credentials. Nil
	// disables logging.
	Logger core.Logger
	// Retry is the retry policy of provider calls. Only idempotent requests are
	// retried. Nil leaves retries to the provider default (off).
	Retry *core.RetryConfig
	// Doer overrides the transport of provider API calls (a proxy, or tests).
	// Nil uses http.DefaultClient.
	Doer core.Doer
	// Validate requests before calling a provider. Nil means true. A provider's
	// own configuration overrides it.
	Validate *bool
	// Handler configures the HTTP handler returned by Handler. Nil uses the
	// handler defaults: only provider callbacks and read-only card queries are
	// exposed, and privileged actions need an Authorize hook.
	Handler *core.HandlerOptions
	// Plugins are applied in order.
	Plugins []core.Plugin
}

// Setup are the shared settings a provider factory receives: the core setup plus
// the locale, which core.ProviderSetupContext does not carry.
type Setup struct {
	core.ProviderSetupContext
	// Locale defaults to "tr" when empty.
	Locale string
}

func (s Setup) locale() string {
	if s.Locale == "" {
		return "tr"
	}
	return s.Locale
}

func (s Setup) sandbox() bool { return s.Mode == core.ModeSandbox }

// resolvedMode normalizes the environment: anything but sandbox is production,
// the same rule core.ProviderURLs.For applies to the hosts.
func resolvedMode(mode core.Mode) core.Mode {
	if mode == core.ModeSandbox {
		return core.ModeSandbox
	}
	return core.ModeProduction
}

// reservedNames are the members of Payment a provider id or a plugin method must
// not shadow. They are matched case-insensitively.
var reservedNames = map[string]bool{}

func init() {
	for _, name := range []string{
		"CreatePayment", "InitThreeDSPayment", "CompleteThreeDSPayment",
		"Refund", "Cancel", "Authorize", "InitThreeDSAuthorize", "Capture",
		"VoidAuthorization", "SaveCard", "ListCards", "DeleteCard", "GetPayment",
		"BinCheck", "InstallmentInfo",
		"Use", "RawProvider", "Run", "Emit", "InterceptRequest", "Respond",
		"PluginContext", "Endpoints", "IsProviderEnabled", "EnabledProviders",
		"DefaultProvider", "Mode", "Handler", "CreateHandler", "On",
		"Method", "Methods", "ErrorCodes",
		"CheckoutFormProvider", "PWIProvider", "SubscriptionProvider",
		"TokenPaymentProvider", "PlainTextCallbackProvider",
	} {
		reservedNames[strings.ToLower(name)] = true
	}
}

// reserve rejects a name that clashes with a member of the payment object.
func reserve(name, what string) error {
	if name == "" {
		return core.NewConfigurationError(what+" needs a name", "")
	}
	if reservedNames[strings.ToLower(name)] {
		return core.NewConfigurationError(
			fmt.Sprintf("%s clashes with a member of the payment object", what), "")
	}
	return nil
}

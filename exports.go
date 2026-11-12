package pos

import (
	"net/http"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/client"
	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/providers/iyzico"
	"github.com/boracomet/go-pos/providers/parampos"
	"github.com/boracomet/go-pos/providers/paytr"
)

// Version is the library version.
const Version = core.Version

// The payment contract: requests, results and the values they carry.
type (
	// PaymentStatus is the normalized outcome of an operation.
	PaymentStatus = core.PaymentStatus
	// Currency is an ISO 4217 code.
	Currency = core.Currency
	// BasketItemType distinguishes physical goods from virtual ones.
	BasketItemType = core.BasketItemType

	// PaymentCard is the card to charge.
	PaymentCard = core.PaymentCard
	// Address is a billing or shipping address.
	Address = core.Address
	// Buyer is the customer a payment belongs to.
	Buyer = core.Buyer
	// BasketItem is one line of the order.
	BasketItem = core.BasketItem
	// SaveCardOption asks the provider to store the card during the payment.
	SaveCardOption = core.SaveCardOption
	// StoredCardReference charges a card the provider already stored.
	StoredCardReference = core.StoredCardReference
	// SavedCardTokens are the provider's keys of a stored card.
	SavedCardTokens = core.SavedCardTokens
	// StoredCard is a card returned by ListCards.
	StoredCard = core.StoredCard

	// PaymentRequest takes a payment or a pre-authorization.
	PaymentRequest = core.PaymentRequest
	// PaymentResponse is the outcome of a payment, capture or query.
	PaymentResponse = core.PaymentResponse
	// ThreeDSPaymentRequest starts a 3D Secure payment or pre-authorization.
	ThreeDSPaymentRequest = core.ThreeDSPaymentRequest
	// ThreeDSInitResponse is the 3D Secure challenge to send the customer to.
	ThreeDSInitResponse = core.ThreeDSInitResponse
	// RefundRequest refunds a settled payment.
	RefundRequest = core.RefundRequest
	// RefundResponse is the outcome of a refund.
	RefundResponse = core.RefundResponse
	// CancelRequest cancels a payment of the same day.
	CancelRequest = core.CancelRequest
	// CancelResponse is the outcome of a cancellation or a void.
	CancelResponse = core.CancelResponse
	// CaptureRequest charges a pre-authorization.
	CaptureRequest = core.CaptureRequest
	// VoidAuthorizationRequest releases a pre-authorization.
	VoidAuthorizationRequest = core.VoidAuthorizationRequest
	// SaveCardRequest stores a card without charging it.
	SaveCardRequest = core.SaveCardRequest
	// SaveCardResponse carries the keys of a stored card.
	SaveCardResponse = core.SaveCardResponse
	// ListCardsRequest lists a customer's stored cards.
	ListCardsRequest = core.ListCardsRequest
	// ListCardsResponse is a customer's stored cards.
	ListCardsResponse = core.ListCardsResponse
	// DeleteCardRequest deletes a stored card.
	DeleteCardRequest = core.DeleteCardRequest
	// DeleteCardResponse is the outcome of a card deletion.
	DeleteCardResponse = core.DeleteCardResponse
	// BinCheckResponse is the issuer and card type of a BIN.
	BinCheckResponse = core.BinCheckResponse
	// InstallmentDetail is one installment option of a BIN.
	InstallmentDetail = core.InstallmentDetail
	// InstallmentPrice is one price of an installment option.
	InstallmentPrice = core.InstallmentPrice
	// InstallmentInfoRequest asks for the installment options of a BIN.
	InstallmentInfoRequest = core.InstallmentInfoRequest
	// InstallmentInfoResponse is the installment options of a BIN.
	InstallmentInfoResponse = core.InstallmentInfoResponse

	// CheckoutFormRequest starts a hosted checkout form.
	CheckoutFormRequest = core.CheckoutFormRequest
	// CheckoutFormInitResponse carries the hosted checkout page.
	CheckoutFormInitResponse = core.CheckoutFormInitResponse
	// CheckoutFormRetrieveResponse is the outcome of a hosted checkout.
	CheckoutFormRetrieveResponse = core.CheckoutFormRetrieveResponse

	// PWIPaymentStatus is the state of a protected bank transfer.
	PWIPaymentStatus = core.PWIPaymentStatus
	// PWIPaymentRequest starts a Payment With IBAN.
	PWIPaymentRequest = core.PWIPaymentRequest
	// PWIPaymentInitResponse carries the IBAN to transfer to.
	PWIPaymentInitResponse = core.PWIPaymentInitResponse
	// PWIPaymentRetrieveResponse is the outcome of a Payment With IBAN.
	PWIPaymentRetrieveResponse = core.PWIPaymentRetrieveResponse

	// SubscriptionStatus is the state of a recurring subscription.
	SubscriptionStatus = core.SubscriptionStatus
	// PaymentInterval is the billing period of a pricing plan.
	PaymentInterval = core.PaymentInterval
	// SubscriptionCustomer is the subscriber a subscription is created for.
	SubscriptionCustomer = core.SubscriptionCustomer
	// SubscriptionInitializeRequest starts a recurring subscription.
	SubscriptionInitializeRequest = core.SubscriptionInitializeRequest
	// SubscriptionInitializeData is the created subscription.
	SubscriptionInitializeData = core.SubscriptionInitializeData
	// SubscriptionInitializeResponse is the outcome of a subscription start.
	SubscriptionInitializeResponse = core.SubscriptionInitializeResponse
	// SubscriptionCancelRequest stops a recurring subscription.
	SubscriptionCancelRequest = core.SubscriptionCancelRequest
	// SubscriptionCancelData is the cancelled subscription.
	SubscriptionCancelData = core.SubscriptionCancelData
	// SubscriptionCancelResponse is the outcome of a subscription cancellation.
	SubscriptionCancelResponse = core.SubscriptionCancelResponse
	// SubscriptionUpgradeRequest moves a subscription to another pricing plan.
	SubscriptionUpgradeRequest = core.SubscriptionUpgradeRequest
	// SubscriptionUpgradeData is the upgraded subscription.
	SubscriptionUpgradeData = core.SubscriptionUpgradeData
	// SubscriptionUpgradeResponse is the outcome of a subscription upgrade.
	SubscriptionUpgradeResponse = core.SubscriptionUpgradeResponse
	// SubscriptionRetrieveRequest queries a subscription.
	SubscriptionRetrieveRequest = core.SubscriptionRetrieveRequest
	// SubscriptionRetrieveData is the queried subscription.
	SubscriptionRetrieveData = core.SubscriptionRetrieveData
	// SubscriptionRetrieveResponse is the outcome of a subscription query.
	SubscriptionRetrieveResponse = core.SubscriptionRetrieveResponse
	// SubscriptionCardUpdateRequest replaces the card of a subscription.
	SubscriptionCardUpdateRequest = core.SubscriptionCardUpdateRequest
	// SubscriptionCardUpdateResponse is the outcome of a subscription card update.
	SubscriptionCardUpdateResponse = core.SubscriptionCardUpdateResponse
	// SubscriptionProductCreateRequest creates a subscription product.
	SubscriptionProductCreateRequest = core.SubscriptionProductCreateRequest
	// SubscriptionProductData is the created subscription product.
	SubscriptionProductData = core.SubscriptionProductData
	// SubscriptionProductResponse is the outcome of a product creation.
	SubscriptionProductResponse = core.SubscriptionProductResponse
	// PricingPlanCreateRequest creates a pricing plan of a product.
	PricingPlanCreateRequest = core.PricingPlanCreateRequest
	// PricingPlanData is the created pricing plan.
	PricingPlanData = core.PricingPlanData
	// PricingPlanResponse is the outcome of a pricing plan creation.
	PricingPlanResponse = core.PricingPlanResponse
)

// The normalized statuses, currencies and basket item types.
const (
	StatusSuccess   = core.StatusSuccess
	StatusFailure   = core.StatusFailure
	StatusPending   = core.StatusPending
	StatusCancelled = core.StatusCancelled

	CurrencyTRY = core.CurrencyTRY
	CurrencyUSD = core.CurrencyUSD
	CurrencyEUR = core.CurrencyEUR
	CurrencyGBP = core.CurrencyGBP

	BasketItemPhysical = core.BasketItemPhysical
	BasketItemVirtual  = core.BasketItemVirtual

	PWIWaiting = core.PWIWaiting
	PWISuccess = core.PWISuccess
	PWIFailure = core.PWIFailure

	SubscriptionActive   = core.SubscriptionActive
	SubscriptionPending  = core.SubscriptionPending
	SubscriptionCanceled = core.SubscriptionCanceled
	SubscriptionExpired  = core.SubscriptionExpired
	SubscriptionUnpaid   = core.SubscriptionUnpaid

	IntervalDaily   = core.IntervalDaily
	IntervalWeekly  = core.IntervalWeekly
	IntervalMonthly = core.IntervalMonthly
	IntervalYearly  = core.IntervalYearly
)

// Failures and errors. Every error carries a stable Code; branch on it, and use
// errors.As to reach the concrete type.
type (
	// Failure is the shared shape of a failed provider result.
	Failure = core.Failure
	// FailureResult is implemented by every result that can carry a failure.
	FailureResult = core.FailureResult
	// BaseError is the base library error.
	BaseError = core.BaseError
	// ValidationIssue names one invalid field by its dotted path.
	ValidationIssue = core.ValidationIssue
	// ValidationError reports input rejected before calling the provider.
	ValidationError = core.ValidationError
	// ConfigurationError reports a provider or merchant configuration problem.
	ConfigurationError = core.ConfigurationError
	// ProviderNotEnabledError reports a call to a provider that is not configured.
	ProviderNotEnabledError = core.ProviderNotEnabledError
	// PaymentFailedError reports a payment the provider declined.
	PaymentFailedError = core.PaymentFailedError
	// EventListenerError reports a failing event listener; Result is the result of
	// the operation, which completed.
	EventListenerError = core.EventListenerError
)

// NetworkErrorCode is the raw errorCode used when no response arrived.
const NetworkErrorCode = core.NetworkErrorCode

// NewError builds a base library error.
func NewError(message, code string) *BaseError { return core.NewError(message, code) }

// NewValidationError builds a validation error naming the invalid fields.
func NewValidationError(message, provider string, issues []ValidationIssue) *ValidationError {
	return core.NewValidationError(message, provider, issues)
}

// NewConfigurationError builds a configuration error.
func NewConfigurationError(message, provider string) *ConfigurationError {
	return core.NewConfigurationError(message, provider)
}

// NewProviderNotEnabledError builds the error for an unconfigured provider.
func NewProviderNotEnabledError(provider string) *ProviderNotEnabledError {
	return core.NewProviderNotEnabledError(provider)
}

// NewPaymentFailedError builds a payment failure carrying the provider's raw code.
func NewPaymentFailedError(message, errorCode string, rawResponse any, provider string) *PaymentFailedError {
	return core.NewPaymentFailedError(message, errorCode, rawResponse, provider)
}

// NewEventListenerError wraps the error a listener returned.
func NewEventListenerError(event *PaymentEvent, result any, cause error) *EventListenerError {
	return core.NewEventListenerError(event, result, cause)
}

// NotSupported builds the error for a feature a provider does not implement.
func NotSupported(provider, feature, detail string) *BaseError {
	return core.NotSupported(provider, feature, detail)
}

// BuildFailure builds the failure of a provider call: a transport error stays
// pending with NETWORK_ERROR, because the provider may have processed it.
func BuildFailure(providerName string, err error, fallback string) Failure {
	return core.BuildFailure(providerName, err, fallback)
}

// Payment events.
type (
	// PaymentEvent is emitted after an operation returned a verified result.
	PaymentEvent = core.PaymentEvent
	// PaymentEventType names a payment event.
	PaymentEventType = core.PaymentEventType
	// PaymentEventName is an event type, or EventAll.
	PaymentEventName = core.PaymentEventName
	// PaymentEventListener handles a payment event.
	PaymentEventListener = core.PaymentEventListener
	// PaymentEventListeners maps event names to a listener.
	PaymentEventListeners = core.PaymentEventListeners
)

// The payment event types.
const (
	EventPaymentSucceeded  = core.EventPaymentSucceeded
	EventPaymentAuthorized = core.EventPaymentAuthorized
	EventPaymentPending    = core.EventPaymentPending
	EventPaymentFailed     = core.EventPaymentFailed
	EventPaymentCancelled  = core.EventPaymentCancelled
	EventPaymentExpired    = core.EventPaymentExpired
	EventPaymentUnresolved = core.EventPaymentUnresolved
	EventRefundSucceeded   = core.EventRefundSucceeded
	EventRefundFailed      = core.EventRefundFailed

	// EventAll subscribes a listener to every payment event.
	EventAll = core.EventAll
	// ResolvedFromPending marks an event whose pending payment was resolved later.
	ResolvedFromPending = core.ResolvedFromPending
)

// PaymentEventTypes lists every event type, in declaration order.
var PaymentEventTypes = core.PaymentEventTypes

// EventFor returns the event an operation result triggers, or nil when it
// triggers none.
func EventFor(operation PaymentOperation, provider string, request, result any) *PaymentEvent {
	return core.EventFor(operation, provider, request, result)
}

// Plugins: operation hooks, event listeners, endpoints and members.
type (
	// Plugin extends the payment object. ID is required and must be unique.
	Plugin = core.Plugin
	// PluginHooks are the operation hooks a plugin registers.
	PluginHooks = core.PluginHooks
	// BeforeHook runs before a provider operation.
	BeforeHook = core.BeforeHook
	// BeforeHookResult is what a before hook returns.
	BeforeHookResult = core.BeforeHookResult
	// AfterHook runs after a provider operation returned a result.
	AfterHook = core.AfterHook
	// AfterHookResult is what an after hook returns.
	AfterHookResult = core.AfterHookResult
	// OperationContext is an operation as before hooks see it.
	OperationContext = core.OperationContext
	// OperationResultContext is an operation and its result, as after hooks see it.
	OperationResultContext = core.OperationResultContext
	// PaymentOperation is a provider operation plugins can hook into.
	PaymentOperation = core.PaymentOperation
	// PluginContext is what plugins get in Init, Methods and endpoints.
	PluginContext = core.PluginContext
	// EndpointContext is what a plugin endpoint handler gets.
	EndpointContext = core.EndpointContext
	// EndpointInfo is a plugin endpoint as other plugins see it.
	EndpointInfo = core.EndpointInfo
	// EndpointHandlerInfo describes the handler serving an endpoint request.
	EndpointHandlerInfo = core.EndpointHandlerInfo
	// EndpointOpenAPIMetadata is optional OpenAPI documentation for an endpoint.
	EndpointOpenAPIMetadata = core.EndpointOpenAPIMetadata
	// PaymentEndpoint is an HTTP route served by the handler.
	PaymentEndpoint = core.PaymentEndpoint
	// RegisteredEndpoint is a plugin endpoint with the id and name it was registered under.
	RegisteredEndpoint = core.RegisteredEndpoint
	// RequestContext is what Plugin.OnRequest gets besides the request.
	RequestContext = core.RequestContext
	// ResponseContext is what Plugin.OnResponse gets besides the response.
	ResponseContext = core.ResponseContext
)

// The hookable operations.
const (
	OperationCreatePayment          = core.OperationCreatePayment
	OperationInitThreeDSPayment     = core.OperationInitThreeDSPayment
	OperationCompleteThreeDSPayment = core.OperationCompleteThreeDSPayment
	OperationAuthorize              = core.OperationAuthorize
	OperationInitThreeDSAuthorize   = core.OperationInitThreeDSAuthorize
	OperationCapture                = core.OperationCapture
	OperationVoidAuthorization      = core.OperationVoidAuthorization
	OperationRefund                 = core.OperationRefund
	OperationCancel                 = core.OperationCancel
	OperationGetPayment             = core.OperationGetPayment
	OperationSaveCard               = core.OperationSaveCard
	OperationListCards              = core.OperationListCards
	OperationDeleteCard             = core.OperationDeleteCard
	OperationBinCheck               = core.OperationBinCheck
	OperationInstallmentInfo        = core.OperationInstallmentInfo
)

// AllPaymentOperations lists every hookable operation, in hook declaration order.
var AllPaymentOperations = core.AllPaymentOperations

// DefinePlugin returns the plugin unchanged, so plugin constructors have a single
// recognizable shape.
func DefinePlugin(plugin Plugin) Plugin { return core.DefinePlugin(plugin) }

// The HTTP handler and the facade it runs against.
type (
	// HandlerCore is the payment facade the handler runs against. Payment
	// implements it.
	HandlerCore = core.HandlerCore
	// RunOptions controls one operation run through the facade.
	RunOptions = core.RunOptions
	// Handler is a framework-agnostic HTTP handler.
	Handler = core.Handler
	// HandlerOptions configures a Handler.
	HandlerOptions = core.HandlerOptions
	// HandlerIdempotencyOptions configures callback and Idempotency-Key deduplication.
	HandlerIdempotencyOptions = core.HandlerIdempotencyOptions
	// HandlerRequest is an incoming HTTP request, independent of any web framework.
	HandlerRequest = core.HandlerRequest
	// HandlerResponse is an outgoing HTTP response, independent of any web framework.
	HandlerResponse = core.HandlerResponse
	// HandlerAction is a handler route.
	HandlerAction = core.HandlerAction
	// HandlerContext is the route being served.
	HandlerContext = core.HandlerContext
	// CallbackContext is the context of a provider callback.
	CallbackContext = core.CallbackContext

	// CheckoutFormProvider is implemented by providers with a hosted checkout form.
	CheckoutFormProvider = core.CheckoutFormProvider
	// PWIProvider is implemented by providers supporting Payment With IBAN.
	PWIProvider = core.PWIProvider
	// SubscriptionProvider is implemented by providers with recurring subscriptions.
	SubscriptionProvider = core.SubscriptionProvider
	// TokenPaymentProvider is implemented by providers that can start a payment
	// with a stored payment token.
	TokenPaymentProvider = core.TokenPaymentProvider
	// PlainTextCallbackProvider is implemented by providers whose callback must be
	// answered with plain text instead of JSON.
	PlainTextCallbackProvider = core.PlainTextCallbackProvider
	// CallbackAcknowledgement is the plain-text body a provider expects for its
	// server-to-server notification.
	CallbackAcknowledgement = core.CallbackAcknowledgement
)

// The built-in handler actions.
const (
	ActionPayment                 = core.ActionPayment
	ActionPaymentInit3DS          = core.ActionPaymentInit3DS
	ActionPaymentToken            = core.ActionPaymentToken
	ActionPaymentComplete3DS      = core.ActionPaymentComplete3DS
	ActionPaymentGet              = core.ActionPaymentGet
	ActionCallback                = core.ActionCallback
	ActionRefund                  = core.ActionRefund
	ActionCancel                  = core.ActionCancel
	ActionAuthorize               = core.ActionAuthorize
	ActionAuthorizeInit3DS        = core.ActionAuthorizeInit3DS
	ActionCapture                 = core.ActionCapture
	ActionVoid                    = core.ActionVoid
	ActionCardsSave               = core.ActionCardsSave
	ActionCardsList               = core.ActionCardsList
	ActionCardsDelete             = core.ActionCardsDelete
	ActionCheckoutInit            = core.ActionCheckoutInit
	ActionCheckoutRetrieve        = core.ActionCheckoutRetrieve
	ActionPWIInit                 = core.ActionPWIInit
	ActionPWIRetrieve             = core.ActionPWIRetrieve
	ActionInstallment             = core.ActionInstallment
	ActionBinCheck                = core.ActionBinCheck
	ActionSubscriptionInitialize  = core.ActionSubscriptionInitialize
	ActionSubscriptionCancel      = core.ActionSubscriptionCancel
	ActionSubscriptionUpgrade     = core.ActionSubscriptionUpgrade
	ActionSubscriptionRetrieve    = core.ActionSubscriptionRetrieve
	ActionSubscriptionCardUpdate  = core.ActionSubscriptionCardUpdate
	ActionSubscriptionProduct     = core.ActionSubscriptionProduct
	ActionSubscriptionPricingPlan = core.ActionSubscriptionPricingPlan
)

// The handler action sets.
var (
	// AllHandlerActions lists every built-in action.
	AllHandlerActions = core.AllHandlerActions
	// DefaultHandlerActions are enabled when AllowedActions is not set.
	DefaultHandlerActions = core.DefaultHandlerActions
	// PrivilegedHandlerActions can only be enabled together with an Authorize hook.
	PrivilegedHandlerActions = core.PrivilegedHandlerActions
	// CallbackHandlerActions are the provider callbacks, signed by the provider.
	CallbackHandlerActions = core.CallbackHandlerActions
	// IdempotentKeyActions honor an Idempotency-Key request header.
	IdempotentKeyActions = core.IdempotentKeyActions
)

// NewHandler builds a handler for a payment facade.
func NewHandler(facade HandlerCore, options HandlerOptions) (*Handler, error) {
	return core.NewHandler(facade, options)
}

// PluginEndpointAction builds the action of a plugin endpoint.
func PluginEndpointAction(pluginID, name string) HandlerAction {
	return core.PluginEndpointAction(pluginID, name)
}

// Configuration and the provider contract.
type (
	// Mode selects the environment: sandbox or production.
	Mode = core.Mode
	// ProviderType is the id of a built-in provider.
	ProviderType = core.ProviderType
	// ProviderSetupContext are the shared settings passed to a ProviderDefinition.
	ProviderSetupContext = core.ProviderSetupContext
	// ProviderDefinition creates a provider once the shared settings are known.
	ProviderDefinition = core.ProviderDefinition
	// ProviderEntry is a provider instance, a definition, or a provider's own
	// configuration.
	ProviderEntry = core.ProviderEntry
	// Options is the configuration of a payment facade, without the locale.
	Options = core.Options
	// ProviderURLs are the API hosts of a provider in both environments.
	ProviderURLs = core.ProviderURLs
	// Provider is the contract every payment gateway implements.
	Provider = core.Provider
	// BaseProvider carries the shared configuration and helpers of a provider.
	BaseProvider = core.BaseProvider
	// ProviderConfig holds the fields shared by all providers.
	ProviderConfig = core.ProviderConfig
	// Logger receives diagnostic messages. Implementations must never log request
	// bodies or headers: they carry card data and credentials.
	Logger = core.Logger
	// NopLogger discards every message.
	NopLogger = core.NopLogger
	// RetryConfig is the retry policy of provider calls.
	RetryConfig = core.RetryConfig
)

// The environments.
const (
	ModeSandbox    = core.ModeSandbox
	ModeProduction = core.ModeProduction
)

// The built-in provider ids.
const (
	ProviderIyzico      = core.ProviderIyzico
	ProviderPayTR       = core.ProviderPayTR
	ProviderAkbank      = core.ProviderAkbank
	ProviderParamPOS    = core.ProviderParamPOS
	ProviderKuveytTurk  = core.ProviderKuveytTurk
	ProviderSipay       = core.ProviderSipay
	ProviderAlbaraka    = core.ProviderAlbaraka
	ProviderQNBPAY      = core.ProviderQNBPAY
	ProviderIsbank      = core.ProviderIsbank
	ProviderZiraat      = core.ProviderZiraat
	ProviderHalkbank    = core.ProviderHalkbank
	ProviderTEB         = core.ProviderTEB
	ProviderSekerbank   = core.ProviderSekerbank
	ProviderAkbankEST   = core.ProviderAkbankEST
	ProviderHalkbankMKD = core.ProviderHalkbankMKD
	ProviderDenizBank   = core.ProviderDenizBank
	ProviderVakifBank   = core.ProviderVakifBank
	ProviderYapiKredi   = core.ProviderYapiKredi
	ProviderQNB         = core.ProviderQNB
	ProviderGaranti     = core.ProviderGaranti
	ProviderMock        = core.ProviderMock
)

// AllProviderTypes lists the built-in provider ids.
var AllProviderTypes = core.AllProviderTypes

// ProviderDefaultURLs are the API hosts of the built-in providers.
var ProviderDefaultURLs = core.ProviderDefaultURLs

// DefineProvider wraps a function that creates a custom provider from the shared
// settings.
func DefineProvider(create func(setup ProviderSetupContext) (Provider, error)) ProviderDefinition {
	return core.DefineProvider(create)
}

// WithProviderDefaults fills the shared settings a provider configuration leaves
// out, including the base URL of the environment.
func WithProviderDefaults(providerType ProviderType, config ProviderConfig, setup ProviderSetupContext) ProviderConfig {
	return core.WithProviderDefaults(providerType, config, setup)
}

// NewBaseProvider applies the shared provider defaults: locale "tr", a nop logger
// and validation enabled.
func NewBaseProvider(name string, config ProviderConfig) BaseProvider {
	return core.NewBaseProvider(name, config)
}

// Normalized error codes.
type (
	// PaymentErrorCode is a provider-independent failure code.
	PaymentErrorCode = core.PaymentErrorCode
)

// The normalized failure codes.
const (
	CodeInsufficientFunds   = core.CodeInsufficientFunds
	CodeCardDeclined        = core.CodeCardDeclined
	CodeInvalidCard         = core.CodeInvalidCard
	CodeExpiredCard         = core.CodeExpiredCard
	CodeInvalidCVC          = core.CodeInvalidCVC
	CodeThreeDSFailed       = core.CodeThreeDSFailed
	CodeFraudSuspected      = core.CodeFraudSuspected
	CodeLimitExceeded       = core.CodeLimitExceeded
	CodeDuplicateOrder      = core.CodeDuplicateOrder
	CodeCancelledByCustomer = core.CodeCancelledByCustomer
	CodeInvalidRequest      = core.CodeInvalidRequest
	CodeNetworkError        = core.CodeNetworkError
	CodeInvalidHash         = core.CodeInvalidHash
	CodeProviderError       = core.CodeProviderError
	CodeUnknown             = core.CodeUnknown
)

// ISO8583ErrorCodes maps the host response codes of Turkish bank virtual POS systems.
var ISO8583ErrorCodes = core.ISO8583ErrorCodes

// The documented error codes of the providers that publish a table.
var (
	// IyzicoErrorCodes maps the iyzico payment error codes.
	IyzicoErrorCodes = iyzico.ErrorCodes
	// PayTRErrorCodes maps PayTR failed_reason_code values.
	PayTRErrorCodes = paytr.ErrorCodes
	// ParamPOSErrorCodes maps Param's Sonuc values for failed requests.
	ParamPOSErrorCodes = parampos.ErrorCodes
)

// ResolveErrorCode maps a provider's raw code to a normalized code.
func ResolveErrorCode(raw string, providerCodes map[string]PaymentErrorCode) PaymentErrorCode {
	return core.ResolveErrorCode(raw, providerCodes)
}

// HTTP transport of provider API calls.
type (
	// Doer is the transport of provider API calls.
	Doer = core.Doer
	// RequestConfig is one provider API call.
	RequestConfig = core.RequestConfig
	// Response is a provider API response.
	Response = core.Response
	// HTTPError reports a failed provider API call.
	HTTPError = core.HTTPError
	// HTTPClientOptions configures an HTTPClient.
	HTTPClientOptions = core.HTTPClientOptions
	// HTTPClient calls a provider API with retries and timeouts.
	HTTPClient = core.HTTPClient
)

// NewHTTPClient builds the HTTP client of a provider.
func NewHTTPClient(options HTTPClientOptions) *HTTPClient { return core.NewHTTPClient(options) }

// Signature helpers for custom providers. Compare signatures with SafeEqual only:
// it runs in constant time.
type (
	// DigestAlgorithm selects the hash used by Digest and HMAC.
	DigestAlgorithm = core.DigestAlgorithm
)

// The supported hash algorithms.
const (
	SHA1   = core.SHA1
	SHA256 = core.SHA256
	SHA512 = core.SHA512
)

// Digest hashes data.
func Digest(algorithm DigestAlgorithm, data []byte) ([]byte, error) {
	return core.Digest(algorithm, data)
}

// HMAC signs data with a key.
func HMAC(algorithm DigestAlgorithm, key, data []byte) ([]byte, error) {
	return core.HMAC(algorithm, key, data)
}

// SafeEqual compares two strings in constant time.
func SafeEqual(a, b string) bool { return core.SafeEqual(a, b) }

// ToBase64 encodes bytes as standard base64.
func ToBase64(data []byte) string { return core.ToBase64(data) }

// FromBase64 decodes standard base64 to text.
func FromBase64(value string) (string, error) { return core.FromBase64(value) }

// Base64ToBytes decodes standard base64.
func Base64ToBytes(value string) ([]byte, error) { return core.Base64ToBytes(value) }

// ToHex encodes bytes as lowercase hex.
func ToHex(b []byte) string { return core.ToHex(b) }

// UTF8 returns the bytes of a string.
func UTF8(value string) []byte { return core.UTF8(value) }

// RandomHex returns cryptographically random bytes as hex.
func RandomHex(byteLength int) (string, error) { return core.RandomHex(byteLength) }

// AESCBCEncrypt encrypts with AES-CBC and PKCS#7 padding.
func AESCBCEncrypt(key, iv, data []byte) ([]byte, error) { return core.AESCBCEncrypt(key, iv, data) }

// AESCBCDecrypt decrypts AES-CBC with PKCS#7 padding.
func AESCBCDecrypt(key, iv, data []byte) ([]byte, error) { return core.AESCBCDecrypt(key, iv, data) }

// Basket building: amounts stay strings, in major units.
type (
	// BasketBuilderItem is one line of an order before it is built.
	BasketBuilderItem = core.BasketBuilderItem
	// BasketShipping is the shipping fee of an order.
	BasketShipping = core.BasketShipping
	// BasketDiscount is a discount applied to the subtotal.
	BasketDiscount = core.BasketDiscount
	// QuantityMode selects how BasketBuilderItem.Quantity is sent to the provider.
	QuantityMode = core.QuantityMode
	// BuildBasketOptions configures BuildBasket.
	BuildBasketOptions = core.BuildBasketOptions
	// BuiltBasket is the basket and the prices to charge.
	BuiltBasket = core.BuiltBasket
)

// The quantity modes.
const (
	QuantityExpand   = core.QuantityExpand
	QuantityMultiply = core.QuantityMultiply
)

// BuildBasket turns order lines into provider basket items, distributing a
// discount so the items add up to the total exactly.
func BuildBasket(options BuildBasketOptions) (*BuiltBasket, error) {
	return core.BuildBasket(options)
}

// Callback and Idempotency-Key deduplication.
type (
	// IdempotencyStore remembers callback and Idempotency-Key results.
	IdempotencyStore = core.IdempotencyStore
	// MemoryIdempotencyStore is a per-process store with a TTL and a size limit.
	MemoryIdempotencyStore = core.MemoryIdempotencyStore
)

// NewMemoryIdempotencyStore builds an in-memory store. Pass a shared store when
// running several instances.
func NewMemoryIdempotencyStore(maxEntries ...int) *MemoryIdempotencyStore {
	return core.NewMemoryIdempotencyStore(maxEntries...)
}

// Fingerprint hashes a value canonically, so the same body has the same key.
func Fingerprint(value any) (string, error) { return core.Fingerprint(value) }

// Request validation, for custom providers.
type (
	// PaymentValidationRules selects the checks a provider needs.
	PaymentValidationRules = core.PaymentValidationRules
	// RequestValidator collects the invalid fields of a request.
	RequestValidator = core.RequestValidator
)

// ValidatePaymentRequest rejects a payment request the provider would reject.
func ValidatePaymentRequest(request *PaymentRequest, rules PaymentValidationRules, provider string) error {
	return core.ValidatePaymentRequest(request, rules, provider)
}

// ValidateRefundAmount rejects a refund amount that is not a positive decimal.
func ValidateRefundAmount(price, provider string) error {
	return core.ValidateRefundAmount(price, provider)
}

// ValidateCaptureRequest rejects a capture request without a payment id.
func ValidateCaptureRequest(request *CaptureRequest, provider string) error {
	return core.ValidateCaptureRequest(request, provider)
}

// Amount and message helpers. Amounts are strings everywhere: no float rounding
// reaches a provider.

// GenerateOrderID returns a unique order id with a prefix.
func GenerateOrderID(prefix string) string { return core.GenerateOrderID(prefix) }

// ParseAmount parses a decimal amount.
func ParseAmount(amount, field string) (float64, error) { return core.ParseAmount(amount, field) }

// ToMinorUnits converts a decimal amount to minor units (kuruş, cents).
func ToMinorUnits(amount, field string) (int64, error) { return core.ToMinorUnits(amount, field) }

// FormatDecimal renders an amount with the separator a provider expects.
func FormatDecimal(amount string, separator byte, field string) (string, error) {
	return core.FormatDecimal(amount, separator, field)
}

// ErrorMessage returns the message of an error, or a fallback.
func ErrorMessage(err error, fallback string) string { return core.ErrorMessage(err, fallback) }

// IsNetworkError reports whether no response arrived, so the outcome is unknown.
func IsNetworkError(err error) bool { return core.IsNetworkError(err) }

// The HTTP client of a served payment handler: call your own routes over HTTP.
type (
	// Client calls a payment handler over HTTP.
	Client = client.Client
	// ClientConfig configures a Client.
	ClientConfig = client.Config
	// ProviderClient calls the routes of one provider.
	ProviderClient = client.ProviderClient
	// HealthResponse is the handler's health check.
	HealthResponse = client.HealthResponse
	// RequestError reports a non-2xx response of a handler route.
	RequestError = client.RequestError
)

// NewClient builds a client pointed at a served payment handler.
func NewClient(config ClientConfig) *Client { return client.New(config) }

// Serving the handler with net/http.
type (
	// RequestHandler serves a payment request.
	RequestHandler = adapters.Handler
	// HandlerFunc adapts a function to RequestHandler.
	HandlerFunc = adapters.HandlerFunc
	// HandlerProvider exposes a handler as a RequestHandler.
	HandlerProvider = adapters.HandlerProvider
	// HandlerSource is a handler, a provider of one, or a function.
	HandlerSource = adapters.HandlerSource
	// AdapterOptions configures the net/http adapters.
	AdapterOptions = adapters.AdapterOptions
	// BodyTooLargeError reports a request body over the size limit.
	BodyTooLargeError = adapters.BodyTooLargeError
	// SerializedResponse is a handler response ready to be written.
	SerializedResponse = adapters.SerializedResponse
)

// DefaultBasePath is the path prefix a handler is mounted on by default.
const DefaultBasePath = adapters.DefaultBasePath

// DefaultMaxBodySize is the request body limit of the adapters, in bytes.
const DefaultMaxBodySize = adapters.DefaultMaxBodySize

// HTTPHandler serves a payment handler as an http.Handler.
func HTTPHandler(source HandlerSource, options ...AdapterOptions) http.Handler {
	return adapters.HTTPHandler(source, options...)
}

// Middleware serves the routes under basePath and passes everything else to the
// next handler.
func Middleware(basePath string, source HandlerSource, options ...AdapterOptions) func(http.Handler) http.Handler {
	return adapters.Middleware(basePath, source, options...)
}

// ResolveHandler turns a handler source into a RequestHandler.
func ResolveHandler(source HandlerSource) (RequestHandler, error) {
	return adapters.ResolveHandler(source)
}

// FromRequest converts an incoming request, reading at most the body limit.
func FromRequest(r *http.Request, options ...AdapterOptions) (*HandlerRequest, error) {
	return adapters.FromRequest(r, options...)
}

// SerializeResponse renders a handler response, including its body bytes.
func SerializeResponse(response *HandlerResponse) (SerializedResponse, error) {
	return adapters.SerializeResponse(response)
}

// WriteResponse writes a handler response to an http.ResponseWriter.
func WriteResponse(w http.ResponseWriter, response *HandlerResponse) error {
	return adapters.WriteResponse(w, response)
}

// WriteSerialized writes an already serialized response.
func WriteSerialized(w http.ResponseWriter, response SerializedResponse) error {
	return adapters.WriteSerialized(w, response)
}

// PayloadTooLarge is the 413 response for a body over the size limit.
func PayloadTooLarge() *HandlerResponse { return adapters.PayloadTooLarge() }

// Payment is a handler source the adapters resolve directly.
var _ interface{ Handler() *core.Handler } = (*Payment)(nil)

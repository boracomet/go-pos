package core

import (
	"context"
	"net/url"
)

// PaymentOperation is a provider operation plugins can hook into. Provider-specific
// methods (hosted checkout forms, subscriptions, token payments) are not
// operations: they run without hooks.
//
// The request and result types of each operation are listed on its constant.
// OperationContext carries them as any; type-assert after checking Operation.
type PaymentOperation string

const (
	// OperationCreatePayment: request *PaymentRequest, result *PaymentResponse.
	OperationCreatePayment PaymentOperation = "createPayment"
	// OperationInitThreeDSPayment: request *ThreeDSPaymentRequest, result *ThreeDSInitResponse.
	OperationInitThreeDSPayment PaymentOperation = "initThreeDSPayment"
	// OperationCompleteThreeDSPayment: request is the provider's callback data, result *PaymentResponse.
	OperationCompleteThreeDSPayment PaymentOperation = "completeThreeDSPayment"
	// OperationAuthorize: request *PaymentRequest, result *PaymentResponse.
	OperationAuthorize PaymentOperation = "authorize"
	// OperationInitThreeDSAuthorize: request *ThreeDSPaymentRequest, result *ThreeDSInitResponse.
	OperationInitThreeDSAuthorize PaymentOperation = "initThreeDSAuthorize"
	// OperationCapture: request *CaptureRequest, result *PaymentResponse.
	OperationCapture PaymentOperation = "capture"
	// OperationVoidAuthorization: request *VoidAuthorizationRequest, result *CancelResponse.
	OperationVoidAuthorization PaymentOperation = "voidAuthorization"
	// OperationRefund: request *RefundRequest, result *RefundResponse.
	OperationRefund PaymentOperation = "refund"
	// OperationCancel: request *CancelRequest, result *CancelResponse.
	OperationCancel PaymentOperation = "cancel"
	// OperationGetPayment: request is the payment id (string), result *PaymentResponse.
	OperationGetPayment PaymentOperation = "getPayment"
	// OperationSaveCard: request *SaveCardRequest, result *SaveCardResponse.
	OperationSaveCard PaymentOperation = "saveCard"
	// OperationListCards: request *ListCardsRequest, result *ListCardsResponse.
	OperationListCards PaymentOperation = "listCards"
	// OperationDeleteCard: request *DeleteCardRequest, result *DeleteCardResponse.
	OperationDeleteCard PaymentOperation = "deleteCard"
	// OperationBinCheck: request is the BIN (string, first 6-8 digits), result *BinCheckResponse.
	OperationBinCheck PaymentOperation = "binCheck"
	// OperationInstallmentInfo: request *InstallmentInfoRequest, result *InstallmentInfoResponse.
	OperationInstallmentInfo PaymentOperation = "installmentInfo"
)

// AllPaymentOperations lists every hookable operation, in hook declaration order.
var AllPaymentOperations = []PaymentOperation{
	OperationCreatePayment,
	OperationInitThreeDSPayment,
	OperationCompleteThreeDSPayment,
	OperationAuthorize,
	OperationInitThreeDSAuthorize,
	OperationCapture,
	OperationVoidAuthorization,
	OperationRefund,
	OperationCancel,
	OperationGetPayment,
	OperationSaveCard,
	OperationListCards,
	OperationDeleteCard,
	OperationBinCheck,
	OperationInstallmentInfo,
}

// OperationContext is an operation as before hooks see it. Request holds the
// operation's request type (see PaymentOperation); assert it after checking
// Operation.
type OperationContext struct {
	Operation PaymentOperation
	Request   any
	// Provider is the provider that runs the operation. It is empty on a call made
	// without a default provider: a before hook must then choose one.
	Provider string
	// Routable is true when the call was made on the payment facade, false when it
	// was made on a specific provider or through the HTTP handler. Only routable
	// calls can be sent to another provider.
	Routable bool
}

// OperationResultContext is an operation and its result, as after hooks see it.
// Provider is always set.
type OperationResultContext struct {
	OperationContext
	Result any
}

// BeforeHookResult is what a before hook returns. Every field is optional; a nil
// return leaves the operation unchanged.
type BeforeHookResult struct {
	// Provider runs the operation on this provider instead (routable calls only).
	// Empty leaves the provider unchanged.
	Provider string
	// Request replaces the request when non-nil.
	Request any
	// Result skips the provider and returns this result: the remaining before hooks
	// do not run, after hooks and events do.
	Result any
}

// BeforeHook runs before a provider operation.
type BeforeHook struct {
	// Matcher selects the operations the hook runs for. Nil means all.
	Matcher func(ctx OperationContext) bool
	// Handler inspects or rewrites the call. Returning an error fails the operation.
	Handler func(ctx OperationContext) (*BeforeHookResult, error)
}

// AfterHookResult is what an after hook returns. A nil return leaves the result
// unchanged.
type AfterHookResult struct {
	// Result replaces the operation's result when non-nil.
	Result any
	// Request replaces the request passed to events when non-nil.
	Request any
}

// AfterHook runs after a provider operation returned a result.
type AfterHook struct {
	// Matcher selects the operations the hook runs for. Nil means all.
	Matcher func(ctx OperationResultContext) bool
	// Handler inspects or rewrites the result. Returning an error fails the operation.
	Handler func(ctx OperationResultContext) (*AfterHookResult, error)
}

// PluginHooks are the operation hooks a plugin registers. Before hooks run in
// registration order, then the provider, then after hooks in registration order.
type PluginHooks struct {
	Before []BeforeHook
	After  []AfterHook
}

// PluginContext is what plugins get in Init, Methods and endpoints.
type PluginContext interface {
	// ProviderIDs returns the ids of the configured providers.
	ProviderIDs() []string
	// ErrorCodes returns the error codes declared by plugins, with their default messages.
	ErrorCodes() map[string]string
	// DefaultProvider returns the default provider id, or an empty string.
	DefaultProvider() string
	// Mode is the configured environment.
	Mode() Mode
	// Logger is the configured logger, or nil.
	Logger() Logger
	// Use returns a provider by id whose operations run with hooks and events.
	Use(providerID string) (Provider, error)
	// Emit sends a payment event to the registered listeners.
	Emit(ctx context.Context, event *PaymentEvent) error
	// Endpoints returns the endpoints registered by plugins, without their handlers.
	Endpoints() []EndpointInfo
}

// EndpointInfo is a plugin endpoint as other plugins see it.
type EndpointInfo struct {
	PluginID   string                   `json:"pluginId"`
	Name       string                   `json:"name"`
	Method     string                   `json:"method"`
	Path       string                   `json:"path"`
	Privileged bool                     `json:"privileged"`
	OpenAPI    *EndpointOpenAPIMetadata `json:"openapi,omitempty"`
}

// EndpointHandlerInfo describes the handler serving an endpoint request.
type EndpointHandlerInfo struct {
	// BasePath is the path prefix the handler is mounted on, e.g. "/api/pay".
	BasePath string
	// AllowedActions are the built-in actions the handler serves.
	AllowedActions []HandlerAction
}

// EndpointContext is what a plugin endpoint handler gets: the plugin context plus
// the incoming request and helpers to build a response.
type EndpointContext interface {
	PluginContext
	// Request is the incoming HTTP request.
	Request() *HandlerRequest
	// Body is the parsed body (JSON or form-urlencoded), nil when there is none.
	Body() map[string]any
	// Query holds the URL query parameters.
	Query() url.Values
	// JSON builds a JSON response. Omitting status, or passing 0, means 200.
	JSON(body any, status ...int) *HandlerResponse
	// HTML builds an HTML response. Omitting status, or passing 0, means 200.
	HTML(body string, status ...int) *HandlerResponse
	// Handler describes the handler serving the request.
	Handler() EndpointHandlerInfo
}

// EndpointOpenAPIMetadata is optional OpenAPI documentation for an endpoint.
type EndpointOpenAPIMetadata struct {
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	RequestBody any            `json:"requestBody,omitempty"`
	Responses   map[string]any `json:"responses,omitempty"`
	Parameters  []any          `json:"parameters,omitempty"`
}

// PaymentEndpoint is an HTTP route served by the handler at basePath + Path. The
// handler returns what Handler returns as JSON (200), unless it returns a
// response built with EndpointContext.JSON or EndpointContext.HTML.
type PaymentEndpoint struct {
	// Method is "GET" or "POST".
	Method string
	// Path is relative to the handler's basePath, e.g. "/router/quote".
	Path string
	// Privileged endpoints can only be served together with an authorize hook,
	// like refunds. The hook runs for every endpoint that has one.
	Privileged bool
	// AcceptForm accepts HTML form bodies (application/x-www-form-urlencoded,
	// multipart/form-data, text/plain) on a POST endpoint. Off by default: a
	// cross-site form can send these without a CORS preflight, carrying the
	// visitor's cookies (CSRF). Turn it on only for an endpoint a third party posts
	// a form to, like a bank callback, and verify that request yourself.
	AcceptForm bool
	// OpenAPI is optional documentation metadata.
	OpenAPI *EndpointOpenAPIMetadata
	// Handler serves the request.
	Handler func(ctx context.Context, ectx EndpointContext) (any, error)
}

// RegisteredEndpoint is a plugin endpoint with the id and name it was registered under.
type RegisteredEndpoint struct {
	PaymentEndpoint
	PluginID string
	Name     string
}

// RequestContext is what Plugin.OnRequest gets besides the request.
type RequestContext struct {
	// Path is relative to the handler's basePath, e.g. "/iyzico/payment".
	Path string
}

// ResponseContext is what Plugin.OnResponse gets besides the response.
type ResponseContext struct {
	Request *HandlerRequest
}

// Plugin extends the payment facade: operation hooks, event listeners, HTTP
// endpoints and request/response interception. Only the fields it needs are set;
// ID is required and must be unique.
type Plugin struct {
	// ID is the unique plugin id, e.g. "commission-router".
	ID string
	// Version is the plugin's own version, for introspection.
	Version string
	// Options are the options the plugin was created with, for introspection.
	Options any
	// Init runs once, before the first operation. Plugins run it in order.
	Init func(ctx context.Context, pctx PluginContext) error
	// Hooks run before and after provider operations.
	Hooks PluginHooks
	// Events are the plugin's payment event listeners.
	Events PaymentEventListeners
	// Methods returns members added to the payment facade, e.g. {"router": ...}.
	Methods func(ctx context.Context, pctx PluginContext) (map[string]any, error)
	// Endpoints are HTTP routes added to the handler, keyed by endpoint name.
	Endpoints map[string]PaymentEndpoint
	// OnRequest runs on every incoming handler request before routing. Returning a
	// response (e.g. 429) short-circuits routing; that response still passes through
	// OnResponse hooks. Returning an error stops the request with 503: a check that
	// could not run (a rate limit whose store is down) must not let the request
	// through unchecked.
	OnRequest func(ctx context.Context, request *HandlerRequest, rctx RequestContext) (*HandlerResponse, error)
	// OnResponse runs on every handler response (also replayed ones) and can return
	// a replacement. An error is logged and the response is sent unchanged.
	OnResponse func(ctx context.Context, response *HandlerResponse, rctx ResponseContext) (*HandlerResponse, error)
	// ErrorCodes are the plugin's error codes and their default (English) messages.
	ErrorCodes map[string]string
}

// DefinePlugin returns the plugin unchanged. It exists so plugin constructors
// have a single, recognizable shape.
func DefinePlugin(plugin Plugin) Plugin { return plugin }

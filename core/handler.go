package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// HandlerRequest is an incoming HTTP request, independent of any web framework.
type HandlerRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	// Body is the raw body (a string) or an already parsed value.
	Body any `json:"body,omitempty"`
}

// HandlerResponse is an outgoing HTTP response, independent of any web framework.
// Body is a JSON-serializable value, except for text responses (Content-Type
// text/plain, e.g. a provider's "OK" acknowledgement) where it is a string.
type HandlerResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

// HandlerAction is a handler route: the URL path after "/:provider/", or a plugin
// endpoint ("plugin:<plugin id>/<endpoint name>").
type HandlerAction string

// The built-in handler actions.
const (
	ActionPayment                 HandlerAction = "payment"
	ActionPaymentInit3DS          HandlerAction = "payment/init-3ds"
	ActionPaymentToken            HandlerAction = "payment/token"
	ActionPaymentComplete3DS      HandlerAction = "payment/complete-3ds"
	ActionPaymentGet              HandlerAction = "payment/get"
	ActionCallback                HandlerAction = "callback"
	ActionRefund                  HandlerAction = "refund"
	ActionCancel                  HandlerAction = "cancel"
	ActionAuthorize               HandlerAction = "authorize"
	ActionAuthorizeInit3DS        HandlerAction = "authorize/init-3ds"
	ActionCapture                 HandlerAction = "capture"
	ActionVoid                    HandlerAction = "void"
	ActionCardsSave               HandlerAction = "cards/save"
	ActionCardsList               HandlerAction = "cards/list"
	ActionCardsDelete             HandlerAction = "cards/delete"
	ActionCheckoutInit            HandlerAction = "checkout/init"
	ActionCheckoutRetrieve        HandlerAction = "checkout/retrieve"
	ActionPWIInit                 HandlerAction = "pwi/init"
	ActionPWIRetrieve             HandlerAction = "pwi/retrieve"
	ActionInstallment             HandlerAction = "installment"
	ActionBinCheck                HandlerAction = "bin-check"
	ActionSubscriptionInitialize  HandlerAction = "subscription/initialize"
	ActionSubscriptionCancel      HandlerAction = "subscription/cancel"
	ActionSubscriptionUpgrade     HandlerAction = "subscription/upgrade"
	ActionSubscriptionRetrieve    HandlerAction = "subscription/retrieve"
	ActionSubscriptionCardUpdate  HandlerAction = "subscription/card-update"
	ActionSubscriptionProduct     HandlerAction = "subscription/product"
	ActionSubscriptionPricingPlan HandlerAction = "subscription/pricing-plan"
)

// PluginEndpointAction builds the action of a plugin endpoint.
func PluginEndpointAction(pluginID, name string) HandlerAction {
	return HandlerAction("plugin:" + pluginID + "/" + name)
}

// AllHandlerActions lists every built-in action. Pass it as
// HandlerOptions.AllowedActions to expose everything (requires Authorize).
var AllHandlerActions = []HandlerAction{
	ActionPayment,
	ActionPaymentInit3DS,
	ActionPaymentToken,
	ActionPaymentComplete3DS,
	ActionPaymentGet,
	ActionCallback,
	ActionRefund,
	ActionCancel,
	ActionAuthorize,
	ActionAuthorizeInit3DS,
	ActionCapture,
	ActionVoid,
	ActionCardsSave,
	ActionCardsList,
	ActionCardsDelete,
	ActionCheckoutInit,
	ActionCheckoutRetrieve,
	ActionPWIInit,
	ActionPWIRetrieve,
	ActionInstallment,
	ActionBinCheck,
	ActionSubscriptionInitialize,
	ActionSubscriptionCancel,
	ActionSubscriptionUpgrade,
	ActionSubscriptionRetrieve,
	ActionSubscriptionCardUpdate,
	ActionSubscriptionProduct,
	ActionSubscriptionPricingPlan,
}

// DefaultHandlerActions are enabled when AllowedActions is not set: provider
// callbacks (which must be reachable by the bank/provider) and read-only card
// queries.
var DefaultHandlerActions = []HandlerAction{
	ActionPaymentComplete3DS,
	ActionCallback,
	ActionInstallment,
	ActionBinCheck,
}

// PrivilegedHandlerActions move money back, manage subscriptions or expose
// payment details. They can only be enabled together with an Authorize hook.
var PrivilegedHandlerActions = []HandlerAction{
	ActionRefund,
	ActionCancel,
	ActionCapture,
	ActionVoid,
	ActionCardsSave,
	ActionCardsList,
	ActionCardsDelete,
	ActionPaymentGet,
	ActionSubscriptionCancel,
	ActionSubscriptionUpgrade,
	ActionSubscriptionRetrieve,
	ActionSubscriptionCardUpdate,
	ActionSubscriptionProduct,
	ActionSubscriptionPricingPlan,
}

// CallbackHandlerActions are provider callbacks. They are signed by the provider
// and are called by the bank or the customer's browser, not by your frontend.
var CallbackHandlerActions = []HandlerAction{
	ActionPaymentComplete3DS,
	ActionCallback,
}

// IdempotentKeyActions honor an Idempotency-Key request header: a repeated key
// returns the first response instead of calling the provider again.
var IdempotentKeyActions = []HandlerAction{
	ActionPayment,
	ActionPaymentInit3DS,
	ActionPaymentToken,
	ActionRefund,
	ActionCancel,
	ActionAuthorize,
	ActionAuthorizeInit3DS,
	ActionCapture,
	ActionVoid,
	ActionCardsSave,
	ActionCardsDelete,
	ActionCheckoutInit,
	ActionPWIInit,
	ActionSubscriptionInitialize,
	ActionSubscriptionCancel,
	ActionSubscriptionUpgrade,
	ActionSubscriptionCardUpdate,
	ActionSubscriptionProduct,
	ActionSubscriptionPricingPlan,
}

func actionSet(actions []HandlerAction) map[HandlerAction]bool {
	set := make(map[HandlerAction]bool, len(actions))
	for _, action := range actions {
		set[action] = true
	}
	return set
}

var (
	allHandlerActionSet        = actionSet(AllHandlerActions)
	privilegedHandlerActionSet = actionSet(PrivilegedHandlerActions)
	callbackHandlerActionSet   = actionSet(CallbackHandlerActions)
	idempotentKeyActionSet     = actionSet(IdempotentKeyActions)
	binNumberRe                = regexp.MustCompile(`^\d{6,8}$`)
)

// HandlerContext is the route being served.
type HandlerContext struct {
	// Provider is the provider id of the route; empty for plugin endpoints.
	Provider string
	Action   HandlerAction
	Params   map[string]string
	Request  *HandlerRequest
	// Body is the parsed request body (form-urlencoded callbacks are converted to
	// objects). Nil when the request has no object body.
	Body map[string]any
}

// CallbackContext is the context of a provider callback: Provider is always set.
type CallbackContext = HandlerContext

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	// BasePath is the path prefix the handler is mounted on. Default: "/api/pay".
	BasePath string
	// AllowedActions are the actions exposed over HTTP. Nil uses
	// DefaultHandlerActions; AllHandlerActions exposes everything (requires
	// Authorize).
	AllowedActions []HandlerAction
	// Authorize is called before every action except the health check. Returning
	// false (or an error) rejects the request with 403. Required when privileged
	// actions are enabled.
	//
	// Provider callbacks are signed by the provider and are called by the
	// bank/customer browser, so the hook usually lets them through.
	Authorize func(ctx context.Context, hctx HandlerContext) (bool, error)
	// TransformRequest returns the body passed to the provider. Use it to set
	// amounts, basket and buyer on the server instead of trusting the client.
	TransformRequest func(ctx context.Context, hctx HandlerContext) (map[string]any, error)
	// OnCallback is called with the verified result of a provider callback. Update
	// your order here. An error makes the handler return 500, so the provider
	// re-sends the notification.
	OnCallback func(ctx context.Context, result *PaymentResponse, cctx CallbackContext) error
	// CallbackRedirect returns a URL to redirect the customer to (303) after a
	// browser callback (3D Secure return). An empty string returns the result as JSON.
	CallbackRedirect func(ctx context.Context, result *PaymentResponse, cctx CallbackContext) (string, error)
	// ExposeErrors includes error messages of unexpected failures in 500 responses.
	// Default: false.
	ExposeErrors bool
	// ExposeRawResponse includes the provider's rawResponse in responses. It can
	// hold merchant data (commission rates, fraud status) and stored-card keys, so
	// it is left out by default. Hooks and events always receive it. Default: false.
	ExposeRawResponse bool
	// Idempotency configures deduplication of provider callbacks and
	// Idempotency-Key requests. Nil enables it with an in-memory store; set
	// HandlerIdempotencyOptions.Disabled to turn it off.
	Idempotency *HandlerIdempotencyOptions
}

// HandlerIdempotencyOptions configures callback and Idempotency-Key deduplication.
type HandlerIdempotencyOptions struct {
	// Disabled turns deduplication off.
	Disabled bool
	// Store defaults to a per-process MemoryIdempotencyStore. Pass a shared store
	// (Redis, SQL) when running several instances.
	Store IdempotencyStore
	// TTLSeconds is how long results are remembered. Default: 86400 (24 hours).
	TTLSeconds int
	// LockSeconds is how long an in-flight request holds its key if the process
	// dies. Default: 60.
	LockSeconds int
	// Scope returns the caller an Idempotency-Key belongs to, such as a user or
	// session id. Keys of different callers never share a stored response. An
	// empty string means the caller is unknown; nil shares keys by all callers.
	Scope func(ctx context.Context, hctx HandlerContext) (string, error)
}

// RunOptions controls one operation run through the facade.
type RunOptions struct {
	// Routable allows before hooks to move the call to another provider.
	Routable bool
	// Emit emits events for the result. Nil means true; the handler defers events
	// for callbacks so a failing listener is retried with the stored result.
	Emit *bool
}

// HandlerCore is the payment facade the handler runs against. It owns the
// providers, the plugin hooks and the event listeners.
type HandlerCore interface {
	// IsProviderEnabled reports whether a provider id is configured.
	IsProviderEnabled(providerID string) bool
	// Use returns a provider whose operations run with hooks and events.
	Use(providerID string) (Provider, error)
	// RawProvider returns a provider without hooks. Provider-specific methods
	// (checkout forms, subscriptions, token payments) are not operations and run
	// without hooks.
	RawProvider(providerID string) (Provider, error)
	// Run executes an operation: before hooks, the provider, after hooks, events.
	Run(ctx context.Context, operation PaymentOperation, providerID string, request any, options RunOptions) (any, error)
	// Emit emits the event of an operation result, if it has one.
	Emit(ctx context.Context, operation PaymentOperation, providerID string, request, result any) error
	// InterceptRequest runs the plugins' OnRequest hooks. A nil response continues
	// routing; hook failures are turned into a 503 response by the facade.
	InterceptRequest(ctx context.Context, request *HandlerRequest, rctx RequestContext) (*HandlerResponse, error)
	// Respond runs the plugins' OnResponse hooks on a handler response.
	Respond(ctx context.Context, response *HandlerResponse, rctx ResponseContext) (*HandlerResponse, error)
	// PluginContext is the context handed to plugins.
	PluginContext() PluginContext
	// Endpoints returns the endpoints registered by plugins, in registration order.
	Endpoints() []RegisteredEndpoint
}

// CheckoutFormProvider is implemented by providers with a hosted checkout form.
type CheckoutFormProvider interface {
	InitCheckoutForm(ctx context.Context, request *CheckoutFormRequest) (*CheckoutFormInitResponse, error)
	RetrieveCheckoutForm(ctx context.Context, token, conversationID string) (*CheckoutFormRetrieveResponse, error)
}

// PWIProvider is implemented by providers supporting Payment With IBAN
// (protected bank transfer).
type PWIProvider interface {
	InitPWIPayment(ctx context.Context, request *PWIPaymentRequest) (*PWIPaymentInitResponse, error)
	RetrievePWIPayment(ctx context.Context, token, conversationID string) (*PWIPaymentRetrieveResponse, error)
}

// SubscriptionProvider is implemented by providers with recurring subscriptions.
type SubscriptionProvider interface {
	InitializeSubscription(ctx context.Context, request *SubscriptionInitializeRequest) (*SubscriptionInitializeResponse, error)
	CancelSubscription(ctx context.Context, request *SubscriptionCancelRequest) (*SubscriptionCancelResponse, error)
	UpgradeSubscription(ctx context.Context, request *SubscriptionUpgradeRequest) (*SubscriptionUpgradeResponse, error)
	RetrieveSubscription(ctx context.Context, request *SubscriptionRetrieveRequest) (*SubscriptionRetrieveResponse, error)
	UpdateSubscriptionCard(ctx context.Context, request *SubscriptionCardUpdateRequest) (*SubscriptionCardUpdateResponse, error)
	CreateSubscriptionProduct(ctx context.Context, request *SubscriptionProductCreateRequest) (*SubscriptionProductResponse, error)
	CreatePricingPlan(ctx context.Context, request *PricingPlanCreateRequest) (*PricingPlanResponse, error)
}

// TokenPaymentProvider is implemented by providers that can start a payment with
// a stored payment token. The request is the parsed JSON body of the HTTP request:
// a provider whose method takes a typed request needs an adapter to satisfy this.
type TokenPaymentProvider interface {
	CreatePaymentWithToken(ctx context.Context, request any) (*ThreeDSInitResponse, error)
}

// CallbackAcknowledgement is the plain-text body a provider expects for its
// server-to-server notification, which it re-sends until it is acknowledged.
type CallbackAcknowledgement struct {
	// Success answers a verified notification.
	Success string
	// Failure answers a notification whose signature did not verify.
	Failure string
}

// PlainTextCallbackProvider is implemented by providers whose "callback" action
// must be answered with plain text instead of JSON.
type PlainTextCallbackProvider interface {
	CallbackAcknowledgement() CallbackAcknowledgement
}

// handlerError carries the HTTP status of a request rejected by the handler.
type handlerError struct {
	status  int
	message string
}

func (e *handlerError) Error() string { return e.message }

type handlerIdempotency struct {
	store       IdempotencyStore
	ttlSeconds  int
	lockSeconds int
	scope       func(context.Context, HandlerContext) (string, error)
}

type callbackRecord struct {
	State    string           `json:"state"`
	Result   *PaymentResponse `json:"result,omitempty"`
	Response *HandlerResponse `json:"response,omitempty"`
}

type requestRecord struct {
	State    string           `json:"state"`
	Request  string           `json:"request"`
	Response *HandlerResponse `json:"response,omitempty"`
}

type routeContext struct {
	provider string
	action   string
	params   map[string]string
}

// Handler is a framework-agnostic HTTP handler.
//
// Routes (relative to BasePath, default "/api/pay"):
//
//	POST /:provider/payment                 -> CreatePayment
//	POST /:provider/payment/init-3ds        -> InitThreeDSPayment
//	POST /:provider/payment/token           -> CreatePaymentWithToken
//	POST /:provider/payment/complete-3ds    -> CompleteThreeDSPayment
//	POST /:provider/callback                -> CompleteThreeDSPayment
//	GET  /:provider/payment/:id             -> GetPayment
//	POST /:provider/refund | cancel         -> Refund / Cancel
//	POST /:provider/authorize[/init-3ds]    -> Authorize / InitThreeDSAuthorize
//	POST /:provider/capture | void          -> Capture / VoidAuthorization
//	POST /:provider/cards/{save,list,delete}-> SaveCard / ListCards / DeleteCard
//	POST /:provider/installment | bin-check -> InstallmentInfo / BinCheck
//	POST /:provider/checkout/*, pwi/*, subscription/* (providers that support them)
//	GET  /health
//
// Secure by default: only callbacks and card queries are enabled. Enable more
// with AllowedActions; privileged actions also require Authorize.
type Handler struct {
	core        HandlerCore
	options     HandlerOptions
	basePath    string
	allowed     map[HandlerAction]bool
	info        EndpointHandlerInfo
	idempotency *handlerIdempotency
	endpoints   map[string]RegisteredEndpoint
}

// NewHandler builds a handler for a payment facade. It returns a
// ConfigurationError when a plugin endpoint uses a reserved or duplicate path, or
// when privileged actions are enabled without an Authorize hook.
func NewHandler(core HandlerCore, options HandlerOptions) (*Handler, error) {
	if core == nil {
		return nil, NewConfigurationError("The handler needs a payment facade", "")
	}
	basePath := options.BasePath
	if basePath == "" {
		basePath = "/api/pay"
	}
	basePath = "/" + trimSlashes(basePath)

	allowed := options.AllowedActions
	if allowed == nil {
		allowed = DefaultHandlerActions
	}
	builtIn := make([]HandlerAction, 0, len(allowed))
	for _, action := range allowed {
		if allHandlerActionSet[action] {
			builtIn = append(builtIn, action)
		}
	}

	h := &Handler{
		core:      core,
		options:   options,
		basePath:  basePath,
		allowed:   actionSet(allowed),
		info:      EndpointHandlerInfo{BasePath: basePath, AllowedActions: builtIn},
		endpoints: make(map[string]RegisteredEndpoint),
	}

	if options.Idempotency == nil || !options.Idempotency.Disabled {
		settings := HandlerIdempotencyOptions{}
		if options.Idempotency != nil {
			settings = *options.Idempotency
		}
		if settings.Store == nil {
			settings.Store = NewMemoryIdempotencyStore()
		}
		if settings.TTLSeconds == 0 {
			settings.TTLSeconds = 86400
		}
		if settings.LockSeconds == 0 {
			settings.LockSeconds = 60
		}
		h.idempotency = &handlerIdempotency{
			store:       settings.Store,
			ttlSeconds:  settings.TTLSeconds,
			lockSeconds: settings.LockSeconds,
			scope:       settings.Scope,
		}
	}

	endpoints := core.Endpoints()
	for _, endpoint := range endpoints {
		path := "/" + trimSlashes(endpoint.Path)
		first := strings.Split(path, "/")[1]
		if core.IsProviderEnabled(first) || first == "health" || first == "ok" {
			return nil, NewConfigurationError(fmt.Sprintf(
				"Endpoint '%s' of plugin '%s' uses the reserved path %s", endpoint.Name, endpoint.PluginID, path), "")
		}
		if _, exists := h.endpoints[path]; exists {
			return nil, NewConfigurationError(fmt.Sprintf("Two plugin endpoints use the path %s", path), "")
		}
		h.endpoints[path] = endpoint
	}

	privileged := make([]string, 0, len(allowed))
	for _, action := range allowed {
		if privilegedHandlerActionSet[action] {
			privileged = append(privileged, string(action))
		}
	}
	for _, endpoint := range endpoints {
		if endpoint.Privileged {
			privileged = append(privileged, string(PluginEndpointAction(endpoint.PluginID, endpoint.Name)))
		}
	}
	if len(privileged) > 0 && options.Authorize == nil {
		return nil, NewConfigurationError(fmt.Sprintf(
			"Handler actions [%s] require an 'authorize' hook. "+
				"Refunds, cancellations and subscription management must never be publicly reachable.",
			strings.Join(privileged, ", ")), "")
	}
	return h, nil
}

// Handle serves a request. Plugin OnRequest hooks run before routing and
// OnResponse hooks run on the way out, including on replayed responses.
func (h *Handler) Handle(ctx context.Context, request *HandlerRequest) (*HandlerResponse, error) {
	if path, ok := h.getPath(request.URL); ok && path != "/health" && path != "/ok" {
		intercepted, err := h.core.InterceptRequest(ctx, request, RequestContext{Path: path})
		if err != nil {
			return nil, err
		}
		if intercepted != nil {
			return h.core.Respond(ctx, intercepted, ResponseContext{Request: request})
		}
	}
	response, err := h.route(ctx, request)
	if err != nil {
		return nil, err
	}
	return h.core.Respond(ctx, response, ResponseContext{Request: request})
}

// route turns every failure into a response, so a caller never sees a panic or a
// leaked internal message.
func (h *Handler) route(ctx context.Context, request *HandlerRequest) (*HandlerResponse, error) {
	response, err := h.dispatch(ctx, request)
	if err == nil {
		return response, nil
	}
	var rejected *handlerError
	if errors.As(err, &rejected) {
		return h.errorResponse(rejected.status, rejected.message), nil
	}
	var baseErr *BaseError
	if errors.As(err, &baseErr) && baseErr.Code == "NOT_SUPPORTED" {
		return h.errorResponse(http.StatusBadRequest, baseErr.Message), nil
	}
	const fallback = "Internal server error"
	if h.options.ExposeErrors {
		return h.errorResponse(http.StatusInternalServerError, ErrorMessage(err, fallback)), nil
	}
	return h.errorResponse(http.StatusInternalServerError, fallback), nil
}

func (h *Handler) dispatch(ctx context.Context, request *HandlerRequest) (*HandlerResponse, error) {
	path, ok := h.getPath(request.URL)
	if !ok {
		return h.errorResponse(http.StatusNotFound, "Route not found"), nil
	}
	if path == "/health" || path == "/ok" {
		return h.healthCheck(), nil
	}
	if endpoint, exists := h.endpoints[path]; exists {
		return h.handleEndpoint(ctx, endpoint, request)
	}
	route, err := h.parseRoute(path)
	if err != nil {
		return nil, err
	}
	if route == nil {
		return h.errorResponse(http.StatusNotFound, "Route not found"), nil
	}

	action := HandlerAction(route.action)
	if !allHandlerActionSet[action] {
		return h.errorResponse(http.StatusNotFound, fmt.Sprintf("Action '%s' not found", route.action)), nil
	}
	if !h.allowed[action] {
		return h.errorResponse(http.StatusNotFound, fmt.Sprintf("Action '%s' is not enabled", route.action)), nil
	}
	if !h.core.IsProviderEnabled(route.provider) {
		return h.errorResponse(http.StatusBadRequest,
			fmt.Sprintf("Provider '%s' is not enabled or configured", route.provider)), nil
	}
	if !callbackHandlerActionSet[action] && isFormRequest(request) {
		// A cross-site HTML form can send these without a CORS preflight (CSRF).
		return h.errorResponse(http.StatusUnsupportedMediaType, "Content-Type must be application/json"), nil
	}

	parsed, err := parseRequestBody(request)
	if err != nil {
		return nil, err
	}
	hctx := HandlerContext{
		Provider: route.provider,
		Action:   action,
		Params:   route.params,
		Request:  request,
		Body:     asRecord(parsed),
	}
	if !h.isAuthorized(ctx, hctx) {
		return h.errorResponse(http.StatusForbidden, "Forbidden"), nil
	}
	if h.options.TransformRequest != nil && !callbackHandlerActionSet[action] {
		transformed, err := h.options.TransformRequest(ctx, hctx)
		if err != nil {
			return nil, err
		}
		hctx.Body = transformed
	}

	idempotencyKey := requestHeader(request, "idempotency-key")
	if h.idempotency != nil && idempotencyKey != "" && idempotentKeyActionSet[action] {
		return h.withIdempotencyKey(ctx, hctx, idempotencyKey)
	}
	return h.runAction(ctx, hctx)
}

// isAuthorized fails closed: a hook that could not decide rejects the request.
func (h *Handler) isAuthorized(ctx context.Context, hctx HandlerContext) bool {
	if h.options.Authorize == nil {
		return true
	}
	allowed, err := h.options.Authorize(ctx, hctx)
	return err == nil && allowed
}

func (h *Handler) handleEndpoint(ctx context.Context, endpoint RegisteredEndpoint, request *HandlerRequest) (*HandlerResponse, error) {
	if strings.ToUpper(request.Method) != endpoint.Method {
		return h.errorResponse(http.StatusMethodNotAllowed, "Method not allowed"), nil
	}
	if endpoint.Method == http.MethodPost && !endpoint.AcceptForm && isFormRequest(request) {
		return h.errorResponse(http.StatusUnsupportedMediaType, "Content-Type must be application/json"), nil
	}
	parsed, err := parseRequestBody(request)
	if err != nil {
		return nil, err
	}
	body := asRecord(parsed)
	hctx := HandlerContext{
		Action:  PluginEndpointAction(endpoint.PluginID, endpoint.Name),
		Params:  map[string]string{},
		Request: request,
		Body:    body,
	}
	if !h.isAuthorized(ctx, hctx) {
		return h.errorResponse(http.StatusForbidden, "Forbidden"), nil
	}
	ectx := &endpointContext{
		PluginContext: h.core.PluginContext(),
		request:       request,
		body:          body,
		query:         queryOf(request.URL),
		info:          h.info,
		responses:     make(map[*HandlerResponse]struct{}),
	}
	result, err := endpoint.Handler(ctx, ectx)
	if err != nil {
		return nil, err
	}
	if response, ok := result.(*HandlerResponse); ok && ectx.built(response) {
		return response, nil
	}
	return h.jsonResponse(http.StatusOK, result), nil
}

// runAction runs an action. When an event listener fails after the provider
// returned a result, the result is still returned: the payment happened, and
// answering with an error could make the client pay twice. Callbacks are
// different: they answer 500, so the provider re-sends them and the listeners run
// again.
func (h *Handler) runAction(ctx context.Context, hctx HandlerContext) (*HandlerResponse, error) {
	response, err := h.handleAction(ctx, hctx)
	var listenerErr *EventListenerError
	if errors.As(err, &listenerErr) && !callbackHandlerActionSet[hctx.Action] {
		if logger := h.core.PluginContext().Logger(); logger != nil {
			logger.Error("Payment event listener failed", err, map[string]any{
				"provider": hctx.Provider,
				"action":   string(hctx.Action),
			})
		}
		return h.resultResponse(listenerErr.Result), nil
	}
	return response, err
}

func (h *Handler) handleAction(ctx context.Context, hctx HandlerContext) (*HandlerResponse, error) {
	providerID, err := providerOf(hctx)
	if err != nil {
		return nil, err
	}
	provider, err := h.core.Use(providerID)
	if err != nil {
		return nil, err
	}

	switch hctx.Action {
	case ActionPayment:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request PaymentRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.CreatePayment(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionPaymentInit3DS:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request ThreeDSPaymentRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.InitThreeDSPayment(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionPaymentToken:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		tokenProvider, err := capability[TokenPaymentProvider](h, hctx, "This provider does not support token payments")
		if err != nil {
			return nil, err
		}
		body, err := requireBody(hctx)
		if err != nil {
			return nil, err
		}
		result, err := tokenProvider.CreatePaymentWithToken(ctx, body)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionPaymentComplete3DS, ActionCallback:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		return h.handleCallback(ctx, hctx)

	case ActionPaymentGet:
		if err := requireMethod(hctx, http.MethodGet); err != nil {
			return nil, err
		}
		result, err := provider.GetPayment(ctx, hctx.Params["paymentId"])
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionRefund:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request RefundRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.Refund(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCancel:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request CancelRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.Cancel(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionAuthorize:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request PaymentRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.Authorize(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionAuthorizeInit3DS:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request ThreeDSPaymentRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.InitThreeDSAuthorize(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCapture:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request CaptureRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.Capture(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionVoid:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request VoidAuthorizationRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.VoidAuthorization(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCardsSave:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request SaveCardRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.SaveCard(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCardsList:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request ListCardsRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.ListCards(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCardsDelete:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request DeleteCardRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.DeleteCard(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionInstallment:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		var request InstallmentInfoRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := provider.InstallmentInfo(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionBinCheck:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		body, err := requireBody(hctx)
		if err != nil {
			return nil, err
		}
		binNumber, _ := body["binNumber"].(string)
		if !binNumberRe.MatchString(binNumber) {
			return nil, &handlerError{status: http.StatusBadRequest, message: "binNumber must be 6-8 digits"}
		}
		result, err := provider.BinCheck(ctx, binNumber)
		if err != nil {
			return h.errorResponse(http.StatusUnprocessableEntity, ErrorMessage(err, "BIN check failed")), nil
		}
		return h.jsonResponse(http.StatusOK, h.publicResult(result)), nil

	case ActionCheckoutInit:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		checkout, err := capability[CheckoutFormProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request CheckoutFormRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := checkout.InitCheckoutForm(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionCheckoutRetrieve:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		checkout, err := capability[CheckoutFormProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		token, conversationID, err := tokenBody(hctx)
		if err != nil {
			return nil, err
		}
		result, err := checkout.RetrieveCheckoutForm(ctx, token, conversationID)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionPWIInit:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		pwi, err := capability[PWIProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request PWIPaymentRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := pwi.InitPWIPayment(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionPWIRetrieve:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		pwi, err := capability[PWIProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		token, conversationID, err := tokenBody(hctx)
		if err != nil {
			return nil, err
		}
		result, err := pwi.RetrievePWIPayment(ctx, token, conversationID)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionInitialize:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionInitializeRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.InitializeSubscription(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionCancel:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionCancelRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.CancelSubscription(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionUpgrade:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionUpgradeRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.UpgradeSubscription(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionRetrieve:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionRetrieveRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.RetrieveSubscription(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionCardUpdate:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionCardUpdateRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.UpdateSubscriptionCard(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionProduct:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request SubscriptionProductCreateRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.CreateSubscriptionProduct(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	case ActionSubscriptionPricingPlan:
		if err := requireMethod(hctx, http.MethodPost); err != nil {
			return nil, err
		}
		subscriptions, err := capability[SubscriptionProvider](h, hctx, iyzicoOnlyMessage)
		if err != nil {
			return nil, err
		}
		var request PricingPlanCreateRequest
		if err := decodeBody(hctx, &request); err != nil {
			return nil, err
		}
		result, err := subscriptions.CreatePricingPlan(ctx, &request)
		if err != nil {
			return nil, err
		}
		return h.resultResponse(result), nil

	default:
		return h.errorResponse(http.StatusNotFound, fmt.Sprintf("Action '%s' not found", hctx.Action)), nil
	}
}

const iyzicoOnlyMessage = "Route only available for iyzico provider"

// capability resolves a provider-specific method set on the raw provider: these
// methods are not operations, so they run without hooks.
func capability[T any](h *Handler, hctx HandlerContext, message string) (T, error) {
	var missing T
	providerID, err := providerOf(hctx)
	if err != nil {
		return missing, err
	}
	raw, err := h.core.RawProvider(providerID)
	if err != nil {
		return missing, err
	}
	supported, ok := raw.(T)
	if !ok {
		return missing, &handlerError{status: http.StatusBadRequest, message: message}
	}
	return supported, nil
}

// handleCallback serves provider callbacks. A server-to-server notification that
// expects plain text must be acknowledged with it, otherwise the provider keeps
// re-sending it. Browser callbacks (3D return) can be redirected.
func (h *Handler) handleCallback(ctx context.Context, hctx CallbackContext) (*HandlerResponse, error) {
	body, err := requireBody(hctx)
	if err != nil {
		return nil, err
	}
	// Events are emitted in finishCallback, next to OnCallback, so that a failing
	// listener is retried with the stored result instead of calling the provider again.
	complete := func() (*PaymentResponse, error) {
		emit := false
		value, err := h.core.Run(ctx, OperationCompleteThreeDSPayment, hctx.Provider, body, RunOptions{
			Routable: false,
			Emit:     &emit,
		})
		if err != nil {
			return nil, err
		}
		return asPaymentResponse(value)
	}

	idem := h.idempotency
	if idem == nil {
		result, err := complete()
		if err != nil {
			return nil, err
		}
		return h.finishCallback(ctx, hctx, result)
	}

	// A repeated delivery has the same body. Keying on the body hash means a forged
	// callback can never match a stored one.
	fingerprint, err := Fingerprint(body)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("bp:callback:%s:%s:%s", hctx.Provider, hctx.Action, fingerprint)

	var result *PaymentResponse
	processing, err := marshalJSON(callbackRecord{State: "processing"})
	if err != nil {
		return nil, err
	}
	stored, err := idem.store.SetIfAbsent(ctx, key, string(processing), idem.lockSeconds)
	if err != nil {
		return nil, err
	}
	if !stored {
		raw, err := idem.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		record := parseCallbackRecord(raw)
		if record != nil && record.State == "done" && record.Response != nil {
			return record.Response, nil
		}
		if record == nil || record.State != "verified" {
			// Another delivery is being processed; the provider will retry later.
			return h.errorResponse(http.StatusConflict, "Callback is already being processed"), nil
		}
		// The provider result is known but OnCallback failed earlier: run it again
		// without calling the provider a second time.
		result = record.Result
		locked, err := marshalJSON(callbackRecord{State: "processing", Result: result})
		if err != nil {
			return nil, err
		}
		if err := idem.store.Set(ctx, key, string(locked), idem.lockSeconds); err != nil {
			return nil, err
		}
	}

	response, err := func() (*HandlerResponse, error) {
		if result == nil {
			completed, err := complete()
			if err != nil {
				return nil, err
			}
			result = completed
			if result.ErrorCode == "INVALID_HASH" || result.ErrorCode == NetworkErrorCode {
				// Nothing trustworthy to remember: forged, or outcome unknown.
				_ = idem.store.Delete(ctx, key)
				return h.finishCallback(ctx, hctx, result)
			}
			verified, err := marshalJSON(callbackRecord{State: "verified", Result: result})
			if err != nil {
				return nil, err
			}
			if err := idem.store.Set(ctx, key, string(verified), idem.ttlSeconds); err != nil {
				return nil, err
			}
		}

		response, err := h.finishCallback(ctx, hctx, result)
		if err != nil {
			return nil, err
		}
		done, err := marshalJSON(callbackRecord{State: "done", Response: response})
		if err != nil {
			return nil, err
		}
		if err := idem.store.Set(ctx, key, string(done), idem.ttlSeconds); err != nil {
			return nil, err
		}
		return response, nil
	}()
	if err != nil {
		// Keep a known result so the next delivery finishes without the provider.
		if result != nil {
			verified, marshalErr := marshalJSON(callbackRecord{State: "verified", Result: result})
			if marshalErr == nil {
				_ = idem.store.Set(ctx, key, string(verified), idem.ttlSeconds)
			}
		} else {
			_ = idem.store.Delete(ctx, key)
		}
		return nil, err
	}
	return response, nil
}

// finishCallback runs OnCallback and builds the response for a callback result.
func (h *Handler) finishCallback(ctx context.Context, hctx CallbackContext, result *PaymentResponse) (*HandlerResponse, error) {
	invalidSignature := result != nil && result.ErrorCode == "INVALID_HASH"
	if !invalidSignature {
		if err := h.core.Emit(ctx, OperationCompleteThreeDSPayment, hctx.Provider, hctx.Body, result); err != nil {
			return nil, err
		}
		if h.options.OnCallback != nil {
			if err := h.options.OnCallback(ctx, result, hctx); err != nil {
				return nil, err
			}
		}
	}

	if hctx.Action == ActionCallback {
		if ack, ok := h.callbackAcknowledgement(hctx.Provider); ok {
			if invalidSignature {
				return textResponse(http.StatusBadRequest, ack.Failure), nil
			}
			return textResponse(http.StatusOK, ack.Success), nil
		}
	}

	if h.options.CallbackRedirect != nil {
		location, err := h.options.CallbackRedirect(ctx, result, hctx)
		if err != nil {
			return nil, err
		}
		if location != "" {
			return &HandlerResponse{
				Status:  http.StatusSeeOther,
				Headers: map[string]string{"Location": location},
				Body:    "",
			}, nil
		}
	}
	return h.resultResponse(result), nil
}

func (h *Handler) callbackAcknowledgement(providerID string) (CallbackAcknowledgement, bool) {
	raw, err := h.core.RawProvider(providerID)
	if err != nil {
		return CallbackAcknowledgement{}, false
	}
	provider, ok := raw.(PlainTextCallbackProvider)
	if !ok {
		return CallbackAcknowledgement{}, false
	}
	return provider.CallbackAcknowledgement(), true
}

// withIdempotencyKey serves an Idempotency-Key request: the first request with a
// key runs, repeats with the same body get the stored response, repeats with a
// different body are rejected.
func (h *Handler) withIdempotencyKey(ctx context.Context, hctx HandlerContext, idempotencyKey string) (*HandlerResponse, error) {
	idem := h.idempotency
	if len(idempotencyKey) > 255 {
		return nil, &handlerError{status: http.StatusBadRequest, message: "Idempotency-Key must be at most 255 characters"}
	}
	key := fmt.Sprintf("bp:request:%s:%s:%s", hctx.Provider, hctx.Action, idempotencyKey)
	if idem.scope != nil {
		owner, err := idem.scope(ctx, hctx)
		if err != nil {
			return nil, err
		}
		// A scoped key is JSON, so neither its parts nor an unscoped key can imitate it.
		scoped, err := marshalJSON([]string{hctx.Provider, string(hctx.Action), owner, idempotencyKey})
		if err != nil {
			return nil, err
		}
		key = "bp:request:" + string(scoped)
	}
	request, err := Fingerprint(hctx.Body)
	if err != nil {
		return nil, err
	}

	processing, err := marshalJSON(requestRecord{State: "processing", Request: request})
	if err != nil {
		return nil, err
	}
	stored, err := idem.store.SetIfAbsent(ctx, key, string(processing), idem.lockSeconds)
	if err != nil {
		return nil, err
	}
	if !stored {
		raw, err := idem.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		record := parseRequestRecord(raw)
		if record != nil && record.Request != request {
			return h.errorResponse(http.StatusUnprocessableEntity,
				"Idempotency-Key was already used with a different request"), nil
		}
		if record != nil && record.State == "done" && record.Response != nil {
			replayed := *record.Response
			replayed.Headers = make(map[string]string, len(record.Response.Headers)+1)
			for name, value := range record.Response.Headers {
				replayed.Headers[name] = value
			}
			replayed.Headers["Idempotent-Replayed"] = "true"
			return &replayed, nil
		}
		return h.errorResponse(http.StatusConflict, "A request with this Idempotency-Key is in progress"), nil
	}

	response, err := h.runAction(ctx, hctx)
	if err != nil {
		// Rejected before a result (validation, unexpected error): the key can be reused.
		_ = idem.store.Delete(ctx, key)
		return nil, err
	}
	done, err := marshalJSON(requestRecord{State: "done", Request: request, Response: response})
	if err != nil {
		return nil, err
	}
	if err := idem.store.Set(ctx, key, string(done), idem.ttlSeconds); err != nil {
		return nil, err
	}
	return response, nil
}

// getPath returns the path relative to basePath, stripping trailing slashes. ok
// is false when the URL is invalid or outside the handler's mount point.
func (h *Handler) getPath(rawURL string) (string, bool) {
	parsed, err := resolveURL(rawURL)
	if err != nil {
		return "", false
	}
	pathname := strings.TrimRight(parsed.EscapedPath(), "/")
	if pathname == h.basePath {
		return "", true
	}
	if !strings.HasPrefix(pathname, h.basePath+"/") {
		return "", false
	}
	return pathname[len(h.basePath):], true
}

// parseRoute parses "/:provider/:action..." (a path relative to basePath).
func (h *Handler) parseRoute(path string) (*routeContext, error) {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return nil, err
		}
		segments = append(segments, decoded)
	}
	if len(segments) < 2 {
		return nil, nil
	}

	provider := segments[0]
	builtIn := false
	for _, providerType := range AllProviderTypes {
		if string(providerType) == provider {
			builtIn = true
			break
		}
	}
	if !builtIn && !h.core.IsProviderEnabled(provider) {
		return nil, nil
	}

	rest := segments[1:]
	params := map[string]string{}
	// GET /payment/:id
	if len(rest) == 2 && rest[0] == "payment" &&
		rest[1] != "init-3ds" && rest[1] != "token" && rest[1] != "complete-3ds" {
		params["paymentId"] = rest[1]
		return &routeContext{provider: provider, action: string(ActionPaymentGet), params: params}, nil
	}
	return &routeContext{provider: provider, action: strings.Join(rest, "/"), params: params}, nil
}

// resultResponse maps a provider result to an HTTP response: success/pending ->
// 200, failure -> 422 (the body carries the details).
func (h *Handler) resultResponse(result any) *HandlerResponse {
	status := http.StatusOK
	if record, ok := jsonRecordOf(result); ok && record["status"] == string(StatusFailure) {
		status = http.StatusUnprocessableEntity
	}
	return h.jsonResponse(status, h.publicResult(result))
}

// publicResult leaves out rawResponse unless ExposeRawResponse is set.
func (h *Handler) publicResult(result any) any {
	if h.options.ExposeRawResponse {
		return result
	}
	record, ok := jsonRecordOf(result)
	if !ok {
		return result
	}
	if _, has := record["rawResponse"]; !has {
		return result
	}
	delete(record, "rawResponse")
	return record
}

func (h *Handler) healthCheck() *HandlerResponse {
	return h.jsonResponse(http.StatusOK, map[string]any{
		"status":    "ok",
		"service":   "go-pos",
		"version":   Version,
		"timestamp": timestamp(),
	})
}

func (h *Handler) jsonResponse(status int, body any) *HandlerResponse {
	return newJSONResponse(status, body)
}

func (h *Handler) errorResponse(status int, message string) *HandlerResponse {
	return h.jsonResponse(status, map[string]any{
		"error":     true,
		"message":   message,
		"timestamp": timestamp(),
	})
}

func newJSONResponse(status int, body any) *HandlerResponse {
	return &HandlerResponse{
		Status:  status,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}
}

func textResponse(status int, body string) *HandlerResponse {
	return &HandlerResponse{
		Status:  status,
		Headers: map[string]string{"Content-Type": "text/plain"},
		Body:    body,
	}
}

// timestamp renders the current time the way an ISO 8601 UTC string with
// millisecond precision is expected by clients.
func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// trimSlashes removes leading and trailing slashes without a backtracking regex.
func trimSlashes(path string) string {
	start, end := 0, len(path)
	for start < end && path[start] == '/' {
		start++
	}
	for end > start && path[end-1] == '/' {
		end--
	}
	return path[start:end]
}

// resolveURL resolves a request URL against a local base, so relative paths work.
func resolveURL(rawURL string) (*url.URL, error) {
	base, err := url.Parse("http://localhost")
	if err != nil {
		return nil, err
	}
	return base.Parse(rawURL)
}

func queryOf(rawURL string) url.Values {
	parsed, err := resolveURL(rawURL)
	if err != nil {
		return url.Values{}
	}
	return parsed.Query()
}

func requestHeader(request *HandlerRequest, name string) string {
	for key, value := range request.Headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// isFormRequest reports the content types a browser form can send cross-site
// without a CORS preflight. Only provider callbacks, and plugin endpoints with
// AcceptForm, are posted this way.
func isFormRequest(request *HandlerRequest) bool {
	contentType := strings.ToLower(requestHeader(request, "content-type"))
	return strings.Contains(contentType, "application/x-www-form-urlencoded") ||
		strings.Contains(contentType, "multipart/form-data") ||
		strings.Contains(contentType, "text/plain")
}

func parseRequestBody(request *HandlerRequest) (any, error) {
	body, isText := request.Body.(string)
	if !isText {
		return request.Body, nil
	}
	contentType := strings.ToLower(requestHeader(request, "content-type"))
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		// Malformed pairs are skipped, and the last value of a repeated key wins.
		values, _ := url.ParseQuery(body)
		record := make(map[string]any, len(values))
		for key, list := range values {
			if len(list) > 0 {
				record[key] = list[len(list)-1]
			}
		}
		return record, nil
	}
	if strings.Contains(contentType, "application/json") {
		var parsed any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			return nil, &handlerError{status: http.StatusBadRequest, message: "Invalid JSON body"}
		}
		return parsed, nil
	}
	return request.Body, nil
}

func asRecord(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return record
}

func requireMethod(hctx HandlerContext, method string) error {
	if strings.ToUpper(hctx.Request.Method) != method {
		return &handlerError{status: http.StatusMethodNotAllowed, message: "Method not allowed"}
	}
	return nil
}

func requireBody(hctx HandlerContext) (map[string]any, error) {
	if hctx.Body == nil {
		return nil, &handlerError{status: http.StatusBadRequest, message: "Request body is required"}
	}
	return hctx.Body, nil
}

// decodeBody fills a typed request from the parsed body. The provider validates
// the fields it needs; a body that does not fit the request shape is rejected.
func decodeBody(hctx HandlerContext, out any) error {
	if _, err := requireBody(hctx); err != nil {
		return err
	}
	data, err := json.Marshal(hctx.Body)
	if err != nil {
		return &handlerError{status: http.StatusBadRequest, message: "Invalid request body"}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &handlerError{status: http.StatusBadRequest, message: "Invalid request body"}
	}
	return nil
}

func tokenBody(hctx HandlerContext) (token, conversationID string, err error) {
	body, err := requireBody(hctx)
	if err != nil {
		return "", "", err
	}
	token, _ = body["token"].(string)
	conversationID, _ = body["conversationId"].(string)
	return token, conversationID, nil
}

func providerOf(hctx HandlerContext) (string, error) {
	if hctx.Provider == "" {
		return "", &handlerError{status: http.StatusNotFound, message: "Route not found"}
	}
	return hctx.Provider, nil
}

func asPaymentResponse(value any) (*PaymentResponse, error) {
	if result, ok := value.(*PaymentResponse); ok {
		return result, nil
	}
	// A before hook may short-circuit with a plain object.
	if value != nil {
		data, err := json.Marshal(value)
		if err == nil {
			var result PaymentResponse
			if err := json.Unmarshal(data, &result); err == nil {
				return &result, nil
			}
		}
	}
	return nil, NewError("The 3D Secure completion did not return a payment result", "PROVIDER_ERROR")
}

// jsonRecordOf converts a result to a JSON object, keeping number literals intact.
func jsonRecordOf(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var record map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil || record == nil {
		return nil, false
	}
	return record, true
}

func parseCallbackRecord(value string) *callbackRecord {
	if value == "" {
		return nil
	}
	var record callbackRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return nil
	}
	return &record
}

func parseRequestRecord(value string) *requestRecord {
	if value == "" {
		return nil
	}
	var record requestRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return nil
	}
	return &record
}

// endpointContext adapts a plugin endpoint call to EndpointContext. It remembers
// the responses built by JSON and HTML so the handler can tell them apart from a
// plain value returned by the endpoint.
type endpointContext struct {
	PluginContext
	request *HandlerRequest
	body    map[string]any
	query   url.Values
	info    EndpointHandlerInfo

	mu        sync.Mutex
	responses map[*HandlerResponse]struct{}
}

func (c *endpointContext) Request() *HandlerRequest     { return c.request }
func (c *endpointContext) Body() map[string]any         { return c.body }
func (c *endpointContext) Query() url.Values            { return c.query }
func (c *endpointContext) Handler() EndpointHandlerInfo { return c.info }

func (c *endpointContext) JSON(body any, status ...int) *HandlerResponse {
	response := newJSONResponse(optionalStatus(status), body)
	c.remember(response)
	return response
}

func (c *endpointContext) HTML(body string, status ...int) *HandlerResponse {
	response := &HandlerResponse{
		Status:  optionalStatus(status),
		Headers: map[string]string{"Content-Type": "text/html; charset=utf-8"},
		Body:    body,
	}
	c.remember(response)
	return response
}

func (c *endpointContext) remember(response *HandlerResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses[response] = struct{}{}
}

func (c *endpointContext) built(response *HandlerResponse) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.responses[response]
	return ok
}

func optionalStatus(status []int) int {
	if len(status) > 0 && status[0] != 0 {
		return status[0]
	}
	return http.StatusOK
}

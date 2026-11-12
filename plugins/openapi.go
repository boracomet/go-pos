package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// ScalarAPIReferenceVersion is the pinned Scalar reference UI version, so the
// served page only changes when this library is updated.
const ScalarAPIReferenceVersion = "1.75.0"

const openAPIDefaultScalarScriptURL = "https://cdn.jsdelivr.net/npm/@scalar/api-reference@" + ScalarAPIReferenceVersion

const (
	openAPIDefaultTitle          = "Go POS API"
	openAPIDefaultDescription    = "OpenAPI 3.1 reference for Go POS HTTP handler routes and plugin endpoints."
	openAPIDefaultReferenceTitle = "Go POS API Reference"
)

// OpenAPIInfoOptions overrides the document information metadata.
type OpenAPIInfoOptions struct {
	Title       string
	Version     string
	Description string
}

// OpenAPIOptions configures the OpenAPI reference plugin.
type OpenAPIOptions struct {
	// Path serves the reference page, and Path+"/openapi.json" the document. Default: "/reference".
	Path string
	// Info overrides the document title, version and description.
	Info *OpenAPIInfoOptions
	// Disabled turns the reference routes off (404). Nil means off in production, on in sandbox.
	Disabled *bool
	// Privileged serves the routes through the handler's authorize hook, which then becomes required.
	Privileged bool
	// ScalarScriptURL overrides the reference UI script, e.g. with a self-hosted copy.
	ScalarScriptURL string
}

// OpenAPIInfo is the document information block.
type OpenAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// OpenAPIServer is one server URL the document is served under.
type OpenAPIServer struct {
	URL string `json:"url"`
}

// OpenAPITag groups operations in the reference page.
type OpenAPITag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// OpenAPIComponents holds the reusable document parts.
type OpenAPIComponents struct {
	SecuritySchemes map[string]any `json:"securitySchemes"`
}

// OpenAPISpec is the generated OpenAPI 3.1.0 document.
type OpenAPISpec struct {
	OpenAPI    string                    `json:"openapi"`
	Info       OpenAPIInfo               `json:"info"`
	Servers    []OpenAPIServer           `json:"servers"`
	Paths      map[string]map[string]any `json:"paths"`
	Components OpenAPIComponents         `json:"components"`
	Tags       []OpenAPITag              `json:"tags,omitempty"`
}

func openAPITrimSlashes(path string) string {
	start, end := 0, len(path)
	for start < end && path[start] == '/' {
		start++
	}
	for end > start && path[end-1] == '/' {
		end--
	}
	return path[start:end]
}

// openAPIRouteActions is the handler action behind each documented core route.
var openAPIRouteActions = []struct {
	path   string
	action core.HandlerAction
}{
	{"/{provider}/payment", core.ActionPayment},
	{"/{provider}/payment/init-3ds", core.ActionPaymentInit3DS},
	{"/{provider}/payment/complete-3ds", core.ActionPaymentComplete3DS},
	{"/{provider}/payment/{paymentId}", core.ActionPaymentGet},
	{"/{provider}/callback", core.ActionCallback},
	{"/{provider}/refund", core.ActionRefund},
	{"/{provider}/cancel", core.ActionCancel},
	{"/{provider}/authorize", core.ActionAuthorize},
	{"/{provider}/capture", core.ActionCapture},
	{"/{provider}/void", core.ActionVoid},
	{"/{provider}/installment", core.ActionInstallment},
	{"/{provider}/bin-check", core.ActionBinCheck},
	{"/{provider}/cards/save", core.ActionCardsSave},
	{"/{provider}/cards/list", core.ActionCardsList},
	{"/{provider}/cards/delete", core.ActionCardsDelete},
}

func openAPIJSONSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object"}
	if len(required) > 0 {
		schema["required"] = required
	}
	if properties != nil {
		schema["properties"] = properties
	}
	return schema
}

func openAPIRequestBody(schema map[string]any) map[string]any {
	return map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{"schema": schema},
		},
	}
}

func openAPIResponses(entries map[string]string) map[string]any {
	out := make(map[string]any, len(entries))
	for status, description := range entries {
		out[status] = map[string]any{"description": description}
	}
	return out
}

// BuildOpenAPISpec builds the OpenAPI 3.1.0 document from the configured
// providers, the core routes the handler serves and the registered plugin
// endpoints. A nil ctx documents no providers or plugin endpoints; a nil handler
// assumes the handler defaults (basePath "/api/pay", core.DefaultHandlerActions).
func BuildOpenAPISpec(options OpenAPIOptions, ctx core.PluginContext, handler *core.EndpointHandlerInfo) OpenAPISpec {
	if handler == nil {
		handler = &core.EndpointHandlerInfo{BasePath: "/api/pay", AllowedActions: core.DefaultHandlerActions}
	}

	providerSchema := map[string]any{"type": "string"}
	if ctx != nil {
		if ids := ctx.ProviderIDs(); len(ids) > 0 {
			providerSchema["enum"] = ids
		}
	}
	providerParam := map[string]any{
		"name":        "provider",
		"in":          "path",
		"required":    true,
		"description": "The payment provider id (e.g. iyzico, paytr, akbank, sipay)",
		"schema":      providerSchema,
	}
	paymentIDParam := map[string]any{
		"name":        "paymentId",
		"in":          "path",
		"required":    true,
		"schema":      map[string]any{"type": "string"},
		"description": "The payment ID returned by the provider",
	}
	bearerAuth := []any{map[string]any{"bearerAuth": []any{}}}

	paths := map[string]map[string]any{
		"/{provider}/payment": {"post": map[string]any{
			"summary":     "Create payment",
			"description": "Initiates and charges a direct (non-3D) payment through the selected provider.",
			"tags":        []string{"Payments"},
			"parameters":  []any{providerParam},
			"requestBody": openAPIRequestBody(openAPIJSONSchema(map[string]any{
				"price":          map[string]any{"type": "string", "example": "100.00"},
				"paidPrice":      map[string]any{"type": "string", "example": "100.00"},
				"currency":       map[string]any{"type": "string", "default": "TRY", "example": "TRY"},
				"conversationId": map[string]any{"type": "string", "example": "order-12345"},
				"paymentCard": map[string]any{"type": "object", "properties": map[string]any{
					"cardHolderName": map[string]any{"type": "string"},
					"cardNumber":     map[string]any{"type": "string"},
					"expireMonth":    map[string]any{"type": "string"},
					"expireYear":     map[string]any{"type": "string"},
					"cvc":            map[string]any{"type": "string"},
				}},
			}, []string{"price", "paidPrice"})),
			"responses": openAPIResponses(map[string]string{
				"200": "Payment response",
				"400": "Bad request or validation error",
			}),
		}},
		"/{provider}/payment/init-3ds": {"post": map[string]any{
			"summary":     "Initiate 3D Secure payment",
			"description": "Initiates a 3D Secure payment session and returns HTML or redirect URL for bank challenge.",
			"tags":        []string{"3D Secure"},
			"parameters":  []any{providerParam},
			"requestBody": openAPIRequestBody(openAPIJSONSchema(map[string]any{
				"price":       map[string]any{"type": "string", "example": "100.00"},
				"paidPrice":   map[string]any{"type": "string", "example": "100.00"},
				"currency":    map[string]any{"type": "string", "default": "TRY"},
				"callbackUrl": map[string]any{"type": "string", "format": "uri"},
			}, []string{"price", "paidPrice", "callbackUrl"})),
			"responses": openAPIResponses(map[string]string{"200": "3D Secure session initialized"}),
		}},
		"/{provider}/payment/complete-3ds": {"post": map[string]any{
			"summary":     "Complete 3D Secure payment",
			"description": "Completes a 3D Secure payment using callback data sent by the bank.",
			"tags":        []string{"3D Secure"},
			"parameters":  []any{providerParam},
			"requestBody": openAPIRequestBody(map[string]any{"type": "object"}),
			"responses":   openAPIResponses(map[string]string{"200": "3D Secure payment completed"}),
		}},
		"/{provider}/payment/{paymentId}": {"get": map[string]any{
			"summary":     "Get payment status",
			"description": "Retrieves the details and outcome of a previously processed payment.",
			"tags":        []string{"Payments"},
			"parameters":  []any{providerParam, paymentIDParam},
			"responses": openAPIResponses(map[string]string{
				"200": "Payment details",
				"404": "Payment not found",
			}),
		}},
		"/{provider}/callback": {"post": map[string]any{
			"summary":     "3D Secure callback",
			"description": "Receives the bank authorization callback payload for 3D Secure authentication.",
			"tags":        []string{"3D Secure"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Callback received and processed"}),
		}},
		"/{provider}/refund": {"post": map[string]any{
			"summary":     "Refund payment",
			"description": "Refunds a captured transaction in whole or in part. Requires authorization.",
			"tags":        []string{"Refunds"},
			"security":    bearerAuth,
			"parameters":  []any{providerParam},
			"requestBody": openAPIRequestBody(openAPIJSONSchema(map[string]any{
				"paymentId": map[string]any{"type": "string"},
				"price":     map[string]any{"type": "string", "example": "50.00"},
				"currency":  map[string]any{"type": "string", "default": "TRY"},
			}, []string{"paymentId", "price"})),
			"responses": openAPIResponses(map[string]string{
				"200": "Refund outcome",
				"401": "Unauthorized",
			}),
		}},
		"/{provider}/cancel": {"post": map[string]any{
			"summary":     "Cancel payment",
			"description": "Voids an un-settled transaction on the same day. Requires authorization.",
			"tags":        []string{"Refunds"},
			"security":    bearerAuth,
			"parameters":  []any{providerParam},
			"requestBody": openAPIRequestBody(openAPIJSONSchema(map[string]any{
				"paymentId": map[string]any{"type": "string"},
			}, []string{"paymentId"})),
			"responses": openAPIResponses(map[string]string{"200": "Cancellation outcome"}),
		}},
		"/{provider}/authorize": {"post": map[string]any{
			"summary":     "Pre-authorize payment",
			"description": "Blocks the requested amount on customer card without charging.",
			"tags":        []string{"Pre-authorization"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Pre-authorization outcome"}),
		}},
		"/{provider}/capture": {"post": map[string]any{
			"summary":     "Capture pre-authorization",
			"description": "Charges previously blocked funds. Requires authorization.",
			"tags":        []string{"Pre-authorization"},
			"security":    bearerAuth,
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Capture outcome"}),
		}},
		"/{provider}/void": {"post": map[string]any{
			"summary":     "Void pre-authorization",
			"description": "Releases blocked funds back to the cardholder without charging.",
			"tags":        []string{"Pre-authorization"},
			"security":    bearerAuth,
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Void outcome"}),
		}},
		"/{provider}/installment": {"post": map[string]any{
			"summary":     "Get installment options",
			"description": "Queries available bank installment tiers and commission rates for card BIN.",
			"tags":        []string{"Installments"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Installment table"}),
		}},
		"/{provider}/bin-check": {"post": map[string]any{
			"summary":     "BIN lookup",
			"description": "Queries card issuer bank, family, card type and brand from 6-8 digit BIN.",
			"tags":        []string{"Cards"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Card BIN information"}),
		}},
		"/{provider}/cards/save": {"post": map[string]any{
			"summary":     "Save card",
			"description": "Stores a credit card token safely on the provider vault.",
			"tags":        []string{"Cards"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Card stored"}),
		}},
		"/{provider}/cards/list": {"post": map[string]any{
			"summary":     "List saved cards",
			"description": "Lists stored cards for a given user token.",
			"tags":        []string{"Cards"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "List of stored cards"}),
		}},
		"/{provider}/cards/delete": {"post": map[string]any{
			"summary":     "Delete saved card",
			"description": "Removes a stored card from the provider vault.",
			"tags":        []string{"Cards"},
			"parameters":  []any{providerParam},
			"responses":   openAPIResponses(map[string]string{"200": "Card deleted"}),
		}},
		"/health": {"get": map[string]any{
			"summary":     "Health check",
			"description": "Returns health status of the payment handler service.",
			"tags":        []string{"System"},
			"responses":   openAPIResponses(map[string]string{"200": "Service is healthy"}),
		}},
	}

	allowed := map[core.HandlerAction]bool{}
	for _, action := range handler.AllowedActions {
		allowed[action] = true
	}
	privileged := map[core.HandlerAction]bool{}
	for _, action := range core.PrivilegedHandlerActions {
		privileged[action] = true
	}
	// Only the core routes the handler serves; the others answer 404.
	for _, route := range openAPIRouteActions {
		if !allowed[route.action] {
			delete(paths, route.path)
			continue
		}
		if privileged[route.action] {
			for _, operation := range paths[route.path] {
				if document, ok := operation.(map[string]any); ok {
					document["security"] = bearerAuth
				}
			}
		}
	}

	if ctx != nil {
		for _, endpoint := range ctx.Endpoints() {
			// The reference routes do not document themselves.
			if endpoint.PluginID == PluginIDOpenAPI {
				continue
			}
			pathKey := endpoint.Path
			if !strings.HasPrefix(pathKey, "/") {
				pathKey = "/" + pathKey
			}
			methodKey := strings.ToLower(endpoint.Method)
			if methodKey != "get" && methodKey != "post" {
				continue
			}
			if _, exists := paths[pathKey]; !exists {
				paths[pathKey] = map[string]any{}
			}

			summary := endpoint.Name + " (" + endpoint.PluginID + ")"
			description := "Plugin endpoint provided by " + endpoint.PluginID
			tags := []string{"Plugins", endpoint.PluginID}
			var parameters []any
			var requestBody any
			responses := map[string]any{"200": map[string]any{"description": "Successful response"}}
			if meta := endpoint.OpenAPI; meta != nil {
				if meta.Summary != "" {
					summary = meta.Summary
				}
				if meta.Description != "" {
					description = meta.Description
				}
				if meta.Tags != nil {
					tags = meta.Tags
				}
				parameters = meta.Parameters
				requestBody = meta.RequestBody
				if meta.Responses != nil {
					responses = meta.Responses
				}
			}

			document := map[string]any{
				"summary":     summary,
				"description": description,
				"tags":        tags,
				"responses":   responses,
			}
			if endpoint.Privileged {
				document["security"] = bearerAuth
			}
			if parameters != nil {
				document["parameters"] = parameters
			}
			if requestBody != nil {
				document["requestBody"] = requestBody
			}
			paths[pathKey][methodKey] = document
		}
	}

	title, version, description := openAPIDefaultTitle, "1.0.0", openAPIDefaultDescription
	if options.Info != nil {
		if options.Info.Title != "" {
			title = options.Info.Title
		}
		if options.Info.Version != "" {
			version = options.Info.Version
		}
		if options.Info.Description != "" {
			description = options.Info.Description
		}
	}

	return OpenAPISpec{
		OpenAPI: "3.1.0",
		Info:    OpenAPIInfo{Title: title, Version: version, Description: description},
		Servers: []OpenAPIServer{{URL: handler.BasePath}},
		Paths:   paths,
		Components: OpenAPIComponents{SecuritySchemes: map[string]any{
			"bearerAuth": map[string]any{
				"type":        "http",
				"scheme":      "bearer",
				"description": "Authorization header for privileged handler endpoints (Bearer token)",
			},
		}},
		Tags: []OpenAPITag{
			{Name: "Payments", Description: "Direct payment processing operations"},
			{Name: "3D Secure", Description: "3D Secure 1.0 & 2.0 authentication flows"},
			{Name: "Refunds", Description: "Refunds and cancellation operations"},
			{Name: "Pre-authorization", Description: "Authorization, capture and void workflows"},
			{Name: "Installments", Description: "Installment queries and bank campaigns"},
			{Name: "Cards", Description: "Stored cards and BIN checks"},
			{Name: "System", Description: "System health checks"},
			{Name: "Plugins", Description: "Endpoints contributed by official and custom plugins"},
		},
	}
}

var openAPIHTMLEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

// openAPIEscapeHTML escapes a value for HTML text or a double-quoted attribute.
func openAPIEscapeHTML(value string) string { return openAPIHTMLEscaper.Replace(value) }

// openAPISpecJSON renders the document without HTML escaping, then escapes "<" as
// a JSON string escape so no value can close the script element it sits in.
func openAPISpecJSON(spec OpenAPISpec) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(spec); err != nil {
		return "", err
	}
	return strings.ReplaceAll(strings.TrimSuffix(buffer.String(), "\n"), "<", "\\u003c"), nil
}

func openAPIRenderScalarHTML(spec OpenAPISpec, title, scriptURL string) (string, error) {
	specJSON, err := openAPISpecJSON(spec)
	if err != nil {
		return "", err
	}
	// The document is the element's text, not an attribute: summaries and
	// descriptions are free text and an apostrophe would end a quoted attribute.
	return `<!doctype html>
<html>
  <head>
    <title>` + openAPIEscapeHTML(title) + `</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style>
      body { margin: 0; }
    </style>
  </head>
  <body>
    <script id="api-reference" type="application/json" data-configuration='{"theme":"purple"}'>` + specJSON + `</script>
    <script src="` + openAPIEscapeHTML(scriptURL) + `"></script>
  </body>
</html>`, nil
}

// OpenAPI builds the plugin that serves an OpenAPI 3.1.0 document and an
// interactive Scalar reference page for the handler routes, the configured
// providers and the registered plugin endpoints.
func OpenAPI(options OpenAPIOptions) (core.Plugin, error) {
	rawPath := options.Path
	if rawPath == "" {
		rawPath = "/reference"
	}
	basePath := "/" + openAPITrimSlashes(rawPath)
	jsonPath := basePath + "/openapi.json"
	scriptURL := options.ScalarScriptURL
	if scriptURL == "" {
		scriptURL = openAPIDefaultScalarScriptURL
	}

	isDisabled := func(ectx core.EndpointContext) bool {
		if options.Disabled != nil {
			return *options.Disabled
		}
		return ectx.Mode() == core.ModeProduction
	}
	notFound := func(ectx core.EndpointContext) *core.HandlerResponse {
		return ectx.JSON(map[string]any{"status": "error", "errorMessage": "OpenAPI reference is disabled"}, 404)
	}
	referenceTitle := openAPIDefaultReferenceTitle
	if options.Info != nil && options.Info.Title != "" {
		referenceTitle = options.Info.Title
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDOpenAPI,
		Options: options,
		Endpoints: map[string]core.PaymentEndpoint{
			"scalarUi": {
				Method:     "GET",
				Path:       basePath,
				Privileged: options.Privileged,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					if isDisabled(ectx) {
						return notFound(ectx), nil
					}
					handler := ectx.Handler()
					spec := BuildOpenAPISpec(options, ectx, &handler)
					page, err := openAPIRenderScalarHTML(spec, referenceTitle, scriptURL)
					if err != nil {
						return nil, err
					}
					return ectx.HTML(page), nil
				},
			},
			"openApiJson": {
				Method:     "GET",
				Path:       jsonPath,
				Privileged: options.Privileged,
				Handler: func(_ context.Context, ectx core.EndpointContext) (any, error) {
					if isDisabled(ectx) {
						return notFound(ectx), nil
					}
					handler := ectx.Handler()
					return ectx.JSON(BuildOpenAPISpec(options, ectx, &handler)), nil
				},
			},
		},
	}), nil
}

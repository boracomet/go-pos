// Package testing provides an in-memory payment provider, test fixtures and an
// httptest server for application tests: no network access to a provider, no
// sandbox credentials.
package testing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"

	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/client"
	"github.com/boracomet/go-pos/core"
)

// ServerOptions configures NewServer.
type ServerOptions struct {
	// Mock is the in-memory provider served as "mock". Default: NewMockProvider().
	// It takes precedence over Providers["mock"].
	Mock *MockProvider
	// Providers are additional providers by id. They are reached only through
	// core.Provider, so any implementation can be served.
	Providers map[string]core.Provider
	// Handler configures the payment handler. By default every action is allowed
	// and authorized, so a test can reach every route; set AllowedActions to test
	// the production defaults (privileged actions then need an Authorize hook).
	Handler core.HandlerOptions
}

// Server is an in-process HTTP server that serves the payment handler over real
// HTTP, backed by in-memory providers. Close it when the test ends.
type Server struct {
	*httptest.Server
	// Mock is the provider served as "mock".
	Mock *MockProvider
	// Handler serves the payment routes; use it to test without a socket.
	Handler *core.Handler
	// BasePath is the path prefix the handler is mounted on.
	BasePath string
	// Payments is a client pointed at this server, for end-to-end tests.
	Payments *client.Client

	registry *serverRegistry
}

// NewServer starts a test server. The returned error is a configuration error of
// the handler, for example privileged actions enabled without an Authorize hook.
func NewServer(options ...ServerOptions) (*Server, error) {
	opts := ServerOptions{}
	if len(options) > 0 {
		opts = options[0]
	}

	registry := newServerRegistry()
	for id, provider := range opts.Providers {
		if provider == nil {
			return nil, core.NewConfigurationError(fmt.Sprintf("Provider '%s' is nil", id), id)
		}
		registry.register(id, provider)
	}
	mock := opts.Mock
	if mock == nil {
		mock = NewMockProvider()
	}
	registry.register(string(core.ProviderMock), mock)

	handlerOptions := opts.Handler
	if handlerOptions.AllowedActions == nil {
		handlerOptions.AllowedActions = core.AllHandlerActions
		if handlerOptions.Authorize == nil {
			handlerOptions.Authorize = func(context.Context, core.HandlerContext) (bool, error) {
				return true, nil
			}
		}
	}
	handler, err := core.NewHandler(registry, handlerOptions)
	if err != nil {
		return nil, err
	}
	basePath := handlerOptions.BasePath
	if basePath == "" {
		basePath = adapters.DefaultBasePath
	}

	httpServer := httptest.NewServer(adapters.HTTPHandler(handler))
	return &Server{
		Server:   httpServer,
		Mock:     mock,
		Handler:  handler,
		BasePath: basePath,
		Payments: client.New(client.Config{BaseURL: httpServer.URL + basePath}),
		registry: registry,
	}, nil
}

// Provider returns a registered provider by id.
func (s *Server) Provider(providerID string) (core.Provider, error) {
	return s.registry.Use(providerID)
}

// serverRegistry is the minimal facade the handler needs: providers, no plugins
// and no event listeners. Operations run directly on the provider.
type serverRegistry struct {
	mu        sync.RWMutex
	providers map[string]core.Provider
	ids       []string
}

func newServerRegistry() *serverRegistry {
	return &serverRegistry{providers: make(map[string]core.Provider)}
}

func (r *serverRegistry) register(id string, provider core.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[id]; !exists {
		r.ids = append(r.ids, id)
	}
	r.providers[id] = provider
}

func (r *serverRegistry) IsProviderEnabled(providerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.providers[providerID]
	return ok
}

func (r *serverRegistry) Use(providerID string) (core.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

func (r *serverRegistry) RawProvider(providerID string) (core.Provider, error) {
	return r.Use(providerID)
}

func (r *serverRegistry) Run(ctx context.Context, operation core.PaymentOperation, providerID string, request any, _ core.RunOptions) (any, error) {
	provider, err := r.Use(providerID)
	if err != nil {
		return nil, err
	}
	switch operation {
	case core.OperationCreatePayment:
		return runWith(ctx, request, provider.CreatePayment)
	case core.OperationInitThreeDSPayment:
		return runWith(ctx, request, provider.InitThreeDSPayment)
	case core.OperationCompleteThreeDSPayment:
		return provider.CompleteThreeDSPayment(ctx, request)
	case core.OperationAuthorize:
		return runWith(ctx, request, provider.Authorize)
	case core.OperationInitThreeDSAuthorize:
		return runWith(ctx, request, provider.InitThreeDSAuthorize)
	case core.OperationCapture:
		return runWith(ctx, request, provider.Capture)
	case core.OperationVoidAuthorization:
		return runWith(ctx, request, provider.VoidAuthorization)
	case core.OperationRefund:
		return runWith(ctx, request, provider.Refund)
	case core.OperationCancel:
		return runWith(ctx, request, provider.Cancel)
	case core.OperationGetPayment:
		return provider.GetPayment(ctx, stringRequest(request))
	case core.OperationSaveCard:
		return runWith(ctx, request, provider.SaveCard)
	case core.OperationListCards:
		return runWith(ctx, request, provider.ListCards)
	case core.OperationDeleteCard:
		return runWith(ctx, request, provider.DeleteCard)
	case core.OperationBinCheck:
		return provider.BinCheck(ctx, stringRequest(request))
	case core.OperationInstallmentInfo:
		return runWith(ctx, request, provider.InstallmentInfo)
	default:
		return nil, core.NewError(fmt.Sprintf("Unknown operation %s", operation), "INVALID_REQUEST")
	}
}

func (r *serverRegistry) Emit(context.Context, core.PaymentOperation, string, any, any) error {
	return nil
}

func (r *serverRegistry) InterceptRequest(context.Context, *core.HandlerRequest, core.RequestContext) (*core.HandlerResponse, error) {
	return nil, nil
}

func (r *serverRegistry) Respond(_ context.Context, response *core.HandlerResponse, _ core.ResponseContext) (*core.HandlerResponse, error) {
	return response, nil
}

func (r *serverRegistry) PluginContext() core.PluginContext {
	return serverPluginContext{registry: r}
}

func (r *serverRegistry) Endpoints() []core.RegisteredEndpoint { return nil }

// runWith decodes an operation request and calls the provider method.
func runWith[Request any, Response any](
	ctx context.Context,
	request any,
	call func(context.Context, *Request) (Response, error),
) (any, error) {
	typed, err := requestAs[Request](request)
	if err != nil {
		return nil, err
	}
	return call(ctx, typed)
}

// requestAs accepts the typed request the handler passes, or a parsed body.
func requestAs[T any](request any) (*T, error) {
	if typed, ok := request.(*T); ok {
		return typed, nil
	}
	if typed, ok := request.(T); ok {
		return &typed, nil
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func stringRequest(request any) string {
	if value, ok := request.(string); ok {
		return value
	}
	return ""
}

// serverPluginContext is the plugin context of a test server: no plugins are
// registered, so it only reports the providers.
type serverPluginContext struct {
	registry *serverRegistry
}

func (c serverPluginContext) ProviderIDs() []string {
	c.registry.mu.RLock()
	defer c.registry.mu.RUnlock()
	return append([]string(nil), c.registry.ids...)
}

func (c serverPluginContext) ErrorCodes() map[string]string { return map[string]string{} }

func (c serverPluginContext) DefaultProvider() string {
	c.registry.mu.RLock()
	defer c.registry.mu.RUnlock()
	if _, ok := c.registry.providers[string(core.ProviderMock)]; ok {
		return string(core.ProviderMock)
	}
	if len(c.registry.ids) > 0 {
		return c.registry.ids[0]
	}
	return ""
}

func (c serverPluginContext) Mode() core.Mode { return core.ModeSandbox }

func (c serverPluginContext) Logger() core.Logger { return core.NopLogger{} }

func (c serverPluginContext) Use(providerID string) (core.Provider, error) {
	return c.registry.Use(providerID)
}

func (c serverPluginContext) Emit(context.Context, *core.PaymentEvent) error { return nil }

func (c serverPluginContext) Endpoints() []core.EndpointInfo { return nil }

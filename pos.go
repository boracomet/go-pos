// Package pos is the entry point of the payment library: one Payment object
// configured with the providers to use, the unified payment API, plugins,
// payment events and an HTTP handler.
//
//	payment, err := pos.New(pos.Config{
//		Providers: map[string]core.ProviderEntry{
//			"iyzico": pos.IyzicoConfig{APIKey: apiKey, SecretKey: secretKey},
//			"paytr":  pos.PayTRConfig{MerchantID: id, MerchantKey: key, MerchantSalt: salt},
//		},
//		DefaultProvider: "iyzico",
//		Mode:            core.ModeSandbox,
//	})
//
//	result, err := payment.CreatePayment(ctx, request)   // default provider
//	result, err := payment.Use("paytr")                  // a specific provider
//	http.Handle("/api/pay/", adapters.HTTPHandler(payment))
package pos

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/boracomet/go-pos/core"
)

// Payment is the payment object returned by New: the operations with the default
// provider, the providers by id, the plugins' members, the event listeners and
// the HTTP handler. It is safe for concurrent use.
type Payment struct {
	providers       map[string]core.Provider
	proxies         map[string]core.Provider
	ids             []string
	defaultProvider string
	mode            core.Mode
	logger          core.Logger

	plugins       []core.Plugin
	beforeHooks   []core.BeforeHook
	afterHooks    []core.AfterHook
	requestHooks  []requestHook
	responseHooks []responseHook
	endpoints     []core.RegisteredEndpoint
	methods       map[string]any
	errorCodes    map[string]string
	pluginCtx     core.PluginContext
	handler       *core.Handler

	initMu      sync.Mutex
	initialized int

	listenerMu sync.RWMutex
	listeners  map[core.PaymentEventName][]listenerEntry
	nextToken  uint64
}

var _ core.HandlerCore = (*Payment)(nil)

type requestHook struct {
	pluginID string
	hook     func(context.Context, *core.HandlerRequest, core.RequestContext) (*core.HandlerResponse, error)
}

type responseHook struct {
	pluginID string
	hook     func(context.Context, *core.HandlerResponse, core.ResponseContext) (*core.HandlerResponse, error)
}

type listenerEntry struct {
	token    uint64
	listener core.PaymentEventListener
}

// New creates the payment object. It instantiates only the providers listed in
// cfg.Providers, in id order, and returns a ConfigurationError for an invalid
// provider configuration, a duplicate plugin id, a name that clashes with a
// member of the payment object, or an invalid handler configuration.
func New(cfg Config) (*Payment, error) {
	mode := resolvedMode(cfg.Mode)
	p := &Payment{
		providers:  make(map[string]core.Provider, len(cfg.Providers)),
		proxies:    make(map[string]core.Provider, len(cfg.Providers)),
		mode:       mode,
		logger:     cfg.Logger,
		plugins:    append([]core.Plugin(nil), cfg.Plugins...),
		listeners:  make(map[core.PaymentEventName][]listenerEntry),
		methods:    make(map[string]any),
		errorCodes: make(map[string]string),
	}

	ids := make([]string, 0, len(cfg.Providers))
	for id := range cfg.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := reserve(id, fmt.Sprintf("Provider id '%s'", id)); err != nil {
			return nil, err
		}
		setup := Setup{
			ProviderSetupContext: core.ProviderSetupContext{
				ID:       id,
				Mode:     mode,
				Logger:   cfg.Logger,
				Retry:    cfg.Retry,
				Doer:     cfg.Doer,
				Validate: cfg.Validate,
			},
			Locale: cfg.Locale,
		}
		provider, err := createProvider(id, cfg.Providers[id], setup)
		if err != nil {
			return nil, err
		}
		p.providers[id] = provider
		p.proxies[id] = &hookedProvider{payment: p, id: id, raw: provider}
		p.ids = append(p.ids, id)
	}

	if cfg.DefaultProvider != "" {
		if _, ok := p.providers[cfg.DefaultProvider]; !ok {
			return nil, core.NewConfigurationError(fmt.Sprintf(
				"Default provider '%s' is not enabled or configured", cfg.DefaultProvider), cfg.DefaultProvider)
		}
		p.defaultProvider = cfg.DefaultProvider
	} else if len(p.ids) == 1 {
		p.defaultProvider = p.ids[0]
	}

	p.pluginCtx = pluginContext{payment: p}
	if err := p.registerPlugins(); err != nil {
		return nil, err
	}

	handlerOptions := core.HandlerOptions{}
	if cfg.Handler != nil {
		handlerOptions = *cfg.Handler
	}
	handler, err := core.NewHandler(p, handlerOptions)
	if err != nil {
		return nil, err
	}
	p.handler = handler
	return p, nil
}

// createProvider resolves one entry of Config.Providers: a ready instance wins
// over a definition, a built-in definition is created through the registry, and
// any other value is the configuration of the provider registered under id.
func createProvider(id string, entry core.ProviderEntry, setup Setup) (core.Provider, error) {
	if isNilEntry(entry) {
		return nil, core.NewConfigurationError(fmt.Sprintf(
			"providers.%s must be a provider, e.g. Iyzico(IyzicoConfig{...})", id), id)
	}
	switch value := entry.(type) {
	case core.Provider:
		return value, nil
	case providerDefinition:
		return NewProvider(value.id, value.config, setup)
	case core.ProviderDefinition:
		return value.Create(setup.ProviderSetupContext)
	default:
		return NewProvider(id, entry, setup)
	}
}

// isNilEntry reports an absent entry, including a typed nil pointer.
func isNilEntry(entry core.ProviderEntry) bool {
	if entry == nil {
		return true
	}
	value := reflect.ValueOf(entry)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return value.IsNil()
	default:
		return false
	}
}

// registerPlugins collects the hooks, listeners, endpoints, error codes and
// members of every plugin, in registration order.
func (p *Payment) registerPlugins() error {
	seen := make(map[string]bool, len(p.plugins))
	for _, plugin := range p.plugins {
		if plugin.ID == "" {
			return core.NewConfigurationError("Every plugin needs an id", "")
		}
		if seen[plugin.ID] {
			return core.NewConfigurationError(fmt.Sprintf("Plugin '%s' is registered twice", plugin.ID), "")
		}
		seen[plugin.ID] = true

		p.beforeHooks = append(p.beforeHooks, plugin.Hooks.Before...)
		p.afterHooks = append(p.afterHooks, plugin.Hooks.After...)
		if plugin.OnRequest != nil {
			p.requestHooks = append(p.requestHooks, requestHook{pluginID: plugin.ID, hook: plugin.OnRequest})
		}
		if plugin.OnResponse != nil {
			p.responseHooks = append(p.responseHooks, responseHook{pluginID: plugin.ID, hook: plugin.OnResponse})
		}
		for _, name := range sortedEventNames(plugin.Events) {
			if listener := plugin.Events[name]; listener != nil {
				p.addListener(name, listener)
			}
		}
		for _, name := range sortedEndpointNames(plugin.Endpoints) {
			p.endpoints = append(p.endpoints, core.RegisteredEndpoint{
				PaymentEndpoint: plugin.Endpoints[name],
				PluginID:        plugin.ID,
				Name:            name,
			})
		}
		for code, message := range plugin.ErrorCodes {
			p.errorCodes[code] = message
		}
		if plugin.Methods == nil {
			continue
		}
		// Methods run at construction time, as the plugins' Init does on the first
		// operation; there is no caller context yet.
		methods, err := plugin.Methods(context.Background(), p.pluginCtx)
		if err != nil {
			return err
		}
		for _, name := range sortedMethodNames(methods) {
			if err := reserve(name, fmt.Sprintf("'%s' added by plugin '%s'", name, plugin.ID)); err != nil {
				return err
			}
			if _, exists := p.methods[name]; exists {
				return core.NewConfigurationError(fmt.Sprintf(
					"'%s' is added by two plugins", name), "")
			}
			p.methods[name] = methods[name]
		}
	}
	return nil
}

func sortedEventNames(events core.PaymentEventListeners) []core.PaymentEventName {
	names := make([]core.PaymentEventName, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func sortedEndpointNames(endpoints map[string]core.PaymentEndpoint) []string {
	names := make([]string, 0, len(endpoints))
	for name := range endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedMethodNames(methods map[string]any) []string {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Use returns a provider by id whose operations run with the plugin hooks and
// emit events. Provider-specific methods (checkout forms, subscriptions, token
// payments) are reached with RawProvider and the capability accessors.
func (p *Payment) Use(providerID string) (core.Provider, error) {
	provider, ok := p.proxies[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

// RawProvider returns a provider by id without hooks: the concrete provider, so
// its capability interfaces can be asserted.
func (p *Payment) RawProvider(providerID string) (core.Provider, error) {
	if providerID == "" {
		providerID = p.defaultProvider
	}
	if providerID == "" {
		return nil, noDefaultProvider()
	}
	provider, ok := p.providers[providerID]
	if !ok {
		return nil, core.NewProviderNotEnabledError(providerID)
	}
	return provider, nil
}

// IsProviderEnabled reports whether a provider id is configured.
func (p *Payment) IsProviderEnabled(providerID string) bool {
	_, ok := p.providers[providerID]
	return ok
}

// EnabledProviders returns the ids of the configured providers, in id order.
func (p *Payment) EnabledProviders() []string {
	return append([]string(nil), p.ids...)
}

// DefaultProvider returns the id the operations on Payment use, or an empty
// string when there is none.
func (p *Payment) DefaultProvider() string { return p.defaultProvider }

// Mode returns the configured environment.
func (p *Payment) Mode() core.Mode { return p.mode }

// Handler returns the HTTP handler configured with Config.Handler. Serve it with
// adapters.HTTPHandler or adapters.Middleware.
func (p *Payment) Handler() *core.Handler { return p.handler }

// CreateHandler builds an additional handler with its own options, for example a
// public handler and an authenticated admin handler.
func (p *Payment) CreateHandler(_ context.Context, options core.HandlerOptions) (*core.Handler, error) {
	return core.NewHandler(p, options)
}

// PluginContext returns the context handed to plugins.
func (p *Payment) PluginContext() core.PluginContext { return p.pluginCtx }

// Endpoints returns the endpoints registered by plugins, in registration order.
func (p *Payment) Endpoints() []core.RegisteredEndpoint {
	return append([]core.RegisteredEndpoint(nil), p.endpoints...)
}

// Method returns a member a plugin added, for example the routing router under
// "routing" or the commission router under "commissionRouter".
func (p *Payment) Method(name string) (any, bool) {
	value, ok := p.methods[name]
	return value, ok
}

// Methods returns the members the plugins added, by name.
func (p *Payment) Methods() map[string]any {
	methods := make(map[string]any, len(p.methods))
	for name, value := range p.methods {
		methods[name] = value
	}
	return methods
}

// ErrorCodes returns the error codes the plugins declared, with their default
// messages.
func (p *Payment) ErrorCodes() map[string]string {
	codes := make(map[string]string, len(p.errorCodes))
	for code, message := range p.errorCodes {
		codes[code] = message
	}
	return codes
}

// On listens to a payment event (core.EventAll for every event). Listeners run in
// order after the operation; one that fails makes the operation return an
// EventListenerError that still carries the result. The returned function removes
// the listener.
func (p *Payment) On(name core.PaymentEventName, listener core.PaymentEventListener) func() {
	if listener == nil {
		return func() {}
	}
	p.listenerMu.Lock()
	p.nextToken++
	token := p.nextToken
	p.listeners[name] = append(p.listeners[name], listenerEntry{token: token, listener: listener})
	p.listenerMu.Unlock()
	return func() { p.removeListener(name, token) }
}

func (p *Payment) addListener(name core.PaymentEventName, listener core.PaymentEventListener) {
	p.listenerMu.Lock()
	defer p.listenerMu.Unlock()
	p.nextToken++
	p.listeners[name] = append(p.listeners[name], listenerEntry{token: p.nextToken, listener: listener})
}

func (p *Payment) removeListener(name core.PaymentEventName, token uint64) {
	p.listenerMu.Lock()
	defer p.listenerMu.Unlock()
	entries := p.listeners[name]
	kept := make([]listenerEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.token != token {
			kept = append(kept, entry)
		}
	}
	p.listeners[name] = kept
}

// CheckoutFormProvider returns the hosted checkout form methods of a provider
// (iyzico). An empty id uses the default provider.
func (p *Payment) CheckoutFormProvider(providerID string) (core.CheckoutFormProvider, error) {
	return capabilityOf[core.CheckoutFormProvider](p, providerID, "Hosted checkout forms")
}

// PWIProvider returns the Payment With IBAN methods of a provider (iyzico). An
// empty id uses the default provider.
func (p *Payment) PWIProvider(providerID string) (core.PWIProvider, error) {
	return capabilityOf[core.PWIProvider](p, providerID, "Payment With IBAN")
}

// SubscriptionProvider returns the subscription methods of a provider (iyzico).
// An empty id uses the default provider.
func (p *Payment) SubscriptionProvider(providerID string) (core.SubscriptionProvider, error) {
	return capabilityOf[core.SubscriptionProvider](p, providerID, "Subscriptions")
}

// TokenPaymentProvider returns the stored-token payment methods of a provider
// (PayTR). An empty id uses the default provider.
func (p *Payment) TokenPaymentProvider(providerID string) (core.TokenPaymentProvider, error) {
	return capabilityOf[core.TokenPaymentProvider](p, providerID, "Token payments")
}

// PlainTextCallbackProvider returns the plain-text callback acknowledgement of a
// provider (PayTR), whose notification must be answered with text instead of
// JSON. An empty id uses the default provider.
func (p *Payment) PlainTextCallbackProvider(providerID string) (core.PlainTextCallbackProvider, error) {
	return capabilityOf[core.PlainTextCallbackProvider](p, providerID, "Plain-text callback acknowledgement")
}

// capabilityOf resolves a provider-specific method set on the raw provider: these
// methods are not operations, so they run without hooks.
func capabilityOf[T any](p *Payment, providerID, feature string) (T, error) {
	var missing T
	raw, err := p.RawProvider(providerID)
	if err != nil {
		return missing, err
	}
	supported, ok := raw.(T)
	if !ok {
		return missing, core.NotSupported(raw.Name(), feature, "")
	}
	return supported, nil
}

func noDefaultProvider() error {
	return core.NewConfigurationError(
		"No default provider set. Call the operation on a provider (payment.Use(id)) or set DefaultProvider.", "")
}

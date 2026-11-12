// Package routing picks the provider of a routable payment from rules the
// merchant writes, sends follow-up operations back to the provider that handled
// the payment, and explains every decision.
package routing

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/boracomet/go-pos/core"
)

const (
	paymentKey      = "routing:pay:"
	conversationKey = "routing:conv:"
	attemptKey      = "routing:attempt:"
)

type requestIDs struct {
	paymentID      string
	conversationID string
	// attempt is the key a charge is pinned to: the order id, never a card field.
	attempt string
}

func idsOf(request any) requestIDs {
	if raw, ok := request.(string); ok {
		return requestIDs{paymentID: strings.TrimSpace(raw)}
	}
	record := toRecord(request)
	conversationID := text(record["conversationId"])
	attempt := conversationID
	if attempt == "" {
		attempt = text(record["basketId"])
	}
	return requestIDs{
		paymentID:      text(record["paymentId"]),
		conversationID: conversationID,
		attempt:        attempt,
	}
}

type listenerEntry struct {
	token    uint64
	listener DecisionListener
}

// Router is the routing plugin instance. It is safe for concurrent use.
type Router struct {
	options Options
	store   Store

	mu           sync.RWMutex
	listeners    []listenerEntry
	nextToken    uint64
	counts       map[string]int
	engine       *Engine
	pctx         core.PluginContext
	lastDecision *Decision
}

// New validates the options and builds the plugin. The engine is bound to the
// configured providers by Init (or Bind).
func New(options Options) (*Router, error) {
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}
	store := options.Store
	if store == nil {
		store = NewMemoryStore(nil)
	}
	return &Router{options: options, store: store, counts: make(map[string]int)}, nil
}

// Plugin returns the plugin to pass to core.Options.Plugins.
func (r *Router) Plugin() core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID:      "routing",
		Options: r.options,
		Init:    r.Bind,
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				// Only operations that can move: new charges by rule, follow-ups
				// by mapping. Card saves, BIN checks and installment queries stay
				// put.
				Matcher: func(octx core.OperationContext) bool {
					return octx.Routable && (IsChargeOperation(octx.Operation) || IsFollowUpOperation(octx.Operation))
				},
				Handler: r.beforeHook,
			}},
			After: []core.AfterHook{{
				Matcher: func(octx core.OperationResultContext) bool {
					return octx.Routable && IsChargeOperation(octx.Operation)
				},
				Handler: r.afterHook,
			}},
		},
		Methods: func(ctx context.Context, pctx core.PluginContext) (map[string]any, error) {
			if err := r.Bind(ctx, pctx); err != nil {
				return nil, err
			}
			return map[string]any{"routing": r}, nil
		},
	})
}

// Bind attaches the router to a payment object's providers. It is idempotent.
func (r *Router) Bind(_ context.Context, pctx core.PluginContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pctx = pctx
	if r.engine != nil {
		return nil
	}
	engine, err := NewEngine(r.options, EngineContext{
		ProviderIDs:     pctx.ProviderIDs(),
		DefaultProvider: pctx.DefaultProvider(),
		Logger:          pctx.Logger(),
	})
	if err != nil {
		return err
	}
	r.engine = engine
	return nil
}

func (r *Router) runtime() (*Engine, core.PluginContext, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.engine == nil || r.pctx == nil {
		return nil, nil, core.NewConfigurationError("routing: the plugin is not initialized", "")
	}
	return r.engine, r.pctx, nil
}

func (r *Router) logError(message string, cause error) {
	r.mu.RLock()
	pctx := r.pctx
	r.mu.RUnlock()
	if pctx == nil {
		return
	}
	if logger := pctx.Logger(); logger != nil {
		logger.Error(message, cause, nil)
	}
}

func (r *Router) logInfo(message string) {
	r.mu.RLock()
	pctx := r.pctx
	r.mu.RUnlock()
	if pctx == nil {
		return
	}
	if logger := pctx.Logger(); logger != nil {
		logger.Info(message, nil)
	}
}

func (r *Router) read(ctx context.Context, key string) string {
	value, err := r.store.Get(ctx, key)
	if err != nil {
		r.logError("[routing] routing store lookup failed; the payment is treated as unknown", err)
		return ""
	}
	return value
}

func (r *Router) remember(ctx context.Context, key, provider string) {
	if err := r.store.Set(ctx, key, provider, RoutingTTLSeconds); err != nil {
		r.logError("[routing] routing store write failed; a follow-up may not find its provider", err)
	}
}

func (r *Router) publish(ctx context.Context, decision *Decision) {
	r.mu.Lock()
	r.lastDecision = decision
	if decision.Provider != "" && IsChargeOperation(decision.Operation) {
		r.counts[decision.Provider]++
	}
	listeners := make([]listenerEntry, len(r.listeners))
	copy(listeners, r.listeners)
	r.mu.Unlock()

	for _, entry := range listeners {
		if err := callListener(ctx, entry.listener, decision); err != nil {
			// Audit logging must never change where a payment goes.
			r.logError("[routing] an onDecision listener failed", err)
		}
	}
}

func callListener(ctx context.Context, listener DecisionListener, decision *Decision) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = recoveredError(recovered)
		}
	}()
	return listener(ctx, decision)
}

// Decide routes one operation: follow-ups by mapping, new charges by rule.
func (r *Router) Decide(ctx context.Context, operation core.PaymentOperation, request any) (*Decision, error) {
	if IsFollowUpOperation(operation) {
		return r.followUpDecision(ctx, operation, request)
	}
	return r.chargeDecision(ctx, operation, request)
}

// chargeDecision is the rule-based decision for a new charge, pinned by the
// order id when possible.
func (r *Router) chargeDecision(ctx context.Context, operation core.PaymentOperation, request any) (*Decision, error) {
	engine, pctx, err := r.runtime()
	if err != nil {
		return nil, err
	}
	ids := idsOf(request)
	if ids.attempt != "" {
		pinned := r.read(ctx, attemptKey+ids.attempt)
		if pinned != "" {
			if !contains(pctx.ProviderIDs(), pinned) {
				return nil, core.NewConfigurationError(fmt.Sprintf(
					"routing: this order was already routed to '%s', which is not enabled. "+
						"Refusing to move a possible charge to another provider.", pinned), "")
			}
			return &Decision{
				Provider:  pinned,
				Reason:    ReasonPinned,
				Operation: operation,
				Skipped:   []Skip{},
				Detail: fmt.Sprintf(
					"this order was already routed to '%s'; retries never move providers", pinned),
			}, nil
		}
	}
	return engine.Decide(ctx, operation, request)
}

// followUpDecision sends refunds and the like to the provider that took the
// payment.
func (r *Router) followUpDecision(ctx context.Context, operation core.PaymentOperation, request any) (*Decision, error) {
	engine, pctx, err := r.runtime()
	if err != nil {
		return nil, err
	}
	ids := idsOf(request)
	handledBy := ""
	if ids.paymentID != "" {
		handledBy = r.read(ctx, paymentKey+ids.paymentID)
	}
	if handledBy == "" && ids.conversationID != "" {
		handledBy = r.read(ctx, conversationKey+ids.conversationID)
	}
	if handledBy != "" {
		if !contains(pctx.ProviderIDs(), handledBy) {
			return nil, core.NewConfigurationError(fmt.Sprintf(
				"routing: the payment was handled by '%s', which is not enabled. "+
					"Refusing to route the %s to another provider.", handledBy, operation), "")
		}
		return &Decision{
			Provider:  handledBy,
			Reason:    ReasonFollowUp,
			Operation: operation,
			Skipped:   []Skip{},
			Detail:    fmt.Sprintf("the payment was handled by '%s'", handledBy),
		}, nil
	}
	// Unknown payment: use the default, but never invent a provider to make the
	// call succeed. Health only keeps new charges away, so it is not applied to
	// a refund or a status call of a payment whose provider is unknown.
	return engine.Fallback(ctx, operation, request, []Skip{}, ReasonUnknownPayment, false)
}

// Explain decides without any side effect: no pin, no counters, no listeners.
func (r *Router) Explain(ctx context.Context, request any, options *ExplainOptions) (*Decision, error) {
	if _, _, err := r.runtime(); err != nil {
		return nil, err
	}
	operation := core.OperationCreatePayment
	if options != nil && options.Operation != "" {
		operation = options.Operation
	}
	if !IsChargeOperation(operation) && !IsFollowUpOperation(operation) {
		return nil, core.NewConfigurationError(fmt.Sprintf(
			"routing: explain() does not route '%s'. Use a charge or a follow-up operation.", operation), "")
	}
	return r.Decide(ctx, operation, request)
}

// OnDecision registers an audit listener and returns its unsubscribe function.
func (r *Router) OnDecision(_ context.Context, listener DecisionListener) func() {
	r.mu.Lock()
	r.nextToken++
	token := r.nextToken
	r.listeners = append(r.listeners, listenerEntry{token: token, listener: listener})
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for index, entry := range r.listeners {
			if entry.token == token {
				r.listeners = append(r.listeners[:index], r.listeners[index+1:]...)
				return
			}
		}
	}
}

// Decision returns the last published decision, or nil before the first one.
func (r *Router) Decision(_ context.Context) *Decision {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastDecision
}

// Status counts the charges routed per provider in this process.
func (r *Router) Status(_ context.Context) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := make(map[string]int, len(r.counts))
	total := 0
	for provider, value := range r.counts {
		counts[provider] = value
		total += value
	}
	return Status{Counts: counts, Total: total}
}

// beforeHook runs the decision and pins a charge before the provider call: if
// the call fails, the outcome is unknown and a retry of the same order must not
// move. core.BeforeHook carries no context, so hooks run on context.Background().
func (r *Router) beforeHook(octx core.OperationContext) (*core.BeforeHookResult, error) {
	ctx := context.Background()
	decision, err := r.Decide(ctx, octx.Operation, octx.Request)
	if err != nil {
		return nil, err
	}
	if decision.Provider != "" && IsChargeOperation(octx.Operation) {
		if attempt := idsOf(octx.Request).attempt; attempt != "" {
			r.remember(ctx, attemptKey+attempt, decision.Provider)
		}
	}
	r.publish(ctx, decision)
	if decision.Provider == "" {
		// Never let a rejected provider be used through the facade default: fail
		// before the charge instead.
		r.mu.RLock()
		pctx := r.pctx
		r.mu.RUnlock()
		if pctx != nil && pctx.DefaultProvider() != "" {
			reasons := make([]string, 0, len(decision.Skipped))
			for _, skip := range decision.Skipped {
				reasons = append(reasons, fmt.Sprintf("%s (%s)", skip.Provider, skip.Reason))
			}
			message := fmt.Sprintf("routing: no provider is eligible for this %s", octx.Operation)
			if len(reasons) > 0 {
				message += ": " + strings.Join(reasons, ", ")
			}
			return nil, core.NewConfigurationError(message, "")
		}
		return nil, nil
	}
	if decision.Provider == octx.Provider {
		return nil, nil
	}
	r.logInfo(fmt.Sprintf("[routing] %s is routed to '%s' (%s)", octx.Operation, decision.Provider, decision.Reason))
	return &core.BeforeHookResult{Provider: decision.Provider}, nil
}

// afterHook remembers the provider even when the result is a decline or a lost
// response: a refund of a payment id that may exist must reach it.
func (r *Router) afterHook(octx core.OperationResultContext) (*core.AfterHookResult, error) {
	ctx := context.Background()
	provider := octx.Provider
	if provider == "" {
		return nil, nil
	}
	result := toRecord(octx.Result)
	paymentID := text(result["paymentId"])
	conversationID := text(result["conversationId"])
	if conversationID == "" {
		conversationID = idsOf(octx.Request).conversationID
	}
	if paymentID != "" {
		r.remember(ctx, paymentKey+paymentID, provider)
	}
	if conversationID != "" {
		r.remember(ctx, conversationKey+conversationID, provider)
	}
	return nil, nil
}

package plugins

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/boracomet/go-pos/core"
)

// StartedThreeDSFlow is a started 3D Secure payment flow the plugin tracks until
// it completes or is abandoned.
type StartedThreeDSFlow struct {
	// PaymentID is the paymentId returned by the provider init, or the
	// conversationId when the provider returned none.
	PaymentID      string                `json:"paymentId"`
	ConversationID string                `json:"conversationId,omitempty"`
	Provider       string                `json:"provider"`
	Operation      core.PaymentOperation `json:"operation"`
	StartedAt      int64                 `json:"startedAt"`
	Amount         string                `json:"amount,omitempty"`
	Currency       string                `json:"currency,omitempty"`
	Request        any                   `json:"request,omitempty"`
}

// ThreeDSFlowStore is the storage backend tracking active 3DS flows. Get returns
// a nil flow when the paymentId is unknown.
type ThreeDSFlowStore interface {
	Save(flow StartedThreeDSFlow) error
	Get(paymentID string) (*StartedThreeDSFlow, error)
	Delete(paymentID string) error
	List() ([]StartedThreeDSFlow, error)
}

// MemoryThreeDSFlowStore is an in-memory store for a single process.
type MemoryThreeDSFlowStore struct {
	mu    sync.Mutex
	order []string
	flows map[string]StartedThreeDSFlow
}

// NewMemoryThreeDSFlowStore builds an empty in-memory 3DS flow store.
func NewMemoryThreeDSFlowStore() *MemoryThreeDSFlowStore {
	return &MemoryThreeDSFlowStore{flows: map[string]StartedThreeDSFlow{}}
}

// Save stores or updates an active flow, keeping the position of an existing id.
func (s *MemoryThreeDSFlowStore) Save(flow StartedThreeDSFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flows == nil {
		s.flows = map[string]StartedThreeDSFlow{}
	}
	if _, exists := s.flows[flow.PaymentID]; !exists {
		s.order = append(s.order, flow.PaymentID)
	}
	s.flows[flow.PaymentID] = flow
	return nil
}

// Get returns a flow by paymentId, or nil when it is unknown.
func (s *MemoryThreeDSFlowStore) Get(paymentID string) (*StartedThreeDSFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, exists := s.flows[paymentID]
	if !exists {
		return nil, nil
	}
	return &flow, nil
}

// Delete removes a flow. An unknown paymentId is not an error.
func (s *MemoryThreeDSFlowStore) Delete(paymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.flows[paymentID]; !exists {
		return nil
	}
	delete(s.flows, paymentID)
	for i, candidate := range s.order {
		if candidate == paymentID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// List returns every tracked flow, in insertion order.
func (s *MemoryThreeDSFlowStore) List() ([]StartedThreeDSFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StartedThreeDSFlow, 0, len(s.order))
	for _, id := range s.order {
		if flow, exists := s.flows[id]; exists {
			out = append(out, flow)
		}
	}
	return out, nil
}

// ExpireStaleResult counts what one expiry run did.
type ExpireStaleResult struct {
	// Checked is the number of stale flows found.
	Checked int `json:"checked"`
	// Resolved is the number of flows the provider confirmed as successful.
	Resolved int `json:"resolved"`
	// Expired are the stale flows that were expired.
	Expired []StartedThreeDSFlow `json:"expired"`
}

// ThreeDSTimeoutOptions configures the 3D Secure timeout plugin.
type ThreeDSTimeoutOptions struct {
	// After is the duration after which an uncompleted 3D Secure flow is
	// abandoned: milliseconds, or a string with a unit such as "15m", "30m",
	// "1h", "900s". Default: "15m".
	After any
	// Store tracks the active flows. Default: an in-memory store.
	Store ThreeDSFlowStore
	// OnExpire is called when a stale payment is expired, for example to release
	// reserved inventory.
	OnExpire func(flow StartedThreeDSFlow) error
	// CheckProvider queries the provider before marking a flow expired. A flow
	// the provider confirms as successful is cleared without calling OnExpire.
	// Default: true.
	CheckProvider *bool
}

var threeDSTimeoutUnits = map[string]int64{
	"ms": 1,
	"s":  1000,
	"m":  60000,
	"h":  3600000,
	"d":  86400000,
}

var threeDSTimeoutDurationPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)$`)

func threeDSTimeoutParseDuration(after any) (int64, error) {
	switch typed := after.(type) {
	case int:
		return threeDSTimeoutNumberDuration(float64(typed))
	case int64:
		return threeDSTimeoutNumberDuration(float64(typed))
	case float64:
		return threeDSTimeoutNumberDuration(typed)
	case float32:
		return threeDSTimeoutNumberDuration(float64(typed))
	case string:
		match := threeDSTimeoutDurationPattern.FindStringSubmatch(strings.TrimSpace(typed))
		if match == nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("threeDSTimeout: invalid duration '%s'. Use milliseconds or '15m', '30m', '1h'.", typed), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("threeDSTimeout: invalid duration '%s'. Use milliseconds or '15m', '30m', '1h'.", typed), "")
		}
		return int64(number * float64(threeDSTimeoutUnits[match[2]])), nil
	default:
		return 0, core.NewConfigurationError(
			fmt.Sprintf("threeDSTimeout: invalid duration '%v'. Use milliseconds or '15m', '30m', '1h'.", after), "")
	}
}

func threeDSTimeoutNumberDuration(number float64) (int64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
		return 0, core.NewConfigurationError("threeDSTimeout: after must be greater than 0", "")
	}
	return int64(number), nil
}

// threeDSTimeoutPaymentID finds the id a flow is tracked under: the result's
// paymentId, the raw provider response's, then the request's ids.
func threeDSTimeoutPaymentID(result, request any) string {
	if id := pluginString(result, "paymentId"); id != "" {
		return id
	}
	if raw := pluginField(result, "rawResponse"); raw != nil {
		if id := pluginString(raw, "paymentId"); id != "" {
			return id
		}
	}
	if id := pluginString(request, "paymentId"); id != "" {
		return id
	}
	return pluginString(request, "conversationId")
}

// ThreeDSTimeoutMethods is the "threeDS" member the plugin adds to the facade.
type ThreeDSTimeoutMethods struct {
	store         ThreeDSFlowStore
	ttlMs         int64
	checkProvider bool
	onExpire      func(StartedThreeDSFlow) error
	pctx          core.PluginContext
}

// ExpireStale checks every tracked flow and expires those that exceeded the
// timeout. Stale flows are checked against the provider first, so a late success
// is not expired. Now is an epoch millisecond timestamp; omitting it uses the
// current time.
func (m *ThreeDSTimeoutMethods) ExpireStale(ctx context.Context, now ...int64) (ExpireStaleResult, error) {
	timestamp := pluginNowMs()
	if len(now) > 0 {
		timestamp = now[0]
	}
	all, err := m.store.List()
	if err != nil {
		return ExpireStaleResult{}, err
	}
	stale := make([]StartedThreeDSFlow, 0, len(all))
	for _, flow := range all {
		if timestamp-flow.StartedAt >= m.ttlMs {
			stale = append(stale, flow)
		}
	}
	result := ExpireStaleResult{Checked: len(stale), Expired: []StartedThreeDSFlow{}}

	for _, flow := range stale {
		if m.checkProvider {
			// A failing provider lookup must not keep an abandoned flow alive.
			resolved, failed := m.providerOutcome(ctx, flow)
			if resolved || failed {
				if err := m.store.Delete(flow.PaymentID); err != nil {
					return result, err
				}
				if resolved {
					result.Resolved++
				}
				continue
			}
		}
		if err := m.store.Delete(flow.PaymentID); err != nil {
			return result, err
		}
		result.Expired = append(result.Expired, flow)
		if m.onExpire != nil {
			if err := pluginCall(func() error { return m.onExpire(flow) }); err != nil {
				if logger := m.pctx.Logger(); logger != nil {
					logger.Error(fmt.Sprintf("threeDSTimeout onExpire failed for %s", flow.PaymentID), err, nil)
				}
			}
		}
	}
	return result, nil
}

// providerOutcome reports whether the provider already settled the flow. Both
// results are false when the flow is still pending or the lookup failed.
func (m *ThreeDSTimeoutMethods) providerOutcome(ctx context.Context, flow StartedThreeDSFlow) (bool, bool) {
	provider, err := m.pctx.Use(flow.Provider)
	if err != nil {
		return false, false
	}
	status, err := provider.GetPayment(ctx, flow.PaymentID)
	if err != nil || status == nil {
		return false, false
	}
	return status.Status == core.StatusSuccess, status.Status == core.StatusFailure
}

// GetTrackedFlows returns every currently tracked active 3D Secure flow.
func (m *ThreeDSTimeoutMethods) GetTrackedFlows() ([]StartedThreeDSFlow, error) {
	return m.store.List()
}

// Store returns the underlying tracking store.
func (m *ThreeDSTimeoutMethods) Store() ThreeDSFlowStore { return m.store }

// ThreeDSTimeout tracks started 3D Secure flows and expires the abandoned ones
// that never returned. Run ExpireStale from a cron job or a scheduled worker.
func ThreeDSTimeout(options ThreeDSTimeoutOptions) (core.Plugin, error) {
	after := options.After
	if after == nil {
		after = "15m"
	}
	ttlMs, err := threeDSTimeoutParseDuration(after)
	if err != nil {
		return core.Plugin{}, err
	}
	store := options.Store
	if store == nil {
		store = NewMemoryThreeDSFlowStore()
	}
	checkProvider := options.CheckProvider == nil || *options.CheckProvider

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDThreeDSTimeout,
		Options: map[string]any{"after": after, "checkProvider": checkProvider},
		Hooks: core.PluginHooks{
			After: []core.AfterHook{
				{
					Matcher: func(ctx core.OperationResultContext) bool {
						return ctx.Operation == core.OperationInitThreeDSPayment ||
							ctx.Operation == core.OperationInitThreeDSAuthorize
					},
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						if pluginString(ctx.Result, "status") == string(core.StatusFailure) {
							return nil, nil
						}
						paymentID := threeDSTimeoutPaymentID(ctx.Result, ctx.Request)
						if paymentID == "" {
							return nil, nil
						}
						conversationID, ok := pluginStringField(ctx.Request, "conversationId")
						if !ok {
							conversationID = pluginString(ctx.Result, "conversationId")
						}
						amount, ok := pluginStringField(ctx.Request, "paidPrice")
						if !ok {
							amount = pluginString(ctx.Request, "price")
						}
						if err := store.Save(StartedThreeDSFlow{
							PaymentID:      paymentID,
							ConversationID: conversationID,
							Provider:       ctx.Provider,
							Operation:      ctx.Operation,
							StartedAt:      pluginNowMs(),
							Amount:         amount,
							Currency:       pluginString(ctx.Request, "currency"),
							Request:        ctx.Request,
						}); err != nil {
							return nil, err
						}
						return nil, nil
					},
				},
				{
					Matcher: func(ctx core.OperationResultContext) bool {
						return ctx.Operation == core.OperationCompleteThreeDSPayment
					},
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						if paymentID := threeDSTimeoutPaymentID(ctx.Result, ctx.Request); paymentID != "" {
							if err := store.Delete(paymentID); err != nil {
								return nil, err
							}
						}
						conversationID, ok := pluginStringField(ctx.Result, "conversationId")
						if !ok {
							conversationID = pluginString(ctx.Request, "conversationId")
						}
						if conversationID != "" {
							if err := store.Delete(conversationID); err != nil {
								return nil, err
							}
						}
						return nil, nil
					},
				},
			},
		},
		Methods: func(_ context.Context, pctx core.PluginContext) (map[string]any, error) {
			return map[string]any{"threeDS": &ThreeDSTimeoutMethods{
				store:         store,
				ttlMs:         ttlMs,
				checkProvider: checkProvider,
				onExpire:      options.OnExpire,
				pctx:          pctx,
			}}, nil
		},
	}), nil
}

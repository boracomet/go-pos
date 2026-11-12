package plugins

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/boracomet/go-pos/core"
)

// PendingPayment is a payment whose outcome is unknown: the provider call timed
// out, so the resolver queries it until it settles or GiveUpAfter elapses.
// Amounts stay strings end to end.
type PendingPayment struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Amount         string `json:"amount,omitempty"`
	Currency       string `json:"currency,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	OrderID        string `json:"orderId,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	Attempts       int    `json:"attempts"`
	NextAttemptAt  int64  `json:"nextAttemptAt"`
	LastError      string `json:"lastError,omitempty"`
}

// PendingStore is where pending payments are kept between resolver runs. Get
// returns a nil payment when the id is unknown.
type PendingStore interface {
	Save(payment PendingPayment) error
	Get(id string) (*PendingPayment, error)
	List() ([]PendingPayment, error)
	Delete(id string) error
}

// PendingClaimStore is the optional atomic ownership transfer of a PendingStore,
// for stores shared by several resolvers (multiple server instances running the
// same cron). Claim returns true only for the resolver that took the payment;
// resolvers that get false skip it. Resolving without a claim risks emitting two
// terminal events for one payment. Stores without it stay usable: the plugin
// then only guards resolvers in its own process.
type PendingClaimStore interface {
	Claim(id string) (bool, error)
}

// MemoryPendingStore is an in-memory PendingStore. Every read returns a copy, so
// a caller cannot mutate stored state by accident.
type MemoryPendingStore struct {
	mu    sync.Mutex
	order []string
	items map[string]PendingPayment
}

// NewMemoryPendingStore builds an empty in-memory pending store.
func NewMemoryPendingStore() *MemoryPendingStore {
	return &MemoryPendingStore{items: map[string]PendingPayment{}}
}

// Save stores a copy of the payment, keeping the position of an existing id.
func (s *MemoryPendingStore) Save(payment PendingPayment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]PendingPayment{}
	}
	if _, exists := s.items[payment.ID]; !exists {
		s.order = append(s.order, payment.ID)
	}
	s.items[payment.ID] = payment
	return nil
}

// Get returns a copy of the stored payment, or nil when the id is unknown.
func (s *MemoryPendingStore) Get(id string) (*PendingPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.items[id]
	if !exists {
		return nil, nil
	}
	return &item, nil
}

// List returns copies of every stored payment, in insertion order.
func (s *MemoryPendingStore) List() ([]PendingPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingPayment, 0, len(s.order))
	for _, id := range s.order {
		if item, exists := s.items[id]; exists {
			out = append(out, item)
		}
	}
	return out, nil
}

// Delete removes a payment. An unknown id is not an error.
func (s *MemoryPendingStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[id]; !exists {
		return nil
	}
	delete(s.items, id)
	for i, candidate := range s.order {
		if candidate == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// Claim takes ownership of a payment by removing it from the store.
func (s *MemoryPendingStore) Claim(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[id]; !exists {
		return false, nil
	}
	delete(s.items, id)
	for i, candidate := range s.order {
		if candidate == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true, nil
}

// pendingResolverClaimSet is the per-store claim set shared by every plugin
// instance using that store, so two overlapping Resolve calls in one process
// never handle the same payment. Stores are held strongly for the life of the
// process, unlike the weakly keyed original.
type pendingResolverClaimSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (s *pendingResolverClaimSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.ids[id]
	return exists
}

func (s *pendingResolverClaimSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = struct{}{}
}

func (s *pendingResolverClaimSet) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

var pendingResolverLocalClaims sync.Map

func pendingResolverClaimsOf(store PendingStore) *pendingResolverClaimSet {
	fresh := func() *pendingResolverClaimSet {
		return &pendingResolverClaimSet{ids: map[string]struct{}{}}
	}
	if store == nil {
		return fresh()
	}
	storeType := reflect.TypeOf(store)
	if !storeType.Comparable() {
		return fresh()
	}
	if existing, ok := pendingResolverLocalClaims.Load(store); ok {
		return existing.(*pendingResolverClaimSet)
	}
	actual, _ := pendingResolverLocalClaims.LoadOrStore(store, fresh())
	return actual.(*pendingResolverClaimSet)
}

// PendingResolverOptions configures the pending resolver plugin.
type PendingResolverOptions struct {
	// Store is where pending payments are stored. Default: an in-memory store.
	Store PendingStore
	// Schedule is when to query: durations such as "30s", "2m", "10m", "1h", or
	// milliseconds. The interval at index n is used after n attempts, and the
	// last one repeats. Default: ["30s", "2m", "10m", "1h"].
	Schedule []any
	// GiveUpAfter is the maximum time before giving up on an unresolved payment:
	// a duration string or milliseconds. Default: "24h".
	GiveUpAfter any
	// Endpoint registers the HTTP endpoint POST /pending/resolve. Default: false.
	Endpoint bool
}

// ResolveSummary counts what one resolver run did.
type ResolveSummary struct {
	Checked      int `json:"checked"`
	Succeeded    int `json:"succeeded"`
	Failed       int `json:"failed"`
	Unresolved   int `json:"unresolved"`
	StillPending int `json:"stillPending"`
}

var pendingResolverDurationPattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)?$`)

func pendingResolverParseDuration(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return pendingResolverNumberDuration(float64(typed))
	case int64:
		return pendingResolverNumberDuration(float64(typed))
	case float64:
		return pendingResolverNumberDuration(typed)
	case float32:
		return pendingResolverNumberDuration(float64(typed))
	case string:
		match := pendingResolverDurationPattern.FindStringSubmatch(strings.TrimSpace(typed))
		if match == nil {
			return 0, core.NewConfigurationError(fmt.Sprintf("pendingResolver: invalid duration '%s'", typed), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(fmt.Sprintf("pendingResolver: invalid duration '%s'", typed), "")
		}
		switch strings.ToLower(match[2]) {
		case "d":
			return int64(math.Round(number * 86400000)), nil
		case "h":
			return int64(math.Round(number * 3600000)), nil
		case "m":
			return int64(math.Round(number * 60000)), nil
		case "s":
			return int64(math.Round(number * 1000)), nil
		default:
			return int64(math.Round(number)), nil
		}
	default:
		return 0, core.NewConfigurationError(fmt.Sprintf("pendingResolver: invalid duration '%v'", value), "")
	}
}

func pendingResolverNumberDuration(number float64) (int64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
		return 0, core.NewConfigurationError(
			fmt.Sprintf("pendingResolver: duration must be a positive number, got %s", strconv.FormatFloat(number, 'g', -1, 64)), "")
	}
	return int64(number), nil
}

// pendingResolverFieldRef names one field of a request or result.
type pendingResolverFieldRef struct {
	value any
	name  string
}

func pendingResolverRef(value any, name string) pendingResolverFieldRef {
	return pendingResolverFieldRef{value: value, name: name}
}

// pendingResolverCoalesce mirrors a nullish chain: an empty string is a value,
// only an absent or non-string field falls through.
func pendingResolverCoalesce(refs ...pendingResolverFieldRef) string {
	text, _ := pendingResolverCoalesceOk(refs...)
	return text
}

func pendingResolverCoalesceOk(refs ...pendingResolverFieldRef) (string, bool) {
	for _, ref := range refs {
		if text, ok := pluginStringField(ref.value, ref.name); ok {
			return text, true
		}
	}
	return "", false
}

// pendingResolverFirst mirrors a truthy chain: an empty string falls through.
func pendingResolverFirst(refs ...pendingResolverFieldRef) string {
	for _, ref := range refs {
		if text := pluginString(ref.value, ref.name); text != "" {
			return text
		}
	}
	return ""
}

type pendingResolverRuntime struct {
	store         PendingStore
	scheduleMs    []int64
	giveUpAfterMs int64
	claims        *pendingResolverClaimSet

	mu   sync.Mutex
	pctx core.PluginContext
}

func (r *pendingResolverRuntime) setContext(pctx core.PluginContext) {
	r.mu.Lock()
	r.pctx = pctx
	r.mu.Unlock()
}

func (r *pendingResolverRuntime) context() core.PluginContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pctx
}

func (r *pendingResolverRuntime) delayFor(attempts int) int64 {
	if attempts >= 0 && attempts < len(r.scheduleMs) {
		return r.scheduleMs[attempts]
	}
	return r.scheduleMs[len(r.scheduleMs)-1]
}

func (r *pendingResolverRuntime) resolve(ctx context.Context) (ResolveSummary, error) {
	summary := ResolveSummary{}
	all, err := r.store.List()
	if err != nil {
		return summary, err
	}
	now := pluginNowMs()
	for _, item := range all {
		if item.NextAttemptAt > now || r.claims.has(item.ID) {
			continue
		}
		r.claims.add(item.ID)
		err := r.resolveOne(ctx, item, now, &summary)
		r.claims.remove(item.ID)
		if err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func (r *pendingResolverRuntime) resolveOne(ctx context.Context, item PendingPayment, now int64, summary *ResolveSummary) error {
	if claimer, ok := r.store.(PendingClaimStore); ok {
		claimed, err := claimer.Claim(item.ID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
	}
	summary.Checked++

	if now-item.CreatedAt >= r.giveUpAfterMs {
		summary.Unresolved++
		if err := r.store.Delete(item.ID); err != nil {
			return err
		}
		return r.context().Emit(ctx, &core.PaymentEvent{
			Type:           core.EventPaymentUnresolved,
			Provider:       item.Provider,
			Operation:      core.OperationCreatePayment,
			PaymentID:      item.ID,
			ConversationID: item.ConversationID,
			Amount:         item.Amount,
			Currency:       item.Currency,
			Result: map[string]any{
				"status":  "unresolved",
				"message": "Payment resolution timed out after giveUpAfter threshold",
			},
			Request: map[string]any{"paymentId": item.ID, "conversationId": item.ConversationID},
		})
	}

	// A failing delete, emit or save is treated like a failing query: the payment
	// stays pending and is retried on the next schedule interval.
	if err := r.query(ctx, item, now, summary); err != nil {
		summary.StillPending++
		item.Attempts++
		item.LastError = err.Error()
		item.NextAttemptAt = now + r.delayFor(item.Attempts)
		return r.store.Save(item)
	}
	return nil
}

func (r *pendingResolverRuntime) query(ctx context.Context, item PendingPayment, now int64, summary *ResolveSummary) error {
	pctx := r.context()
	provider, err := pctx.Use(item.Provider)
	if err != nil {
		return err
	}
	result, err := provider.GetPayment(ctx, item.ID)
	if err != nil {
		return err
	}
	request := map[string]any{"paymentId": item.ID, "conversationId": item.ConversationID}

	switch result.Status {
	case core.StatusSuccess:
		summary.Succeeded++
		if err := r.store.Delete(item.ID); err != nil {
			return err
		}
		paymentID, ok := pluginStringField(result, "paymentId")
		if !ok {
			paymentID = item.ID
		}
		conversationID, ok := pluginStringField(result, "conversationId")
		if !ok {
			conversationID = item.ConversationID
		}
		amount, ok := pendingResolverCoalesceOk(
			pendingResolverRef(result, "paidPrice"), pendingResolverRef(result, "price"))
		if !ok {
			amount = item.Amount
		}
		currency, ok := pluginStringField(result, "currency")
		if !ok {
			currency = item.Currency
		}
		return pctx.Emit(ctx, &core.PaymentEvent{
			Type:           core.EventPaymentSucceeded,
			Provider:       item.Provider,
			Operation:      core.OperationCreatePayment,
			PaymentID:      paymentID,
			ConversationID: conversationID,
			Amount:         amount,
			Currency:       currency,
			Result:         result,
			Request:        request,
			ResolvedFrom:   core.ResolvedFromPending,
		})
	case core.StatusFailure:
		summary.Failed++
		if err := r.store.Delete(item.ID); err != nil {
			return err
		}
		paymentID, ok := pluginStringField(result, "paymentId")
		if !ok {
			paymentID = item.ID
		}
		conversationID, ok := pluginStringField(result, "conversationId")
		if !ok {
			conversationID = item.ConversationID
		}
		return pctx.Emit(ctx, &core.PaymentEvent{
			Type:           core.EventPaymentFailed,
			Provider:       item.Provider,
			Operation:      core.OperationCreatePayment,
			PaymentID:      paymentID,
			ConversationID: conversationID,
			Amount:         item.Amount,
			Currency:       item.Currency,
			Code:           result.Code,
			Result:         result,
			Request:        request,
			ResolvedFrom:   core.ResolvedFromPending,
		})
	default:
		summary.StillPending++
		item.Attempts++
		item.NextAttemptAt = now + r.delayFor(item.Attempts)
		return r.store.Save(item)
	}
}

// PendingResolverMethods is the "pending" member the plugin adds to the facade.
type PendingResolverMethods struct {
	runtime *pendingResolverRuntime
}

// Resolve queries every due pending payment and emits its final outcome.
func (m *PendingResolverMethods) Resolve(ctx context.Context) (ResolveSummary, error) {
	return m.runtime.resolve(ctx)
}

// List returns every pending payment currently stored.
func (m *PendingResolverMethods) List() ([]PendingPayment, error) { return m.runtime.store.List() }

// Get returns one pending payment by id, or nil when it is unknown.
func (m *PendingResolverMethods) Get(id string) (*PendingPayment, error) {
	return m.runtime.store.Get(id)
}

// Delete manually removes a pending payment from storage.
func (m *PendingResolverMethods) Delete(id string) error { return m.runtime.store.Delete(id) }

// Store returns the storage backend.
func (m *PendingResolverMethods) Store() PendingStore { return m.runtime.store }

// PendingResolver tracks payments that timed out (NETWORK_ERROR with a pending
// status), queries the provider on a configurable schedule, and emits the final
// outcome events. Call Resolve from a cron job or a scheduled function.
func PendingResolver(options PendingResolverOptions) (core.Plugin, error) {
	store := options.Store
	if store == nil {
		store = NewMemoryPendingStore()
	}
	schedule := options.Schedule
	if schedule == nil {
		schedule = []any{"30s", "2m", "10m", "1h"}
	}
	scheduleMs := make([]int64, 0, len(schedule))
	for _, entry := range schedule {
		parsed, err := pendingResolverParseDuration(entry)
		if err != nil {
			return core.Plugin{}, err
		}
		scheduleMs = append(scheduleMs, parsed)
	}
	if len(scheduleMs) == 0 {
		return core.Plugin{}, core.NewConfigurationError("pendingResolver: schedule must contain at least one interval", "")
	}
	giveUpAfter := options.GiveUpAfter
	if giveUpAfter == nil {
		giveUpAfter = "24h"
	}
	giveUpAfterMs, err := pendingResolverParseDuration(giveUpAfter)
	if err != nil {
		return core.Plugin{}, err
	}

	runtime := &pendingResolverRuntime{
		store:         store,
		scheduleMs:    scheduleMs,
		giveUpAfterMs: giveUpAfterMs,
		claims:        pendingResolverClaimsOf(store),
	}
	firstInterval := scheduleMs[0]

	plugin := core.Plugin{
		ID:      PluginIDPendingResolver,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			runtime.setContext(pctx)
			return nil
		},
		Hooks: core.PluginHooks{
			After: []core.AfterHook{
				{
					Matcher: func(ctx core.OperationResultContext) bool {
						return pluginString(ctx.Result, "status") == string(core.StatusPending) &&
							(pluginString(ctx.Result, "code") == string(core.CodeNetworkError) ||
								pluginString(ctx.Result, "errorCode") == string(core.CodeNetworkError))
					},
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						id := pendingResolverFirst(
							pendingResolverRef(ctx.Result, "paymentId"),
							pendingResolverRef(ctx.Request, "conversationId"),
							pendingResolverRef(ctx.Request, "orderId"),
							pendingResolverRef(ctx.Result, "conversationId"),
						)
						if id == "" {
							return nil, nil
						}
						now := pluginNowMs()
						if err := store.Save(PendingPayment{
							ID:       id,
							Provider: ctx.Provider,
							Amount: pendingResolverCoalesce(
								pendingResolverRef(ctx.Request, "paidPrice"),
								pendingResolverRef(ctx.Request, "price"),
								pendingResolverRef(ctx.Request, "amount"),
								pendingResolverRef(ctx.Result, "amount"),
							),
							Currency: pendingResolverCoalesce(
								pendingResolverRef(ctx.Request, "currency"),
								pendingResolverRef(ctx.Result, "currency"),
							),
							ConversationID: pendingResolverCoalesce(
								pendingResolverRef(ctx.Request, "conversationId"),
								pendingResolverRef(ctx.Result, "conversationId"),
							),
							OrderID:       pendingResolverCoalesce(pendingResolverRef(ctx.Request, "orderId")),
							CreatedAt:     now,
							Attempts:      0,
							NextAttemptAt: now + firstInterval,
						}); err != nil {
							return nil, err
						}
						return nil, nil
					},
				},
			},
		},
		Methods: func(_ context.Context, pctx core.PluginContext) (map[string]any, error) {
			runtime.setContext(pctx)
			return map[string]any{"pending": &PendingResolverMethods{runtime: runtime}}, nil
		},
	}

	if options.Endpoint {
		plugin.Endpoints = map[string]core.PaymentEndpoint{
			"resolve": {
				Method:     "POST",
				Path:       "/pending/resolve",
				Privileged: true,
				Handler: func(ctx context.Context, ectx core.EndpointContext) (any, error) {
					runtime.setContext(ectx)
					return runtime.resolve(ctx)
				},
			},
		}
	}
	return core.DefinePlugin(plugin), nil
}

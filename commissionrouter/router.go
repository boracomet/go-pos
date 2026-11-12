package commissionrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// ChargeOperations start a charge and therefore may be routed by cost.
var ChargeOperations = []core.PaymentOperation{
	core.OperationCreatePayment,
	core.OperationAuthorize,
	core.OperationInitThreeDSPayment,
	core.OperationInitThreeDSAuthorize,
}

// BoundOperations must reach the provider that handled the payment.
var BoundOperations = []core.PaymentOperation{
	core.OperationRefund,
	core.OperationCancel,
	core.OperationGetPayment,
	core.OperationCapture,
	core.OperationVoidAuthorization,
	core.OperationCompleteThreeDSPayment,
}

var (
	chargeSet = operationSet(ChargeOperations)
	boundSet  = operationSet(BoundOperations)
	routedSet = operationSet(append(append([]core.PaymentOperation{}, ChargeOperations...), BoundOperations...))
)

func operationSet(operations []core.PaymentOperation) map[core.PaymentOperation]bool {
	set := make(map[core.PaymentOperation]bool, len(operations))
	for _, operation := range operations {
		set[operation] = true
	}
	return set
}

// IsChargeOperation reports whether the operation starts a charge.
func IsChargeOperation(operation core.PaymentOperation) bool { return chargeSet[operation] }

// IsBoundOperation reports whether the operation must stay with its provider.
func IsBoundOperation(operation core.PaymentOperation) bool { return boundSet[operation] }

// IsRoutedOperation reports whether the router handles the operation at all.
func IsRoutedOperation(operation core.PaymentOperation) bool { return routedSet[operation] }

// neverRetry lists the codes that are never retried, whatever FallbackOn says. A
// decline proves the card was not charged, but retrying it at another provider is
// a scheme violation and can look like card testing; NETWORK_ERROR, pending and
// unknown outcomes have an unknown charge state and retrying them is the
// double-charge bug.
var neverRetry = map[string]bool{
	string(core.CodeNetworkError):        true,
	string(core.CodeCardDeclined):        true,
	string(core.CodeInsufficientFunds):   true,
	string(core.CodeInvalidCard):         true,
	string(core.CodeExpiredCard):         true,
	string(core.CodeInvalidCVC):          true,
	string(core.CodeThreeDSFailed):       true,
	string(core.CodeFraudSuspected):      true,
	string(core.CodeLimitExceeded):       true,
	string(core.CodeDuplicateOrder):      true,
	string(core.CodeCancelledByCustomer): true,
	string(core.CodeInvalidHash):         true,
	string(core.CodeUnknown):             true,
}

// defaultFallbackOn lists the errors that prove no charge happened and therefore
// allow one fallback to the next candidate. Merchants can narrow it, never widen
// it to an unknown outcome.
var defaultFallbackOn = []string{string(core.CodeInvalidRequest), string(core.CodeProviderError)}

// maxAttempts is the initial attempt plus two fallbacks: bounded latency and
// blast radius.
const maxAttempts = 3

const (
	defaultCacheEntries  = 1_024
	defaultCacheTTLMS    = 86_400_000
	defaultShareWindow   = 100
	defaultPlanEntries   = 1_024
	maxShareWindowBounds = 10_000
)

type eligibleProvider struct {
	provider  string
	line      *PreparedLine
	costMinor int64
	// tableOrder is the index of the provider's first line, a stable tie-breaker.
	tableOrder int
}

// shareWindow holds the routing decisions of the last window payments, for the
// share cap. It is safe for concurrent use.
type shareWindow struct {
	mu      sync.Mutex
	window  int
	entries []string
}

func (w *shareWindow) count(provider string) int {
	count := 0
	for _, entry := range w.entries {
		if entry == provider {
			count++
		}
	}
	return count
}

// projection is the share this provider would have after one more payment.
func (w *shareWindow) projection(provider string) (int, int) {
	denominator := w.window
	if len(w.entries)+1 > denominator {
		denominator = len(w.entries) + 1
	}
	return w.count(provider) + 1, denominator
}

// overCap is true when one more payment would push the provider over the cap.
func (w *shareWindow) overCap(provider string, capPerMillion int) bool {
	count, denominator := w.projection(provider)
	return int64(count)*1_000_000 > int64(capPerMillion)*int64(denominator)
}

func (w *shareWindow) record(provider string) {
	w.entries = append(w.entries, provider)
	if len(w.entries) > w.window {
		w.entries = w.entries[1:]
	}
}

func (w *shareWindow) clear() { w.entries = nil }

// snapshot reports the per-provider counts and the window total consistently.
func (w *shareWindow) snapshot(providers []string) (map[string]int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := make(map[string]int, len(providers))
	for _, provider := range providers {
		counts[provider] = w.count(provider)
	}
	return counts, len(w.entries)
}

func (w *shareWindow) locked(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn()
}

// planStore holds the providers to try after a known-safe failure, per in-flight
// request. Go 1.23 has no weak references, so the request identity is held in a
// bounded FIFO map instead: the after hook removes the entry, and the bound keeps
// a request whose hook never ran from being retained forever.
type planStore struct {
	mu      sync.Mutex
	entries map[any][]string
	order   []any
}

func newPlanStore() *planStore {
	return &planStore{entries: make(map[any][]string)}
}

// planKey is the request identity. Only pointers have one in Go; a request passed
// by value cannot be tracked and is therefore not retried.
func planKey(request any) (any, bool) {
	if request == nil {
		return nil, false
	}
	value := reflect.ValueOf(request)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil, false
	}
	return request, true
}

func (p *planStore) set(key any, plan []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.entries[key]; !exists {
		p.order = append(p.order, key)
	}
	p.entries[key] = plan
	for len(p.order) > defaultPlanEntries {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.entries, oldest)
	}
}

// take reads and forgets the plan of a request.
func (p *planStore) take(key any) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	plan, ok := p.entries[key]
	if !ok {
		return nil
	}
	delete(p.entries, key)
	for index, entry := range p.order {
		if entry == key {
			p.order = append(p.order[:index], p.order[index+1:]...)
			break
		}
	}
	return plan
}

type binEntry struct {
	info *BinInfo
}

// Router routes payments by commission cost. It is safe for concurrent use.
type Router struct {
	options       Options
	now           func() int64
	cacheEntries  int
	cacheTTLMS    int64
	binCache      *TTLCache[binEntry]
	store         Store
	fallback      []string
	prefer        []string
	fallbackOn    map[string]bool
	windowSize    int
	capPerMillion *int

	share *shareWindow
	plans *planStore

	mu         sync.RWMutex
	pctx       core.PluginContext
	bound      bool
	prepared   []PreparedLine
	tableOrder map[string]int
}

// New validates the options and builds the router. Provider ids are checked when
// the plugin is attached, by Init (or Bind).
func New(options Options) (*Router, error) {
	if options.Rates == nil {
		return nil, configError("commissionRouter: rates must be an array")
	}
	// Shape validation before the providers are known.
	if err := ValidateRateTable(options.Rates, nil); err != nil {
		return nil, err
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}

	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	cacheEntries := defaultCacheEntries
	cacheTTLMS := int64(defaultCacheTTLMS)
	if options.Cache != nil {
		if options.Cache.MaxEntries != 0 {
			cacheEntries = options.Cache.MaxEntries
		}
		if options.Cache.TTLMS != 0 {
			cacheTTLMS = options.Cache.TTLMS
		}
	}
	binCache, err := NewTTLCache[binEntry](TTLCacheOptions{
		MaxEntries: cacheEntries,
		TTLMS:      cacheTTLMS,
		Now:        now,
	})
	if err != nil {
		return nil, err
	}
	store := options.Store
	if store == nil {
		memory, err := NewMemoryStore(&MemoryStoreOptions{Now: now})
		if err != nil {
			return nil, err
		}
		store = memory
	}
	fallback := dedupe(options.Fallback)
	prefer := []string{}
	if options.Policy != nil {
		prefer = dedupe(options.Policy.Prefer)
	}
	codes := options.FallbackOn
	if codes == nil {
		codes = defaultFallbackOn
	}
	fallbackOn := make(map[string]bool, len(codes))
	for _, code := range codes {
		fallbackOn[code] = true
	}
	windowSize := defaultShareWindow
	var capPerMillion *int
	if options.Policy != nil && options.Policy.ShareCap != nil {
		if options.Policy.ShareCap.Window != 0 {
			windowSize = options.Policy.ShareCap.Window
		}
		cap := int(math.Round(options.Policy.ShareCap.MaxShare * 1_000_000))
		capPerMillion = &cap
	}

	return &Router{
		options:       options,
		now:           now,
		cacheEntries:  cacheEntries,
		cacheTTLMS:    cacheTTLMS,
		binCache:      binCache,
		store:         store,
		fallback:      fallback,
		prefer:        prefer,
		fallbackOn:    fallbackOn,
		windowSize:    windowSize,
		capPerMillion: capPerMillion,
		share:         &shareWindow{window: windowSize},
		plans:         newPlanStore(),
		tableOrder:    make(map[string]int),
	}, nil
}

// Plugin returns the plugin to pass to core.Options.Plugins.
func (r *Router) Plugin() core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID:      "commission-router",
		Options: r.options,
		Init:    r.Bind,
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				Matcher: func(octx core.OperationContext) bool {
					return octx.Routable && IsRoutedOperation(octx.Operation)
				},
				Handler: r.beforeHook,
			}},
			After: []core.AfterHook{{
				Matcher: func(octx core.OperationResultContext) bool {
					return octx.Routable && IsRoutedOperation(octx.Operation)
				},
				Handler: r.afterHook,
			}},
		},
		Methods: func(ctx context.Context, pctx core.PluginContext) (map[string]any, error) {
			if err := r.Bind(ctx, pctx); err != nil {
				return nil, err
			}
			return map[string]any{"router": r}, nil
		},
		Endpoints: map[string]core.PaymentEndpoint{
			"quote": {
				Method:  "POST",
				Path:    "/router/quote",
				Handler: r.quoteEndpoint,
			},
		},
	})
}

// Bind attaches the router to a payment object's providers. It is idempotent.
func (r *Router) Bind(_ context.Context, pctx core.PluginContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound {
		return nil
	}
	providerIDs := pctx.ProviderIDs()
	if err := ValidateRateTable(r.options.Rates, providerIDs); err != nil {
		return err
	}
	for _, provider := range append(append([]string{}, r.fallback...), r.prefer...) {
		if !containsString(providerIDs, provider) {
			return configError(fmt.Sprintf("commissionRouter: '%s' is not a configured provider (%s)",
				provider, orNone(strings.Join(providerIDs, ", "))))
		}
	}
	prepared, err := PrepareRates(r.options.Rates)
	if err != nil {
		return err
	}
	tableOrder := make(map[string]int, len(prepared))
	for index := range prepared {
		if _, seen := tableOrder[prepared[index].Provider]; !seen {
			tableOrder[prepared[index].Provider] = prepared[index].Index
		}
	}
	r.prepared = prepared
	r.tableOrder = tableOrder
	r.pctx = pctx
	r.bound = true
	return nil
}

type runtimeState struct {
	pctx       core.PluginContext
	prepared   []PreparedLine
	tableOrder map[string]int
}

func (r *Router) runtime() (runtimeState, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.bound || r.pctx == nil {
		return runtimeState{}, configError("commissionRouter: the plugin is not attached to a payment object")
	}
	return runtimeState{pctx: r.pctx, prepared: r.prepared, tableOrder: r.tableOrder}, nil
}

func (r *Router) pluginContext() core.PluginContext {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pctx
}

func (r *Router) logInfo(message string) {
	if pctx := r.pluginContext(); pctx != nil {
		if logger := pctx.Logger(); logger != nil {
			logger.Info(message, nil)
		}
	}
}

func (r *Router) logError(message string, cause error) {
	if pctx := r.pluginContext(); pctx != nil {
		if logger := pctx.Logger(); logger != nil {
			logger.Error(message, cause, nil)
		}
	}
}

// checkHealth fails open: a broken health monitor must not stop every payment.
func (r *Router) checkHealth(ctx context.Context, provider string, notes *[]string) bool {
	if r.options.IsHealthy == nil {
		return true
	}
	healthy, err := callIsHealthy(ctx, r.options.IsHealthy, provider)
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("health check for '%s' failed; treated as healthy", provider))
		return true
	}
	return healthy
}

func callIsHealthy(ctx context.Context, isHealthy IsHealthy, provider string) (healthy bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			healthy, err = false, recoveredError(recovered)
		}
	}()
	return isHealthy(ctx, provider)
}

// classify returns one BIN classification, cached (bounded and with a TTL).
func (r *Router) classify(ctx context.Context, binPrefix string) *BinInfo {
	if cached, ok := r.binCache.Get(ctx, binPrefix); ok {
		return cached.info
	}
	if r.options.BinLookup == nil {
		return nil
	}
	info, err := callBinLookup(ctx, r.options.BinLookup, binPrefix)
	if err != nil {
		// Never log the BIN: a failed lookup is reported without card data.
		r.logInfo("[commission-router] BIN lookup failed; the card is treated as unknown")
		return nil
	}
	r.binCache.Set(ctx, binPrefix, binEntry{info: info})
	return info
}

func callBinLookup(ctx context.Context, lookup BinLookup, binPrefix string) (info *BinInfo, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			info, err = nil, recoveredError(recovered)
		}
	}()
	return lookup(ctx, binPrefix)
}

// classifyCard classifies an extracted BIN prefix (8 digits, then 6), never a
// full PAN.
func (r *Router) classifyCard(ctx context.Context, cardNumber string) *BinInfo {
	digits := onlyDigits(cardNumber)
	if len(digits) < 6 {
		return nil
	}
	eight := digits[:6]
	if len(digits) >= 8 {
		eight = digits[:8]
	}
	if info := r.classify(ctx, eight); info != nil {
		return info
	}
	six := digits[:6]
	if eight == six {
		return nil
	}
	return r.classify(ctx, six)
}

func (r *Router) criteriaFromRequest(ctx context.Context, request any, operation core.PaymentOperation) (*QuoteCriteria, error) {
	var price, paidPrice, currency, cardNumber string
	var installment any
	switch typed := request.(type) {
	case *core.PaymentRequest:
		if typed == nil {
			return nil, nil
		}
		price, paidPrice, currency = typed.Price, typed.PaidPrice, typed.Currency
		installment = typed.Installment
		if typed.PaymentCard != nil {
			cardNumber = typed.PaymentCard.CardNumber
		}
	case *core.ThreeDSPaymentRequest:
		if typed == nil {
			return nil, nil
		}
		price, paidPrice, currency = typed.Price, typed.PaidPrice, typed.Currency
		installment = typed.Installment
		if typed.PaymentCard != nil {
			cardNumber = typed.PaymentCard.CardNumber
		}
	default:
		record := toRecord(request)
		if record == nil {
			return nil, nil
		}
		price, paidPrice, currency = text(record["price"]), text(record["paidPrice"]), text(record["currency"])
		installment = record["installment"]
		if card, ok := record["paymentCard"].(map[string]any); ok {
			cardNumber = text(card["cardNumber"])
		}
	}

	amountText := paidPrice
	if amountText == "" {
		amountText = price
	}
	if amountText == "" {
		return nil, nil
	}
	amount, err := AmountMinor(amountText, "amount")
	if err != nil {
		return nil, err
	}
	if !currencyCode.MatchString(currency) {
		currency = "TRY"
	} else {
		currency = strings.ToUpper(currency)
	}
	count := 1
	if value, ok := numberOf(installment); ok && value == math.Trunc(value) && value >= 1 && value <= 99 {
		count = int(value)
	}
	var card *BinInfo
	if cardNumber != "" {
		card = r.classifyCard(ctx, cardNumber)
	}
	return &QuoteCriteria{
		AmountMinor: amount,
		Currency:    currency,
		Installment: count,
		ThreeDS:     isThreeDSOperation(operation),
		Card:        card,
		Now:         r.now(),
	}, nil
}

func isThreeDSOperation(operation core.PaymentOperation) bool {
	return operation == core.OperationInitThreeDSPayment || operation == core.OperationInitThreeDSAuthorize
}

// mayRetry reports whether a failure proves no charge happened.
func (r *Router) mayRetry(result providerResult) bool {
	if result.status != string(core.StatusFailure) {
		return false
	}
	if neverRetry[result.code] || neverRetry[result.errorCode] {
		return false
	}
	return r.fallbackOn[result.code] || r.fallbackOn[result.errorCode]
}

// remember keeps the provider of a payment that may exist. The payment already
// happened; a store failure must not turn a confirmed success into an error.
func (r *Router) remember(ctx context.Context, paymentID, provider string) {
	if provider == "" {
		return
	}
	if err := r.store.Set(ctx, paymentID, provider); err != nil {
		r.logError("[commission-router] routing store write failed; later refunds may use the default provider", err)
	}
}

func (r *Router) runOverride(
	ctx context.Context,
	criteria QuoteCriteria,
	operation core.PaymentOperation,
	conversationID string,
	candidates []string,
	notes *[]string,
) string {
	if r.options.Policy == nil || r.options.Policy.Override == nil {
		return ""
	}
	octx := OverrideContext{
		Operation:      operation,
		ConversationID: conversationID,
		Amount:         amountOf(criteria),
		Candidates:     candidates,
		Card:           quoteCardOf(criteria.Card),
	}
	provider, err := callOverride(ctx, r.options.Policy.Override, octx)
	if err != nil {
		r.logInfo("[commission-router] policy.override threw; normal ranking used")
		*notes = append(*notes, "policy.override threw; normal ranking used")
		return ""
	}
	return provider
}

func callOverride(ctx context.Context, override Override, octx OverrideContext) (provider string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			provider, err = "", recoveredError(recovered)
		}
	}()
	return override(ctx, octx)
}

func amountOf(criteria QuoteCriteria) QuoteAmount {
	return QuoteAmount{
		Price:       FormatMinor(criteria.AmountMinor),
		PriceMinor:  criteria.AmountMinor,
		Currency:    criteria.Currency,
		Installment: criteria.Installment,
		ThreeDS:     criteria.ThreeDS,
	}
}

func quoteCardOf(card *BinInfo) *QuoteCard {
	if card == nil {
		return nil
	}
	return &QuoteCard{
		BankName:        card.BankName,
		CardFamily:      card.CardFamily,
		CardType:        card.CardType,
		CardAssociation: card.CardAssociation,
		Commercial:      card.Commercial,
	}
}

func candidateDetail(candidate eligibleProvider, chosen *eligibleProvider, currency string, reason CandidateReason) string {
	cost := fmt.Sprintf("%s %s", FormatMinor(candidate.costMinor), currency)
	switch reason {
	case CandidateLowestCost:
		return "cheapest eligible commission: " + cost
	case CandidatePreferredOnTie:
		return fmt.Sprintf("same cost as the runner-up; preferred by policy.prefer (%s)", cost)
	case CandidateOverride:
		return fmt.Sprintf("selected by policy.override (%s)", cost)
	}
	if chosen == nil {
		return cost
	}
	difference := candidate.costMinor - chosen.costMinor
	if difference == 0 {
		return fmt.Sprintf("same cost as '%s' (%s); ranked lower by policy.prefer or the rate table order",
			chosen.provider, cost)
	}
	return fmt.Sprintf("costs %s %s more than '%s'", FormatMinor(difference), currency, chosen.provider)
}

func (r *Router) preferIndex(provider string) int {
	for index, entry := range r.prefer {
		if entry == provider {
			return index
		}
	}
	return maxSafeInteger
}

// buildQuote ranks every configured provider for one payment and explains the
// outcome. It never mutates the share window.
func (r *Router) buildQuote(ctx context.Context, criteria QuoteCriteria, operation core.PaymentOperation, conversationID string) (*Quote, error) {
	state, err := r.runtime()
	if err != nil {
		return nil, err
	}
	notes := []string{}
	excluded := []ExcludedProvider{}
	eligible := []eligibleProvider{}

	for _, provider := range state.pctx.ProviderIDs() {
		evaluation := EvaluateProvider(provider, state.prepared, criteria)
		if !evaluation.Eligible {
			excluded = append(excluded, ExcludedProvider{
				Provider:  evaluation.Provider,
				Reason:    evaluation.Reason,
				Detail:    evaluation.Detail,
				RateIndex: evaluation.RateIndex,
			})
			continue
		}
		if !r.checkHealth(ctx, provider, &notes) {
			excluded = append(excluded, ExcludedProvider{
				Provider: provider,
				Reason:   ExclusionProviderUnhealthy,
				Detail:   "isHealthy() returned false",
			})
			continue
		}
		order, ok := state.tableOrder[provider]
		if !ok {
			order = maxSafeInteger
		}
		eligible = append(eligible, eligibleProvider{
			provider:   provider,
			line:       evaluation.Line,
			costMinor:  evaluation.CostMinor,
			tableOrder: order,
		})
	}

	sort.SliceStable(eligible, func(a, b int) bool {
		left, right := eligible[a], eligible[b]
		if left.costMinor != right.costMinor {
			return left.costMinor < right.costMinor
		}
		leftPrefer, rightPrefer := r.preferIndex(left.provider), r.preferIndex(right.provider)
		if leftPrefer != rightPrefer {
			return leftPrefer < rightPrefer
		}
		if left.tableOrder != right.tableOrder {
			return left.tableOrder < right.tableOrder
		}
		return left.provider < right.provider
	})

	var selected *eligibleProvider
	var selectionReason SelectionReason

	providers := make([]string, len(eligible))
	for index := range eligible {
		providers[index] = eligible[index].provider
	}
	overrideProvider := r.runOverride(ctx, criteria, operation, conversationID, providers, &notes)
	if overrideProvider != "" {
		for index := range eligible {
			if eligible[index].provider == overrideProvider {
				selected = &eligible[index]
				break
			}
		}
		if selected != nil {
			// An explicit per-order decision wins over the share cap.
			selectionReason = SelectionOverride
		} else {
			notes = append(notes, fmt.Sprintf(
				"policy.override selected '%s', which is not eligible; normal ranking used", overrideProvider))
		}
	}

	ranked := eligible
	if selected == nil && r.capPerMillion != nil && len(eligible) > 0 {
		cap := *r.capPerMillion
		type capped struct {
			entry       eligibleProvider
			count       int
			denominator int
		}
		kept := []eligibleProvider{}
		dropped := []capped{}
		r.share.locked(func() {
			for _, entry := range eligible {
				if r.share.overCap(entry.provider, cap) {
					count, denominator := r.share.projection(entry.provider)
					dropped = append(dropped, capped{entry: entry, count: count, denominator: denominator})
					continue
				}
				kept = append(kept, entry)
			}
		})
		if len(kept) == 0 {
			notes = append(notes, "every eligible provider is at its share cap; the cheapest eligible provider is used")
		} else {
			for _, entry := range dropped {
				index := entry.entry.line.Index
				excluded = append(excluded, ExcludedProvider{
					Provider: entry.entry.provider,
					Reason:   ExclusionShareCap,
					Detail: fmt.Sprintf(
						"one more payment would take %d%% of the last %d routing decisions (cap %d%%)",
						int(math.Round(float64(entry.count)/float64(entry.denominator)*100)),
						entry.denominator,
						int(math.Round(float64(cap)/1_000_000*100))),
					RateIndex: &index,
				})
			}
			ranked = kept
		}
	}

	if selected == nil && len(ranked) > 0 {
		selected = &ranked[0]
		selectionReason = SelectionLowestCost
		if len(ranked) > 1 {
			runnerUp := ranked[1]
			if runnerUp.costMinor == selected.costMinor && r.preferIndex(selected.provider) < r.preferIndex(runnerUp.provider) {
				selectionReason = SelectionPreferredOnTie
			}
		}
	}

	candidates := []Candidate{}
	if selected != nil {
		chosenReason := CandidateReason(selectionReason)
		if chosenReason == "" {
			chosenReason = CandidateLowestCost
		}
		candidates = append(candidates, costCandidate(*selected, true, chosenReason,
			candidateDetail(*selected, nil, criteria.Currency, chosenReason)))
		for index := range ranked {
			entry := ranked[index]
			if entry.provider == selected.provider {
				continue
			}
			reason := CandidateRankedLower
			if entry.costMinor > selected.costMinor {
				reason = CandidateHigherCost
			}
			candidates = append(candidates, costCandidate(entry, false, reason,
				candidateDetail(entry, selected, criteria.Currency, reason)))
		}
	}

	// No rate line is eligible: use the merchant's fallback order, but never a
	// provider that was excluded by a filter (wrong currency, 3DS, card...).
	if selected == nil {
		fallbackCandidates := []string{}
		for _, provider := range r.fallback {
			exclusion := findExcluded(excluded, provider)
			if exclusion == nil || exclusion.Reason != ExclusionNoRateLine {
				continue
			}
			if !r.checkHealth(ctx, provider, &notes) {
				continue
			}
			fallbackCandidates = append(fallbackCandidates, provider)
		}
		if len(fallbackCandidates) > 0 {
			for _, provider := range fallbackCandidates {
				for index := range excluded {
					if excluded[index].Provider == provider {
						excluded = append(excluded[:index], excluded[index+1:]...)
						break
					}
				}
			}
			selectionReason = SelectionFallback
			for index, provider := range fallbackCandidates {
				detail := fmt.Sprintf("comes after '%s' in the fallback order", fallbackCandidates[0])
				reason := CandidateRankedLower
				if index == 0 {
					detail = "no rate line matched; first provider in the fallback order"
					reason = CandidateFallback
				}
				candidates = append(candidates, Candidate{
					Provider: provider,
					Chosen:   index == 0,
					Reason:   reason,
					Detail:   detail,
				})
			}
		} else {
			notes = append(notes, "no provider is eligible and the fallback order is empty or unavailable")
		}
	}

	quote := &Quote{
		Strategy:   StrategyLowestCost,
		Amount:     amountOf(criteria),
		Card:       quoteCardOf(criteria.Card),
		Candidates: candidates,
		Excluded:   excluded,
		Notes:      notes,
	}
	for index := range candidates {
		if candidates[index].Chosen {
			provider := candidates[index].Provider
			quote.Selected = &provider
			break
		}
	}
	if len(candidates) > 0 {
		reason := selectionReason
		quote.SelectionReason = &reason
	}
	return quote, nil
}

func costCandidate(entry eligibleProvider, chosen bool, reason CandidateReason, detail string) Candidate {
	cost := FormatMinor(entry.costMinor)
	rate := entry.line.Rate
	fixed := entry.line.Fixed
	index := entry.line.Index
	minor := entry.costMinor
	return Candidate{
		Provider:  entry.provider,
		Cost:      &cost,
		CostMinor: &minor,
		Rate:      &rate,
		Fixed:     &fixed,
		RateIndex: &index,
		Chosen:    chosen,
		Reason:    reason,
		Detail:    detail,
	}
}

func findExcluded(excluded []ExcludedProvider, provider string) *ExcludedProvider {
	for index := range excluded {
		if excluded[index].Provider == provider {
			return &excluded[index]
		}
	}
	return nil
}

var binPattern = regexp.MustCompile(`^\d{6,8}$`)
var upperCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

// Quote prices one payment and explains the ranking. It has no side effect: no
// routing decision is recorded and no provider is called.
func (r *Router) Quote(ctx context.Context, input QuoteInput) (*Quote, error) {
	operation := input.Operation
	if operation == "" {
		operation = core.OperationCreatePayment
	}
	if !IsChargeOperation(operation) {
		return nil, validationError("commissionRouter: operation must be createPayment, authorize, initThreeDSPayment or initThreeDSAuthorize")
	}
	priceText := strings.TrimSpace(input.Price)
	if priceText == "" {
		return nil, validationError("commissionRouter: price is required")
	}
	priceMinor, err := AmountMinor(priceText, "price")
	if err != nil {
		return nil, validationError("commissionRouter: price must be a non-negative decimal with at most 2 decimals")
	}
	installment := 1
	if input.Installment != nil {
		installment = *input.Installment
	}
	if installment < 1 || installment > 99 {
		return nil, validationError("commissionRouter: installment must be an integer between 1 and 99")
	}
	currency := "TRY"
	if input.Currency != "" {
		currency = strings.ToUpper(input.Currency)
	}
	if !upperCurrency.MatchString(currency) {
		return nil, validationError("commissionRouter: currency must be a 3-letter ISO code")
	}
	if input.BinNumber != "" && !binPattern.MatchString(input.BinNumber) {
		// Never echo the value: callers sometimes pass a full card number by mistake.
		return nil, validationError("commissionRouter: binNumber must be the 6-8 digit BIN prefix, never a full card number")
	}
	var card *BinInfo
	if input.BinNumber != "" {
		card = r.classify(ctx, input.BinNumber)
	}
	threeDS := isThreeDSOperation(operation)
	if input.ThreeDS != nil {
		threeDS = *input.ThreeDS
	}
	return r.buildQuote(ctx, QuoteCriteria{
		AmountMinor: priceMinor,
		Currency:    currency,
		Installment: installment,
		ThreeDS:     threeDS,
		Card:        card,
		Now:         r.now(),
	}, operation, input.ConversationID)
}

// Status reports the routing counters and the BIN-cache usage.
func (r *Router) Status(ctx context.Context) Status {
	state, err := r.runtime()
	providerIDs := []string{}
	if err == nil {
		providerIDs = state.pctx.ProviderIDs()
	}
	routed, total := r.share.snapshot(providerIDs)
	return Status{
		Routed:      routed,
		Total:       total,
		ShareWindow: r.windowSize,
		BinCache: CacheStatus{
			Entries:    r.binCache.Size(ctx),
			MaxEntries: r.cacheEntries,
			TTLMS:      r.cacheTTLMS,
		},
	}
}

// Reset forgets the share window and the BIN cache; routing history is kept.
func (r *Router) Reset(ctx context.Context) {
	r.share.locked(func() { r.share.clear() })
	r.binCache.Clear(ctx)
}

// beforeHook routes a charge by cost and a follow-up back to its provider.
// core.BeforeHook carries no context, so hooks run on context.Background().
func (r *Router) beforeHook(octx core.OperationContext) (*core.BeforeHookResult, error) {
	ctx := context.Background()
	if _, err := r.runtime(); err != nil {
		return nil, err
	}

	if IsBoundOperation(octx.Operation) {
		// Refunds, cancels and status calls must reach the provider that handled
		// the payment, even when it is no longer the cheapest.
		paymentID := requestPaymentID(octx.Request)
		if paymentID == "" {
			return nil, nil
		}
		provider, err := r.store.Get(ctx, paymentID)
		if err != nil {
			r.logError("[commission-router] routing store lookup failed; the default provider is used", err)
			return nil, nil
		}
		if provider != "" && provider != octx.Provider {
			r.logInfo(fmt.Sprintf("[commission-router] %s is routed to '%s', which handled the payment",
				octx.Operation, provider))
			return &core.BeforeHookResult{Provider: provider}, nil
		}
		return nil, nil
	}

	criteria, err := r.criteriaFromRequest(ctx, octx.Request, octx.Operation)
	if err != nil {
		// Invalid amounts are the provider's validation to report.
		return nil, nil
	}
	if criteria == nil {
		return nil, nil
	}
	decision, err := r.buildQuote(ctx, *criteria, octx.Operation, requestConversationID(octx.Request))
	if err != nil {
		return nil, err
	}
	var chosen *Candidate
	for index := range decision.Candidates {
		if decision.Candidates[index].Chosen {
			chosen = &decision.Candidates[index]
			break
		}
	}
	if chosen == nil {
		return nil, nil
	}

	if key, ok := planKey(octx.Request); ok {
		plan := []string{}
		for _, candidate := range decision.Candidates {
			if candidate.Chosen {
				continue
			}
			plan = append(plan, candidate.Provider)
			if len(plan) == maxAttempts-1 {
				break
			}
		}
		r.plans.set(key, plan)
	}
	// Record every routing decision, not only when a cap is configured: Status
	// reports what the router actually did.
	r.share.locked(func() { r.share.record(chosen.Provider) })
	if chosen.Provider != octx.Provider {
		message := fmt.Sprintf("[commission-router] %s is routed to '%s'", octx.Operation, chosen.Provider)
		if chosen.Cost != nil && *chosen.Cost != "" {
			message += fmt.Sprintf(" (commission %s %s)", *chosen.Cost, criteria.Currency)
		}
		r.logInfo(message)
		return &core.BeforeHookResult{Provider: chosen.Provider}, nil
	}
	return nil, nil
}

// afterHook remembers the provider of a payment that may exist, retries a failure
// that proves no charge happened, and reports the provider that handled the call.
func (r *Router) afterHook(octx core.OperationResultContext) (*core.AfterHookResult, error) {
	ctx := context.Background()
	initial, hasInitial := asResult(octx.Result)
	startedWith := octx.Provider
	provider := startedWith
	result := octx.Result

	var plan []string
	if key, ok := planKey(octx.Request); ok {
		plan = r.plans.take(key)
	}

	// Remember the provider of a payment that may exist even when the attempt
	// failed (a payment id was returned).
	if hasInitial && initial.paymentID != "" && startedWith != "" {
		r.remember(ctx, initial.paymentID, startedWith)
	}

	if len(plan) > 0 && hasInitial && r.mayRetry(initial) {
		for _, candidate := range plan {
			if candidate == provider {
				continue
			}
			r.logInfo(fmt.Sprintf(
				"[commission-router] %s failed with a code that proves no charge; retrying with '%s'",
				octx.Operation, candidate))
			next, err := r.callBackup(ctx, candidate, octx.Operation, octx.Request)
			if err != nil {
				// The backup call threw: its outcome is unknown. Never try another
				// provider after an unknown outcome.
				r.logError(fmt.Sprintf(
					"[commission-router] fallback call to '%s' threw; no further retries", candidate), err)
				return nil, err
			}
			outcome, hasOutcome := asResult(next)
			result = next
			provider = candidate
			if hasOutcome && outcome.paymentID != "" {
				r.remember(ctx, outcome.paymentID, candidate)
			}
			if !hasOutcome || !r.mayRetry(outcome) {
				break
			}
		}
	}

	wrapped := withProvider(result, provider)
	if wrapped == nil {
		return nil, nil
	}
	return &core.AfterHookResult{Result: wrapped}, nil
}

func (r *Router) callBackup(ctx context.Context, candidate string, operation core.PaymentOperation, request any) (any, error) {
	state, err := r.runtime()
	if err != nil {
		return nil, err
	}
	backup, err := state.pctx.Use(candidate)
	if err != nil {
		return nil, err
	}
	return callOperation(ctx, backup, operation, request)
}

// callOperation dispatches a routed operation on a provider, converting the
// request to the operation's type when the caller used another representation.
func callOperation(ctx context.Context, provider core.Provider, operation core.PaymentOperation, request any) (any, error) {
	switch operation {
	case core.OperationCreatePayment:
		typed, err := decodeRequest[core.PaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.CreatePayment(ctx, typed)
	case core.OperationAuthorize:
		typed, err := decodeRequest[core.PaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Authorize(ctx, typed)
	case core.OperationInitThreeDSPayment:
		typed, err := decodeRequest[core.ThreeDSPaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.InitThreeDSPayment(ctx, typed)
	case core.OperationInitThreeDSAuthorize:
		typed, err := decodeRequest[core.ThreeDSPaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.InitThreeDSAuthorize(ctx, typed)
	case core.OperationRefund:
		typed, err := decodeRequest[core.RefundRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Refund(ctx, typed)
	case core.OperationCancel:
		typed, err := decodeRequest[core.CancelRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Cancel(ctx, typed)
	case core.OperationCapture:
		typed, err := decodeRequest[core.CaptureRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Capture(ctx, typed)
	case core.OperationVoidAuthorization:
		typed, err := decodeRequest[core.VoidAuthorizationRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.VoidAuthorization(ctx, typed)
	case core.OperationGetPayment:
		return provider.GetPayment(ctx, requestPaymentID(request))
	case core.OperationCompleteThreeDSPayment:
		return provider.CompleteThreeDSPayment(ctx, request)
	}
	return nil, core.NotSupported(provider.Name(), string(operation), "")
}

func decodeRequest[T any](request any) (*T, error) {
	if typed, ok := request.(*T); ok {
		return typed, nil
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, validationError("commissionRouter: the request cannot be retried on another provider")
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, validationError("commissionRouter: the request cannot be retried on another provider")
	}
	return &out, nil
}

// withProvider adds the provider that handled the call to a result. A non-object
// result is left unchanged.
func withProvider(result any, provider string) any {
	switch typed := result.(type) {
	case nil:
		return nil
	case *core.PaymentResponse:
		if typed == nil {
			return nil
		}
		return RoutedPaymentResponse{PaymentResponse: typed, Provider: provider}
	case *core.ThreeDSInitResponse:
		if typed == nil {
			return nil
		}
		return RoutedThreeDSInitResponse{ThreeDSInitResponse: typed, Provider: provider}
	case *core.RefundResponse:
		if typed == nil {
			return nil
		}
		return RoutedRefundResponse{RefundResponse: typed, Provider: provider}
	case *core.CancelResponse:
		if typed == nil {
			return nil
		}
		return RoutedCancelResponse{CancelResponse: typed, Provider: provider}
	}
	record := toRecord(result)
	if record == nil {
		return nil
	}
	wrapped := make(map[string]any, len(record)+1)
	for key, value := range record {
		wrapped[key] = value
	}
	wrapped["provider"] = provider
	return wrapped
}

func (r *Router) quoteEndpoint(ctx context.Context, ectx core.EndpointContext) (any, error) {
	body := ectx.Body()
	if body == nil {
		body = map[string]any{}
	}
	input := QuoteInput{
		BinNumber:      text(body["binNumber"]),
		Price:          priceText(body["price"]),
		Currency:       text(body["currency"]),
		ConversationID: text(body["conversationId"]),
		Operation:      core.PaymentOperation(text(body["operation"])),
	}
	if value, ok := numberOf(body["installment"]); ok {
		installment := int(value)
		input.Installment = &installment
	}
	if value, ok := body["threeDS"].(bool); ok {
		input.ThreeDS = &value
	}
	quote, err := r.Quote(ctx, input)
	if err != nil {
		var validation *core.ValidationError
		if errors.As(err, &validation) {
			return ectx.JSON(map[string]any{"error": validation.Message}, 400), nil
		}
		return nil, err
	}
	return quote, nil
}

// priceText keeps a JSON number's literal form, so no amount passes through a
// float on its way to the exact decimal parser.
func priceText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return string(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return ""
}

func validateOptions(options Options) error {
	if options.Strategy != "" && options.Strategy != StrategyLowestCost {
		return configError(fmt.Sprintf(
			"commissionRouter: unknown strategy '%s'; only 'lowest-cost' is supported", options.Strategy))
	}
	for _, provider := range options.Fallback {
		if provider == "" {
			return configError("commissionRouter: fallback must contain provider ids")
		}
	}
	if len(options.Rates) == 0 && len(options.Fallback) == 0 {
		return configError("commissionRouter: configure at least one rate line or a fallback provider")
	}
	if options.Cache != nil {
		if options.Cache.MaxEntries != 0 && options.Cache.MaxEntries < 1 {
			return configError("commissionRouter: cache.maxEntries must be an integer >= 1")
		}
		if options.Cache.TTLMS != 0 && options.Cache.TTLMS <= 0 {
			return configError("commissionRouter: cache.ttlMs must be greater than 0")
		}
	}
	if options.Policy != nil {
		for _, provider := range options.Policy.Prefer {
			if provider == "" {
				return configError("commissionRouter: policy.prefer must contain provider ids")
			}
		}
		if options.Policy.ShareCap != nil {
			maxShare := options.Policy.ShareCap.MaxShare
			if !(maxShare > 0) || maxShare > 1 {
				return configError("commissionRouter: policy.shareCap.maxShare must be in (0, 1]")
			}
			window := options.Policy.ShareCap.Window
			if window != 0 && (window < 1 || window > maxShareWindowBounds) {
				return configError("commissionRouter: policy.shareCap.window must be an integer between 1 and 10000")
			}
		}
	}
	codes := options.FallbackOn
	if codes == nil {
		codes = defaultFallbackOn
	}
	for _, code := range codes {
		if code == "" {
			return configError("commissionRouter: fallbackOn must contain error codes")
		}
		if neverRetry[code] {
			return configError(fmt.Sprintf(
				"commissionRouter: fallbackOn must not contain '%s'. Only errors that prove no "+
					"charge happened may fall back; retrying a decline, NETWORK_ERROR or an unknown "+
					"outcome can charge the customer twice.", code))
		}
	}
	return nil
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func configError(message string) error {
	return core.NewConfigurationError(message, "")
}

func validationError(message string) error {
	return core.NewValidationError(message, "", nil)
}

type providerResult struct {
	status    string
	paymentID string
	code      string
	errorCode string
}

func asResult(value any) (providerResult, bool) {
	switch typed := value.(type) {
	case *core.PaymentResponse:
		if typed == nil {
			return providerResult{}, false
		}
		return providerResult{string(typed.Status), typed.PaymentID, string(typed.Code), typed.ErrorCode}, true
	case *core.ThreeDSInitResponse:
		if typed == nil {
			return providerResult{}, false
		}
		return providerResult{string(typed.Status), typed.PaymentID, string(typed.Code), typed.ErrorCode}, true
	case *core.RefundResponse:
		if typed == nil {
			return providerResult{}, false
		}
		return providerResult{string(typed.Status), "", string(typed.Code), typed.ErrorCode}, true
	case *core.CancelResponse:
		if typed == nil {
			return providerResult{}, false
		}
		return providerResult{string(typed.Status), "", string(typed.Code), typed.ErrorCode}, true
	}
	record := toRecord(value)
	if record == nil {
		return providerResult{}, false
	}
	status, ok := record["status"].(string)
	if !ok {
		return providerResult{}, false
	}
	return providerResult{
		status:    status,
		paymentID: text(record["paymentId"]),
		code:      text(record["code"]),
		errorCode: text(record["errorCode"]),
	}, true
}

func requestPaymentID(request any) string {
	switch typed := request.(type) {
	case string:
		return typed
	case *core.RefundRequest:
		if typed != nil {
			return typed.PaymentID
		}
	case *core.CancelRequest:
		if typed != nil {
			return typed.PaymentID
		}
	case *core.CaptureRequest:
		if typed != nil {
			return typed.PaymentID
		}
	case *core.VoidAuthorizationRequest:
		if typed != nil {
			return typed.PaymentID
		}
	}
	return text(toRecord(request)["paymentId"])
}

func requestConversationID(request any) string {
	switch typed := request.(type) {
	case *core.PaymentRequest:
		if typed != nil {
			return typed.ConversationID
		}
	case *core.ThreeDSPaymentRequest:
		if typed != nil {
			return typed.ConversationID
		}
	case *core.RefundRequest:
		if typed != nil {
			return typed.ConversationID
		}
	case *core.CancelRequest:
		if typed != nil {
			return typed.ConversationID
		}
	}
	return text(toRecord(request)["conversationId"])
}

func text(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func numberOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

// toRecord normalizes any request or result representation to a JSON record, so
// a typed core struct and a loose map behave identically. Scalars are not
// records.
func toRecord(value any) map[string]any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case string, bool, int, int64, float64, json.Number:
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		return nil
	}
	return record
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= '0' && character <= '9' {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func recoveredError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return core.NewError(fmt.Sprintf("%v", recovered), "PLUGIN_ERROR")
}

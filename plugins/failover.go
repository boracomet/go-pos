package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// FailoverCircuitState is the state of a provider's circuit breaker.
type FailoverCircuitState string

const (
	// FailoverCircuitClosed lets every call through.
	FailoverCircuitClosed FailoverCircuitState = "closed"
	// FailoverCircuitOpen skips the provider until the cooldown expires.
	FailoverCircuitOpen FailoverCircuitState = "open"
	// FailoverCircuitHalfOpen allows one probe after the cooldown expired.
	FailoverCircuitHalfOpen FailoverCircuitState = "half-open"
)

// FailoverCircuitBreakerOptions configures the circuit breaker. Window and
// Cooldown take milliseconds as a number, or a duration string such as "1m".
type FailoverCircuitBreakerOptions struct {
	// Failures is the number of failures inside Window that trips the circuit. Default: 5.
	Failures int
	// Window is the period failures accumulate in. Default: "1m".
	Window any
	// Cooldown is how long an open circuit stays open. Default: "5m".
	Cooldown any
}

// FailoverEventError carries the failure that triggered a switch.
type FailoverEventError struct {
	Code         core.PaymentErrorCode `json:"code,omitempty"`
	ErrorCode    string                `json:"errorCode,omitempty"`
	ErrorMessage string                `json:"errorMessage,omitempty"`
}

// FailoverEvent is reported to OnSwitch whenever failover moves to another provider.
type FailoverEvent struct {
	From      string                `json:"from"`
	To        string                `json:"to"`
	Operation core.PaymentOperation `json:"operation"`
	Error     FailoverEventError    `json:"error"`
	// Attempt is 1-indexed: the first backup call is attempt 2.
	Attempt int `json:"attempt"`
}

// FailoverOptions configures the failover plugin.
type FailoverOptions struct {
	// Order is the priority list of provider ids to try.
	Order []string
	// OrderFunc selects the order per operation and wins over Order when set.
	OrderFunc func(ctx core.OperationContext) []string
	// On lists the error codes that trigger failover. Nil selects the defaults:
	// PROVIDER_ERROR, PROVIDER_UNAVAILABLE, TIMEOUT_BEFORE_SEND, SYSTEM_ERROR, 503.
	On []string
	// Operations limits failover to these operations. Nil selects createPayment and authorize.
	Operations []core.PaymentOperation
	// CircuitBreaker skips providers that keep failing.
	CircuitBreaker *FailoverCircuitBreakerOptions
	// OnSwitch is called on every switch. An error is logged and the switch continues.
	OnSwitch func(event FailoverEvent) error
}

// FailoverProviderCircuitStatus is one provider's circuit breaker status.
type FailoverProviderCircuitStatus struct {
	State     FailoverCircuitState `json:"state"`
	Failures  int                  `json:"failures"`
	OpenUntil *time.Time           `json:"openUntil"`
}

var failoverDurationPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m|h)$`)

var failoverDurationUnits = map[string]float64{"ms": 1, "s": 1000, "m": 60000, "h": 3600000}

func failoverToMs(duration any, defaultValue float64) (float64, error) {
	switch value := duration.(type) {
	case nil:
		return defaultValue, nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case float64:
		return value, nil
	case string:
		match := failoverDurationPattern.FindStringSubmatch(strings.TrimSpace(value))
		if match == nil {
			return 0, core.NewConfigurationError(fmt.Sprintf(
				"failover: invalid duration '%s'. Use milliseconds or '500ms', '30s', '1m', '5m'.", value), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(fmt.Sprintf(
				"failover: invalid duration '%s'. Use milliseconds or '500ms', '30s', '1m', '5m'.", value), "")
		}
		return number * failoverDurationUnits[match[2]], nil
	default:
		return 0, core.NewConfigurationError(fmt.Sprintf(
			"failover: invalid duration '%v'. Use milliseconds or '500ms', '30s', '1m', '5m'.", duration), "")
	}
}

type failoverCircuitBreaker struct {
	mu           sync.Mutex
	maxFailures  int
	windowMs     float64
	cooldownMs   float64
	failures     map[string][]int64
	openUntil    map[string]int64
	failuresKeys []string
	openKeys     []string
}

func newFailoverCircuitBreaker(options *FailoverCircuitBreakerOptions) (*failoverCircuitBreaker, error) {
	breaker := &failoverCircuitBreaker{
		maxFailures: 5,
		failures:    map[string][]int64{},
		openUntil:   map[string]int64{},
	}
	var window, cooldown any
	if options != nil {
		if options.Failures != 0 {
			breaker.maxFailures = options.Failures
		}
		window, cooldown = options.Window, options.Cooldown
	}
	windowMs, err := failoverToMs(window, 60000)
	if err != nil {
		return nil, err
	}
	cooldownMs, err := failoverToMs(cooldown, 300000)
	if err != nil {
		return nil, err
	}
	breaker.windowMs, breaker.cooldownMs = windowMs, cooldownMs
	return breaker, nil
}

func (b *failoverCircuitBreaker) isOpen(providerID string, now int64) bool {
	openUntil, ok := b.openUntil[providerID]
	return ok && openUntil != 0 && now < openUntil
}

func (b *failoverCircuitBreaker) state(providerID string, now int64) FailoverCircuitState {
	openUntil, ok := b.openUntil[providerID]
	if !ok || openUntil == 0 {
		return FailoverCircuitClosed
	}
	if now >= openUntil {
		return FailoverCircuitHalfOpen
	}
	return FailoverCircuitOpen
}

func (b *failoverCircuitBreaker) setFailureTimestamps(providerID string, list []int64) {
	if _, seen := b.failures[providerID]; !seen {
		b.failuresKeys = append(b.failuresKeys, providerID)
	}
	b.failures[providerID] = list
}

func (b *failoverCircuitBreaker) setOpenUntil(providerID string, until int64) {
	if _, seen := b.openUntil[providerID]; !seen {
		b.openKeys = append(b.openKeys, providerID)
	}
	b.openUntil[providerID] = until
}

func (b *failoverCircuitBreaker) drop(providerID string) {
	delete(b.failures, providerID)
	delete(b.openUntil, providerID)
	b.failuresKeys = failoverWithout(b.failuresKeys, providerID)
	b.openKeys = failoverWithout(b.openKeys, providerID)
}

func failoverWithout(keys []string, drop string) []string {
	kept := keys[:0]
	for _, key := range keys {
		if key != drop {
			kept = append(kept, key)
		}
	}
	return kept
}

func (b *failoverCircuitBreaker) recordFailure(providerID string, now int64) {
	cutoff := now - int64(b.windowMs)
	list := make([]int64, 0, len(b.failures[providerID])+1)
	for _, ts := range b.failures[providerID] {
		if ts >= cutoff {
			list = append(list, ts)
		}
	}
	list = append(list, now)
	b.setFailureTimestamps(providerID, list)
	if len(list) >= b.maxFailures {
		b.setOpenUntil(providerID, now+int64(b.cooldownMs))
	}
}

func (b *failoverCircuitBreaker) recordSuccess(providerID string) { b.drop(providerID) }

func (b *failoverCircuitBreaker) reset(providerID string) {
	if providerID != "" {
		b.drop(providerID)
		return
	}
	b.failures = map[string][]int64{}
	b.openUntil = map[string]int64{}
	b.failuresKeys = nil
	b.openKeys = nil
}

func (b *failoverCircuitBreaker) trip(providerID string, duration any) error {
	cooldown := b.cooldownMs
	if !failoverFalsy(duration) {
		value, err := failoverToMs(duration, b.cooldownMs)
		if err != nil {
			return err
		}
		cooldown = value
	}
	b.setOpenUntil(providerID, time.Now().UnixMilli()+int64(cooldown))
	return nil
}

func failoverFalsy(duration any) bool {
	switch value := duration.(type) {
	case nil:
		return true
	case int:
		return value == 0
	case int64:
		return value == 0
	case float64:
		return value == 0
	case string:
		return value == ""
	default:
		return false
	}
}

func (b *failoverCircuitBreaker) status(providerIDs []string, now int64) map[string]FailoverProviderCircuitStatus {
	seen := map[string]bool{}
	ids := make([]string, 0, len(providerIDs)+len(b.failuresKeys)+len(b.openKeys))
	for _, group := range [][]string{providerIDs, b.failuresKeys, b.openKeys} {
		for _, id := range group {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	out := make(map[string]FailoverProviderCircuitStatus, len(ids))
	cutoff := now - int64(b.windowMs)
	for _, id := range ids {
		state := b.state(id, now)
		failures := 0
		for _, ts := range b.failures[id] {
			if ts >= cutoff {
				failures++
			}
		}
		status := FailoverProviderCircuitStatus{State: state, Failures: failures}
		if until, ok := b.openUntil[id]; ok && until != 0 && state == FailoverCircuitOpen {
			openUntil := time.UnixMilli(until)
			status.OpenUntil = &openUntil
		}
		out[id] = status
	}
	return out
}

var failoverDefaultOnCodes = []string{
	string(core.CodeProviderError),
	"PROVIDER_UNAVAILABLE",
	"TIMEOUT_BEFORE_SEND",
	"SYSTEM_ERROR",
	"503",
}

// failoverUnknownOutcomeStatuses are answers after which the provider may already
// have charged the card, so the payment must never move to another provider.
var failoverUnknownOutcomeStatuses = map[int]bool{500: true, 502: true, 504: true}

var failoverDefaultOperations = []core.PaymentOperation{
	core.OperationCreatePayment,
	core.OperationAuthorize,
}

// failoverResultView is the charge outcome of a result, read without assuming its type.
type failoverResultView struct {
	status       core.PaymentStatus
	code         core.PaymentErrorCode
	errorCode    string
	errorMessage string
	httpStatus   int
}

func failoverViewOf(result any) (failoverResultView, bool) {
	switch value := result.(type) {
	case *core.PaymentResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, value.HTTPStatus}, true
	case *core.ThreeDSInitResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, value.HTTPStatus}, true
	case *core.RefundResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, value.HTTPStatus}, true
	case *core.CancelResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, value.HTTPStatus}, true
	case *core.SaveCardResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, 0}, true
	case *core.ListCardsResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, 0}, true
	case *core.DeleteCardResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, 0}, true
	case *core.InstallmentInfoResponse:
		if value == nil {
			return failoverResultView{}, false
		}
		return failoverResultView{value.Status, value.Code, value.ErrorCode, value.ErrorMessage, 0}, true
	case map[string]any:
		status, ok := value["status"].(string)
		if !ok {
			return failoverResultView{}, false
		}
		view := failoverResultView{status: core.PaymentStatus(status)}
		if code, ok := value["code"].(string); ok {
			view.code = core.PaymentErrorCode(code)
		}
		view.errorCode, _ = value["errorCode"].(string)
		view.errorMessage, _ = value["errorMessage"].(string)
		if number, ok := value["httpStatus"].(float64); ok {
			view.httpStatus = int(number)
		}
		return view, true
	default:
		return failoverResultView{}, false
	}
}

func failoverAs[T any](request any) (*T, error) {
	if typed, ok := request.(*T); ok {
		if typed == nil {
			return nil, core.NewValidationError("request is required", "", nil)
		}
		return typed, nil
	}
	if value, ok := request.(T); ok {
		return &value, nil
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

func failoverStringArg(request any) (string, error) {
	if value, ok := request.(string); ok {
		return value, nil
	}
	return "", core.NewValidationError("request must be a string id", "", nil)
}

// failoverCall dispatches an operation to a provider, the way the facade runs it.
func failoverCall(ctx context.Context, provider core.Provider, operation core.PaymentOperation, request any) (any, error) {
	switch operation {
	case core.OperationCreatePayment:
		typed, err := failoverAs[core.PaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.CreatePayment(ctx, typed)
	case core.OperationAuthorize:
		typed, err := failoverAs[core.PaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Authorize(ctx, typed)
	case core.OperationInitThreeDSPayment:
		typed, err := failoverAs[core.ThreeDSPaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.InitThreeDSPayment(ctx, typed)
	case core.OperationInitThreeDSAuthorize:
		typed, err := failoverAs[core.ThreeDSPaymentRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.InitThreeDSAuthorize(ctx, typed)
	case core.OperationCompleteThreeDSPayment:
		return provider.CompleteThreeDSPayment(ctx, request)
	case core.OperationCapture:
		typed, err := failoverAs[core.CaptureRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Capture(ctx, typed)
	case core.OperationVoidAuthorization:
		typed, err := failoverAs[core.VoidAuthorizationRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.VoidAuthorization(ctx, typed)
	case core.OperationRefund:
		typed, err := failoverAs[core.RefundRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Refund(ctx, typed)
	case core.OperationCancel:
		typed, err := failoverAs[core.CancelRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.Cancel(ctx, typed)
	case core.OperationGetPayment:
		id, err := failoverStringArg(request)
		if err != nil {
			return nil, err
		}
		return provider.GetPayment(ctx, id)
	case core.OperationSaveCard:
		typed, err := failoverAs[core.SaveCardRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.SaveCard(ctx, typed)
	case core.OperationListCards:
		typed, err := failoverAs[core.ListCardsRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.ListCards(ctx, typed)
	case core.OperationDeleteCard:
		typed, err := failoverAs[core.DeleteCardRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.DeleteCard(ctx, typed)
	case core.OperationBinCheck:
		bin, err := failoverStringArg(request)
		if err != nil {
			return nil, err
		}
		return provider.BinCheck(ctx, bin)
	case core.OperationInstallmentInfo:
		typed, err := failoverAs[core.InstallmentInfoRequest](request)
		if err != nil {
			return nil, err
		}
		return provider.InstallmentInfo(ctx, typed)
	default:
		return nil, core.NotSupported("failover", string(operation), "")
	}
}

// Failover builds the plugin that moves a payment to a backup provider when the
// active one fails transiently, guarded by a circuit breaker. It never retries an
// unknown outcome (NETWORK_ERROR, pending, or an HTTP 500/502/504 answer), so a
// customer is never charged twice.
func Failover(options FailoverOptions) (core.Plugin, error) {
	allowedOperations := options.Operations
	if allowedOperations == nil {
		allowedOperations = failoverDefaultOperations
	}
	allowed := map[core.PaymentOperation]bool{}
	for _, operation := range allowedOperations {
		allowed[operation] = true
	}

	onCodes := options.On
	if onCodes == nil {
		onCodes = failoverDefaultOnCodes
	}
	codes := map[string]bool{}
	for _, code := range onCodes {
		codes[code] = true
	}

	breaker, err := newFailoverCircuitBreaker(options.CircuitBreaker)
	if err != nil {
		return core.Plugin{}, err
	}

	var mu sync.Mutex
	var pluginCtx core.PluginContext

	contextOf := func() core.PluginContext {
		mu.Lock()
		defer mu.Unlock()
		return pluginCtx
	}
	setContext := func(ctx core.PluginContext) {
		mu.Lock()
		pluginCtx = ctx
		mu.Unlock()
	}

	providerIDs := func() []string {
		if ctx := contextOf(); ctx != nil {
			return ctx.ProviderIDs()
		}
		return nil
	}
	configured := func(id string) bool {
		for _, candidate := range providerIDs() {
			if candidate == id {
				return true
			}
		}
		return false
	}
	logInfo := func(message string) {
		if ctx := contextOf(); ctx != nil {
			if logger := ctx.Logger(); logger != nil {
				logger.Info(message, nil)
			}
		}
	}
	logError := func(message string, err error) {
		if ctx := contextOf(); ctx != nil {
			if logger := ctx.Logger(); logger != nil {
				logger.Error(message, err, nil)
			}
		}
	}

	resolveOrder := func(ctx core.OperationContext) []string {
		if options.OrderFunc != nil {
			return options.OrderFunc(ctx)
		}
		if len(options.Order) > 0 {
			return options.Order
		}
		return append([]string(nil), providerIDs()...)
	}

	isOpen := func(providerID string) bool {
		breaker.mu.Lock()
		defer breaker.mu.Unlock()
		return breaker.isOpen(providerID, time.Now().UnixMilli())
	}
	recordFailure := func(providerID string) {
		breaker.mu.Lock()
		breaker.recordFailure(providerID, time.Now().UnixMilli())
		breaker.mu.Unlock()
	}
	recordSuccess := func(providerID string) {
		breaker.mu.Lock()
		breaker.recordSuccess(providerID)
		breaker.mu.Unlock()
	}

	isTransientFailure := func(view failoverResultView) bool {
		if view.status == core.StatusPending {
			return false
		}
		if view.code == core.CodeNetworkError {
			return false
		}
		if view.status != core.StatusFailure {
			return false
		}
		if view.httpStatus != 0 && failoverUnknownOutcomeStatuses[view.httpStatus] {
			return false
		}
		return codes[string(view.code)] || codes[view.errorCode]
	}

	matcher := func(ctx core.OperationContext) bool {
		return allowed[ctx.Operation] && ctx.Routable
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDFailover,
		Options: options,
		Init: func(_ context.Context, ctx core.PluginContext) error {
			setContext(ctx)
			return nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				Matcher: matcher,
				Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
					current := ctx.Provider
					fullOrder := resolveOrder(ctx)
					if current != "" && isOpen(current) {
						for _, candidate := range fullOrder {
							if configured(candidate) && !isOpen(candidate) {
								if candidate != current {
									logInfo(fmt.Sprintf(
										"[failover] Provider '%s' circuit is open. Routing to healthy provider '%s'.",
										current, candidate))
									return &core.BeforeHookResult{Provider: candidate}, nil
								}
								break
							}
						}
					}
					return nil, nil
				},
			}},
			After: []core.AfterHook{{
				Matcher: func(ctx core.OperationResultContext) bool { return matcher(ctx.OperationContext) },
				Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
					currentProvider := ctx.Provider
					res, ok := failoverViewOf(ctx.Result)

					if !ok || !isTransientFailure(res) {
						if ok && res.status == core.StatusSuccess {
							recordSuccess(currentProvider)
						}
						return nil, nil
					}

					recordFailure(currentProvider)

					fullOrder := resolveOrder(ctx.OperationContext)
					tried := map[string]bool{currentProvider: true}
					backups := make([]string, 0, len(fullOrder))
					for _, candidate := range fullOrder {
						if configured(candidate) && !tried[candidate] && !isOpen(candidate) {
							backups = append(backups, candidate)
						}
					}

					lastResult := any(ctx.Result)
					attempt := 1

					for _, backupProvider := range backups {
						tried[backupProvider] = true
						attempt++

						logInfo(fmt.Sprintf(
							"[failover] Switching payment operation '%s' from '%s' to '%s' (attempt %d).",
							ctx.Operation, currentProvider, backupProvider, attempt))

						if options.OnSwitch != nil {
							err := options.OnSwitch(FailoverEvent{
								From:      currentProvider,
								To:        backupProvider,
								Operation: ctx.Operation,
								Error: FailoverEventError{
									Code:         res.code,
									ErrorCode:    res.errorCode,
									ErrorMessage: res.errorMessage,
								},
								Attempt: attempt,
							})
							if err != nil {
								logError("[failover] onSwitch handler threw an error", err)
							}
						}

						nextResult, err := failoverUseAndCall(contextOf(), backupProvider, ctx.Operation, ctx.Request)
						if err != nil {
							logError(fmt.Sprintf("[failover] Backup provider '%s' call failed", backupProvider), err)
							recordFailure(backupProvider)
							continue
						}
						outcome, isOutcome := failoverViewOf(nextResult)
						if isOutcome && outcome.status == core.StatusSuccess {
							recordSuccess(backupProvider)
							return &core.AfterHookResult{Result: nextResult}, nil
						}
						if !isOutcome || !isTransientFailure(outcome) {
							return &core.AfterHookResult{Result: nextResult}, nil
						}
						recordFailure(backupProvider)
						lastResult = nextResult
					}

					return &core.AfterHookResult{Result: lastResult}, nil
				},
			}},
		},
		Methods: func(_ context.Context, ctx core.PluginContext) (map[string]any, error) {
			setContext(ctx)
			return map[string]any{
				"failover": map[string]any{
					"status": func() map[string]FailoverProviderCircuitStatus {
						breaker.mu.Lock()
						defer breaker.mu.Unlock()
						return breaker.status(providerIDs(), time.Now().UnixMilli())
					},
					// An empty providerID resets every provider.
					"reset": func(providerID string) {
						breaker.mu.Lock()
						breaker.reset(providerID)
						breaker.mu.Unlock()
					},
					"trip": func(providerID string, duration any) error {
						breaker.mu.Lock()
						defer breaker.mu.Unlock()
						return breaker.trip(providerID, duration)
					},
				},
			}, nil
		},
	}), nil
}

func failoverUseAndCall(ctx core.PluginContext, providerID string, operation core.PaymentOperation, request any) (any, error) {
	if ctx == nil {
		return nil, core.NewConfigurationError("failover: plugin is not initialized", "")
	}
	provider, err := ctx.Use(providerID)
	if err != nil {
		return nil, err
	}
	return failoverCall(context.Background(), provider, operation, request)
}

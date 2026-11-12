package plugins

import (
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// ChaosRule is one way to break operations. Every field is optional.
type ChaosRule struct {
	// Operations it applies to. Nil applies to all.
	Operations []core.PaymentOperation
	// Providers it applies to. Nil applies to all.
	Providers []string
	// Probability that the rule fires on a matching call, from 0 to 1. Default: 1.
	Probability *float64
	// Fail returns a failure with this code instead of calling the provider.
	// NETWORK_ERROR returns a pending result, like a lost response.
	Fail core.PaymentErrorCode
	// Delay waits before the call: milliseconds, or "500ms", "3s", "1m".
	Delay any
	// Message is the errorMessage of injected failures.
	Message string
}

// ChaosOptions configures the chaos plugin.
type ChaosOptions struct {
	// Enabled turns injection on. Default: true.
	Enabled *bool
	// Rules are checked in order; the first matching rule that fires is applied.
	Rules []ChaosRule
	// Seed makes runs reproducible: the same seed fires the same rules in the
	// same order. Nil uses a random source.
	Seed *int64
	// AllowProduction allows the plugin in production mode. Default: false, which
	// makes Init fail.
	AllowProduction bool
}

type chaosRule struct {
	operations  []core.PaymentOperation
	providers   []string
	probability float64
	fail        core.PaymentErrorCode
	delayMs     int64
	message     string
}

var chaosDelayUnits = map[string]int64{"ms": 1, "s": 1000, "m": 60000}

var chaosDelayPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m)$`)

func chaosToMs(delay any) (int64, error) {
	switch typed := delay.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case float32:
		return int64(typed), nil
	case string:
		match := chaosDelayPattern.FindStringSubmatch(strings.TrimSpace(typed))
		if match == nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("chaos: invalid delay '%s'. Use milliseconds or '500ms', '3s', '1m'.", typed), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("chaos: invalid delay '%s'. Use milliseconds or '500ms', '3s', '1m'.", typed), "")
		}
		return int64(number * float64(chaosDelayUnits[match[2]])), nil
	default:
		return 0, core.NewConfigurationError(
			fmt.Sprintf("chaos: invalid delay '%v'. Use milliseconds or '500ms', '3s', '1m'.", delay), "")
	}
}

// chaosCodes are the normalized error codes a rule may inject.
var chaosCodes = map[core.PaymentErrorCode]struct{}{
	core.CodeInsufficientFunds:   {},
	core.CodeCardDeclined:        {},
	core.CodeInvalidCard:         {},
	core.CodeExpiredCard:         {},
	core.CodeInvalidCVC:          {},
	core.CodeThreeDSFailed:       {},
	core.CodeFraudSuspected:      {},
	core.CodeLimitExceeded:       {},
	core.CodeDuplicateOrder:      {},
	core.CodeCancelledByCustomer: {},
	core.CodeInvalidRequest:      {},
	core.CodeNetworkError:        {},
	core.CodeInvalidHash:         {},
	core.CodeProviderError:       {},
	core.CodeUnknown:             {},
}

// chaosSeeded is mulberry32: a small seeded random number generator, so a seeded
// run fires the same rules in the same order.
func chaosSeeded(seed uint32) func() float64 {
	state := seed
	return func() float64 {
		state += 0x6d2b79f5
		t := state
		t = (t ^ (t >> 15)) * (t | 1)
		t ^= t + (t^(t>>7))*(t|61)
		return float64(t^(t>>14)) / 4294967296
	}
}

func (r chaosRule) matches(ctx core.OperationContext) bool {
	if r.operations != nil {
		found := false
		for _, operation := range r.operations {
			if operation == ctx.Operation {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if r.providers != nil {
		if ctx.Provider == "" {
			return false
		}
		found := false
		for _, provider := range r.providers {
			if provider == ctx.Provider {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// chaosResult builds the injected result of the operation's own result type, so
// the caller's type assertion still holds.
func chaosResult(operation core.PaymentOperation, status core.PaymentStatus, code core.PaymentErrorCode, message string) any {
	switch operation {
	case core.OperationInitThreeDSPayment, core.OperationInitThreeDSAuthorize:
		return &core.ThreeDSInitResponse{Status: status, Code: code, ErrorCode: string(code), ErrorMessage: message}
	case core.OperationRefund:
		return &core.RefundResponse{Status: status, Code: code, ErrorCode: string(code), ErrorMessage: message}
	case core.OperationCancel, core.OperationVoidAuthorization:
		return &core.CancelResponse{Status: status, Code: code, ErrorCode: string(code), ErrorMessage: message}
	default:
		return &core.PaymentResponse{Status: status, Code: code, ErrorCode: string(code), ErrorMessage: message}
	}
}

type chaosRuntime struct {
	rules           []chaosRule
	allowProduction bool

	mu      sync.Mutex
	enabled bool
	random  func() float64
	pctx    core.PluginContext
}

func (r *chaosRuntime) guard() error {
	r.mu.Lock()
	pctx := r.pctx
	r.mu.Unlock()
	if pctx != nil && pctx.Mode() == core.ModeProduction && !r.allowProduction {
		return core.NewConfigurationError(
			"chaos: refusing to run in production mode. Disable the plugin, or set allowProduction.", "")
	}
	return nil
}

func (r *chaosRuntime) isEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled
}

// draw advances the random source only for a rule that matches, so a seeded run
// stays reproducible no matter how many rules were skipped.
func (r *chaosRuntime) draw() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.random()
}

func (r *chaosRuntime) inject(ctx core.OperationContext) (*core.BeforeHookResult, error) {
	var selected *chaosRule
	for i := range r.rules {
		if r.rules[i].matches(ctx) && r.draw() < r.rules[i].probability {
			selected = &r.rules[i]
			break
		}
	}
	if selected == nil {
		return nil, nil
	}
	if selected.delayMs > 0 {
		if err := pluginSleep(context.Background(), time.Duration(selected.delayMs)*time.Millisecond); err != nil {
			return nil, err
		}
	}
	if selected.fail == "" {
		return nil, nil
	}
	status := core.StatusFailure
	if selected.fail == core.CodeNetworkError {
		status = core.StatusPending
	}
	message := selected.message
	if message == "" {
		message = fmt.Sprintf("Injected by the chaos plugin (%s)", selected.fail)
	}
	return &core.BeforeHookResult{Result: chaosResult(ctx.Operation, status, selected.fail, message)}, nil
}

// ChaosMethods is the "chaos" member the plugin adds to the facade.
type ChaosMethods struct {
	runtime *chaosRuntime
}

// Enable starts injecting failures. It fails in production mode unless
// AllowProduction is set.
func (m *ChaosMethods) Enable() error {
	if err := m.runtime.guard(); err != nil {
		return err
	}
	m.runtime.mu.Lock()
	defer m.runtime.mu.Unlock()
	m.runtime.enabled = true
	return nil
}

// Disable stops injecting failures; operations run normally.
func (m *ChaosMethods) Disable() {
	m.runtime.mu.Lock()
	defer m.runtime.mu.Unlock()
	m.runtime.enabled = false
}

// Enabled reports whether failures are being injected.
func (m *ChaosMethods) Enabled() bool { return m.runtime.isEnabled() }

// Chaos injects failures and delays into operations, so an application can be
// tested against declines, lost responses (NETWORK_ERROR) and slow providers.
// Failed calls never reach the provider. It refuses to run in production mode
// unless AllowProduction is set.
func Chaos(options ChaosOptions) (core.Plugin, error) {
	rules := make([]chaosRule, 0, len(options.Rules))
	for _, rule := range options.Rules {
		probability := 1.0
		if rule.Probability != nil {
			probability = *rule.Probability
		}
		if !(probability >= 0 && probability <= 1) {
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("chaos: probability must be between 0 and 1, got %s.",
					strconv.FormatFloat(probability, 'g', -1, 64)), "")
		}
		if rule.Fail != "" {
			if _, known := chaosCodes[rule.Fail]; !known {
				return core.Plugin{}, core.NewConfigurationError(
					fmt.Sprintf("chaos: unknown error code '%s'.", rule.Fail), "")
			}
		}
		var delayMs int64
		if rule.Delay != nil {
			parsed, err := chaosToMs(rule.Delay)
			if err != nil {
				return core.Plugin{}, err
			}
			delayMs = parsed
		}
		rules = append(rules, chaosRule{
			operations:  rule.Operations,
			providers:   rule.Providers,
			probability: probability,
			fail:        rule.Fail,
			delayMs:     delayMs,
			message:     rule.Message,
		})
	}

	random := rand.Float64
	if options.Seed != nil {
		random = chaosSeeded(uint32(uint64(*options.Seed)))
	}
	runtime := &chaosRuntime{
		rules:           rules,
		allowProduction: options.AllowProduction,
		enabled:         options.Enabled == nil || *options.Enabled,
		random:          random,
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDChaos,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			runtime.mu.Lock()
			runtime.pctx = pctx
			runtime.mu.Unlock()
			if runtime.isEnabled() {
				return runtime.guard()
			}
			return nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{
				{
					Matcher: func(core.OperationContext) bool { return runtime.isEnabled() },
					Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
						return runtime.inject(ctx)
					},
				},
			},
		},
		Methods: func(_ context.Context, pctx core.PluginContext) (map[string]any, error) {
			runtime.mu.Lock()
			runtime.pctx = pctx
			runtime.mu.Unlock()
			return map[string]any{"chaos": &ChaosMethods{runtime: runtime}}, nil
		},
	}), nil
}

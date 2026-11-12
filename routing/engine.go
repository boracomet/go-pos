package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/boracomet/go-pos/core"
)

// ChargeOperations start a charge and can be routed by rules.
var ChargeOperations = []core.PaymentOperation{
	core.OperationCreatePayment,
	core.OperationAuthorize,
	core.OperationInitThreeDSPayment,
	core.OperationInitThreeDSAuthorize,
}

// FollowUpOperations go to the provider that handled the payment.
var FollowUpOperations = []core.PaymentOperation{
	core.OperationCompleteThreeDSPayment,
	core.OperationRefund,
	core.OperationCancel,
	core.OperationCapture,
	core.OperationVoidAuthorization,
	core.OperationGetPayment,
}

const (
	maxAmountDigits = 10
	amountScale     = 10_000
)

var (
	chargeSet    = operationSet(ChargeOperations)
	followUpSet  = operationSet(FollowUpOperations)
	allOpSet     = operationSet(core.AllPaymentOperations)
	amountRegexp = regexp.MustCompile(`^(\d{1,` + strconv.Itoa(maxAmountDigits) + `})(?:\.(\d{1,4}))?$`)
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

// IsFollowUpOperation reports whether the operation must reach the provider that
// handled the payment.
func IsFollowUpOperation(operation core.PaymentOperation) bool { return followUpSet[operation] }

func fail(message string) error {
	return core.NewConfigurationError("routing: "+message, "")
}

func failf(format string, args ...any) error {
	return fail(fmt.Sprintf(format, args...))
}

// text mirrors the source's text(): a non-empty trimmed string, or "".
func text(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

// count mirrors the source's count(): an integer of at least 1.
func count(value any) (int, bool) {
	number, ok := numberOf(value)
	if !ok || number != math.Trunc(number) || number < 1 || number > math.MaxInt32 {
		return 0, false
	}
	return int(number), true
}

func numberOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func integerOf(value any) *int {
	number, ok := numberOf(value)
	if !ok || number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
		return nil
	}
	parsed := int(number)
	return &parsed
}

func boolOf(value any) *bool {
	typed, ok := value.(bool)
	if !ok {
		return nil
	}
	return &typed
}

// minorUnits parses a non-negative decimal string into 1/10000 units, exactly
// enough for the amount bounds.
func minorUnits(value string) (int64, bool) {
	match := amountRegexp.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, false
	}
	whole, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	fraction := match[2]
	for len(fraction) < 4 {
		fraction += "0"
	}
	part, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, false
	}
	return whole*amountScale + part, true
}

// Bucket is FNV-1a plus a murmur3 finalizer: cheap, stable and well spread for
// short keys. It hashes UTF-16 code units, so a key buckets identically to the
// reference implementation.
func Bucket(key string) uint32 {
	hash := uint32(0x811c9dc5)
	for _, unit := range utf16.Encode([]rune(key)) {
		hash ^= uint32(unit)
		hash *= 0x01000193
	}
	hash ^= hash >> 16
	hash *= 0x85ebca6b
	hash ^= hash >> 13
	hash *= 0xc2b2ae35
	hash ^= hash >> 16
	return hash
}

// SplitKey is the key a weighted split is derived from: the order, never
// randomness, so a retry of the same payment always sees the same bucket.
// Identical requests without any id share a bucket; pass ConversationID for real
// traffic splits.
func SplitKey(request Request, operation core.PaymentOperation) string {
	if request.ConversationID != "" {
		return request.ConversationID
	}
	if request.PaymentID != "" {
		return request.PaymentID
	}
	if request.BasketID != "" {
		return request.BasketID
	}
	installment := 1
	if request.Installment != nil && *request.Installment >= 1 {
		installment = *request.Installment
	}
	return fmt.Sprintf("%s|%s|%s|%d", operation, request.Currency, request.Price, installment)
}

func pushSkip(skipped []Skip, provider string, reason SkipReason) []Skip {
	for _, entry := range skipped {
		if entry.Provider == provider {
			return skipped
		}
	}
	return append(skipped, Skip{Provider: provider, Reason: reason})
}

func mergeSkips(into []Skip, from []Skip) []Skip {
	for _, skip := range from {
		into = pushSkip(into, skip.Provider, skip.Reason)
	}
	return into
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// dedupe keeps the first occurrence of every id.
func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func assertTarget(target *Target, where string) error {
	switch target.kind() {
	case targetProvider:
		if strings.TrimSpace(target.Provider) == "" {
			return failf("%s must not be an empty provider id", where)
		}
		return nil
	case targetList:
		if len(target.Providers) == 0 {
			return failf("%s must list at least one provider", where)
		}
		for _, id := range target.Providers {
			if strings.TrimSpace(id) == "" {
				return failf("%s must contain provider ids", where)
			}
		}
		return nil
	case targetWeights:
		if len(target.Weights) == 0 {
			return failf("%s.weights must not be empty", where)
		}
		positive := 0
		for _, entry := range target.Weights {
			if strings.TrimSpace(entry.Provider) == "" {
				return failf("%s.weights must not contain an empty provider id", where)
			}
			if math.IsNaN(entry.Weight) || math.IsInf(entry.Weight, 0) || entry.Weight < 0 {
				return failf("%s.weights.%s must be a non-negative number", where, entry.Provider)
			}
			if entry.Weight > 0 {
				positive++
			}
		}
		if positive == 0 {
			return failf("%s.weights must give at least one provider a positive weight", where)
		}
		return nil
	default:
		return failf("%s must be a provider id, a list or { weights }", where)
	}
}

func assertStringList(list StringList, where string) error {
	if len(list) == 0 {
		return failf("%s must be a non-empty string or an array of them", where)
	}
	for _, entry := range list {
		if strings.TrimSpace(entry) == "" {
			return failf("%s must be a non-empty string or an array of them", where)
		}
	}
	return nil
}

func assertWhen(when *When, where string) error {
	for _, field := range []struct {
		name string
		list StringList
	}{
		{"bin", when.Bin},
		{"bank", when.Bank},
		{"cardFamily", when.CardFamily},
		{"currency", when.Currency},
	} {
		if field.list == nil {
			continue
		}
		if err := assertStringList(field.list, where+".when."+field.name); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name    string
		list    StringList
		allowed []string
	}{
		{"cardType", when.CardType, []string{"credit", "debit"}},
		{"cardOrigin", when.CardOrigin, []string{"domestic", "foreign"}},
	} {
		if field.list == nil {
			continue
		}
		if len(field.list) == 0 {
			return failf("%s.when.%s must not be empty", where, field.name)
		}
		for _, entry := range field.list {
			if !contains(field.allowed, entry) {
				return failf("%s.when.%s must be %s", where, field.name, strings.Join(field.allowed, " or "))
			}
		}
	}
	if when.Operation != nil {
		if len(when.Operation) == 0 {
			return failf("%s.when.operation must not be empty", where)
		}
		for _, operation := range when.Operation {
			if !allOpSet[core.PaymentOperation(operation)] {
				return failf("%s.when.operation must be a payment operation", where)
			}
		}
	}
	if when.Installment != nil {
		if when.Installment.Exact != nil {
			if *when.Installment.Exact < 1 {
				return failf("%s.when.installment must be an integer of at least 1", where)
			}
		} else {
			minimum, maximum := when.Installment.Min, when.Installment.Max
			if minimum == nil && maximum == nil {
				return failf("%s.when.installment needs min or max", where)
			}
			if minimum != nil && *minimum < 1 {
				return failf("%s.when.installment.min must be an integer of at least 1", where)
			}
			if maximum != nil && *maximum < 1 {
				return failf("%s.when.installment.max must be an integer of at least 1", where)
			}
			if minimum != nil && maximum != nil && *minimum > *maximum {
				return failf("%s.when.installment.min must not exceed max", where)
			}
		}
	}
	if when.Amount != nil {
		if when.Amount.Min == "" && when.Amount.Max == "" {
			return failf("%s.when.amount needs min or max", where)
		}
		var minimum, maximum int64
		var hasMin, hasMax bool
		if when.Amount.Min != "" {
			minimum, hasMin = minorUnits(when.Amount.Min)
			if !hasMin {
				return failf("%s.when.amount.min must be a non-negative decimal string", where)
			}
		}
		if when.Amount.Max != "" {
			maximum, hasMax = minorUnits(when.Amount.Max)
			if !hasMax {
				return failf("%s.when.amount.max must be a non-negative decimal string", where)
			}
		}
		if hasMin && hasMax && minimum > maximum {
			return failf("%s.when.amount.min must not exceed max", where)
		}
	}
	return nil
}

// ValidateOptions fails fast on a bad configuration, before the first payment.
func ValidateOptions(options Options) error {
	for name, strategy := range options.Strategies {
		if strategy == nil || strategy.Choose == nil {
			return failf("strategies.%s must have a choose() function", name)
		}
	}
	for index := range options.Rules {
		rule := &options.Rules[index]
		where := fmt.Sprintf("rules[%d]", index)
		if rule.When != nil {
			if err := assertWhen(rule.When, where); err != nil {
				return err
			}
		}
		if rule.Use != nil {
			if err := assertTarget(rule.Use, where+".use"); err != nil {
				return err
			}
		}
		switch {
		case rule.StrategyRef != nil:
			if rule.StrategyRef.Choose == nil {
				return failf("%s.strategy must be a name or an object with choose()", where)
			}
		case rule.Strategy != "":
			if options.Strategies[rule.Strategy] == nil {
				return failf("%s.strategy '%s' is not in strategies", where, rule.Strategy)
			}
		default:
			if rule.Use == nil {
				return failf("%s needs use or strategy", where)
			}
		}
	}
	if options.Default != nil {
		return assertTarget(options.Default, "default")
	}
	return nil
}

func matches(value string, expected StringList) bool {
	if expected == nil {
		return true
	}
	if value == "" {
		return false
	}
	return matchesAny(value, expected)
}

func matchesAny(value string, expected []string) bool {
	if value == "" {
		return false
	}
	normalized := strings.ToLower(value)
	for _, entry := range expected {
		if strings.ToLower(entry) == normalized {
			return true
		}
	}
	return false
}

func matchesWhen(when *When, ctx *Context, where string) (bool, error) {
	if when == nil {
		return true, nil
	}
	if when.Bin != nil {
		if ctx.Bin == "" {
			return false, nil
		}
		found := false
		for _, prefix := range when.Bin {
			if strings.HasPrefix(ctx.Bin, prefix) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	if when.Bank != nil && !matches(ctx.Bank, when.Bank) {
		return false, nil
	}
	if when.CardFamily != nil && !matches(ctx.CardFamily, when.CardFamily) {
		return false, nil
	}
	if when.CardType != nil && !matchesAny(ctx.CardType, when.CardType) {
		return false, nil
	}
	if when.CardOrigin != nil && !matchesAny(ctx.CardOrigin, when.CardOrigin) {
		return false, nil
	}
	if when.ThreeDS != nil && ctx.ThreeDS != *when.ThreeDS {
		return false, nil
	}
	if when.Operation != nil && !contains(when.Operation, string(ctx.Operation)) {
		return false, nil
	}
	if when.Currency != nil && !matches(ctx.Currency, when.Currency) {
		return false, nil
	}
	if when.Installment != nil {
		if when.Installment.Exact != nil {
			if ctx.Installment != *when.Installment.Exact {
				return false, nil
			}
		} else {
			if when.Installment.Min != nil && ctx.Installment < *when.Installment.Min {
				return false, nil
			}
			if when.Installment.Max != nil && ctx.Installment > *when.Installment.Max {
				return false, nil
			}
		}
	}
	if when.Amount != nil {
		if ctx.Amount == "" {
			return false, nil
		}
		amount, ok := minorUnits(ctx.Amount)
		if !ok {
			return false, nil
		}
		if when.Amount.Min != "" {
			minimum, ok := minorUnits(when.Amount.Min)
			if !ok || amount < minimum {
				return false, nil
			}
		}
		if when.Amount.Max != "" {
			maximum, ok := minorUnits(when.Amount.Max)
			if !ok || amount > maximum {
				return false, nil
			}
		}
	}
	if when.Predicate != nil {
		matched, err := callPredicate(when.Predicate, ctx)
		if err != nil {
			return false, failf("%s.when.predicate threw: %s", where, err.Error())
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

// callPredicate turns a panic in merchant code into an error, like a throw.
func callPredicate(predicate func(*Context) (bool, error), ctx *Context) (matched bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			matched, err = false, recoveredError(recovered)
		}
	}()
	return predicate(ctx)
}

func recoveredError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("%v", recovered)
}

type selection struct {
	provider    string
	skipped     []Skip
	viaStrategy bool
}

// Engine is a rule engine bound to one payment object's providers. It has no
// side effects on the store; the plugin records mappings around it.
type Engine struct {
	options         Options
	providerIDs     []string
	defaultProvider string
	logger          core.Logger
}

// NewEngine validates the options and binds the engine to the configured
// providers.
func NewEngine(options Options, ectx EngineContext) (*Engine, error) {
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}
	return &Engine{
		options:         options,
		providerIDs:     append([]string(nil), ectx.ProviderIDs...),
		defaultProvider: ectx.DefaultProvider,
		logger:          ectx.Logger,
	}, nil
}

// Decide applies the rules, then the default. Used for new charges.
func (e *Engine) Decide(ctx context.Context, operation core.PaymentOperation, request any) (*Decision, error) {
	routingCtx, err := e.contextFor(ctx, operation, request)
	if err != nil {
		return nil, err
	}
	skipped := []Skip{}

	for index := range e.options.Rules {
		rule := &e.options.Rules[index]
		name := ruleName(rule, index)
		matched, err := matchesWhen(rule.When, routingCtx, name)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		chosen, err := e.selectRule(ctx, rule, routingCtx)
		if err != nil {
			return nil, err
		}
		skipped = mergeSkips(skipped, chosen.skipped)
		if chosen.provider != "" {
			reason := ReasonRule
			if chosen.viaStrategy {
				reason = ReasonStrategy
			}
			return &Decision{
				Provider:  chosen.provider,
				Rule:      stringPtr(name),
				RuleIndex: intPtr(index),
				Reason:    reason,
				Operation: operation,
				Skipped:   skipped,
				Detail:    fmt.Sprintf("%s matched; '%s' is the first eligible choice", name, chosen.provider),
			}, nil
		}
	}

	return e.fallbackFor(ctx, routingCtx, skipped, ReasonDefault, true)
}

// Fallback applies the default target only (no rules): follow-ups of unknown
// payments use it.
func (e *Engine) Fallback(ctx context.Context, operation core.PaymentOperation, request any, skipped []Skip, reason Reason, applyHealth bool) (*Decision, error) {
	routingCtx, err := e.contextFor(ctx, operation, request)
	if err != nil {
		return nil, err
	}
	return e.fallbackFor(ctx, routingCtx, skipped, reason, applyHealth)
}

func (e *Engine) fallbackFor(ctx context.Context, routingCtx *Context, skipped []Skip, reason Reason, applyHealth bool) (*Decision, error) {
	if skipped == nil {
		skipped = []Skip{}
	}
	target := e.options.Default
	if target == nil && e.defaultProvider != "" {
		target = ProviderTarget(e.defaultProvider)
	}
	if target == nil {
		return &Decision{
			Provider:  "",
			Reason:    ReasonNone,
			Operation: routingCtx.Operation,
			Skipped:   skipped,
			Detail:    "no rule matched and no default target is configured",
		}, nil
	}
	chosen, err := e.selectTarget(ctx, target, routingCtx, applyHealth)
	if err != nil {
		return nil, err
	}
	skipped = mergeSkips(skipped, chosen.skipped)
	decision := &Decision{
		Provider:  chosen.provider,
		Reason:    reason,
		Operation: routingCtx.Operation,
		Skipped:   skipped,
	}
	if chosen.provider == "" {
		decision.Reason = ReasonNone
		decision.Detail = "no default provider is eligible"
		return decision, nil
	}
	if reason == ReasonUnknownPayment {
		decision.Detail = fmt.Sprintf("the payment is unknown; fell back to '%s'", chosen.provider)
	} else {
		decision.Detail = fmt.Sprintf("default target selected '%s'", chosen.provider)
	}
	return decision, nil
}

func (e *Engine) selectRule(ctx context.Context, rule *Rule, routingCtx *Context) (selection, error) {
	if rule.StrategyRef != nil || rule.Strategy != "" {
		strategy := rule.StrategyRef
		if strategy == nil {
			strategy = e.options.Strategies[rule.Strategy]
		}
		if strategy == nil || strategy.Choose == nil {
			return selection{}, failf("strategy '%s' is not configured", rule.Strategy)
		}
		ids := e.providerIDs
		if rule.Use != nil {
			ids = rule.Use.ids()
		}
		eligible, skipped, err := e.eligible(ctx, ids)
		if err != nil {
			return selection{}, err
		}
		if len(eligible) == 0 {
			return selection{skipped: skipped}, nil
		}
		chosen, err := callStrategy(ctx, strategy, StrategyContext{
			Operation:  routingCtx.Operation,
			Request:    routingCtx.Request,
			Candidates: eligible,
		})
		if err != nil {
			return selection{}, err
		}
		if chosen == "" {
			return selection{skipped: skipped}, nil
		}
		if !contains(eligible, chosen) {
			return selection{}, failf("strategy chose '%s', which is not an eligible candidate", chosen)
		}
		return selection{provider: chosen, skipped: skipped, viaStrategy: true}, nil
	}
	if rule.Use == nil {
		return selection{}, fail("rule needs use or strategy")
	}
	return e.selectTarget(ctx, rule.Use, routingCtx, true)
}

func callStrategy(ctx context.Context, strategy *Strategy, sctx StrategyContext) (chosen string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			chosen, err = "", recoveredError(recovered)
		}
	}()
	return strategy.Choose(ctx, sctx)
}

func (e *Engine) selectTarget(ctx context.Context, target *Target, routingCtx *Context, applyHealth bool) (selection, error) {
	skipped := []Skip{}
	switch target.kind() {
	case targetProvider, targetList:
		for _, provider := range dedupe(target.ids()) {
			reason, err := e.ineligibility(ctx, provider, applyHealth)
			if err != nil {
				return selection{}, err
			}
			if reason != "" {
				skipped = pushSkip(skipped, provider, reason)
				continue
			}
			return selection{provider: provider, skipped: skipped}, nil
		}
		return selection{skipped: skipped}, nil
	case targetWeights:
		type weighted struct {
			provider string
			weight   float64
		}
		eligible := make([]weighted, 0, len(target.Weights))
		for _, entry := range target.Weights {
			if entry.Weight <= 0 {
				continue
			}
			reason, err := e.ineligibility(ctx, entry.Provider, applyHealth)
			if err != nil {
				return selection{}, err
			}
			if reason != "" {
				skipped = pushSkip(skipped, entry.Provider, reason)
				continue
			}
			eligible = append(eligible, weighted{provider: entry.Provider, weight: entry.Weight})
		}
		if len(eligible) == 0 {
			return selection{skipped: skipped}, nil
		}
		total := 0.0
		for _, entry := range eligible {
			total += entry.weight
		}
		bucketValue := math.Mod(float64(Bucket(SplitKey(routingCtx.Request, routingCtx.Operation))), total)
		for _, entry := range eligible {
			if bucketValue < entry.weight {
				return selection{provider: entry.provider, skipped: skipped}, nil
			}
			bucketValue -= entry.weight
		}
		// Floating point weights can add up to less than total in the last bucket.
		return selection{provider: eligible[len(eligible)-1].provider, skipped: skipped}, nil
	default:
		return selection{}, fail("target must be a provider id, a list or { weights }")
	}
}

func (e *Engine) eligible(ctx context.Context, ids []string) ([]string, []Skip, error) {
	eligible := []string{}
	skipped := []Skip{}
	for _, provider := range dedupe(ids) {
		reason, err := e.ineligibility(ctx, provider, true)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			skipped = pushSkip(skipped, provider, reason)
			continue
		}
		eligible = append(eligible, provider)
	}
	return eligible, skipped, nil
}

func (e *Engine) ineligibility(ctx context.Context, provider string, applyHealth bool) (SkipReason, error) {
	if !contains(e.providerIDs, provider) {
		return SkipNotEnabled, nil
	}
	if e.options.Health == nil || e.options.Health.IsAvailable == nil || !applyHealth {
		return "", nil
	}
	available, err := callHealth(ctx, e.options.Health.IsAvailable, provider)
	if err != nil {
		if e.logger != nil {
			e.logger.Error(fmt.Sprintf("[routing] health check for '%s' threw; treating it as unavailable", provider), err, nil)
		}
		return SkipUnhealthy, nil
	}
	if !available {
		return SkipUnhealthy, nil
	}
	return "", nil
}

func callHealth(ctx context.Context, isAvailable func(context.Context, string) (bool, error), provider string) (available bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			available, err = false, recoveredError(recovered)
		}
	}()
	return isAvailable(ctx, provider)
}

func (e *Engine) contextFor(ctx context.Context, operation core.PaymentOperation, request any) (*Context, error) {
	record := toRecord(request)
	card := recordOf(record["card"])
	paymentCard := recordOf(record["paymentCard"])
	digits := onlyDigits(text(paymentCard["cardNumber"]))
	explicitBin := firstText(record["binNumber"], record["bin"], card["bin"])
	bin := ""
	if explicitBin != "" {
		binDigits := onlyDigits(explicitBin)
		if len(binDigits) >= 6 {
			bin = binDigits[:min(8, len(binDigits))]
		}
	} else if len(digits) >= 6 {
		bin = digits[:min(8, len(digits))]
	}

	resolved := &BinInfo{}
	if bin != "" && e.options.BinResolver != nil {
		info, err := callBinResolver(ctx, e.options.BinResolver, bin)
		if err != nil {
			if e.logger != nil {
				e.logger.Error("[routing] binResolver threw; BIN conditions are treated as unknown", err, nil)
			}
		} else if info != nil {
			resolved = info
		}
	}

	installment := 1
	if value, ok := count(record["installment"]); ok {
		installment = value
	}
	threeDS := operation == core.OperationInitThreeDSPayment || operation == core.OperationInitThreeDSAuthorize
	if !threeDS {
		threeDS, _ = record["threeDS"].(bool)
	}

	return &Context{
		Operation:   operation,
		Request:     requestFromRecord(record),
		Amount:      firstText(record["price"], record["amount"]),
		Currency:    text(record["currency"]),
		Installment: installment,
		ThreeDS:     threeDS,
		Bin:         bin,
		Bank: firstText(record["bank"], record["bankName"], card["bank"],
			resolved.Bank, resolved.BankName),
		CardFamily: firstText(record["cardFamily"], card["cardFamily"], resolved.CardFamily),
		CardType:   cardTypeOf(firstText(record["cardType"], card["cardType"], resolved.CardType)),
		CardOrigin: cardOriginOf(firstText(record["cardOrigin"], card["cardOrigin"], resolved.CardOrigin)),
	}, nil
}

func callBinResolver(ctx context.Context, resolver func(context.Context, string) (*BinInfo, error), bin string) (info *BinInfo, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			info, err = nil, recoveredError(recovered)
		}
	}()
	return resolver(ctx, bin)
}

func firstText(values ...any) string {
	for _, value := range values {
		if found := text(value); found != "" {
			return found
		}
	}
	return ""
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// cardTypeOf accepts provider BIN values such as CREDIT_CARD / DEBIT_CARD too.
func cardTypeOf(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.ToLower(value)
	if strings.Contains(normalized, "debit") {
		return "debit"
	}
	if strings.Contains(normalized, "credit") {
		return "credit"
	}
	return normalized
}

func cardOriginOf(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.ToLower(value)
	switch normalized {
	case "international":
		return "foreign"
	case "local":
		return "domestic"
	default:
		return normalized
	}
}

func ruleName(rule *Rule, index int) string {
	if rule.Name != "" {
		return rule.Name
	}
	return fmt.Sprintf("rule[%d]", index)
}

func stringPtr(value string) *string { return &value }
func intPtr(value int) *int          { return &value }

// toRecord mirrors asRecord(): only objects become records. Structs are read
// through their JSON field names, so any core request type works unchanged.
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

func recordOf(value any) map[string]any {
	if value == nil {
		return nil
	}
	return toRecord(value)
}

func requestFromRecord(record map[string]any) Request {
	request := Request{
		Price:          text(record["price"]),
		PaidPrice:      text(record["paidPrice"]),
		Amount:         text(record["amount"]),
		Currency:       text(record["currency"]),
		Installment:    integerOf(record["installment"]),
		ThreeDS:        boolOf(record["threeDS"]),
		ConversationID: text(record["conversationId"]),
		BasketID:       text(record["basketId"]),
		PaymentID:      text(record["paymentId"]),
		BinNumber:      text(record["binNumber"]),
		Bin:            text(record["bin"]),
		Bank:           text(record["bank"]),
		BankName:       text(record["bankName"]),
		CardFamily:     text(record["cardFamily"]),
		CardType:       text(record["cardType"]),
		CardOrigin:     text(record["cardOrigin"]),
	}
	if card := recordOf(record["card"]); card != nil {
		request.Card = &CardInfo{
			BinInfo: BinInfo{
				Bank:       text(card["bank"]),
				BankName:   text(card["bankName"]),
				CardFamily: text(card["cardFamily"]),
				CardType:   text(card["cardType"]),
				CardOrigin: text(card["cardOrigin"]),
			},
			Bin: text(card["bin"]),
		}
	}
	if paymentCard := recordOf(record["paymentCard"]); paymentCard != nil {
		request.PaymentCard = &CardNumber{CardNumber: text(paymentCard["cardNumber"])}
	}
	return request
}

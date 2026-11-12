package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func TestNoOptionsUsesTheLibraryDefaultProvider(t *testing.T) {
	h := newHarness(t, Options{}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" || decision.Reason != ReasonDefault {
		t.Fatalf("got provider %q reason %q", decision.Provider, decision.Reason)
	}
	if decision.Rule != nil || decision.RuleIndex != nil {
		t.Fatalf("expected no rule, got %v / %v", decision.Rule, decision.RuleIndex)
	}
	if len(decision.Skipped) != 0 {
		t.Fatalf("expected no skips, got %v", decision.Skipped)
	}
	result, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Fatalf("expected success, got %v", result)
	}
	if h.providers["a"].paymentCount() != 1 {
		t.Fatalf("expected one payment on a")
	}
}

func TestOnlyConfiguredProviderWithoutDefault(t *testing.T) {
	h := newHarness(t, Options{}, []string{"only"}, "")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "only" || decision.Reason != ReasonDefault {
		t.Fatalf("got %+v", decision)
	}
}

func TestDoesNotGuessWithoutDefaultOrEligibleRule(t *testing.T) {
	h := newHarness(t, Options{}, []string{"a", "b"}, "")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "" || decision.Reason != ReasonNone {
		t.Fatalf("got %+v", decision)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		options Options
	}{
		{"bad card type", Options{Rules: []Rule{{Use: ProviderTarget("a"), When: &When{CardType: StringList{"prepaid"}}}}}},
		{"rule without target", Options{Rules: []Rule{{When: &When{}}}}},
		{"unknown strategy", Options{Rules: []Rule{{Strategy: "missing"}}}},
		{"zero weights", Options{Default: WeightsTarget(Weight{"a", 0}, Weight{"b", 0})}},
		{"negative weight", Options{Default: WeightsTarget(Weight{"a", -1}, Weight{"b", 2})}},
		{"empty list", Options{Default: &Target{Providers: []string{}}}},
		{"bad amount", Options{Rules: []Rule{{Use: ProviderTarget("a"), When: &When{Amount: &AmountRange{Min: "1,00"}}}}}},
		{"inverted amount", Options{Rules: []Rule{{Use: ProviderTarget("a"), When: &When{Amount: &AmountRange{Min: "2", Max: "1"}}}}}},
		{"inverted installment", Options{Rules: []Rule{{Use: ProviderTarget("a"), When: &When{Installment: &InstallmentCondition{Min: intPtr(3), Max: intPtr(2)}}}}}},
		{"strategy without choose", Options{Rules: []Rule{{StrategyRef: &Strategy{}}}}},
		{"named strategy without choose", Options{Strategies: map[string]*Strategy{"broken": {}}, Rules: []Rule{{Strategy: "broken"}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.options); !isConfigurationError(err) {
				t.Fatalf("expected a ConfigurationError, got %v", err)
			}
		})
	}
}

func TestThrowingPredicateBecomesConfigurationError(t *testing.T) {
	h := newHarness(t, Options{Rules: []Rule{{
		When: &When{Predicate: func(*Context) (bool, error) { panic("boom") }},
		Use:  ProviderTarget("a"),
	}}}, []string{"a", "b"}, "a")
	if _, err := h.explain(baseRequest(), ""); !isConfigurationError(err) {
		t.Fatalf("expected a ConfigurationError, got %v", err)
	}
}

func TestMatchesEverySupportedWhenField(t *testing.T) {
	cases := []struct {
		label     string
		when      When
		request   map[string]any
		operation core.PaymentOperation
	}{
		{"bin prefix", When{Bin: StringList{"5528"}}, withRequest(map[string]any{"binNumber": "552879"}), ""},
		{"bin from the card number", When{Bin: StringList{"552879"}}, baseRequest(), ""},
		{"bank", When{Bank: StringList{"garanti bbva"}}, withRequest(map[string]any{"bank": "Garanti BBVA"}), ""},
		{"card family", When{CardFamily: StringList{"bonus"}}, withRequest(map[string]any{"cardFamily": "Bonus"}), ""},
		{"card type in any casing", When{CardType: StringList{"debit"}}, withRequest(map[string]any{"cardType": "DEBIT_CARD"}), ""},
		{"foreign cards", When{CardOrigin: StringList{"foreign"}}, withRequest(map[string]any{"cardOrigin": "international"}), ""},
		{"threeDS by operation", When{ThreeDS: boolPtr(true)}, threeDSRequest(), core.OperationInitThreeDSPayment},
		{"operation", When{Operation: StringList{"authorize"}}, baseRequest(), core.OperationAuthorize},
		{"currency", When{Currency: StringList{"usd"}}, withRequest(map[string]any{"currency": "USD"}), ""},
		{"exact installment", When{Installment: &InstallmentCondition{Exact: intPtr(6)}}, withRequest(map[string]any{"installment": 6}), ""},
		{"installment range", When{Installment: &InstallmentCondition{Min: intPtr(2), Max: intPtr(6)}}, withRequest(map[string]any{"installment": 3}), ""},
		{"amount min", When{Amount: &AmountRange{Min: "100"}}, withRequest(map[string]any{"price": "100.00"}), ""},
		{"amount max", When{Amount: &AmountRange{Max: "50.00"}}, withRequest(map[string]any{"price": "49.99"}), ""},
		{"predicate", When{Predicate: func(ctx *Context) (bool, error) {
			amount, ok := minorUnits(ctx.Request.Price)
			return ok && amount > 5000, nil
		}}, baseRequest(), ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(t *testing.T) {
			when := testCase.when
			h := newHarness(t, Options{
				Rules:   []Rule{{Name: testCase.label, When: &when, Use: ProviderTarget("a")}},
				Default: ProviderTarget("b"),
			}, []string{"a", "b"}, "a")
			decision, err := h.explain(testCase.request, testCase.operation)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			if decision.Provider != "a" {
				t.Fatalf("expected a, got %+v", decision)
			}
		})
	}
}

func TestRequiresEveryConditionToMatch(t *testing.T) {
	h := newHarness(t, Options{
		Rules: []Rule{{
			Name: "bonus installments",
			When: &When{CardFamily: StringList{"Bonus"}, Installment: &InstallmentCondition{Min: intPtr(2)}, Currency: StringList{"TRY"}},
			Use:  ProviderTarget("a"),
		}},
		Default: ProviderTarget("b"),
	}, []string{"a", "b"}, "a")

	cases := []struct {
		request map[string]any
		want    string
	}{
		{withRequest(map[string]any{"cardFamily": "Bonus", "installment": 3}), "a"},
		{withRequest(map[string]any{"cardFamily": "Bonus", "installment": 1}), "b"},
		{withRequest(map[string]any{"cardFamily": "World", "installment": 3}), "b"},
		{withRequest(map[string]any{"cardFamily": "Bonus", "installment": 3, "currency": "USD"}), "b"},
	}
	for _, testCase := range cases {
		decision, err := h.explain(testCase.request, "")
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if decision.Provider != testCase.want {
			t.Fatalf("expected %s, got %+v", testCase.want, decision)
		}
	}
}

func TestInvalidRequestAmountDoesNotMatch(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{Amount: &AmountRange{Min: "1"}}, Use: ProviderTarget("a")}},
		Default: ProviderTarget("b"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(withRequest(map[string]any{"price": "not-a-number"}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" {
		t.Fatalf("expected b, got %+v", decision)
	}
}

func TestBinResolverClassifiesWithoutNetwork(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	h := newHarness(t, Options{
		BinResolver: func(_ context.Context, bin string) (*BinInfo, error) {
			mu.Lock()
			seen = append(seen, bin)
			mu.Unlock()
			return &BinInfo{BankName: "Garanti BBVA", CardFamily: "Bonus", CardType: "CREDIT_CARD", CardOrigin: "domestic"}, nil
		},
		Rules: []Rule{{When: &When{
			Bin:        StringList{"552879"},
			Bank:       StringList{"Garanti BBVA"},
			CardFamily: StringList{"Bonus"},
			CardType:   StringList{"credit"},
			CardOrigin: StringList{"domestic"},
		}, Use: ProviderTarget("a")}},
		Default: ProviderTarget("b"),
	}, []string{"a", "b"}, "a")

	decision, err := h.explain(withRequest(map[string]any{
		"paymentCard": map[string]any{"cardNumber": "5528790000000008"},
	}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" {
		t.Fatalf("expected a, got %+v", decision)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "55287900" {
		t.Fatalf("expected the 8-digit prefix, got %v", seen)
	}
}

func TestExplicitRequestFieldsWinOverTheResolver(t *testing.T) {
	h := newHarness(t, Options{
		BinResolver: func(context.Context, string) (*BinInfo, error) { return &BinInfo{CardFamily: "World"}, nil },
		Rules:       []Rule{{When: &When{CardFamily: StringList{"Bonus"}}, Use: ProviderTarget("a")}},
		Default:     ProviderTarget("b"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(withRequest(map[string]any{"cardFamily": "Bonus", "binNumber": "552879"}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" {
		t.Fatalf("expected a, got %+v", decision)
	}
}

func TestThrowingResolverIsUnknownBinData(t *testing.T) {
	logger := &recordingLogger{}
	h := newHarnessWithLogger(t, Options{
		BinResolver: func(context.Context, string) (*BinInfo, error) { panic("lookup down") },
		Rules:       []Rule{{When: &When{CardFamily: StringList{"Bonus"}}, Use: ProviderTarget("a")}},
		Default:     ProviderTarget("b"),
	}, []string{"a", "b"}, "a", logger)

	decision, err := h.explain(withRequest(map[string]any{
		"paymentCard": map[string]any{"cardNumber": "5528790000000008"},
	}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" {
		t.Fatalf("expected b, got %+v", decision)
	}
	if strings.Contains(logger.joined(), "5528790000000008") {
		t.Fatalf("the BIN resolver failure logged card data")
	}
}

func TestTakesTheFirstMatchingRuleInOrder(t *testing.T) {
	h := newHarness(t, Options{
		Rules: []Rule{
			{Name: "first", When: &When{Currency: StringList{"TRY"}}, Use: ProviderTarget("a")},
			{Name: "second", When: &When{}, Use: ProviderTarget("b")},
		},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" || decision.Reason != ReasonRule {
		t.Fatalf("got %+v", decision)
	}
	if decision.Rule == nil || *decision.Rule != "first" || decision.RuleIndex == nil || *decision.RuleIndex != 0 {
		t.Fatalf("got rule %v index %v", decision.Rule, decision.RuleIndex)
	}
	if !strings.Contains(decision.Detail, "first") {
		t.Fatalf("detail does not name the rule: %q", decision.Detail)
	}
}

func TestNamesUnnamedRulesByIndex(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("a")}},
		Default: ProviderTarget("b"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Rule == nil || *decision.Rule != "rule[0]" || decision.RuleIndex == nil || *decision.RuleIndex != 0 {
		t.Fatalf("got %+v", decision)
	}
}

func TestMovesToTheNextRuleAndReportsSkips(t *testing.T) {
	h := newHarness(t, Options{
		Rules: []Rule{
			{Name: "ghost", When: &When{}, Use: ProviderTarget("ghost")},
			{Name: "second", When: &When{}, Use: ProviderTarget("b")},
		},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" || decision.Rule == nil || *decision.Rule != "second" || *decision.RuleIndex != 1 {
		t.Fatalf("got %+v", decision)
	}
	if len(decision.Skipped) != 1 || decision.Skipped[0] != (Skip{Provider: "ghost", Reason: SkipNotEnabled}) {
		t.Fatalf("got skips %v", decision.Skipped)
	}
}

func TestUsesTheFirstEligibleProviderOfAList(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ListTarget("ghost", "b", "a")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" {
		t.Fatalf("got %+v", decision)
	}
	if len(decision.Skipped) != 1 || decision.Skipped[0].Provider != "ghost" {
		t.Fatalf("got skips %v", decision.Skipped)
	}
}

func TestSkipsProvidersTheHealthHookMarksUnavailable(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	h := newHarness(t, Options{
		Health: &Health{IsAvailable: func(_ context.Context, provider string) (bool, error) {
			mu.Lock()
			seen = append(seen, provider)
			mu.Unlock()
			return provider != "a", nil
		}},
		Rules:   []Rule{{When: &When{}, Use: ListTarget("a", "b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" {
		t.Fatalf("got %+v", decision)
	}
	if len(decision.Skipped) != 1 || decision.Skipped[0] != (Skip{Provider: "a", Reason: SkipUnhealthy}) {
		t.Fatalf("got skips %v", decision.Skipped)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "a" {
		t.Fatalf("expected a health check for a, got %v", seen)
	}
}

func TestThrowingHealthCheckIsUnavailable(t *testing.T) {
	h := newHarness(t, Options{
		Health:  &Health{IsAvailable: func(context.Context, string) (bool, error) { panic("health endpoint down") }},
		Default: WeightsTarget(Weight{"a", 50}, Weight{"b", 50}),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "" || decision.Reason != ReasonNone {
		t.Fatalf("got %+v", decision)
	}
	want := []Skip{{Provider: "a", Reason: SkipUnhealthy}, {Provider: "b", Reason: SkipUnhealthy}}
	if len(decision.Skipped) != 2 || decision.Skipped[0] != want[0] || decision.Skipped[1] != want[1] {
		t.Fatalf("got skips %v", decision.Skipped)
	}
}

func TestFailsAChargeWhenHealthExcludesEveryProvider(t *testing.T) {
	h := newHarness(t, Options{
		Health:  &Health{IsAvailable: func(context.Context, string) (bool, error) { return false, nil }},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	if _, err := h.call(core.OperationCreatePayment, paymentRequest(), true); !isConfigurationError(err) {
		t.Fatalf("expected a ConfigurationError, got %v", err)
	}
	if h.providers["a"].paymentCount() != 0 || h.providers["b"].paymentCount() != 0 {
		t.Fatalf("a provider was called despite the failure")
	}
}

func TestRefusesToRouteWhenThePinnedProviderIsNotEnabled(t *testing.T) {
	h := newHarness(t, Options{
		Store:   newManualStore(map[string]string{"routing:attempt:123456789": "ghost"}),
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	if _, err := h.call(core.OperationCreatePayment, paymentRequest(), true); !isConfigurationError(err) {
		t.Fatalf("expected a ConfigurationError, got %v", err)
	}
	if h.providers["a"].paymentCount() != 0 {
		t.Fatalf("a was called")
	}
}

func TestWeightedSplitIsDeterministic(t *testing.T) {
	h := newHarness(t, Options{Default: WeightsTarget(Weight{"a", 70}, Weight{"b", 30})}, []string{"a", "b"}, "a")
	first, err := h.explain(withRequest(map[string]any{"conversationId": "order-42"}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	second, err := h.explain(withRequest(map[string]any{"conversationId": "order-42"}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if first.Provider != second.Provider || first.Provider == "" {
		t.Fatalf("split is not deterministic: %q vs %q", first.Provider, second.Provider)
	}
}

func TestWeightedSplitRespectsTheConfiguredWeights(t *testing.T) {
	h := newHarness(t, Options{Default: WeightsTarget(Weight{"a", 70}, Weight{"b", 30})}, []string{"a", "b"}, "a")
	fromA := 0
	for index := 0; index < 5000; index++ {
		decision, err := h.explain(withRequest(map[string]any{"conversationId": fmt.Sprintf("order-%d", index)}), "")
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if decision.Provider == "a" {
			fromA++
		}
	}
	if fromA <= 3400 || fromA >= 3600 {
		t.Fatalf("expected roughly 3500 of 5000 on a, got %d", fromA)
	}
}

func TestWeightedSplitRenormalizesOverEligibleProviders(t *testing.T) {
	h := newHarness(t, Options{
		Health:  &Health{IsAvailable: func(_ context.Context, provider string) (bool, error) { return provider != "a", nil }},
		Default: WeightsTarget(Weight{"a", 70}, Weight{"b", 30}),
	}, []string{"a", "b"}, "a")
	for index := 0; index < 20; index++ {
		decision, err := h.explain(withRequest(map[string]any{"conversationId": fmt.Sprintf("order-%d", index)}), "")
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if decision.Provider != "b" {
			t.Fatalf("expected b, got %+v", decision)
		}
		if index == 0 {
			if len(decision.Skipped) != 1 || decision.Skipped[0] != (Skip{Provider: "a", Reason: SkipUnhealthy}) {
				t.Fatalf("got skips %v", decision.Skipped)
			}
		}
	}
}

func TestStrategyChoosesAmongEligibleCandidates(t *testing.T) {
	var mu sync.Mutex
	var candidates []string
	h := newHarness(t, Options{
		Rules: []Rule{{
			Name: "custom",
			When: &When{},
			Use:  ListTarget("a", "b", "c"),
			StrategyRef: &Strategy{Choose: func(_ context.Context, sctx StrategyContext) (string, error) {
				mu.Lock()
				candidates = append([]string(nil), sctx.Candidates...)
				mu.Unlock()
				return sctx.Candidates[1], nil
			}},
		}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b", "c"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "b" || decision.Reason != ReasonStrategy {
		t.Fatalf("got %+v", decision)
	}
	if decision.Rule == nil || *decision.Rule != "custom" || *decision.RuleIndex != 0 {
		t.Fatalf("got rule %v", decision.Rule)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(candidates, ",") != "a,b,c" {
		t.Fatalf("strategy saw %v", candidates)
	}
}

func TestNamedStrategyIsRestrictedToTheRuleTarget(t *testing.T) {
	h := newHarness(t, Options{
		Strategies: map[string]*Strategy{"second": {Choose: func(_ context.Context, sctx StrategyContext) (string, error) {
			return sctx.Candidates[1], nil
		}}},
		Rules:   []Rule{{Strategy: "second", Use: ListTarget("b", "c")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b", "c"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "c" || decision.Reason != ReasonStrategy {
		t.Fatalf("got %+v", decision)
	}
}

func TestStrategySeesOnlyHealthyProviders(t *testing.T) {
	var mu sync.Mutex
	var candidates []string
	h := newHarness(t, Options{
		Health: &Health{IsAvailable: func(_ context.Context, provider string) (bool, error) { return provider != "b", nil }},
		Rules: []Rule{{StrategyRef: &Strategy{Choose: func(_ context.Context, sctx StrategyContext) (string, error) {
			mu.Lock()
			candidates = append([]string(nil), sctx.Candidates...)
			mu.Unlock()
			return sctx.Candidates[1], nil
		}}}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b", "c"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "c" {
		t.Fatalf("got %+v", decision)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(candidates, ",") != "a,c" {
		t.Fatalf("strategy saw %v", candidates)
	}
}

func TestStrategyWithoutChoiceFallsThrough(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{StrategyRef: &Strategy{Choose: func(context.Context, StrategyContext) (string, error) { return "", nil }}}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" || decision.Reason != ReasonDefault {
		t.Fatalf("got %+v", decision)
	}
}

func TestStrategyRejectsAnIneligibleChoice(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{StrategyRef: &Strategy{Choose: func(context.Context, StrategyContext) (string, error) { return "ghost", nil }}}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	if _, err := h.explain(baseRequest(), ""); !isConfigurationError(err) {
		t.Fatalf("expected a ConfigurationError, got %v", err)
	}
}

func TestFollowUpsReachTheProviderThatTookThePayment(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	paid, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	paymentID := paid.(*core.PaymentResponse).PaymentID
	if h.providers["a"].paymentCount() != 0 {
		t.Fatalf("a was used")
	}

	if _, err := h.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: paymentID, Price: "0.5", Currency: "TRY", IP: "85.34.78.112", ConversationID: "refund-1",
	}, true); err != nil {
		t.Fatalf("refund: %v", err)
	}
	decision := h.router.Decision(context.Background())
	if decision == nil || decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}
	if h.providers["b"].payments[0].refunds != 1 {
		t.Fatalf("expected one refund on b")
	}

	if _, err := h.call(core.OperationGetPayment, paymentID, true); err != nil {
		t.Fatalf("getPayment: %v", err)
	}
	if decision = h.router.Decision(context.Background()); decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}

	second, err := h.call(core.OperationCreatePayment, withRequestTyped("cancel-order"), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if _, err := h.call(core.OperationCancel, &core.CancelRequest{
		PaymentID: second.(*core.PaymentResponse).PaymentID, IP: "85.34.78.112", ConversationID: "cancel-order",
	}, true); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if decision = h.router.Decision(context.Background()); decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}
}

func withRequestTyped(conversationID string) *core.PaymentRequest {
	request := paymentRequest()
	request.ConversationID = conversationID
	return request
}

func TestCapturesAndVoidsReachTheAuthorizingProvider(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	authorized, err := h.call(core.OperationAuthorize, paymentRequest(), true)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if _, err := h.call(core.OperationCapture, &core.CaptureRequest{
		PaymentID: authorized.(*core.PaymentResponse).PaymentID, Amount: "1.00", IP: "85.34.78.112",
	}, true); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if decision := h.router.Decision(context.Background()); decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}

	other, err := h.call(core.OperationAuthorize, withRequestTyped("void-order"), true)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if _, err := h.call(core.OperationVoidAuthorization, &core.VoidAuthorizationRequest{
		PaymentID: other.(*core.PaymentResponse).PaymentID, IP: "85.34.78.112",
	}, true); err != nil {
		t.Fatalf("voidAuthorization: %v", err)
	}
	if decision := h.router.Decision(context.Background()); decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}
}

func TestCompletesThreeDSOnTheProviderThatStartedIt(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	request := &core.ThreeDSPaymentRequest{PaymentRequest: *paymentRequest()}
	request.CallbackURL = "https://example.com/callback"
	init, err := h.call(core.OperationInitThreeDSPayment, request, true)
	if err != nil {
		t.Fatalf("initThreeDSPayment: %v", err)
	}
	if init.(*core.ThreeDSInitResponse).Status != core.StatusPending {
		t.Fatalf("expected pending, got %+v", init)
	}
	paymentID := init.(*core.ThreeDSInitResponse).PaymentID
	completed, err := h.call(core.OperationCompleteThreeDSPayment, map[string]any{"paymentId": paymentID}, true)
	if err != nil {
		t.Fatalf("completeThreeDSPayment: %v", err)
	}
	if completed.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Fatalf("expected success, got %+v", completed)
	}
	if decision := h.router.Decision(context.Background()); decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}
	if h.providers["a"].paymentCount() != 0 {
		t.Fatalf("a was used")
	}
}

func TestDoesNotReRouteAfterALostResponseOrADecline(t *testing.T) {
	for _, outcome := range []string{"network", "decline"} {
		t.Run(outcome, func(t *testing.T) {
			var mu sync.Mutex
			healthy := true
			h := newHarness(t, Options{
				Default: ProviderTarget("a"),
				Health: &Health{IsAvailable: func(context.Context, string) (bool, error) {
					mu.Lock()
					defer mu.Unlock()
					return healthy, nil
				}},
			}, []string{"a", "b"}, "a")
			if outcome == "network" {
				h.providers["a"].nextNetwork = true
			} else {
				h.providers["a"].nextFailure = core.CodeCardDeclined
			}

			first, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
			if err != nil {
				t.Fatalf("createPayment: %v", err)
			}
			wantStatus := core.StatusFailure
			if outcome == "network" {
				wantStatus = core.StatusPending
			}
			if first.(*core.PaymentResponse).Status != wantStatus {
				t.Fatalf("expected %s, got %+v", wantStatus, first)
			}
			if h.providers["a"].paymentCount() != 1 || h.providers["b"].paymentCount() != 0 {
				t.Fatalf("unexpected provider usage")
			}

			// The provider becomes unhealthy, but a retry of the same order must not move.
			mu.Lock()
			healthy = false
			mu.Unlock()
			retry, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if retry.(*core.PaymentResponse).Status != core.StatusSuccess {
				t.Fatalf("expected success, got %+v", retry)
			}
			if h.providers["a"].paymentCount() != 2 || h.providers["b"].paymentCount() != 0 {
				t.Fatalf("the retry moved providers")
			}
			if decision := h.router.Decision(context.Background()); decision == nil || decision.Reason != ReasonPinned {
				t.Fatalf("got %+v", decision)
			}
		})
	}
}

func TestUnknownPaymentFallsBackToTheDefault(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	refund, err := h.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: "MISSING", Price: "1.00", Currency: "TRY", IP: "85.34.78.112", ConversationID: "unknown-1",
	}, true)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if refund.(*core.RefundResponse).Status != core.StatusFailure {
		t.Fatalf("expected failure, got %+v", refund)
	}
	decision := h.router.Decision(context.Background())
	if decision.Provider != "a" || decision.Reason != ReasonUnknownPayment || decision.Rule != nil {
		t.Fatalf("got %+v", decision)
	}
}

func TestDoesNotInventAProviderForAnUnknownPayment(t *testing.T) {
	h := newHarness(t, Options{Rules: []Rule{{When: &When{}, Use: ProviderTarget("b")}}}, []string{"a", "b"}, "")
	decision, err := h.explain(map[string]any{
		"paymentId": "MISSING", "price": "1.00", "currency": "TRY", "ip": "85.34.78.112",
	}, core.OperationRefund)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "" || decision.Reason != ReasonNone {
		t.Fatalf("got %+v", decision)
	}
}

func TestDoesNotApplyHealthToAnUnknownPaymentFollowUp(t *testing.T) {
	h := newHarness(t, Options{
		Health:  &Health{IsAvailable: func(context.Context, string) (bool, error) { return false, nil }},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	if _, err := h.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: "MISSING", Price: "1.00", Currency: "TRY", IP: "85.34.78.112", ConversationID: "unknown-2",
	}, true); err != nil {
		t.Fatalf("refund: %v", err)
	}
	decision := h.router.Decision(context.Background())
	if decision.Provider != "a" || decision.Reason != ReasonUnknownPayment {
		t.Fatalf("got %+v", decision)
	}
	if h.providers["a"].paymentCount() != 0 {
		t.Fatalf("a charge was made")
	}
}

func TestRefusesAKnownPaymentWhoseProviderIsNotEnabled(t *testing.T) {
	h := newHarness(t, Options{
		Store:   newManualStore(map[string]string{"routing:pay:pay-1": "ghost"}),
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	_, err := h.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: "pay-1", Price: "1.00", Currency: "TRY", IP: "85.34.78.112",
	}, true)
	if !isConfigurationError(err) {
		t.Fatalf("expected a ConfigurationError, got %v", err)
	}
}

func TestSharedStoreRemembersTheProviderAcrossInstances(t *testing.T) {
	store := NewMemoryStore(nil)
	first := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
		Store:   store,
	}, []string{"a", "b"}, "a")
	paid, err := first.call(core.OperationCreatePayment, paymentRequest(), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	paymentID := paid.(*core.PaymentResponse).PaymentID
	ctx := context.Background()
	if value, _ := store.Get(ctx, "routing:pay:"+paymentID); value != "b" {
		t.Fatalf("payment mapping is %q", value)
	}
	if value, _ := store.Get(ctx, "routing:conv:123456789"); value != "b" {
		t.Fatalf("conversation mapping is %q", value)
	}

	second := newHarness(t, Options{Default: ProviderTarget("a"), Store: store}, []string{"a", "b"}, "a")
	if _, err := second.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: paymentID, Price: "0.5", Currency: "TRY", IP: "85.34.78.112",
	}, true); err != nil {
		t.Fatalf("refund: %v", err)
	}
	decision := second.router.Decision(ctx)
	if decision.Provider != "b" || decision.Reason != ReasonFollowUp {
		t.Fatalf("got %+v", decision)
	}
}

func TestStoreDownDoesNotFailPayments(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
		Store:   brokenStore{},
	}, []string{"a", "b"}, "a")
	paid, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if paid.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Fatalf("expected success, got %+v", paid)
	}
	if decision := h.router.Decision(context.Background()); decision.Provider != "b" {
		t.Fatalf("got %+v", decision)
	}
	if _, err := h.call(core.OperationRefund, &core.RefundRequest{
		PaymentID: paid.(*core.PaymentResponse).PaymentID, Price: "0.5", Currency: "TRY", IP: "85.34.78.112",
	}, true); err != nil {
		t.Fatalf("refund: %v", err)
	}
	decision := h.router.Decision(context.Background())
	if decision.Provider != "a" || decision.Reason != ReasonUnknownPayment {
		t.Fatalf("got %+v", decision)
	}
}

func TestPublishesDecisionsAndKeepsTheLastOne(t *testing.T) {
	h := newHarness(t, Options{Default: ProviderTarget("a")}, []string{"a", "b"}, "a")
	var mu sync.Mutex
	var seen []*Decision
	off := h.router.OnDecision(context.Background(), func(_ context.Context, decision *Decision) error {
		mu.Lock()
		seen = append(seen, decision)
		mu.Unlock()
		return nil
	})
	if h.router.Decision(context.Background()) != nil {
		t.Fatalf("expected no decision before the first payment")
	}
	if _, err := h.call(core.OperationCreatePayment, paymentRequest(), true); err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	mu.Lock()
	count := len(seen)
	first := seen[0]
	mu.Unlock()
	if count != 1 || first.Provider != "a" || first.Reason != ReasonDefault {
		t.Fatalf("got %d decisions, first %+v", count, first)
	}
	status := h.router.Status(context.Background())
	if status.Total != 1 || status.Counts["a"] != 1 {
		t.Fatalf("got %+v", status)
	}

	off()
	if _, err := h.call(core.OperationCreatePayment, withRequestTyped("other-order"), true); err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	mu.Lock()
	count = len(seen)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("the listener was not removed")
	}
	if status = h.router.Status(context.Background()); status.Total != 2 || status.Counts["a"] != 2 {
		t.Fatalf("got %+v", status)
	}
}

func TestFailingAuditListenerDoesNotChangeRouting(t *testing.T) {
	h := newHarness(t, Options{Default: ProviderTarget("b")}, []string{"a", "b"}, "a")
	h.router.OnDecision(context.Background(), func(context.Context, *Decision) error { panic("audit down") })
	result, err := h.call(core.OperationCreatePayment, paymentRequest(), true)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Fatalf("expected success, got %+v", result)
	}
	if h.providers["b"].paymentCount() != 1 {
		t.Fatalf("expected one payment on b")
	}
}

func TestExplainHasNoSideEffects(t *testing.T) {
	h := newHarness(t, Options{Default: ProviderTarget("a")}, []string{"a", "b"}, "a")
	called := false
	h.router.OnDecision(context.Background(), func(context.Context, *Decision) error {
		called = true
		return nil
	})
	decision, err := h.explain(baseRequest(), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if decision.Provider != "a" {
		t.Fatalf("got %+v", decision)
	}
	if called {
		t.Fatalf("explain notified a listener")
	}
	if h.router.Decision(context.Background()) != nil {
		t.Fatalf("explain published a decision")
	}
	status := h.router.Status(context.Background())
	if status.Total != 0 || len(status.Counts) != 0 {
		t.Fatalf("got %+v", status)
	}
	if h.providers["a"].paymentCount() != 0 {
		t.Fatalf("explain called a provider")
	}
}

func TestDecisionNeverContainsCardData(t *testing.T) {
	h := newHarness(t, Options{
		BinResolver: func(context.Context, string) (*BinInfo, error) { return &BinInfo{CardFamily: "Bonus"}, nil },
		Rules:       []Rule{{Name: "bonus", When: &When{CardFamily: StringList{"Bonus"}}, Use: ProviderTarget("a")}},
		Default:     ProviderTarget("b"),
	}, []string{"a", "b"}, "a")
	decision, err := h.explain(withRequest(map[string]any{
		"paymentCard": map[string]any{"cardNumber": "5528790000000008"},
		"binNumber":   "552879",
	}), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	serialized, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"5528790000000008", "552879", "cardNumber"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("the decision contains %q: %s", forbidden, serialized)
		}
	}
}

func TestProviderCallsAreNotMoved(t *testing.T) {
	h := newHarness(t, Options{
		Rules:   []Rule{{When: &When{}, Use: ProviderTarget("b")}},
		Default: ProviderTarget("a"),
	}, []string{"a", "b"}, "a")
	result, err := h.call(core.OperationCreatePayment, paymentRequest(), false)
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Fatalf("expected success, got %+v", result)
	}
	if h.providers["a"].paymentCount() != 1 || h.providers["b"].paymentCount() != 0 {
		t.Fatalf("a provider call was moved")
	}
}

func TestMemoryStoreExpiresEntriesAndBoundsItsSize(t *testing.T) {
	now := int64(1_000_000)
	store := NewMemoryStore(&MemoryStoreOptions{TTLSeconds: 10, MaxEntries: 2, Now: func() int64 { return now }})
	ctx := context.Background()
	if err := store.Set(ctx, "first", "a", 10); err != nil {
		t.Fatalf("set: %v", err)
	}
	now += 5_000
	if err := store.Set(ctx, "second", "b", 10); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.Set(ctx, "third", "c", 10); err != nil {
		t.Fatalf("set: %v", err)
	}
	if store.Size(ctx) != 2 {
		t.Fatalf("expected 2 entries, got %d", store.Size(ctx))
	}
	if value, _ := store.Get(ctx, "first"); value != "" {
		t.Fatalf("the oldest entry was not evicted: %q", value)
	}
	if value, _ := store.Get(ctx, "third"); value != "c" {
		t.Fatalf("got %q", value)
	}
	now += 11_000
	if value, _ := store.Get(ctx, "third"); value != "" {
		t.Fatalf("the entry did not expire: %q", value)
	}
}

func TestMemoryStoreSetRefreshesTheEvictionOrder(t *testing.T) {
	now := int64(0)
	store := NewMemoryStore(&MemoryStoreOptions{MaxEntries: 2, Now: func() int64 { return now }})
	ctx := context.Background()
	_ = store.Set(ctx, "first", "a", -1)
	_ = store.Set(ctx, "second", "b", -1)
	_ = store.Set(ctx, "first", "a2", -1)
	_ = store.Set(ctx, "third", "c", -1)
	if value, _ := store.Get(ctx, "first"); value != "a2" {
		t.Fatalf("re-inserting lost the entry: %q", value)
	}
	if value, _ := store.Get(ctx, "second"); value != "" {
		t.Fatalf("the oldest entry was not evicted: %q", value)
	}
}

func TestBucketHashesUTF16CodeUnits(t *testing.T) {
	// The reference hash iterates UTF-16 code units, so a non-BMP character
	// contributes two units, not one rune.
	if Bucket("a") == Bucket("b") {
		t.Fatalf("the hash does not spread")
	}
	if Bucket("order-42") != Bucket("order-42") {
		t.Fatalf("the hash is not stable")
	}

	finalize := func(hash uint32) uint32 {
		hash ^= hash >> 16
		hash *= 0x85ebca6b
		hash ^= hash >> 13
		hash *= 0xc2b2ae35
		hash ^= hash >> 16
		return hash
	}
	units := uint32(0x811c9dc5)
	for _, unit := range []uint32{0xD83D, 0xDE00} {
		units ^= unit
		units *= 0x01000193
	}
	runes := uint32(0x811c9dc5)
	for _, r := range "😀" {
		runes ^= uint32(r)
		runes *= 0x01000193
	}
	if Bucket("😀") != finalize(units) {
		t.Fatalf("expected the UTF-16 bucket %08x, got %08x", finalize(units), Bucket("😀"))
	}
	if Bucket("😀") == finalize(runes) {
		t.Fatalf("the hash iterated runes instead of UTF-16 code units")
	}
}

func TestTargetJSONRoundTripKeepsWeightOrder(t *testing.T) {
	original := WeightsTarget(Weight{"b", 30}, Weight{"a", 70})
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"weights":{"b":30,"a":70}}` {
		t.Fatalf("got %s", data)
	}
	var decoded Target
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Weights) != 2 || decoded.Weights[0].Provider != "b" || decoded.Weights[1].Provider != "a" {
		t.Fatalf("weight order was lost: %+v", decoded.Weights)
	}
}

func TestWhenJSONAcceptsBothShapes(t *testing.T) {
	var when When
	if err := json.Unmarshal([]byte(`{"currency":"TRY","installment":6,"amount":{"min":"1.00"}}`), &when); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(when.Currency) != 1 || when.Currency[0] != "TRY" {
		t.Fatalf("got %+v", when.Currency)
	}
	if when.Installment == nil || when.Installment.Exact == nil || *when.Installment.Exact != 6 {
		t.Fatalf("got %+v", when.Installment)
	}
	if err := json.Unmarshal([]byte(`{"installment":{"min":2,"max":6},"bin":["5528","4546"]}`), &when); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if when.Installment.Exact != nil || when.Installment.Min == nil || *when.Installment.Min != 2 {
		t.Fatalf("got %+v", when.Installment)
	}
	if len(when.Bin) != 2 {
		t.Fatalf("got %+v", when.Bin)
	}
}

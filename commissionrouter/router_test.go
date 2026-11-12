package commissionrouter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func TestRoutesToTheCheapestEligibleProviderWithExactIntegerCosts(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{
		{Provider: "alpha", Rate: "1.10", Fixed: "0.10"},
		{Provider: "beta", Rate: "1.00", Fixed: "0.05"},
		{Provider: "gamma", Rate: "1.25"},
	}}, nil)

	quote := h.mustQuote(t, QuoteInput{Price: "1000.00"})
	if selectedOf(quote) != "beta" || reasonOf(quote) != SelectionLowestCost {
		t.Fatalf("got %+v", quote)
	}
	type row struct {
		provider string
		cost     string
		chosen   bool
		reason   CandidateReason
	}
	rows := make([]row, 0, len(quote.Candidates))
	for _, candidate := range quote.Candidates {
		rows = append(rows, row{candidate.Provider, costOf(candidate), candidate.Chosen, candidate.Reason})
	}
	want := []row{
		{"beta", "10.05", true, CandidateLowestCost},
		{"alpha", "11.10", false, CandidateHigherCost},
		{"gamma", "12.50", false, CandidateHigherCost},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %+v, want %+v", rows, want)
	}
	if quote.Candidates[1].Detail != "costs 1.05 TRY more than 'beta'" {
		t.Fatalf("got detail %q", quote.Candidates[1].Detail)
	}
	if len(quote.Excluded) != 0 {
		t.Fatalf("expected no exclusion, got %+v", quote.Excluded)
	}
	wantAmount := QuoteAmount{Price: "1000.00", PriceMinor: 100000, Currency: "TRY", Installment: 1, ThreeDS: false}
	if quote.Amount != wantAmount {
		t.Fatalf("got %+v, want %+v", quote.Amount, wantAmount)
	}
	// Quoting must not count as a routing decision.
	if h.status().Total != 0 {
		t.Fatalf("a quote was counted: %+v", h.status())
	}

	result, err := h.call(core.OperationCreatePayment, order("1000.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	paid := asPayment(t, result)
	if paid.Status != core.StatusSuccess || paid.Provider != "beta" {
		t.Fatalf("got %+v", paid)
	}
	if h.providers["beta"].paymentCount() != 1 || h.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("the payment did not reach beta")
	}
	if want := map[string]int{"alpha": 0, "beta": 1, "gamma": 0}; !reflect.DeepEqual(h.status().Routed, want) {
		t.Fatalf("got %+v, want %+v", h.status().Routed, want)
	}
}

func TestRoundsHalfAMinorUnitUpAndNeverComparesFloats(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "10"}}}, []string{"alpha"})
	half := h.mustQuote(t, QuoteInput{Price: "0.05"})
	if costOf(half.Candidates[0]) != "0.01" || *half.Candidates[0].CostMinor != 1 {
		t.Fatalf("got %+v", half.Candidates[0])
	}
	below := h.mustQuote(t, QuoteInput{Price: "0.04"})
	if costOf(below.Candidates[0]) != "0.00" || *below.Candidates[0].CostMinor != 0 {
		t.Fatalf("got %+v", below.Candidates[0])
	}

	// 2.49% of 10.10 TRY is 0.25149 TRY -> 25 minor units, rounded half up.
	percent := newHarness(t, Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "2.49"}}}, []string{"alpha"})
	exact := percent.mustQuote(t, QuoteInput{Price: "10.10"})
	if costOf(exact.Candidates[0]) != "0.25" || *exact.Candidates[0].CostMinor != 25 {
		t.Fatalf("got %+v", exact.Candidates[0])
	}
}

func TestExplainsCurrencyAndInstallmentExclusionsInsteadOfGuessing(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{
		{Provider: "alpha", Rate: "1", Currencies: []string{"USD"}},
		{Provider: "beta", Rate: "1", Installments: []int{1}},
	}}, []string{"alpha", "beta"})

	quote := h.mustQuote(t, QuoteInput{Price: "100.00", Currency: "TRY", Installment: intPtr(3)})
	if quote.Selected != nil || len(quote.Candidates) != 0 {
		t.Fatalf("got %+v", quote)
	}
	want := []ExcludedProvider{
		{Provider: "alpha", Reason: ExclusionCurrencyNotSupported, Detail: "the line covers USD, not TRY", RateIndex: intPtr(0)},
		{Provider: "beta", Reason: ExclusionInstallmentNotSupported, Detail: "installment 3 is not in [1]", RateIndex: intPtr(1)},
	}
	if !reflect.DeepEqual(quote.Excluded, want) {
		t.Fatalf("got %+v, want %+v", quote.Excluded, want)
	}
	if !reflect.DeepEqual(quote.Notes, []string{"no provider is eligible and the fallback order is empty or unavailable"}) {
		t.Fatalf("got %+v", quote.Notes)
	}
}

func TestFiltersByAmountBoundsAndByRateValidityDates(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	h := newHarness(t, Options{
		Now: func() int64 { return now },
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1", MinAmount: "100.00", MaxAmount: "500.00",
				ValidFrom: "2026-01-01T00:00:00Z", ValidTo: "2026-09-01T00:00:00Z"},
			{Provider: "beta", Rate: "2"},
		},
	}, []string{"alpha", "beta"})

	below := h.mustQuote(t, QuoteInput{Price: "50.00"})
	if below.Excluded[0].Provider != "alpha" || below.Excluded[0].Reason != ExclusionAmountBelowMin ||
		below.Excluded[0].Detail != "the amount is below the line's minimum of 100.00" {
		t.Fatalf("got %+v", below.Excluded[0])
	}
	above := h.mustQuote(t, QuoteInput{Price: "600.00"})
	if above.Excluded[0].Reason != ExclusionAmountAboveMax ||
		above.Excluded[0].Detail != "the amount is above the line's maximum of 500.00" {
		t.Fatalf("got %+v", above.Excluded[0])
	}

	now = time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if early := h.mustQuote(t, QuoteInput{Price: "100.00"}); early.Excluded[0].Reason != ExclusionRateNotYetValid {
		t.Fatalf("got %+v", early.Excluded[0])
	}
	now = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	late := h.mustQuote(t, QuoteInput{Price: "100.00"})
	if late.Excluded[0].Reason != ExclusionRateExpired {
		t.Fatalf("got %+v", late.Excluded[0])
	}
	// The fallback line without any filter stays eligible throughout.
	if selectedOf(late) != "beta" {
		t.Fatalf("got %q", selectedOf(late))
	}
}

func TestFiltersByThe3DSecureRequirement(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{
		{Provider: "alpha", Rate: "1", ThreeDS: boolPtr(true)},
		{Provider: "beta", Rate: "1", ThreeDS: boolPtr(false)},
	}}, []string{"alpha", "beta"})

	if got := selectedOf(h.mustQuote(t, QuoteInput{Price: "100.00"})); got != "beta" {
		t.Fatalf("got %q", got)
	}
	if got := selectedOf(h.mustQuote(t, QuoteInput{Price: "100.00", Operation: core.OperationInitThreeDSPayment})); got != "alpha" {
		t.Fatalf("got %q", got)
	}
	if got := selectedOf(h.mustQuote(t, QuoteInput{
		Price: "100.00", Operation: core.OperationInitThreeDSPayment, ThreeDS: boolPtr(false),
	})); got != "beta" {
		t.Fatalf("got %q", got)
	}
}

func TestMatchesCardFiltersFromTheBinLookupAndReportsUnknownCards(t *testing.T) {
	var calls []string
	lookup := func(_ context.Context, bin string) (*BinInfo, error) {
		calls = append(calls, bin)
		return &BinInfo{
			BinNumber:       bin,
			CardFamily:      "Bonus",
			BankName:        "Fictional Bank",
			CardAssociation: "MASTER_CARD",
			CardType:        "CREDIT_CARD",
			Commercial:      boolPtr(false),
		}, nil
	}
	h := newHarness(t, Options{
		BinLookup: lookup,
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1", CardFamily: StringList{"Bonus"}},
			{Provider: "beta", Rate: "0.5", CardFamily: StringList{"Axess"}},
			{Provider: "gamma", Rate: "3"},
		},
	}, nil)

	quote := h.mustQuote(t, QuoteInput{Price: "100.00", BinNumber: "552879"})
	if selectedOf(quote) != "alpha" {
		t.Fatalf("got %+v", quote)
	}
	wantCard := &QuoteCard{
		BankName: "Fictional Bank", CardFamily: "Bonus", CardType: "CREDIT_CARD",
		CardAssociation: "MASTER_CARD", Commercial: boolPtr(false),
	}
	if !reflect.DeepEqual(quote.Card, wantCard) {
		t.Fatalf("got %+v, want %+v", quote.Card, wantCard)
	}
	wantExcluded := []ExcludedProvider{
		{Provider: "beta", Reason: ExclusionCardMismatch, Detail: "card family 'Bonus' is not axess", RateIndex: intPtr(1)},
	}
	if !reflect.DeepEqual(quote.Excluded, wantExcluded) {
		t.Fatalf("got %+v, want %+v", quote.Excluded, wantExcluded)
	}
	if !reflect.DeepEqual(calls, []string{"552879"}) {
		t.Fatalf("got lookup calls %v", calls)
	}
	serialized, err := json.Marshal(quote)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(serialized), "552879") {
		t.Fatalf("the quote contains the BIN: %s", serialized)
	}

	unknown := h.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(unknown) != "gamma" {
		t.Fatalf("got %+v", unknown)
	}
	reasons := make([]ExclusionReason, 0, len(unknown.Excluded))
	for _, entry := range unknown.Excluded {
		reasons = append(reasons, entry.Reason)
	}
	if !reflect.DeepEqual(reasons, []ExclusionReason{ExclusionCardUnknown, ExclusionCardUnknown}) {
		t.Fatalf("got %v", reasons)
	}
}

func TestSkipsUnhealthyProvidersAndFailsOpenWhenTheMonitorThrows(t *testing.T) {
	skipped := newHarness(t, Options{
		IsHealthy: func(_ context.Context, provider string) (bool, error) { return provider != "alpha", nil },
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1"},
			{Provider: "beta", Rate: "2"},
		},
	}, []string{"alpha", "beta"})
	quote := skipped.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(quote) != "beta" {
		t.Fatalf("got %+v", quote)
	}
	want := []ExcludedProvider{{Provider: "alpha", Reason: ExclusionProviderUnhealthy, Detail: "isHealthy() returned false"}}
	if !reflect.DeepEqual(quote.Excluded, want) {
		t.Fatalf("got %+v, want %+v", quote.Excluded, want)
	}

	broken := newHarness(t, Options{
		IsHealthy: func(context.Context, string) (bool, error) { panic("monitor down") },
		Rates:     []CommissionRate{{Provider: "alpha", Rate: "1"}},
	}, []string{"alpha"})
	open := broken.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(open) != "alpha" {
		t.Fatalf("got %+v", open)
	}
	if !hasNote(open, "health check for 'alpha' failed; treated as healthy") {
		t.Fatalf("got %+v", open.Notes)
	}
}

func TestUsesTheFallbackOrderOnlyForProvidersThatHaveNoRateLine(t *testing.T) {
	h := newHarness(t, Options{
		Rates:     []CommissionRate{{Provider: "alpha", Rate: "1", Currencies: []string{"USD"}}},
		Fallback:  []string{"beta", "gamma"},
		IsHealthy: func(_ context.Context, provider string) (bool, error) { return provider != "gamma", nil },
	}, nil)

	quote := h.mustQuote(t, QuoteInput{Price: "100.00", Currency: "TRY"})
	if selectedOf(quote) != "beta" || reasonOf(quote) != SelectionFallback {
		t.Fatalf("got %+v", quote)
	}
	want := []Candidate{{Provider: "beta", Chosen: true, Reason: CandidateFallback,
		Detail: "no rate line matched; first provider in the fallback order"}}
	if !reflect.DeepEqual(quote.Candidates, want) {
		t.Fatalf("got %+v, want %+v", quote.Candidates, want)
	}
	// A hard filter (currency) is never bypassed by the fallback order.
	wantExcluded := []ExcludedProvider{
		{Provider: "alpha", Reason: ExclusionCurrencyNotSupported, Detail: "the line covers USD, not TRY", RateIndex: intPtr(0)},
		{Provider: "gamma", Reason: ExclusionNoRateLine, Detail: "no rate line for this provider"},
	}
	if !reflect.DeepEqual(quote.Excluded, wantExcluded) {
		t.Fatalf("got %+v, want %+v", quote.Excluded, wantExcluded)
	}
}

func TestResolvesTiesWithPolicyPreferThenTheRateTableOrder(t *testing.T) {
	rates := []CommissionRate{
		{Provider: "alpha", Rate: "1.50"},
		{Provider: "beta", Rate: "1.50"},
		{Provider: "gamma", Rate: "2"},
	}
	plain := newHarness(t, Options{Rates: rates}, nil)
	first := plain.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(first) != "alpha" || reasonOf(first) != SelectionLowestCost {
		t.Fatalf("got %+v", first)
	}

	preferred := newHarness(t, Options{Rates: rates, Policy: &Policy{Prefer: []string{"beta"}}}, nil)
	second := preferred.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(second) != "beta" || reasonOf(second) != SelectionPreferredOnTie {
		t.Fatalf("got %+v", second)
	}
	if second.Candidates[0].Provider != "beta" || !second.Candidates[0].Chosen ||
		second.Candidates[0].Reason != CandidatePreferredOnTie ||
		second.Candidates[0].Detail != "same cost as the runner-up; preferred by policy.prefer (1.50 TRY)" {
		t.Fatalf("got %+v", second.Candidates[0])
	}
	if second.Candidates[1].Provider != "alpha" || second.Candidates[1].Chosen ||
		second.Candidates[1].Reason != CandidateRankedLower {
		t.Fatalf("got %+v", second.Candidates[1])
	}
}

func TestRotatesProvidersUnderAShareCapInsteadOfExceedingIt(t *testing.T) {
	h := newHarness(t, Options{
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1"},
			{Provider: "beta", Rate: "2"},
		},
		Policy: &Policy{ShareCap: &ShareCapOptions{MaxShare: 0.5, Window: 2}},
	}, []string{"alpha", "beta"})

	first, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("first payment: %v", err)
	}
	afterOne := h.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(afterOne) != "beta" {
		t.Fatalf("got %+v", afterOne)
	}
	want := []ExcludedProvider{{
		Provider: "alpha", Reason: ExclusionShareCap,
		Detail:    "one more payment would take 100% of the last 2 routing decisions (cap 50%)",
		RateIndex: intPtr(0),
	}}
	if !reflect.DeepEqual(afterOne.Excluded, want) {
		t.Fatalf("got %+v, want %+v", afterOne.Excluded, want)
	}

	second, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}
	afterTwo := h.mustQuote(t, QuoteInput{Price: "100.00"})
	// Both providers are at the cap: the cheapest eligible provider is used.
	if selectedOf(afterTwo) != "alpha" {
		t.Fatalf("got %+v", afterTwo)
	}
	if !hasNote(afterTwo, "every eligible provider is at its share cap; the cheapest eligible provider is used") {
		t.Fatalf("got %+v", afterTwo.Notes)
	}

	third, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("third payment: %v", err)
	}
	got := []string{asPayment(t, first).Provider, asPayment(t, second).Provider, asPayment(t, third).Provider}
	if !reflect.DeepEqual(got, []string{"alpha", "beta", "alpha"}) {
		t.Fatalf("got %v", got)
	}
	// The window keeps only the last 2 decisions.
	status := h.status()
	if !reflect.DeepEqual(status.Routed, map[string]int{"alpha": 1, "beta": 1}) || status.Total != 2 || status.ShareWindow != 2 {
		t.Fatalf("got %+v", status)
	}
	if status.BinCache != (CacheStatus{Entries: 0, MaxEntries: 1024, TTLMS: 86_400_000}) {
		t.Fatalf("got %+v", status.BinCache)
	}

	h.router.Reset(context.Background())
	reset := h.status()
	if !reflect.DeepEqual(reset.Routed, map[string]int{"alpha": 0, "beta": 0}) || reset.Total != 0 {
		t.Fatalf("got %+v", reset)
	}
}

func TestAppliesAPerOrderOverrideEvenWhenItIsNotTheCheapest(t *testing.T) {
	h := newHarness(t, Options{
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1"},
			{Provider: "beta", Rate: "2"},
			{Provider: "gamma", Rate: "3"},
		},
		Policy: &Policy{Override: func(_ context.Context, octx OverrideContext) (string, error) {
			if octx.ConversationID == "order-gamma" {
				return "gamma", nil
			}
			return "", nil
		}},
	}, nil)

	quote := h.mustQuote(t, QuoteInput{Price: "100.00", ConversationID: "order-gamma"})
	if selectedOf(quote) != "gamma" || reasonOf(quote) != SelectionOverride {
		t.Fatalf("got %+v", quote)
	}
	if quote.Candidates[0].Provider != "gamma" || costOf(quote.Candidates[0]) != "3.00" ||
		!quote.Candidates[0].Chosen || quote.Candidates[0].Reason != CandidateOverride ||
		quote.Candidates[0].Detail != "selected by policy.override (3.00 TRY)" {
		t.Fatalf("got %+v", quote.Candidates[0])
	}

	result, err := h.call(core.OperationCreatePayment, orderWith("100.00", "order-gamma"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if asPayment(t, result).Provider != "gamma" {
		t.Fatalf("got %+v", result)
	}
	if h.providers["gamma"].paymentCount() != 1 || h.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("the override did not reach gamma")
	}
}

func TestIgnoresAnOverrideToAProviderThatIsNotEligible(t *testing.T) {
	h := newHarness(t, Options{
		Rates: []CommissionRate{
			{Provider: "alpha", Rate: "1"},
			{Provider: "beta", Rate: "2"},
		},
		Policy: &Policy{Override: func(context.Context, OverrideContext) (string, error) { return "delta", nil }},
	}, []string{"alpha", "beta"})

	quote := h.mustQuote(t, QuoteInput{Price: "100.00"})
	if selectedOf(quote) != "alpha" {
		t.Fatalf("got %+v", quote)
	}
	if !hasNote(quote, "policy.override selected 'delta', which is not eligible; normal ranking used") {
		t.Fatalf("got %+v", quote.Notes)
	}
}

func TestDoesNotRetryFailuresWhoseChargeStateIsUnknownOrDeclined(t *testing.T) {
	rates := []CommissionRate{
		{Provider: "alpha", Rate: "2"},
		{Provider: "beta", Rate: "1"},
	}
	network := newHarness(t, Options{Rates: rates}, []string{"alpha", "beta"})
	network.providers["beta"].networkErrorNext(core.OperationCreatePayment)
	result, err := network.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	pending := asPayment(t, result)
	if pending.Status != core.StatusPending || pending.Code != core.CodeNetworkError || pending.Provider != "beta" {
		t.Fatalf("got %+v", pending)
	}
	if network.providers["beta"].paymentCount() != 1 || network.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("an unknown outcome was retried")
	}

	declined := newHarness(t, Options{Rates: rates}, []string{"alpha", "beta"})
	declined.providers["beta"].failNext(core.OperationCreatePayment, core.CodeCardDeclined)
	result, err = declined.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	failure := asPayment(t, result)
	if failure.Status != core.StatusFailure || failure.Code != core.CodeCardDeclined {
		t.Fatalf("got %+v", failure)
	}
	if declined.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("a decline was retried")
	}
}

func TestFallsBackOnceWhenTheErrorProvesNoChargeHappened(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{
		{Provider: "alpha", Rate: "2"},
		{Provider: "beta", Rate: "1"},
	}}, []string{"alpha", "beta"})
	h.providers["beta"].failNext(core.OperationCreatePayment, core.CodeProviderError)

	result, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	paid := asPayment(t, result)
	if paid.Status != core.StatusSuccess || paid.Provider != "alpha" {
		t.Fatalf("got %+v", paid)
	}
	if h.providers["beta"].paymentCount() != 1 || h.providers["alpha"].paymentCount() != 1 {
		t.Fatalf("expected exactly one attempt per provider")
	}
	record := h.providers["alpha"].getRecord(paid.PaymentID)
	if record == nil || record.state != "succeeded" || record.amount != 10000 {
		t.Fatalf("got %+v", record)
	}
}

func TestRefusesAFallbackOnConfigurationThatCouldDoubleCharge(t *testing.T) {
	rates := []CommissionRate{{Provider: "alpha", Rate: "1"}}
	_, err := New(Options{Rates: rates, FallbackOn: []string{string(core.CodeNetworkError)}})
	if err == nil || !strings.Contains(err.Error(), "can charge the customer twice") {
		t.Fatalf("got %v", err)
	}
	_, err = New(Options{Rates: rates, FallbackOn: []string{string(core.CodeCardDeclined)}})
	if !isConfigurationError(err) {
		t.Fatalf("expected a configuration error, got %v", err)
	}
}

func TestSendsRefundCancelAndGetPaymentToTheProviderThatHandledThePayment(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{
		{Provider: "alpha", Rate: "2"},
		{Provider: "beta", Rate: "1"},
	}}, []string{"alpha", "beta"})

	result, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	paid := asPayment(t, result)
	if paid.Provider != "beta" {
		t.Fatalf("got %+v", paid)
	}

	result, err = h.call(core.OperationRefund, refundRequest(paid.PaymentID))
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	refund := asRefund(t, result)
	if refund.Status != core.StatusSuccess || refund.Provider != "beta" {
		t.Fatalf("got %+v", refund)
	}
	if record := h.providers["beta"].getRecord(paid.PaymentID); record == nil || len(record.refunds) != 1 {
		t.Fatalf("got %+v", record)
	}
	if h.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("the refund reached another provider")
	}

	result, err = h.call(core.OperationGetPayment, paid.PaymentID)
	if err != nil {
		t.Fatalf("getPayment: %v", err)
	}
	status := asPayment(t, result)
	if status.Status != core.StatusSuccess || status.Provider != "beta" {
		t.Fatalf("got %+v", status)
	}

	result, err = h.call(core.OperationCreatePayment, order("200.00"))
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}
	second := asPayment(t, result)
	result, err = h.call(core.OperationCancel, cancelRequest(second.PaymentID))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelled := asCancel(t, result)
	if cancelled.Status != core.StatusSuccess || cancelled.Provider != "beta" {
		t.Fatalf("got %+v", cancelled)
	}
	if h.providers["alpha"].paymentCount() != 0 {
		t.Fatalf("the cancel reached another provider")
	}
}

func TestBoundsTheBinCacheAndExpiresItsEntries(t *testing.T) {
	now := int64(1_000)
	calls := 0
	lookup := func(context.Context, string) (*BinInfo, error) {
		calls++
		return &BinInfo{CardFamily: "Bonus"}, nil
	}
	h := newHarness(t, Options{
		Now:       func() int64 { return now },
		Cache:     &CacheOptions{MaxEntries: 2, TTLMS: 1_000},
		BinLookup: lookup,
		Rates:     []CommissionRate{{Provider: "alpha", Rate: "1"}},
	}, []string{"alpha"})

	h.mustQuote(t, QuoteInput{Price: "1.00", BinNumber: "552879"})
	h.mustQuote(t, QuoteInput{Price: "1.00", BinNumber: "552879"})
	if calls != 1 {
		t.Fatalf("the cached BIN was looked up again: %d calls", calls)
	}

	now = 2_001
	h.mustQuote(t, QuoteInput{Price: "1.00", BinNumber: "552879"})
	if calls != 2 {
		t.Fatalf("the expired BIN was not looked up again: %d calls", calls)
	}

	h.mustQuote(t, QuoteInput{Price: "1.00", BinNumber: "400000"})
	h.mustQuote(t, QuoteInput{Price: "1.00", BinNumber: "555555"})
	if got := h.status().BinCache; got != (CacheStatus{Entries: 2, MaxEntries: 2, TTLMS: 1_000}) {
		t.Fatalf("got %+v", got)
	}
}

func TestLooksUpThe8DigitPrefixThenThe6DigitPrefixNeverTheFullPan(t *testing.T) {
	const fullPan = "5528790000000008"
	var calls []string
	lookup := func(_ context.Context, bin string) (*BinInfo, error) {
		calls = append(calls, bin)
		if bin == "552879" {
			return &BinInfo{CardFamily: "Bonus"}, nil
		}
		return nil, nil
	}
	logger := &recordingLogger{}
	h := newHarnessWithLogger(t, Options{
		BinLookup: lookup,
		Rates:     []CommissionRate{{Provider: "alpha", Rate: "1", CardFamily: StringList{"Bonus"}}},
	}, []string{"alpha"}, logger)

	result, err := h.call(core.OperationCreatePayment, order("100.00"))
	if err != nil {
		t.Fatalf("createPayment: %v", err)
	}
	if asPayment(t, result).Status != core.StatusSuccess {
		t.Fatalf("got %+v", result)
	}
	if !reflect.DeepEqual(calls, []string{"55287900", "552879"}) {
		t.Fatalf("got lookup calls %v", calls)
	}
	if strings.Contains(logger.joined(), fullPan) {
		t.Fatalf("the full card number was logged")
	}
	serialized, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(serialized), fullPan) {
		t.Fatalf("the result contains the full card number: %s", serialized)
	}

	for attempt := 0; attempt < 2; attempt++ {
		_, err := h.quote(QuoteInput{Price: "1.00", BinNumber: fullPan})
		if !isValidationError(err) {
			t.Fatalf("expected a validation error, got %v", err)
		}
		if !strings.Contains(err.Error(), "6-8 digit BIN prefix") {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), fullPan) {
			t.Fatalf("the error echoes the card number: %v", err)
		}
	}
}

func TestServesTheQuoteEndpointWith400ForInvalidInput(t *testing.T) {
	h := newHarness(t, Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "1.50"}}}, []string{"alpha"})
	endpoint, ok := h.plugin.Endpoints["quote"]
	if !ok {
		t.Fatalf("the quote endpoint is not registered")
	}
	// The handler answers 405 for any other method; the plugin only declares POST.
	if endpoint.Method != "POST" || endpoint.Path != "/router/quote" {
		t.Fatalf("got %s %s", endpoint.Method, endpoint.Path)
	}
	ectx := &fakeEndpoint{fakeContext: h.pctx, request: &core.HandlerRequest{
		Method: "POST", URL: "/api/pay/router/quote", Headers: map[string]string{"content-type": "application/json"},
	}}

	ectx.body = map[string]any{"price": "200.00", "currency": "try"}
	served, err := endpoint.Handler(context.Background(), ectx)
	if err != nil {
		t.Fatalf("quote endpoint: %v", err)
	}
	quote, ok := served.(*Quote)
	if !ok {
		t.Fatalf("expected a quote, got %T", served)
	}
	if selectedOf(quote) != "alpha" {
		t.Fatalf("got %+v", quote)
	}
	if quote.Amount.Price != "200.00" || quote.Amount.PriceMinor != 20000 || quote.Amount.Currency != "TRY" {
		t.Fatalf("got %+v", quote.Amount)
	}
	if len(quote.Candidates) != 1 || quote.Candidates[0].Provider != "alpha" ||
		costOf(quote.Candidates[0]) != "3.00" || !quote.Candidates[0].Chosen {
		t.Fatalf("got %+v", quote.Candidates)
	}

	ectx.body = map[string]any{"price": "not-a-number"}
	served, err = endpoint.Handler(context.Background(), ectx)
	if err != nil {
		t.Fatalf("quote endpoint: %v", err)
	}
	response, ok := served.(*core.HandlerResponse)
	if !ok || response.Status != 400 {
		t.Fatalf("expected 400, got %+v", served)
	}
	body, ok := response.Body.(map[string]any)
	if !ok {
		t.Fatalf("expected an error body, got %T", response.Body)
	}
	message, _ := body["error"].(string)
	if !strings.Contains(message, "non-negative decimal") {
		t.Fatalf("got %q", message)
	}
	serialized, err := json.Marshal(response.Body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(serialized), "not-a-number") {
		t.Fatalf("the error echoes the input: %s", serialized)
	}
}

func TestValidatesTheRateTableTheOptionsTheProvidersAndQuoteInput(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options Options
		want    string
	}{
		{"rate above 100", Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "100.01"}}}, "at most 100"},
		{"empty installments", Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "1", Installments: []int{}}}}, "non-empty array"},
		{"inverted bounds", Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "1", MinAmount: "10.00", MaxAmount: "5.00"}}}, "minAmount is above maxAmount"},
		{"unknown strategy", Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "1"}}, Strategy: "highest-cost"}, "unknown strategy"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := New(testCase.options)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("got %v, want an error containing %q", err, testCase.want)
			}
			if !isConfigurationError(err) {
				t.Fatalf("expected a configuration error, got %v", err)
			}
		})
	}

	// The provider of every line is checked when the plugin is attached.
	router, err := New(Options{Rates: []CommissionRate{{Provider: "ghost", Rate: "1"}}})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	pctx := &fakeContext{ids: []string{"alpha"}, defaultProvider: "alpha", logger: core.NopLogger{}}
	err = router.Bind(context.Background(), pctx)
	if err == nil || !strings.Contains(err.Error(), "'ghost' is not a configured provider") {
		t.Fatalf("got %v", err)
	}

	h := newHarness(t, Options{Rates: []CommissionRate{{Provider: "alpha", Rate: "1"}}}, []string{"alpha"})
	for _, input := range []QuoteInput{
		{Price: "-1.00"},
		{Price: "1.00", Installment: intPtr(0)},
		{Price: "1.00", Currency: "TL"},
	} {
		if _, err := h.quote(input); !isValidationError(err) {
			t.Fatalf("quote(%+v): expected a validation error, got %v", input, err)
		}
	}
	_, err = h.quote(QuoteInput{Price: "1.00", Operation: core.OperationRefund})
	if err == nil || !strings.Contains(err.Error(), "operation must be") {
		t.Fatalf("got %v", err)
	}
}

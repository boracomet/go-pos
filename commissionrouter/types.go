// Package commissionrouter routes each payment to the cheapest eligible
// provider from merchant-defined commission rate tables.
package commissionrouter

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/boracomet/go-pos/core"
)

// Strategy is the routing strategy. Only lowest-cost is supported.
type Strategy string

// StrategyLowestCost ranks eligible providers by commission, cheapest first.
const StrategyLowestCost Strategy = "lowest-cost"

// ExclusionReason is why a configured provider is not in the ranking. Codes are
// stable; Detail is a human-readable explanation that never contains card data.
type ExclusionReason string

const (
	ExclusionProviderUnhealthy       ExclusionReason = "provider-unhealthy"
	ExclusionNoRateLine              ExclusionReason = "no-rate-line"
	ExclusionRateNotYetValid         ExclusionReason = "rate-not-yet-valid"
	ExclusionRateExpired             ExclusionReason = "rate-expired"
	ExclusionAmountBelowMin          ExclusionReason = "amount-below-min"
	ExclusionAmountAboveMax          ExclusionReason = "amount-above-max"
	ExclusionCurrencyNotSupported    ExclusionReason = "currency-not-supported"
	ExclusionInstallmentNotSupported ExclusionReason = "installment-not-supported"
	ExclusionThreeDSNotSupported     ExclusionReason = "threeds-not-supported"
	ExclusionCardUnknown             ExclusionReason = "card-unknown"
	ExclusionCardMismatch            ExclusionReason = "card-mismatch"
	ExclusionShareCap                ExclusionReason = "share-cap"
)

// CandidateReason explains a candidate's position in the ranking.
type CandidateReason string

const (
	CandidateLowestCost     CandidateReason = "lowest-cost"
	CandidatePreferredOnTie CandidateReason = "preferred-on-tie"
	CandidateOverride       CandidateReason = "override"
	CandidateFallback       CandidateReason = "fallback"
	CandidateHigherCost     CandidateReason = "higher-cost"
	CandidateRankedLower    CandidateReason = "ranked-lower"
)

// SelectionReason is why the selected provider won.
type SelectionReason string

const (
	SelectionLowestCost     SelectionReason = "lowest-cost"
	SelectionPreferredOnTie SelectionReason = "preferred-on-tie"
	SelectionOverride       SelectionReason = "override"
	SelectionFallback       SelectionReason = "fallback"
)

// ExcludedProvider is a configured provider that was not eligible.
type ExcludedProvider struct {
	Provider string          `json:"provider"`
	Reason   ExclusionReason `json:"reason"`
	Detail   string          `json:"detail"`
	// RateIndex is the index in the configured rate table of the closest line,
	// when known.
	RateIndex *int `json:"rateIndex,omitempty"`
}

// QuoteInput is what the router is asked to price.
type QuoteInput struct {
	// BinNumber is the first 6 to 8 digits of the card, never a full card
	// number; longer values are rejected.
	BinNumber string `json:"binNumber,omitempty"`
	// Price is the amount the customer pays, a decimal string with at most 2
	// decimals.
	Price string `json:"price"`
	// Installment defaults to 1.
	Installment *int `json:"installment,omitempty"`
	// Currency defaults to TRY.
	Currency string `json:"currency,omitempty"`
	// ThreeDS defaults to the operation: initThreeDSPayment and
	// initThreeDSAuthorize are 3DS.
	ThreeDS *bool `json:"threeDS,omitempty"`
	// Operation defaults to createPayment.
	Operation core.PaymentOperation `json:"operation,omitempty"`
	// ConversationID lets a per-order Policy.Override apply to this quote.
	ConversationID string `json:"conversationId,omitempty"`
}

// QuoteAmount is the priced amount.
type QuoteAmount struct {
	Price       string `json:"price"`
	PriceMinor  int64  `json:"priceMinor"`
	Currency    string `json:"currency"`
	Installment int    `json:"installment"`
	ThreeDS     bool   `json:"threeDS"`
}

// QuoteCard is the card classification used for a quote. It never contains the
// PAN or the BIN.
type QuoteCard struct {
	BankName        string `json:"bankName,omitempty"`
	CardFamily      string `json:"cardFamily,omitempty"`
	CardType        string `json:"cardType,omitempty"`
	CardAssociation string `json:"cardAssociation,omitempty"`
	Commercial      *bool  `json:"commercial,omitempty"`
}

// Candidate is one ranked provider. Cost, Rate, Fixed, CostMinor and RateIndex
// are nil for a fallback provider without a rate line.
type Candidate struct {
	Provider string `json:"provider"`
	// Cost is the commission for this payment, formatted with 2 decimals.
	Cost *string `json:"cost"`
	// CostMinor is the same commission in minor units, the value the ranking
	// compares.
	CostMinor *int64 `json:"costMinor"`
	// Rate is the applied percentage, as configured.
	Rate *string `json:"rate"`
	// Fixed is the applied fixed fee, formatted with 2 decimals.
	Fixed     *string         `json:"fixed"`
	RateIndex *int            `json:"rateIndex"`
	Chosen    bool            `json:"chosen"`
	Reason    CandidateReason `json:"reason"`
	Detail    string          `json:"detail"`
}

// Quote is the explainable result of pricing one payment.
type Quote struct {
	Strategy Strategy    `json:"strategy"`
	Amount   QuoteAmount `json:"amount"`
	// Card is nil when the BIN classification is unknown.
	Card *QuoteCard `json:"card"`
	// Selected is nil when nothing is eligible.
	Selected *string `json:"selected"`
	// SelectionReason is nil when there is no candidate.
	SelectionReason *SelectionReason   `json:"selectionReason"`
	Candidates      []Candidate        `json:"candidates"`
	Excluded        []ExcludedProvider `json:"excluded"`
	Notes           []string           `json:"notes"`
}

// CacheOptions bounds the BIN cache. The cache is always bounded and has a TTL.
type CacheOptions struct {
	// MaxEntries defaults to 1024.
	MaxEntries int `json:"maxEntries,omitempty"`
	// TTLMS is the entry lifetime in milliseconds. Defaults to 24 hours.
	TTLMS int64 `json:"ttlMs,omitempty"`
}

// ShareCapOptions caps the share of one provider over a sliding window.
type ShareCapOptions struct {
	// MaxShare is between 0 (exclusive) and 1 (inclusive).
	MaxShare float64 `json:"maxShare"`
	// Window is the number of recent routing decisions the cap looks at.
	// Defaults to 100.
	Window int `json:"window,omitempty"`
}

// OverrideContext is what Policy.Override sees. It never receives the payment
// request or card data.
type OverrideContext struct {
	Operation      core.PaymentOperation
	ConversationID string
	Amount         QuoteAmount
	// Candidates are the eligible providers, cheapest first.
	Candidates []string
	Card       *QuoteCard
}

// Override returns a provider id to route this payment there, or an empty
// string to keep the normal ranking.
type Override func(ctx context.Context, octx OverrideContext) (string, error)

// Policy holds the routing policies: ties, share cap, per-order override.
type Policy struct {
	// Prefer lists providers to prefer when total costs are equal (earlier wins).
	Prefer []string `json:"prefer,omitempty"`
	// ShareCap caps the share of one provider over a sliding window.
	ShareCap *ShareCapOptions `json:"shareCap,omitempty"`
	// Override is a per-order decision.
	Override Override `json:"-"`
}

// IsHealthy reports provider health for new payments. An error is treated as
// healthy: a broken health monitor must not stop every payment.
type IsHealthy func(ctx context.Context, providerID string) (bool, error)

// Options configures the commission router.
type Options struct {
	// Strategy defaults to StrategyLowestCost.
	Strategy Strategy `json:"strategy,omitempty"`
	// Rates are the merchant's contract lines. They are never guessed.
	Rates []CommissionRate `json:"rates"`
	// Fallback lists provider ids to use when no rate line is eligible. Tried in
	// order, and also the retry order after a known-safe failure.
	Fallback []string `json:"fallback,omitempty"`
	// BinLookup classifies the 6-8 digit BIN prefix. Without it, rate lines that
	// filter on card family/bank/type/commercial cannot match.
	BinLookup BinLookup     `json:"-"`
	Cache     *CacheOptions `json:"cache,omitempty"`
	Policy    *Policy       `json:"policy,omitempty"`
	// Store remembers the provider of each payment so refunds, cancels and
	// status calls reach the same provider. Defaults to a bounded in-process
	// store; multi-instance deployments must pass a shared one.
	Store Store `json:"-"`
	// IsHealthy excludes unhealthy providers from new payments.
	IsHealthy IsHealthy `json:"-"`
	// FallbackOn lists the error codes that allow a fallback to the next
	// candidate. Only errors that prove no charge happened may be listed.
	// Defaults to INVALID_REQUEST and PROVIDER_ERROR. NETWORK_ERROR, declines
	// and unknown outcomes are always refused.
	FallbackOn []string `json:"fallbackOn,omitempty"`
	// Now is the clock seam in milliseconds since the epoch, for rate validity,
	// the BIN cache and the share window. Nil keeps time.Now.
	Now func() int64 `json:"-"`
}

// CacheStatus is the BIN-cache usage reported by Status.
type CacheStatus struct {
	Entries    int   `json:"entries"`
	MaxEntries int   `json:"maxEntries"`
	TTLMS      int64 `json:"ttlMs"`
}

// Status reports routing counters and BIN-cache usage.
type Status struct {
	Routed      map[string]int `json:"routed"`
	Total       int            `json:"total"`
	ShareWindow int            `json:"shareWindow"`
	BinCache    CacheStatus    `json:"binCache"`
}

// StringList accepts a single string or a list of them in JSON.
type StringList []string

// UnmarshalJSON accepts both "a" and ["a","b"].
func (l *StringList) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*l = nil
		return nil
	}
	if raw[0] == '"' {
		var single string
		if err := json.Unmarshal(raw, &single); err != nil {
			return err
		}
		*l = StringList{single}
		return nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	*l = items
	return nil
}

// MarshalJSON writes a single entry as a string and everything else as a list.
func (l StringList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("null"), nil
	}
	if len(l) == 1 {
		return json.Marshal(l[0])
	}
	return json.Marshal([]string(l))
}

// RoutedPaymentResponse is a payment result with the provider that actually
// handled it. The core response types carry no provider field, so the after hook
// returns this wrapper; the embedded response keeps every field and JSON name.
type RoutedPaymentResponse struct {
	*core.PaymentResponse
	Provider string `json:"provider,omitempty"`
}

// RoutedThreeDSInitResponse is a 3D Secure initiation result with its provider.
type RoutedThreeDSInitResponse struct {
	*core.ThreeDSInitResponse
	Provider string `json:"provider,omitempty"`
}

// RoutedRefundResponse is a refund result with its provider.
type RoutedRefundResponse struct {
	*core.RefundResponse
	Provider string `json:"provider,omitempty"`
}

// RoutedCancelResponse is a cancellation result with its provider.
type RoutedCancelResponse struct {
	*core.CancelResponse
	Provider string `json:"provider,omitempty"`
}

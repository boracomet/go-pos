package routing

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/boracomet/go-pos/core"
)

// BinInfo is the card classification injected by BinResolver or carried by the
// request. It is never fetched from the network inside the plugin.
type BinInfo struct {
	// Bank is the issuing bank, e.g. "Garanti BBVA".
	Bank string `json:"bank,omitempty"`
	// BankName is an alias of Bank for resolvers built on provider BIN responses.
	BankName string `json:"bankName,omitempty"`
	// CardFamily is the card program, e.g. "Bonus" or "World".
	CardFamily string `json:"cardFamily,omitempty"`
	CardType   string `json:"cardType,omitempty"`
	CardOrigin string `json:"cardOrigin,omitempty"`
}

// CardInfo groups the BIN fields a request may carry under "card".
type CardInfo struct {
	BinInfo
	Bin string `json:"bin,omitempty"`
}

// CardNumber is the only part of a payment card routing reads.
type CardNumber struct {
	CardNumber string `json:"cardNumber,omitempty"`
}

// Request is the part of a payment request routing reads. A *core.PaymentRequest
// is assignable to it through the engine's normalization.
type Request struct {
	Price          string      `json:"price,omitempty"`
	PaidPrice      string      `json:"paidPrice,omitempty"`
	Amount         string      `json:"amount,omitempty"`
	Currency       string      `json:"currency,omitempty"`
	Installment    *int        `json:"installment,omitempty"`
	ThreeDS        *bool       `json:"threeDS,omitempty"`
	ConversationID string      `json:"conversationId,omitempty"`
	BasketID       string      `json:"basketId,omitempty"`
	PaymentID      string      `json:"paymentId,omitempty"`
	BinNumber      string      `json:"binNumber,omitempty"`
	Bin            string      `json:"bin,omitempty"`
	Bank           string      `json:"bank,omitempty"`
	BankName       string      `json:"bankName,omitempty"`
	CardFamily     string      `json:"cardFamily,omitempty"`
	CardType       string      `json:"cardType,omitempty"`
	CardOrigin     string      `json:"cardOrigin,omitempty"`
	Card           *CardInfo   `json:"card,omitempty"`
	PaymentCard    *CardNumber `json:"paymentCard,omitempty"`
}

// Context is what a rule predicate gets. Never log it: Request has card data.
type Context struct {
	Operation core.PaymentOperation
	Request   Request
	Amount    string
	Currency  string
	// Installment is 1 when the request carries no usable count.
	Installment int
	ThreeDS     bool
	// The fields below are empty when unknown.
	Bin        string
	Bank       string
	CardFamily string
	CardType   string
	CardOrigin string
}

// StringList is a condition value that accepts one string or a list of them.
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

// InstallmentCondition is an exact count or an inclusive range. Min 2 means 2
// and above.
type InstallmentCondition struct {
	Exact *int `json:"-"`
	Min   *int `json:"min,omitempty"`
	Max   *int `json:"max,omitempty"`
}

// UnmarshalJSON accepts both 6 and {"min":2,"max":6}.
func (c *InstallmentCondition) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '{' {
		type bounds struct {
			Min *int `json:"min"`
			Max *int `json:"max"`
		}
		var b bounds
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		c.Exact, c.Min, c.Max = nil, b.Min, b.Max
		return nil
	}
	var exact int
	if err := json.Unmarshal(raw, &exact); err != nil {
		return err
	}
	c.Exact, c.Min, c.Max = &exact, nil, nil
	return nil
}

// MarshalJSON writes an exact count as a number and a range as an object.
func (c InstallmentCondition) MarshalJSON() ([]byte, error) {
	if c.Exact != nil {
		return json.Marshal(*c.Exact)
	}
	type bounds struct {
		Min *int `json:"min,omitempty"`
		Max *int `json:"max,omitempty"`
	}
	return json.Marshal(bounds{Min: c.Min, Max: c.Max})
}

// AmountRange holds inclusive bounds on the request price, as decimal strings.
type AmountRange struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

// When are the conditions of a rule. Every field that is set must match (AND);
// a field that is not set does not restrict the rule.
type When struct {
	// Bin holds prefixes: "454671" matches every BIN starting with it.
	Bin         StringList            `json:"bin,omitempty"`
	Bank        StringList            `json:"bank,omitempty"`
	CardFamily  StringList            `json:"cardFamily,omitempty"`
	CardType    StringList            `json:"cardType,omitempty"`
	CardOrigin  StringList            `json:"cardOrigin,omitempty"`
	ThreeDS     *bool                 `json:"threeDS,omitempty"`
	Operation   StringList            `json:"operation,omitempty"`
	Currency    StringList            `json:"currency,omitempty"`
	Installment *InstallmentCondition `json:"installment,omitempty"`
	Amount      *AmountRange          `json:"amount,omitempty"`
	// Predicate is a custom condition. An error fails the operation before any
	// charge.
	Predicate func(ctx *Context) (bool, error) `json:"-"`
}

// Weight is one provider's share of a weighted split.
type Weight struct {
	Provider string
	Weight   float64
}

// Target is where a rule (or the default) sends a payment: one provider id, an
// ordered list whose first eligible entry is used, or a deterministic weighted
// split. Set exactly one field.
type Target struct {
	Provider  string   `json:"-"`
	Providers []string `json:"-"`
	// Weights keeps the configured order: the split walks it in that order.
	Weights []Weight `json:"-"`
}

// ProviderTarget targets a single provider id.
func ProviderTarget(provider string) *Target { return &Target{Provider: provider} }

// ListTarget targets an ordered list of provider ids.
func ListTarget(providers ...string) *Target { return &Target{Providers: providers} }

// WeightsTarget targets a weighted split, in the given order.
func WeightsTarget(weights ...Weight) *Target { return &Target{Weights: weights} }

type targetKind int

const (
	targetInvalid targetKind = iota
	targetProvider
	targetList
	targetWeights
)

func (t *Target) kind() targetKind {
	if t == nil {
		return targetInvalid
	}
	if t.Provider != "" {
		return targetProvider
	}
	if t.Providers != nil {
		return targetList
	}
	if t.Weights != nil {
		return targetWeights
	}
	return targetInvalid
}

// ids returns the provider ids of the target in configuration order.
func (t *Target) ids() []string {
	switch t.kind() {
	case targetProvider:
		return []string{t.Provider}
	case targetList:
		return t.Providers
	case targetWeights:
		ids := make([]string, 0, len(t.Weights))
		for _, entry := range t.Weights {
			ids = append(ids, entry.Provider)
		}
		return ids
	default:
		return nil
	}
}

// MarshalJSON writes the accepted shapes: a string, a list, or {"weights":{...}}.
func (t Target) MarshalJSON() ([]byte, error) {
	switch t.kind() {
	case targetProvider:
		return json.Marshal(t.Provider)
	case targetList:
		return json.Marshal(t.Providers)
	case targetWeights:
		var buf bytes.Buffer
		buf.WriteString(`{"weights":{`)
		for index, entry := range t.Weights {
			if index > 0 {
				buf.WriteByte(',')
			}
			key, err := json.Marshal(entry.Provider)
			if err != nil {
				return nil, err
			}
			buf.Write(key)
			buf.WriteByte(':')
			value, err := json.Marshal(entry.Weight)
			if err != nil {
				return nil, err
			}
			buf.Write(value)
		}
		buf.WriteString("}}")
		return buf.Bytes(), nil
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON reads a string, a list or {"weights":{...}}, keeping the weight
// order so a split stays deterministic across a persistence round trip.
func (t *Target) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	*t = Target{}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	switch raw[0] {
	case '"':
		return json.Unmarshal(raw, &t.Provider)
	case '[':
		return json.Unmarshal(raw, &t.Providers)
	case '{':
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if _, err := decoder.Token(); err != nil {
			return err
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, _ := key.(string)
			if name != "weights" {
				var skip json.RawMessage
				if err := decoder.Decode(&skip); err != nil {
					return err
				}
				continue
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			for decoder.More() {
				weightKey, err := decoder.Token()
				if err != nil {
					return err
				}
				provider, _ := weightKey.(string)
				var value float64
				if err := decoder.Decode(&value); err != nil {
					return err
				}
				t.Weights = append(t.Weights, Weight{Provider: provider, Weight: value})
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if t.Weights == nil {
			t.Weights = []Weight{}
		}
		return nil
	default:
		return core.NewConfigurationError("routing: target must be a provider id, a list or { weights }", "")
	}
}

// StrategyContext is what a custom strategy chooses from.
type StrategyContext struct {
	Operation core.PaymentOperation
	// Request is the operation's request; never log it.
	Request Request
	// Candidates are the eligible provider ids, in target (or provider
	// configuration) order.
	Candidates []string
}

// Strategy picks one of Candidates. An empty return lets the next rule (or the
// default) decide.
type Strategy struct {
	ID     string
	Choose func(ctx context.Context, sctx StrategyContext) (string, error)
}

// Rule is one routing rule.
type Rule struct {
	// Name is shown in Explain and in decision events. Default: "rule[index]".
	Name string `json:"name,omitempty"`
	When *When  `json:"when,omitempty"`
	// Use is optional when a strategy is set: then every enabled provider is a
	// candidate.
	Use *Target `json:"use,omitempty"`
	// Strategy names one of Options.Strategies.
	Strategy string `json:"strategy,omitempty"`
	// StrategyRef is an inline strategy; it wins over Strategy.
	StrategyRef *Strategy `json:"-"`
}

// Health is injected: the plugin never measures providers itself.
type Health struct {
	// IsAvailable returns false to exclude a provider from new payments. An
	// error counts as false. Refunds and cancels still go to the provider that
	// took the payment.
	IsAvailable func(ctx context.Context, providerID string) (bool, error)
}

// Store remembers which provider handled a payment, so refunds, cancels,
// captures, voids and status calls reach that provider and retries of the same
// order never move. It has the shape of core.IdempotencyStore, so the same
// Redis/SQL adapter can be passed.
type Store interface {
	// Get returns an empty string when the key is absent or expired.
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttlSeconds int) error
	Delete(ctx context.Context, key string) error
}

// Options configures the routing plugin.
type Options struct {
	// Rules are checked in order; the first matching rule with an eligible
	// target wins.
	Rules []Rule `json:"rules,omitempty"`
	// Default is the target when no rule matched. Empty means the facade's
	// default provider.
	Default *Target `json:"default,omitempty"`
	Health  *Health `json:"-"`
	Store   Store   `json:"-"`
	// BinResolver classifies the 6-8 digit BIN prefix. It must not fetch the
	// network from inside the plugin.
	BinResolver func(ctx context.Context, bin string) (*BinInfo, error) `json:"-"`
	// Strategies are the named strategies Rules may reference.
	Strategies map[string]*Strategy `json:"-"`
}

// SkipReason is why a provider was not used.
type SkipReason string

const (
	// SkipNotEnabled means the provider is not configured.
	SkipNotEnabled SkipReason = "not-enabled"
	// SkipUnhealthy means the health hook excluded the provider.
	SkipUnhealthy SkipReason = "unhealthy"
)

// Skip names one provider that was not used and why.
type Skip struct {
	Provider string     `json:"provider"`
	Reason   SkipReason `json:"reason"`
}

// Reason is what selected the provider.
type Reason string

const (
	ReasonRule     Reason = "rule"
	ReasonStrategy Reason = "strategy"
	ReasonDefault  Reason = "default"
	// ReasonPinned is a retry of an order that was already routed: the provider
	// never moves.
	ReasonPinned Reason = "pinned"
	// ReasonFollowUp is the provider that handled the payment.
	ReasonFollowUp Reason = "follow-up"
	// ReasonUnknownPayment is a follow-up of a payment the plugin does not know:
	// it fell back to the default.
	ReasonUnknownPayment Reason = "unknown-payment"
	// ReasonNone means nothing was eligible; the facade default is not overridden.
	ReasonNone Reason = "none"
)

// Decision is the result of a routing decision. It never contains card data or
// the BIN.
type Decision struct {
	// Provider is empty when nothing is eligible.
	Provider string `json:"provider"`
	// Rule is the rule name, or nil for a default/pinned/follow-up decision.
	Rule      *string               `json:"rule"`
	RuleIndex *int                  `json:"ruleIndex"`
	Reason    Reason                `json:"reason"`
	Operation core.PaymentOperation `json:"operation"`
	Skipped   []Skip                `json:"skipped"`
	// Detail is a human-readable explanation, safe for logs and dashboards.
	Detail string `json:"detail"`
}

// Status counts routed charges per provider in this process.
type Status struct {
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// ExplainOptions are the options of Explain.
type ExplainOptions struct {
	// Operation defaults to createPayment.
	Operation core.PaymentOperation
}

// DecisionListener is an audit listener, called for every routing decision
// before the provider call. An error never changes where a payment goes.
type DecisionListener func(ctx context.Context, decision *Decision) error

// EngineContext is everything the engine needs besides the rule options.
type EngineContext struct {
	ProviderIDs     []string
	DefaultProvider string
	Logger          core.Logger
}

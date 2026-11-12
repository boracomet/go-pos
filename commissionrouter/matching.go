package commissionrouter

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// QuoteCriteria is one payment, reduced to what a rate line is matched against.
// Matching is pure: no clock other than Now, no I/O, no floats in the comparison
// path.
type QuoteCriteria struct {
	AmountMinor int64
	// Currency is the uppercased 3-letter currency.
	Currency    string
	Installment int
	ThreeDS     bool
	// Card is nil when no BIN lookup is configured or the BIN is unknown.
	Card *BinInfo
	// Now is milliseconds since the epoch, for validity dates.
	Now int64
}

// PreparedLine is a rate line with its maths precomputed once, at construction.
type PreparedLine struct {
	// Index in the configured table, used in quotes and for stable tie-breaking.
	Index    int
	Provider string
	// Rate is the configured percentage as written ("2.49").
	Rate string
	// Fixed is the fixed fee formatted with 2 decimals ("0.00").
	Fixed string
	// RateScaled is the percentage multiplied by 10^4: "2.49" -> 24900.
	RateScaled      *big.Int
	FixedMinor      int64
	MinMinor        *int64
	MaxMinor        *int64
	Currencies      []string
	Installments    []int
	ThreeDS         *bool
	ValidFrom       *int64
	ValidTo         *int64
	CardFamily      []string
	CardAssociation []string
	CardType        []string
	BankName        []string
	Commercial      *bool
}

func toList(value StringList) []string {
	if value == nil {
		return nil
	}
	items := make([]string, len(value))
	for index, item := range value {
		items[index] = strings.ToLower(strings.TrimSpace(item))
	}
	return items
}

func int64Ptr(value int64) *int64 { return &value }

func boolPtr(value bool) *bool { return &value }

// PrepareRates validates and normalizes the table for one router. The table has
// already passed ValidateRateTable; this only converts decimals to integers and
// lowercases the card filters.
func PrepareRates(rates []CommissionRate) ([]PreparedLine, error) {
	lines := make([]PreparedLine, 0, len(rates))
	for index, entry := range rates {
		var fixedMinor int64
		if entry.Fixed != "" {
			value, err := AmountMinor(entry.Fixed, fmt.Sprintf("rates[%d].fixed", index))
			if err != nil {
				return nil, err
			}
			fixedMinor = value
		}
		rate := strings.TrimSpace(entry.Rate)
		scaled, err := parseRate(rate, index)
		if err != nil {
			return nil, err
		}
		line := PreparedLine{
			Index:           index,
			Provider:        entry.Provider,
			Rate:            rate,
			Fixed:           FormatMinor(fixedMinor),
			RateScaled:      scaled,
			FixedMinor:      fixedMinor,
			ThreeDS:         entry.ThreeDS,
			CardFamily:      toList(entry.CardFamily),
			CardAssociation: toList(entry.CardAssociation),
			CardType:        toList(entry.CardType),
			BankName:        toList(entry.BankName),
			Commercial:      entry.Commercial,
		}
		if entry.MinAmount != "" {
			value, err := AmountMinor(entry.MinAmount, "minAmount")
			if err != nil {
				return nil, err
			}
			line.MinMinor = int64Ptr(value)
		}
		if entry.MaxAmount != "" {
			value, err := AmountMinor(entry.MaxAmount, "maxAmount")
			if err != nil {
				return nil, err
			}
			line.MaxMinor = int64Ptr(value)
		}
		if entry.Currencies != nil {
			codes := make([]string, len(entry.Currencies))
			for position, code := range entry.Currencies {
				codes[position] = strings.ToUpper(code)
			}
			line.Currencies = codes
		}
		if entry.Installments != nil {
			line.Installments = append([]int(nil), entry.Installments...)
		}
		if entry.ValidFrom != "" {
			if parsed, ok := parseDate(entry.ValidFrom); ok {
				line.ValidFrom = int64Ptr(parsed)
			}
		}
		if entry.ValidTo != "" {
			if parsed, ok := parseDate(entry.ValidTo); ok {
				line.ValidTo = int64Ptr(parsed)
			}
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// parseRate parses a validated percentage into its 10^4-scaled integer form.
func parseRate(rate string, index int) (*big.Int, error) {
	whole, fraction := rate, ""
	if position := strings.IndexByte(rate, '.'); position >= 0 {
		whole, fraction = rate[:position], rate[position+1:]
	}
	padded := fraction + "0000"
	scaled, ok := new(big.Int).SetString(whole+padded[:4], 10)
	if !ok {
		return nil, core.NewConfigurationError(fmt.Sprintf(
			"rates[%d].rate must be a positive decimal with at most 4 decimals, got '%s'", index, rate), "")
	}
	return scaled, nil
}

type lineFailure struct {
	reason ExclusionReason
	detail string
}

func cardValue(value string) (string, bool) {
	text := strings.ToLower(strings.TrimSpace(value))
	return text, text != ""
}

func matchesCardText(expected []string, actual string, known bool) string {
	if !known {
		return "unknown"
	}
	if containsString(expected, actual) {
		return "ok"
	}
	return "mismatch"
}

// collectFailures lists every reason this line does not apply to the payment, in
// a stable order.
func collectFailures(line *PreparedLine, criteria QuoteCriteria) []lineFailure {
	failures := make([]lineFailure, 0, 4)
	card := criteria.Card

	if line.ValidFrom != nil && criteria.Now < *line.ValidFrom {
		failures = append(failures, lineFailure{ExclusionRateNotYetValid, "the rate line is not valid yet"})
	}
	if line.ValidTo != nil && criteria.Now >= *line.ValidTo {
		failures = append(failures, lineFailure{ExclusionRateExpired, "the rate line has expired"})
	}
	if line.MinMinor != nil && criteria.AmountMinor < *line.MinMinor {
		failures = append(failures, lineFailure{ExclusionAmountBelowMin,
			fmt.Sprintf("the amount is below the line's minimum of %s", FormatMinor(*line.MinMinor))})
	}
	if line.MaxMinor != nil && criteria.AmountMinor > *line.MaxMinor {
		failures = append(failures, lineFailure{ExclusionAmountAboveMax,
			fmt.Sprintf("the amount is above the line's maximum of %s", FormatMinor(*line.MaxMinor))})
	}
	if line.Currencies != nil && !containsString(line.Currencies, criteria.Currency) {
		failures = append(failures, lineFailure{ExclusionCurrencyNotSupported,
			fmt.Sprintf("the line covers %s, not %s", strings.Join(line.Currencies, ", "), criteria.Currency)})
	}
	if line.Installments != nil && !containsInt(line.Installments, criteria.Installment) {
		failures = append(failures, lineFailure{ExclusionInstallmentNotSupported,
			fmt.Sprintf("installment %d is not in [%s]", criteria.Installment, joinInts(line.Installments))})
	}
	if line.ThreeDS != nil && *line.ThreeDS != criteria.ThreeDS {
		detail := "the line is for non-3DS payments only"
		if *line.ThreeDS {
			detail = "the line is for 3D Secure payments only"
		}
		failures = append(failures, lineFailure{ExclusionThreeDSNotSupported, detail})
	}

	for _, filter := range []struct {
		expected []string
		raw      string
		label    string
		unknown  string
	}{
		{line.CardFamily, cardField(card, func(info *BinInfo) string { return info.CardFamily }),
			"card family", "the card family is unknown (no BIN lookup)"},
		{line.CardAssociation, cardField(card, func(info *BinInfo) string { return info.CardAssociation }),
			"card association", "the card association is unknown (no BIN lookup)"},
		{line.CardType, cardField(card, func(info *BinInfo) string { return info.CardType }),
			"card type", "the card type is unknown (no BIN lookup)"},
		{line.BankName, cardField(card, func(info *BinInfo) string { return info.BankName }),
			"issuing bank", "the issuing bank is unknown (no BIN lookup)"},
	} {
		if filter.expected == nil {
			continue
		}
		actual, known := cardValue(filter.raw)
		switch matchesCardText(filter.expected, actual, known) {
		case "unknown":
			failures = append(failures, lineFailure{ExclusionCardUnknown, filter.unknown})
		case "mismatch":
			failures = append(failures, lineFailure{ExclusionCardMismatch,
				fmt.Sprintf("%s '%s' is not %s", filter.label, filter.raw, strings.Join(filter.expected, ", "))})
		}
	}

	if line.Commercial != nil {
		if card == nil || card.Commercial == nil {
			failures = append(failures, lineFailure{ExclusionCardUnknown,
				"whether the card is commercial is unknown (no BIN lookup)"})
		} else if *card.Commercial != *line.Commercial {
			detail := "the card is commercial"
			if *line.Commercial {
				detail = "the card is not commercial"
			}
			failures = append(failures, lineFailure{ExclusionCardMismatch, detail})
		}
	}
	return failures
}

func cardField(card *BinInfo, pick func(*BinInfo) string) string {
	if card == nil {
		return ""
	}
	return pick(card)
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = fmt.Sprintf("%d", value)
	}
	return strings.Join(parts, ", ")
}

// ProviderEvaluation is one provider's outcome: either an eligible cost from its
// cheapest applicable line, or the reason from the line that came closest.
type ProviderEvaluation struct {
	Provider  string
	Eligible  bool
	Line      *PreparedLine
	CostMinor int64
	Reason    ExclusionReason
	Detail    string
	RateIndex *int
}

// EvaluateProvider evaluates one provider. When several lines apply, the
// cheapest one is the provider's cost (the merchant's contract is the best
// applicable line). When none applies, the reason reported is from the line that
// came closest, so the quote explains what the merchant would have to change.
func EvaluateProvider(provider string, lines []PreparedLine, criteria QuoteCriteria) ProviderEvaluation {
	providerLines := make([]*PreparedLine, 0, len(lines))
	for index := range lines {
		if lines[index].Provider == provider {
			providerLines = append(providerLines, &lines[index])
		}
	}
	if len(providerLines) == 0 {
		return ProviderEvaluation{
			Provider: provider,
			Reason:   ExclusionNoRateLine,
			Detail:   "no rate line for this provider",
		}
	}

	var best *PreparedLine
	var bestCost int64
	var closest *PreparedLine
	var closestFailures []lineFailure

	for _, line := range providerLines {
		failures := collectFailures(line, criteria)
		if len(failures) == 0 {
			cost := PercentCostMinorScaled(criteria.AmountMinor, line.RateScaled) + line.FixedMinor
			if best == nil || cost < bestCost || (cost == bestCost && line.Index < best.Index) {
				best, bestCost = line, cost
			}
			continue
		}
		if closest == nil || len(failures) < len(closestFailures) ||
			(len(failures) == len(closestFailures) && line.Index < closest.Index) {
			closest, closestFailures = line, failures
		}
	}

	if best != nil {
		return ProviderEvaluation{Provider: provider, Eligible: true, Line: best, CostMinor: bestCost}
	}

	evaluation := ProviderEvaluation{
		Provider: provider,
		Reason:   ExclusionNoRateLine,
		Detail:   "no rate line applies",
	}
	if closest != nil {
		index := closest.Index
		evaluation.RateIndex = &index
		if len(closestFailures) > 0 {
			evaluation.Reason = closestFailures[0].reason
			evaluation.Detail = closestFailures[0].detail
		}
	}
	return evaluation
}

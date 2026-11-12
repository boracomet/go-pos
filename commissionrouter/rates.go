package commissionrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// BinInfo is a card classification as a BIN lookup returns it. Every field is
// optional, so a provider's *core.BinCheckResponse can be passed unchanged; a
// missing field simply cannot satisfy a filter.
type BinInfo struct {
	// BinNumber is the 6 or 8 digit BIN prefix that matched.
	BinNumber       string `json:"binNumber,omitempty"`
	BankName        string `json:"bankName,omitempty"`
	BankCode        *int   `json:"bankCode,omitempty"`
	CardFamily      string `json:"cardFamily,omitempty"`
	CardType        string `json:"cardType,omitempty"`
	CardAssociation string `json:"cardAssociation,omitempty"`
	Commercial      *bool  `json:"commercial,omitempty"`
}

// BinLookup receives at most the first 8 digits of the card (never a full PAN)
// and returns the classification, or nil for unknown.
type BinLookup func(ctx context.Context, binPrefix string) (*BinInfo, error)

// CommissionRate is one merchant contract line: what Provider charges for such
// a card, such an installment plan and such an amount. Rates are the merchant's
// real contract values; the router never estimates them.
type CommissionRate struct {
	Provider        string     `json:"provider"`
	CardFamily      StringList `json:"cardFamily,omitempty"`
	CardAssociation StringList `json:"cardAssociation,omitempty"`
	CardType        StringList `json:"cardType,omitempty"`
	BankName        StringList `json:"bankName,omitempty"`
	Commercial      *bool      `json:"commercial,omitempty"`
	Installments    []int      `json:"installments,omitempty"`
	// Rate is the percentage commission with at most 4 decimals: "2.49" is
	// 2.49%. A JSON number is accepted and kept as written.
	Rate string `json:"rate"`
	// Fixed is the per-transaction fee with at most 2 decimals.
	Fixed      string   `json:"fixed,omitempty"`
	MinAmount  string   `json:"minAmount,omitempty"`
	MaxAmount  string   `json:"maxAmount,omitempty"`
	Currencies []string `json:"currencies,omitempty"`
	// ValidFrom is an ISO date/datetime from which the line is valid, inclusive.
	ValidFrom string `json:"validFrom,omitempty"`
	// ValidTo is an ISO date/datetime until which the line is valid, exclusive.
	ValidTo string `json:"validTo,omitempty"`
	// ThreeDS true covers 3D Secure operations only, false non-3DS only, nil both.
	ThreeDS *bool `json:"threeDS,omitempty"`
}

// UnmarshalJSON also accepts JSON numbers for the decimal fields, keeping the
// literal text so no amount ever passes through a float.
func (r *CommissionRate) UnmarshalJSON(data []byte) error {
	type plain CommissionRate
	var raw struct {
		*plain
		Rate      json.RawMessage `json:"rate"`
		Fixed     json.RawMessage `json:"fixed"`
		MinAmount json.RawMessage `json:"minAmount"`
		MaxAmount json.RawMessage `json:"maxAmount"`
	}
	raw.plain = (*plain)(r)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, field := range []struct {
		raw  json.RawMessage
		dest *string
	}{
		{raw.Rate, &r.Rate},
		{raw.Fixed, &r.Fixed},
		{raw.MinAmount, &r.MinAmount},
		{raw.MaxAmount, &r.MaxAmount},
	} {
		value, err := decimalFromJSON(field.raw)
		if err != nil {
			return err
		}
		*field.dest = value
	}
	return nil
}

func decimalFromJSON(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return "", err
	}
	return string(number), nil
}

// Amounts up to 2 decimals, no sign, at most 13 integer digits: 13 digits * 100
// is still a safe integer in minor units, so every amount compares exactly.
var decimal2 = regexp.MustCompile(`^\d{1,13}(\.\d{1,2})?$`)

// Percentages up to 4 decimals.
var decimal4 = regexp.MustCompile(`^\d{1,3}(\.\d{1,4})?$`)

// maxSafeInteger is the largest integer the reference implementation compares
// exactly (Number.MAX_SAFE_INTEGER).
const maxSafeInteger = 9007199254740991

// ParseDecimal parses a decimal into its exact scaled integer form: "2.49" with
// 2 decimals is 249. The decimal is kept exact, never parsed as a float.
func ParseDecimal(value string, maxDecimals int, field string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	pattern := decimal2
	if maxDecimals == 4 {
		pattern = decimal4
	}
	if !pattern.MatchString(trimmed) {
		return nil, core.NewConfigurationError(fmt.Sprintf(
			"%s must be a positive decimal with at most %d decimals, got '%s'", field, maxDecimals, trimmed), "")
	}
	whole, fraction := trimmed, ""
	if index := strings.IndexByte(trimmed, '.'); index >= 0 {
		whole, fraction = trimmed[:index], trimmed[index+1:]
	}
	for len(fraction) < maxDecimals {
		fraction += "0"
	}
	digits, ok := new(big.Int).SetString(whole+fraction[:maxDecimals], 10)
	if !ok {
		return nil, core.NewConfigurationError(fmt.Sprintf(
			"%s must be a positive decimal with at most %d decimals, got '%s'", field, maxDecimals, trimmed), "")
	}
	return digits, nil
}

// AmountMinor converts a 2-decimal amount to exact integer minor units
// (kuruş/cents). It rejects values whose minor-unit form could not be compared
// exactly.
func AmountMinor(value string, field string) (int64, error) {
	digits, err := ParseDecimal(value, 2, field)
	if err != nil {
		return 0, err
	}
	if digits.Cmp(big.NewInt(maxSafeInteger)) > 0 {
		return 0, core.NewConfigurationError(field+" is too large to be compared exactly", "")
	}
	return digits.Int64(), nil
}

// FormatMinor renders integer minor units as a 2-decimal string using integer
// arithmetic only (1290 -> "12.90"). Locale-independent on purpose.
func FormatMinor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

// PercentCostMinorScaled is amountMinor * ratePct / 100, rounded half up,
// exactly. scaledRate is the percentage multiplied by 10^4 (so "2.49" is 24900);
// the result is in the same minor units as the input and compares as an integer.
//
// A tie (exactly half a minor unit) rounds up: 0.5 kuruş becomes 1 kuruş. Used
// for ranking only; no price charged to a customer is computed from it.
func PercentCostMinorScaled(amountMinor int64, scaledRate *big.Int) int64 {
	numerator := new(big.Int).Mul(big.NewInt(amountMinor), scaledRate)
	denominator := big.NewInt(100 * 10_000)
	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient.Int64()
}

// PercentCostMinor is PercentCostMinorScaled from a configured percentage
// ("2.49" -> 2.49%).
func PercentCostMinor(amountMinor int64, rate string) (int64, error) {
	scaled, err := ParseDecimal(rate, 4, "rate")
	if err != nil {
		return 0, err
	}
	return PercentCostMinorScaled(amountMinor, scaled), nil
}

func checkList(value StringList, field string, index int) error {
	for _, item := range value {
		if strings.TrimSpace(item) == "" {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d].%s must be a non-empty string or a list of them", index, field), "")
		}
	}
	return nil
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseDate reads an ISO date or datetime as milliseconds since the epoch.
// A value without an offset is read as UTC.
func parseDate(value string) (int64, bool) {
	trimmed := strings.TrimSpace(value)
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UnixMilli(), true
		}
	}
	return 0, false
}

func checkDate(value string, field string, index int) (int64, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	parsed, ok := parseDate(value)
	if !ok {
		return 0, false, core.NewConfigurationError(fmt.Sprintf(
			"rates[%d].%s is not a valid ISO date: '%s'", index, field, value), "")
	}
	return parsed, true, nil
}

var currencyCode = regexp.MustCompile(`^[A-Za-z]{3}$`)

// ValidateRateTable validates the whole table once, at construction and at init,
// so a bad contract line can never reach a payment. With providerIDs the
// provider of every line must be configured; without it (nil) only the shape is
// checked. Bad decimals, empty filters and inverted ranges fail with a
// ConfigurationError.
func ValidateRateTable(rates []CommissionRate, providerIDs []string) error {
	for index := range rates {
		entry := &rates[index]
		if entry.Provider == "" {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d].provider must be a non-empty string", index), "")
		}
		if providerIDs != nil && !containsString(providerIDs, entry.Provider) {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d].provider '%s' is not a configured provider (%s)",
				index, entry.Provider, orNone(strings.Join(providerIDs, ", "))), "")
		}
		rate, err := ParseDecimal(entry.Rate, 4, fmt.Sprintf("rates[%d].rate", index))
		if err != nil {
			return err
		}
		if rate.Cmp(big.NewInt(1_000_000)) > 0 {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d].rate must be at most 100 (percent)", index), "")
		}
		if entry.Fixed != "" {
			if _, err := ParseDecimal(entry.Fixed, 2, fmt.Sprintf("rates[%d].fixed", index)); err != nil {
				return err
			}
		}
		var minimum, maximum *big.Int
		if entry.MinAmount != "" {
			minimum, err = ParseDecimal(entry.MinAmount, 2, fmt.Sprintf("rates[%d].minAmount", index))
			if err != nil {
				return err
			}
		}
		if entry.MaxAmount != "" {
			maximum, err = ParseDecimal(entry.MaxAmount, 2, fmt.Sprintf("rates[%d].maxAmount", index))
			if err != nil {
				return err
			}
		}
		if minimum != nil && maximum != nil && minimum.Cmp(maximum) > 0 {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d]: minAmount is above maxAmount", index), "")
		}
		if entry.Installments != nil {
			if len(entry.Installments) == 0 {
				return core.NewConfigurationError(fmt.Sprintf(
					"rates[%d].installments must be a non-empty array of counts", index), "")
			}
			for _, count := range entry.Installments {
				if count < 1 || count > 99 {
					return core.NewConfigurationError(fmt.Sprintf(
						"rates[%d].installments: every count must be an integer between 1 and 99", index), "")
				}
			}
		}
		for _, field := range []struct {
			name  string
			value StringList
		}{
			{"cardFamily", entry.CardFamily},
			{"cardAssociation", entry.CardAssociation},
			{"cardType", entry.CardType},
			{"bankName", entry.BankName},
		} {
			if err := checkList(field.value, field.name, index); err != nil {
				return err
			}
		}
		if entry.Currencies != nil {
			if len(entry.Currencies) == 0 {
				return core.NewConfigurationError(fmt.Sprintf(
					"rates[%d].currencies must be a non-empty array", index), "")
			}
			for _, code := range entry.Currencies {
				if !currencyCode.MatchString(code) {
					return core.NewConfigurationError(fmt.Sprintf(
						"rates[%d].currencies: '%s' is not a 3-letter currency code", index, code), "")
				}
			}
		}
		from, hasFrom, err := checkDate(entry.ValidFrom, "validFrom", index)
		if err != nil {
			return err
		}
		to, hasTo, err := checkDate(entry.ValidTo, "validTo", index)
		if err != nil {
			return err
		}
		if hasFrom && hasTo && from >= to {
			return core.NewConfigurationError(fmt.Sprintf(
				"rates[%d]: validFrom is not before validTo", index), "")
		}
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

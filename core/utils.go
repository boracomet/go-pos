package core

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// GenerateOrderID builds an alphanumeric order id (no separators) accepted by
// every supported provider, e.g. BP1727180000000A1B2C3D4.
func GenerateOrderID(prefix string) string {
	if prefix == "" {
		prefix = "BP"
	}
	suffix, err := RandomHex(4)
	if err != nil {
		// crypto/rand only fails when the system entropy source is unavailable.
		suffix = strconv.FormatInt(time.Now().UnixNano()%0xFFFFFFFF, 16)
	}
	return fmt.Sprintf("%s%d%s", prefix, time.Now().UnixMilli(), strings.ToUpper(suffix))
}

// ParseAmount parses a decimal amount. It rejects invalid or negative input.
func ParseAmount(amount, field string) (float64, error) {
	if field == "" {
		field = "amount"
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, NewValidationError(fmt.Sprintf("Invalid %s: %s", field, amount), "", nil)
	}
	return value, nil
}

// ToMinorUnits converts a decimal amount to kuruş/cents, rounding half up on the
// decimal representation (1.005 -> 101) rather than on the binary float.
func ToMinorUnits(amount, field string) (int64, error) {
	if _, err := ParseAmount(amount, field); err != nil {
		return 0, err
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	if !ok {
		return 0, NewValidationError(fmt.Sprintf("Invalid %s: %s", field, amount), "", nil)
	}
	r.Mul(r, big.NewRat(100, 1))
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64(), nil
}

// FormatDecimal renders an amount with exactly 2 decimals ("1.01"), using
// decimal rounding. Separator must be "." or ",".
func FormatDecimal(amount string, separator byte, field string) (string, error) {
	minor, err := ToMinorUnits(amount, field)
	if err != nil {
		return "", err
	}
	fixed := fmt.Sprintf("%d.%02d", minor/100, minor%100)
	if separator == ',' {
		return strings.Replace(fixed, ".", ",", 1), nil
	}
	return fixed, nil
}

// ErrorMessage returns a message safe to expose, never empty.
func ErrorMessage(err error, fallback string) string {
	if err != nil {
		if msg := err.Error(); msg != "" {
			return msg
		}
	}
	return fallback
}

// IsNetworkError reports a transport-level failure (timeout, connection reset,
// no response). The provider may still have processed the request, so the
// outcome is unknown and must be verified with a status query.
func IsNetworkError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.IsNetworkError
}

package core

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
)

func TestToMinorUnitsRoundsHalfUp(t *testing.T) {
	cases := []struct {
		amount string
		want   int64
	}{
		{"1.005", 101},
		{"1.004", 100},
		{"1.006", 101},
		{"0.005", 1},
		{"0.004", 0},
		{"2.675", 268},
		{"1.015", 102},
		{"0.1", 10},
		{"0.10", 10},
		{"1", 100},
		{"1.1", 110},
		{"1.10", 110},
		{"0", 0},
		{"0.00", 0},
		{"10.555", 1056},
		{"1234567.89", 123456789},
		{"  1.005  ", 101},
		{"+1.5", 150},
	}
	for _, tc := range cases {
		t.Run(tc.amount, func(t *testing.T) {
			got, err := ToMinorUnits(tc.amount, "price")
			if err != nil {
				t.Fatalf("ToMinorUnits(%q): %v", tc.amount, err)
			}
			if got != tc.want {
				t.Errorf("ToMinorUnits(%q) = %d, want %d", tc.amount, got, tc.want)
			}
		})
	}
}

func TestToMinorUnitsRejectsInvalidInput(t *testing.T) {
	for _, amount := range []string{"", " ", "abc", "1,5", "-1", "-0.01", "NaN", "Inf", "+Inf", "-Inf", "1.2.3", "1e", "0x10"} {
		got, err := ToMinorUnits(amount, "price")
		if err == nil {
			t.Errorf("ToMinorUnits(%q) = %d, want an error", amount, got)
			continue
		}
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("ToMinorUnits(%q) error = %T, want a ValidationError", amount, err)
		}
		if !strings.Contains(err.Error(), "price") {
			t.Errorf("ToMinorUnits(%q) error = %q, want it to name the field", amount, err)
		}
	}
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		amount string
		want   float64
		ok     bool
	}{
		{"1", 1, true},
		{"1.25", 1.25, true},
		{"0", 0, true},
		{"  2.50  ", 2.5, true},
		{"1e3", 1000, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-1", 0, false},
		{"NaN", 0, false},
		{"Inf", 0, false},
		{"-Inf", 0, false},
		{"1,5", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.amount, func(t *testing.T) {
			got, err := ParseAmount(tc.amount, "price")
			if tc.ok {
				if err != nil {
					t.Fatalf("ParseAmount(%q): %v", tc.amount, err)
				}
				if math.Abs(got-tc.want) > 1e-9 {
					t.Errorf("ParseAmount(%q) = %v, want %v", tc.amount, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseAmount(%q) = %v, want an error", tc.amount, got)
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Errorf("ParseAmount(%q) error = %T, want a ValidationError", tc.amount, err)
			}
		})
	}
}

func TestParseAmountNamesTheField(t *testing.T) {
	_, err := ParseAmount("x", "paidPrice")
	if err == nil || !strings.Contains(err.Error(), "Invalid paidPrice: x") {
		t.Errorf("error = %v, want it to name paidPrice", err)
	}
	_, err = ParseAmount("x", "")
	if err == nil || !strings.Contains(err.Error(), "Invalid amount: x") {
		t.Errorf("error = %v, want the default field name 'amount'", err)
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		amount    string
		separator byte
		want      string
	}{
		{"1", '.', "1.00"},
		{"1", ',', "1,00"},
		{"1.005", '.', "1.01"},
		{"1.005", ',', "1,01"},
		{"0.1", '.', "0.10"},
		{"1234.5", ',', "1234,50"},
		{"0", '.', "0.00"},
		{"2.675", '.', "2.68"},
	}
	for _, tc := range cases {
		t.Run(tc.amount+string(tc.separator), func(t *testing.T) {
			got, err := FormatDecimal(tc.amount, tc.separator, "price")
			if err != nil {
				t.Fatalf("FormatDecimal(%q): %v", tc.amount, err)
			}
			if got != tc.want {
				t.Errorf("FormatDecimal(%q, %q) = %q, want %q", tc.amount, tc.separator, got, tc.want)
			}
		})
	}
}

func TestFormatDecimalRejectsInvalidInput(t *testing.T) {
	for _, amount := range []string{"", "abc", "-1"} {
		if _, err := FormatDecimal(amount, '.', "price"); err == nil {
			t.Errorf("FormatDecimal(%q): want an error", amount)
		}
	}
}

func TestGenerateOrderID(t *testing.T) {
	alphanumeric := regexp.MustCompile(`^[A-Za-z0-9]+$`)
	cases := []struct {
		prefix string
		want   string
	}{
		{"", "BP"},
		{"BP", "BP"},
		{"ORDER", "ORDER"},
	}
	for _, tc := range cases {
		got := GenerateOrderID(tc.prefix)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("GenerateOrderID(%q) = %q, want the prefix %q", tc.prefix, got, tc.want)
		}
		if !alphanumeric.MatchString(got) {
			t.Errorf("GenerateOrderID(%q) = %q, want letters and digits only", tc.prefix, got)
		}
		if len(got) <= len(tc.want)+8 {
			t.Errorf("GenerateOrderID(%q) = %q, want a timestamp and a random suffix", tc.prefix, got)
		}
	}

	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		got := GenerateOrderID("")
		if seen[got] {
			t.Fatalf("GenerateOrderID returned %q twice", got)
		}
		seen[got] = true
	}
}

func TestErrorMessage(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		fallback string
		want     string
	}{
		{"nil error", nil, "fallback", "fallback"},
		{"error message", errors.New("boom"), "fallback", "boom"},
		{"empty message", errors.New(""), "fallback", "fallback"},
		{"empty fallback", errors.New("boom"), "", "boom"},
		{"both empty", nil, "", ""},
		{"wrapped error", fmt.Errorf("context: %w", errors.New("inner")), "fallback", "context: inner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorMessage(tc.err, tc.fallback); got != tc.want {
				t.Errorf("ErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsNetworkError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"transport error", &HTTPError{Message: "timeout", IsNetworkError: true}, true},
		{"http status error", &HTTPError{Message: "500", Response: &Response{Status: 500}}, false},
		{"wrapped transport error", fmt.Errorf("call failed: %w", &HTTPError{IsNetworkError: true}), true},
		{"validation error", NewValidationError("bad", "p", nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNetworkError(tc.err); got != tc.want {
				t.Errorf("IsNetworkError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

package albaraka

import (
	"strings"
	"testing"
)

func TestFormatAmount(t *testing.T) {
	got, err := FormatAmount("1.20")
	if err != nil {
		t.Fatalf("FormatAmount() error = %v", err)
	}
	if got != 120 {
		t.Errorf("FormatAmount(1.20) = %d, want 120", got)
	}
}

func TestCurrencyCode(t *testing.T) {
	cases := []struct {
		currency string
		want     string
	}{
		{"", "TL"},
		{"TRY", "TL"},
		{"TL", "TL"},
		{"usd", "US"},
		{"EUR", "EU"},
		{"GBP", "GB"},
	}
	for _, tc := range cases {
		got, err := CurrencyCode(tc.currency)
		if err != nil {
			t.Fatalf("CurrencyCode(%q) error = %v", tc.currency, err)
		}
		if got != tc.want {
			t.Errorf("CurrencyCode(%q) = %q, want %q", tc.currency, got, tc.want)
		}
	}
	if _, err := CurrencyCode("JPY"); err == nil {
		t.Error("CurrencyCode(JPY) succeeded, want an error")
	}
}

func TestFormatInstallment(t *testing.T) {
	cases := []struct {
		count int
		want  Installment
	}{
		{1, Installment{Count: 0, Type: "N"}},
		{3, Installment{Count: 3, Type: "Y"}},
		{99, Installment{Count: 99, Type: "Y"}},
	}
	for _, tc := range cases {
		got, err := FormatInstallment(tc.count)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", tc.count, err)
		}
		if got != tc.want {
			t.Errorf("FormatInstallment(%d) = %+v, want %+v", tc.count, got, tc.want)
		}
	}
	for _, count := range []int{0, -1, 100} {
		if _, err := FormatInstallment(count); err == nil {
			t.Errorf("FormatInstallment(%d) succeeded, want an error", count)
		}
	}
}

func TestParseInstallment(t *testing.T) {
	cases := []struct {
		value string
		want  Installment
	}{
		{"", Installment{Count: 0, Type: "N"}},
		{"1", Installment{Count: 0, Type: "N"}},
		{"3", Installment{Count: 3, Type: "Y"}},
	}
	for _, tc := range cases {
		got, err := ParseInstallment(tc.value)
		if err != nil {
			t.Fatalf("ParseInstallment(%q) error = %v", tc.value, err)
		}
		if got != tc.want {
			t.Errorf("ParseInstallment(%q) = %+v, want %+v", tc.value, got, tc.want)
		}
	}
	for _, value := range []string{"0", "100", "abc", "2.5"} {
		if _, err := ParseInstallment(value); err == nil {
			t.Errorf("ParseInstallment(%q) succeeded, want an error", value)
		}
	}
}

func TestFormatExpiry(t *testing.T) {
	got, err := FormatExpiry("1", "2030")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if got != "3001" {
		t.Errorf("FormatExpiry(1, 2030) = %q, want 3001", got)
	}
	got, err = FormatExpiry("12", "30")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if got != "3012" {
		t.Errorf("FormatExpiry(12, 30) = %q, want 3012", got)
	}
	if _, err := FormatExpiry("13", "2030"); err == nil || !strings.Contains(err.Error(), "Invalid card expiry") {
		t.Errorf("FormatExpiry(13, 2030) error = %v, want an 'Invalid card expiry' error", err)
	}
}

func TestFormatOrderID(t *testing.T) {
	got, err := FormatOrderID("ORDER1")
	if err != nil {
		t.Fatalf("FormatOrderID() error = %v", err)
	}
	if got != "00000000000000ORDER1" {
		t.Errorf("FormatOrderID(ORDER1) = %q, want 00000000000000ORDER1", got)
	}
	for _, orderID := range []string{"ORDER-1", strings.Repeat("X", 21), ""} {
		if _, err := FormatOrderID(orderID); err == nil {
			t.Errorf("FormatOrderID(%q) succeeded, want an error", orderID)
		}
	}
}

func TestCreateMac(t *testing.T) {
	got, err := CreateMac([]string{"6700000067", "67000067", "5400010000000004", "000", "3012", "120"}, "TESTENCKEY")
	if err != nil {
		t.Fatalf("CreateMac() error = %v", err)
	}
	if got != saleMAC {
		t.Errorf("CreateMac() = %q, want %q", got, saleMAC)
	}
}

func TestGenerateOrderID(t *testing.T) {
	first, err := GenerateOrderID()
	if err != nil {
		t.Fatalf("GenerateOrderID() error = %v", err)
	}
	second, err := GenerateOrderID()
	if err != nil {
		t.Fatalf("GenerateOrderID() error = %v", err)
	}
	if !generatedOrderIDRe.MatchString(first) {
		t.Errorf("GenerateOrderID() = %q, want 20 uppercase hex characters", first)
	}
	if first == second {
		t.Error("GenerateOrderID() returned the same id twice")
	}
	if _, err := FormatOrderID(first); err != nil {
		t.Errorf("FormatOrderID(generated) error = %v", err)
	}
}

package kuveytturk

import (
	"strings"
	"testing"
)

const testOrderID = "ORDER-1001"

func TestHashPassword(t *testing.T) {
	cases := []struct {
		password string
		want     string
	}{
		{"secret123", "8rFPaOuZX6yzocNSh7d41b14VRE="},
		{"şifreĞü", "9S86Z+hsqFqyJIMxH5M5Ad8Yw4c="},
	}
	for _, tc := range cases {
		got, err := HashPassword(tc.password)
		if err != nil {
			t.Fatalf("HashPassword(%q) error = %v", tc.password, err)
		}
		if got != tc.want {
			t.Errorf("HashPassword(%q) = %q, want %q", tc.password, got, tc.want)
		}
	}
}

func TestCreatePaymentHash(t *testing.T) {
	got, err := CreatePaymentHash(testCredentials(), testOrderID, "10265", "https://shop.test/ok", "https://shop.test/fail")
	if err != nil {
		t.Fatalf("CreatePaymentHash() error = %v", err)
	}
	want := "i9V7jBEhV7XYid0Qf7X9ApZXb6klrUlF+cy88TrBG9rsBuxaKmKqw1wGVohS4BHqEBuWCc1G8C7nR0ZsuT7pkQ=="
	if got != want {
		t.Errorf("CreatePaymentHash() = %q, want %q", got, want)
	}
}

func TestCreateProvisionHash(t *testing.T) {
	got, err := CreateProvisionHash(testCredentials(), testOrderID, "10265")
	if err != nil {
		t.Fatalf("CreateProvisionHash() error = %v", err)
	}
	want := "2v6YPkkDQwcqtbDi/HX0NhqLhLrTzAQSSBohxubY2uhggHBhL21jNbyjJs+ze+59cwR+d8qCn+gdF2BTfcyJlg=="
	if got != want {
		t.Errorf("CreateProvisionHash() = %q, want %q", got, want)
	}

	other := testCredentials()
	other.Password = "other"
	wrong, err := CreateProvisionHash(other, testOrderID, "10265")
	if err != nil {
		t.Fatalf("CreateProvisionHash() error = %v", err)
	}
	if wrong == want {
		t.Error("CreateProvisionHash() with another password did not change")
	}
}

func TestCreateSaleReversalHash(t *testing.T) {
	want := "34HOIOjh36XJo/pRPiIGFMa8MC6F1AANxus3SC3F6G5icEibuwkcOvN2sBzQ2FFjGRu64KvrR1AqaL6N5jhjBg=="
	for _, reversalType := range []SaleReversalType{SaleReversalCancel, SaleReversalDrawback} {
		got, err := CreateSaleReversalHash(testCredentials(), testOrderID, reversalType, "10265")
		if err != nil {
			t.Fatalf("CreateSaleReversalHash(%s) error = %v", reversalType, err)
		}
		if got != want {
			t.Errorf("CreateSaleReversalHash(%s) = %q, want %q", reversalType, got, want)
		}
	}

	got, err := CreateSaleReversalHash(testCredentials(), testOrderID, SaleReversalPartialDrawback, "5000")
	if err != nil {
		t.Fatalf("CreateSaleReversalHash(PartialDrawback) error = %v", err)
	}
	partial := "GQ2V/7auVPqxqs6LKs0xpi41HwSWtkifKTF3/0497Wgce/VLsxx8AIAO5ge5PLN48QZKi2qGWVti8FJad5ZndA=="
	if got != partial {
		t.Errorf("CreateSaleReversalHash(PartialDrawback) = %q, want %q", got, partial)
	}
}

func TestCreateQueryHashes(t *testing.T) {
	got, err := CreateGetTransactionHash(testCredentials())
	if err != nil {
		t.Fatalf("CreateGetTransactionHash() error = %v", err)
	}
	want := "/QK7PRM8CziWRK84/q+sIrtmPgH12J/6+n+oRJ4Py0b8zWSi/fsW4nPaGANjnTt00VVoM4a1LsYi0TvTILqzgA=="
	if got != want {
		t.Errorf("CreateGetTransactionHash() = %q, want %q", got, want)
	}

	got, err = CreateGetTransactionsHash(testCredentials(), testOrderID)
	if err != nil {
		t.Fatalf("CreateGetTransactionsHash() error = %v", err)
	}
	want = "96DoN27bamxnJhs+EKsss7Y4hRzQ2M2tpUM+udeTHHV32ZCk6cvPtYnPrH5uO7SkwlQQUl8rNFnfAjOzXUxVkg=="
	if got != want {
		t.Errorf("CreateGetTransactionsHash() = %q, want %q", got, want)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		amount string
		want   string
	}{
		{"102.65", "10265"},
		{"1", "100"},
		{"1234.50", "123450"},
		{"0.29", "29"},
		{"19.99", "1999"},
	}
	for _, tc := range cases {
		got, err := FormatAmount(tc.amount)
		if err != nil {
			t.Fatalf("FormatAmount(%q) error = %v", tc.amount, err)
		}
		if got != tc.want {
			t.Errorf("FormatAmount(%q) = %q, want %q", tc.amount, got, tc.want)
		}
	}
}

func TestCurrencyCode(t *testing.T) {
	cases := []struct {
		currency string
		want     string
	}{
		{"", "0949"},
		{"TRY", "0949"},
		{"usd", "0840"},
		{"EUR", "0978"},
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

	if _, err := CurrencyCode("GBP"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(GBP) error = %v, want a 'not supported' error", err)
	}
}

func TestFormatExpiry(t *testing.T) {
	cases := []struct {
		month, year string
		want        Expiry
	}{
		{"1", "2025", Expiry{Month: "01", Year: "25"}},
		{"12", "33", Expiry{Month: "12", Year: "33"}},
	}
	for _, tc := range cases {
		got, err := FormatExpiry(tc.month, tc.year)
		if err != nil {
			t.Fatalf("FormatExpiry(%q, %q) error = %v", tc.month, tc.year, err)
		}
		if got != tc.want {
			t.Errorf("FormatExpiry(%q, %q) = %+v, want %+v", tc.month, tc.year, got, tc.want)
		}
	}

	if _, err := FormatExpiry("13", "25"); err == nil || !strings.Contains(err.Error(), "Invalid card expiry") {
		t.Errorf("FormatExpiry(13, 25) error = %v, want an 'Invalid card expiry' error", err)
	}
}

func TestFormatInstallment(t *testing.T) {
	cases := []struct {
		installment int
		want        int
	}{
		{0, 1},
		{-3, 1},
		{12, 12},
	}
	for _, tc := range cases {
		got, err := FormatInstallment(tc.installment)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", tc.installment, err)
		}
		if got != tc.want {
			t.Errorf("FormatInstallment(%d) = %d, want %d", tc.installment, got, tc.want)
		}
	}

	if _, err := FormatInstallment(13); err == nil || !strings.Contains(err.Error(), "1 to 12") {
		t.Errorf("FormatInstallment(13) error = %v, want a '1 to 12' error", err)
	}
}

func TestLanguage(t *testing.T) {
	cases := []struct {
		locale string
		want   int
	}{
		{"", 1},
		{"tr", 1},
		{"en-US", 2},
	}
	for _, tc := range cases {
		if got := Language(tc.locale); got != tc.want {
			t.Errorf("Language(%q) = %d, want %d", tc.locale, got, tc.want)
		}
	}
}

func TestParsePhone(t *testing.T) {
	for _, input := range []string{"+905350000000", "905350000000", "05350000000", "535 000 00 00"} {
		got, err := ParsePhone(input)
		if err != nil {
			t.Fatalf("ParsePhone(%q) error = %v", input, err)
		}
		want := Phone{CC: "90", Subscriber: "5350000000"}
		if got != want {
			t.Errorf("ParsePhone(%q) = %+v, want %+v", input, got, want)
		}
	}

	for _, input := range []string{"12345", "+4915112345678"} {
		if _, err := ParsePhone(input); err == nil || !strings.Contains(err.Error(), "Turkish GSM number") {
			t.Errorf("ParsePhone(%q) error = %v, want a 'Turkish GSM number' error", input, err)
		}
	}
}

func TestParseForm(t *testing.T) {
	fields := ParseForm(`<form><input type="hidden" name="Success" value="False" />` +
		`<input name="ResponseMessage" type="hidden" value="Hata &amp; uyar&#305;" /></form>`)
	want := map[string]string{"Success": "False", "ResponseMessage": "Hata & uyarı"}
	if len(fields) != len(want) {
		t.Fatalf("ParseForm() = %v, want %v", fields, want)
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("ParseForm()[%q] = %q, want %q", key, fields[key], value)
		}
	}
}

func TestStatusCode(t *testing.T) {
	cases := []struct {
		label string
		want  int
		found bool
	}{
		{"Basarısız (2)", 2, true},
		{"Zaman Aşımı (3)", 3, true},
		{"Bilinmiyor", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, found := StatusCode(tc.label)
		if got != tc.want || found != tc.found {
			t.Errorf("StatusCode(%q) = (%d, %v), want (%d, %v)", tc.label, got, found, tc.want, tc.found)
		}
	}
}

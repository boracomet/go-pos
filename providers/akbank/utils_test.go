package akbank

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCreate3DFormHash(t *testing.T) {
	cfg := testConfig()
	hash, err := Create3DFormHash(map[string]string{
		"paymentModel":    "3D",
		"txnCode":         "3000",
		"merchantSafeId":  cfg.MerchantSafeID,
		"terminalSafeId":  cfg.TerminalSafeID,
		"orderId":         "20240404A4B0",
		"lang":            "TR",
		"amount":          "1.01",
		"ccbRewardAmount": "1.00",
		"pcbRewardAmount": "1.00",
		"xcbRewardAmount": "1.00",
		"currencyCode":    "949",
		"installCount":    "1",
		"okUrl":           "http://localhost/akbankpos/3d/response.php",
		"failUrl":         "http://localhost/akbankpos/3d/response.php",
		"emailAddress":    "test@test.com",
		"randomNumber":    "AEDDD8688E11A3DC588DAB2ED59B2F64D45E798761CEFF17F4DB47581072697890180C4195986250F89C2C67A04A3B96F0AC66AE99B49BB7BEE618FBD621C4CD",
		"requestDateTime": "2024-04-04T21:11:41.000",
		"creditCard":      "4355093000315232",
		"expiredDate":     "1135",
		"cvv":             "665",
	}, cfg.SecretKey)
	if err != nil {
		t.Fatalf("Create3DFormHash() error = %v", err)
	}
	want := "ilR2mCExklKEti+2x61A8pcOfzJ5z5M6xMYmmU8ClaKaDuxKooFuH3v7XW/ba25xlTDqGN1H//i0zTiJl5YnfA=="
	if hash != want {
		t.Errorf("Create3DFormHash() = %q, want %q", hash, want)
	}
}

func TestVerifyCallback(t *testing.T) {
	secretKey := testConfig().SecretKey

	t.Run("accepts the reference 3D_PAY callback", func(t *testing.T) {
		if !VerifyCallback(test3DPayCallback(), secretKey) {
			t.Error("VerifyCallback() = false, want true")
		}
	})

	t.Run("rejects a tampered responseCode", func(t *testing.T) {
		data := test3DPayCallback()
		data["responseCode"] = "VPS-1005"
		if VerifyCallback(data, secretKey) {
			t.Error("VerifyCallback(tampered) = true, want false")
		}
	})

	t.Run("rejects a wrong key", func(t *testing.T) {
		if VerifyCallback(test3DPayCallback(), "other-key") {
			t.Error("VerifyCallback(wrong key) = true, want false")
		}
	})

	t.Run("rejects a hashParams list that does not cover responseCode/orderId", func(t *testing.T) {
		hash, err := Sign("X", secretKey)
		if err != nil {
			t.Fatalf("Sign() error = %v", err)
		}
		forged := map[string]string{
			"orderId": "X", "responseCode": "VPS-0000", "hashParams": "orderId", "hash": hash,
		}
		if VerifyCallback(forged, secretKey) {
			t.Error("VerifyCallback(short hashParams) = true, want false")
		}
	})

	t.Run("rejects a missing hash", func(t *testing.T) {
		data := test3DPayCallback()
		delete(data, "hash")
		if VerifyCallback(data, secretKey) {
			t.Error("VerifyCallback(no hash) = true, want false")
		}
	})

	t.Run("accepts a re-split signature, which the order history must then reject", func(t *testing.T) {
		if !VerifyCallback(resplitCallback(t), secretKey) {
			t.Error("VerifyCallback(re-split) = false; the values are not pinned by the hash")
		}
	})
}

func TestGenerateRandomNumber(t *testing.T) {
	value, err := GenerateRandomNumber()
	if err != nil {
		t.Fatalf("GenerateRandomNumber() error = %v", err)
	}
	if !regexp.MustCompile(`^[0-9A-F]{128}$`).MatchString(value) {
		t.Errorf("GenerateRandomNumber() = %q, want 128 uppercase hex chars", value)
	}
}

func TestFormatDateTime(t *testing.T) {
	date := time.Date(2024, 4, 4, 18, 11, 41, 123000000, time.UTC)
	if got := FormatDateTime(date); got != "2024-04-04T21:11:41.000" {
		t.Errorf("FormatDateTime() = %q, want 2024-04-04T21:11:41.000", got)
	}
}

func TestFormatAmountExpiryCurrency(t *testing.T) {
	amount, err := FormatAmount("1.1")
	if err != nil {
		t.Fatalf("FormatAmount() error = %v", err)
	}
	if amount != "1.10" {
		t.Errorf("FormatAmount(1.1) = %q, want 1.10", amount)
	}

	for _, tc := range []struct{ month, year, want string }{
		{"1", "2035", "0135"},
		{"11", "35", "1135"},
	} {
		got, err := FormatExpiry(tc.month, tc.year)
		if err != nil {
			t.Fatalf("FormatExpiry(%q, %q) error = %v", tc.month, tc.year, err)
		}
		if got != tc.want {
			t.Errorf("FormatExpiry(%q, %q) = %q, want %q", tc.month, tc.year, got, tc.want)
		}
	}
	if _, err := FormatExpiry("13", "35"); err == nil {
		t.Error("FormatExpiry(13, 35) error = nil, want an error")
	}

	for _, tc := range []struct {
		currency string
		want     int
	}{
		{"TRY", 949},
		{"usd", 840},
	} {
		got, err := CurrencyCode(tc.currency)
		if err != nil {
			t.Fatalf("CurrencyCode(%q) error = %v", tc.currency, err)
		}
		if got != tc.want {
			t.Errorf("CurrencyCode(%q) = %d, want %d", tc.currency, got, tc.want)
		}
	}
	if _, err := CurrencyCode("CHF"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(CHF) error = %v, want a not supported error", err)
	}
}

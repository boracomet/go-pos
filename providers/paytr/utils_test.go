package paytr

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// Reference credentials and vectors for the PayTR signature scheme: the HMAC key
// is the merchant key, and the merchant salt is appended to the signed string.
const (
	testSalt = "YEUaNcdHXqyt7hjt"
	testKey  = "wWwU8buJp6jo1r25"
)

// mustToken returns a helper that fails the test when a signature helper errors.
func mustToken(t *testing.T) func(string, error) string {
	return func(token string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("token generation failed: %v", err)
		}
		return token
	}
}

func TestGenerateIframeToken(t *testing.T) {
	got := mustToken(t)(GenerateIframeToken(IframeTokenParams{
		MerchantID:     "123456",
		UserIP:         "127.0.0.1",
		MerchantOid:    "order-123",
		Email:          "test@example.com",
		PaymentAmount:  "10050",
		UserBasket:     "W1siUHJvZHVjdCIsIjEwLjUwIiwxXV0=",
		NoInstallment:  "0",
		MaxInstallment: "0",
		Currency:       "TL",
		TestMode:       "1",
	}, testSalt, testKey))
	if want := "O5CaLankrigiiPDh3G75lLWdRX6wrEXGbLRjt37tuzY="; got != want {
		t.Fatalf("iframe token = %q, want %q", got, want)
	}
}

func TestGenerateDirectToken(t *testing.T) {
	got := mustToken(t)(GenerateDirectToken(DirectTokenParams{
		MerchantID:       "123456",
		UserIP:           "127.0.0.1",
		MerchantOid:      "order-456",
		Email:            "test@example.com",
		PaymentAmount:    "5000",
		PaymentType:      "card",
		InstallmentCount: "0",
		Currency:         "TL",
		TestMode:         "1",
		Non3D:            "1",
	}, testSalt, testKey))
	if want := "guwHEsodarZg3KTuihE7fpb+qAdKI0IQMUkrGFWZDO0="; got != want {
		t.Fatalf("direct token = %q, want %q", got, want)
	}
}

func TestGenerateQueryTokens(t *testing.T) {
	cases := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"refund", func() (string, error) {
			return GenerateRefundToken("123456", "order-789", "5000", testSalt, testKey)
		}, "7ZqQEds0nem2gCqpLltwuNlmV9f7KHVlc73qeiNjwMg="},
		{"status", func() (string, error) {
			return GenerateStatusToken("123456", "order-999", testSalt, testKey)
		}, "mIFJNpzMuo/gd9pcPNBinujmMVlEkR3DHZ6stLoQo/s="},
		{"bin", func() (string, error) {
			return GenerateBinToken("415956", "123456", testSalt, testKey)
		}, "lAB6uLq369TxD4S7VDGIzOYt6Fy3XKicfn15Xy1TiBM="},
		{"installment rates", func() (string, error) {
			return GenerateInstallmentRatesToken("123456", "REQ12345", testSalt, testKey)
		}, "dx7O59UIbRaErFkP72oGXtQb/TgwFs8l/Qznl06iLls="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustToken(t)(tc.got()); got != tc.want {
				t.Fatalf("%s token = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func genuineCallback() CallbackData {
	return CallbackData{
		MerchantOid: "202606234E4E",
		Status:      "success",
		TotalAmount: "1001",
		Hash:        "ZDVOQUw4aDJhNR5dWYBC5bD95bLtSOtj9DzzSQ9sUHs=",
	}
}

func TestVerifyCallbackAcceptsGenuineNotification(t *testing.T) {
	ok, err := VerifyCallback(genuineCallback(), testSalt, testKey)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if !ok {
		t.Fatal("genuine notification was rejected")
	}
}

func TestVerifyCallbackRejectsTamperedFields(t *testing.T) {
	tamperedStatus := genuineCallback()
	tamperedStatus.Status = "failed"
	tamperedAmount := genuineCallback()
	tamperedAmount.TotalAmount = "1"

	for name, data := range map[string]CallbackData{"status": tamperedStatus, "amount": tamperedAmount} {
		ok, err := VerifyCallback(data, testSalt, testKey)
		if err != nil {
			t.Fatalf("verify %s failed: %v", name, err)
		}
		if ok {
			t.Fatalf("tampered %s was accepted", name)
		}
	}
}

// The salt must never be used as the HMAC key: that older, wrong algorithm
// produces a hash an attacker could forge without the merchant key.
func TestVerifyCallbackRejectsSaltAsKey(t *testing.T) {
	ok, err := VerifyCallback(genuineCallback(), testKey, testSalt)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if ok {
		t.Fatal("hash made with the salt as key was accepted")
	}
}

func TestVerifyCallbackRejectsMissingFields(t *testing.T) {
	noHash := genuineCallback()
	noHash.Hash = ""

	for name, data := range map[string]CallbackData{"no hash": noHash, "empty": {}} {
		ok, err := VerifyCallback(data, testSalt, testKey)
		if err != nil {
			t.Fatalf("verify %s failed: %v", name, err)
		}
		if ok {
			t.Fatalf("callback with missing fields (%s) was accepted", name)
		}
	}
}

func TestFormatBasketIsBase64JSON(t *testing.T) {
	basket, err := FormatBasket([]BasketItem{{Name: "Product", Price: "10.50", Quantity: 1}})
	if err != nil {
		t.Fatalf("format basket failed: %v", err)
	}
	if want := "W1siUHJvZHVjdCIsIjEwLjUwIiwxXV0="; basket != want {
		t.Fatalf("basket = %q, want %q", basket, want)
	}
	raw, err := base64.StdEncoding.DecodeString(basket)
	if err != nil {
		t.Fatalf("basket is not base64: %v", err)
	}
	var decoded [][]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("basket is not JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0][0] != "Product" || decoded[0][1] != "10.50" || decoded[0][2] != float64(1) {
		t.Fatalf("decoded basket = %v", decoded)
	}
}

func TestFormatDirectBasketIsPlainJSON(t *testing.T) {
	basket, err := FormatDirectBasket([]BasketItem{{Name: "Product", Price: "10.50", Quantity: 2}})
	if err != nil {
		t.Fatalf("format basket failed: %v", err)
	}
	if want := `[["Product","10.50",2]]`; basket != want {
		t.Fatalf("direct basket = %q, want %q", basket, want)
	}
}

func TestAmountConversions(t *testing.T) {
	kurus := []struct{ in, want string }{
		{"100.50", "10050"},
		{"0.29", "29"},
		{"1.005", "101"},
	}
	for _, tc := range kurus {
		got, err := ConvertToKurus(tc.in)
		if err != nil {
			t.Fatalf("ConvertToKurus(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ConvertToKurus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := ConvertFromKurus("1001"); got != "10.01" {
		t.Fatalf("ConvertFromKurus(1001) = %q, want 10.01", got)
	}
	amount, err := FormatAmount("5")
	if err != nil {
		t.Fatalf("FormatAmount: %v", err)
	}
	if amount != "5.00" {
		t.Fatalf("FormatAmount(5) = %q, want 5.00", amount)
	}
	if _, err := ConvertToKurus("abc"); err == nil {
		t.Fatal("ConvertToKurus(abc) did not fail")
	}
}

func TestMapCurrency(t *testing.T) {
	for in, want := range map[string]string{"TRY": "TL", "": "TL", "usd": "USD"} {
		got, err := MapCurrency(in)
		if err != nil {
			t.Fatalf("MapCurrency(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("MapCurrency(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := MapCurrency("CHF"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("MapCurrency(CHF) error = %v, want a 'not supported' error", err)
	}
}

func TestAssertMerchantOid(t *testing.T) {
	if err := AssertMerchantOid("ORDER123"); err != nil {
		t.Fatalf("ORDER123 rejected: %v", err)
	}
	for _, bad := range []string{"ORDER-123", ""} {
		if err := AssertMerchantOid(bad); err == nil {
			t.Fatalf("merchant_oid %q was accepted", bad)
		}
	}
}

func TestCreateFormData(t *testing.T) {
	got := CreateFormData([]FormField{{Name: "a", Value: "1 2"}, {Name: "b", Value: "x&y"}})
	if want := "a=1%202&b=x%26y"; got != want {
		t.Fatalf("form data = %q, want %q", got, want)
	}
}

func TestBuildAutoSubmitFormEscapesOnce(t *testing.T) {
	html := BuildAutoSubmitForm("https://www.paytr.com/odeme", []FormField{{Name: "user_name", Value: `"><script>`}})
	if !strings.Contains(html, `value="&quot;&gt;&lt;script&gt;"`) {
		t.Fatalf("form value is not escaped: %s", html)
	}
	if !strings.Contains(html, `action="https://www.paytr.com/odeme"`) {
		t.Fatalf("form action missing: %s", html)
	}
}

func TestSignUsesMerchantKey(t *testing.T) {
	got := mustToken(t)(Sign("123456"+testSalt, testKey))
	want := mustToken(t)(GenerateStatusToken("123456", "", testSalt, testKey))
	if !core.SafeEqual(got, want) {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

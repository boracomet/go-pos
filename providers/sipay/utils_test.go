package sipay

import (
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// referenceHashKey is a hash key encrypted as the provider's documentation
// describes, for the payment result fields posted after 3D Secure.
const (
	referenceHashKey   = "0123456789abcdef:a1b2:J3v+eSuMITtIyU__TQQ8KEbj4eQKOOT7Nkz0td1TvB8uxn6qe0Fs5Rn__ci6aMA6PYfziJi996pEIdoergEUcQfg=="
	referenceHashPlain = "1|10.00|BP3D1791714553140|VP17917145548423635|TRY"
)

var hashKeyFormatRe = regexp.MustCompile(`^[0-9a-f]{16}:[0-9a-f]{4}:[A-Za-z0-9+_=]+$`)

func hexDigest(t *testing.T, algorithm core.DigestAlgorithm, text string) string {
	t.Helper()
	sum, err := core.Digest(algorithm, core.UTF8(text))
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	return core.ToHex(sum)
}

// referenceDecrypt is the decryption sample of the hash validation documentation.
func referenceDecrypt(t *testing.T, hashKey, appSecret string) string {
	t.Helper()
	value := hashKey
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	value = strings.ReplaceAll(value, "__", "/")
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		t.Fatalf("hash key %q is not iv:salt:ciphertext", hashKey)
	}
	iv, salt, encrypted := parts[0], parts[1], parts[2]
	key := hexDigest(t, core.SHA256, hexDigest(t, core.SHA1, appSecret)+salt)[:32]
	ciphertext, err := core.Base64ToBytes(encrypted)
	if err != nil {
		t.Fatalf("base64 error = %v", err)
	}
	plain, err := core.AESCBCDecrypt(core.UTF8(key), core.UTF8(iv), ciphertext)
	if err != nil {
		t.Fatalf("AES-CBC error = %v", err)
	}
	return string(plain)
}

func TestDecryptHashKeyReferenceVector(t *testing.T) {
	fields := DecryptHashKey(referenceHashKey, testAppSecret)
	if got := strings.Join(fields, "|"); got != referenceHashPlain {
		t.Errorf("DecryptHashKey() = %q, want %q", got, referenceHashPlain)
	}
}

func TestCreatePaymentHashIsDecryptable(t *testing.T) {
	hash, err := CreatePaymentHash("10.00", 1, "TRY", testMerchantKey, "INV1", testAppSecret)
	if err != nil {
		t.Fatalf("CreatePaymentHash() error = %v", err)
	}
	if !hashKeyFormatRe.MatchString(hash) {
		t.Errorf("hash key %q does not match iv:salt:ciphertext", hash)
	}
	if strings.Contains(hash, "/") {
		t.Errorf("hash key %q contains '/'", hash)
	}
	want := "10.00|1|TRY|" + testMerchantKey + "|INV1"
	if got := referenceDecrypt(t, hash, testAppSecret); got != want {
		t.Errorf("decrypted hash = %q, want %q", got, want)
	}
}

func TestCreateHashKeyUsesNewIVAndSalt(t *testing.T) {
	a, err := CreateHashKey([]string{"x"}, testAppSecret)
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}
	b, err := CreateHashKey([]string{"x"}, testAppSecret)
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}
	if a == b {
		t.Error("two hash keys are identical, want a new IV and salt")
	}
	if got := DecryptHashKey(a, testAppSecret); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("DecryptHashKey() = %v, want [x]", got)
	}
}

func TestDecryptHashKeyAcceptsEncodedValues(t *testing.T) {
	encoded := url.PathEscape(referenceHashKey)
	if got := strings.Join(DecryptHashKey(encoded, testAppSecret), "|"); got != referenceHashPlain {
		t.Errorf("URL-encoded hash key = %q, want %q", got, referenceHashPlain)
	}
	spaced := strings.ReplaceAll(referenceHashKey, "+", " ")
	if got := strings.Join(DecryptHashKey(spaced, testAppSecret), "|"); got != referenceHashPlain {
		t.Errorf("hash key with spaces = %q, want %q", got, referenceHashPlain)
	}
}

func TestDecryptHashKeyRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hashKey string
		secret  string
	}{
		{"another secret", referenceHashKey, "not-the-secret"},
		{"a malformed value", "abc", testAppSecret},
		{"a tampered ciphertext", strings.Replace(referenceHashKey, "J3v+", "J3w+", 1), testAppSecret},
		{"an empty value", "", testAppSecret},
	} {
		if got := DecryptHashKey(tc.hashKey, tc.secret); got != nil {
			t.Errorf("%s: DecryptHashKey() = %v, want nil", tc.name, got)
		}
	}
}

func TestVerifyPaymentHash(t *testing.T) {
	want := &VerifiedHash{
		Status:    "1",
		Total:     "10.00",
		InvoiceID: "BP3D1791714553140",
		OrderID:   "VP17917145548423635",
		Currency:  "TRY",
	}
	if got := VerifyPaymentHash(referenceHashKey, testAppSecret); !reflect.DeepEqual(got, want) {
		t.Errorf("VerifyPaymentHash() = %+v, want %+v", got, want)
	}

	short, err := CreateHashKey([]string{"1", "2"}, testAppSecret)
	if err != nil {
		t.Fatalf("CreateHashKey() error = %v", err)
	}
	if got := VerifyPaymentHash(short, testAppSecret); got != nil {
		t.Errorf("VerifyPaymentHash() = %+v, want nil for fewer than five fields", got)
	}
}

func TestFormatAmount(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1", "1.00"},
		{"1.2", "1.20"},
		{"10.005", "10.01"},
	} {
		got, err := FormatAmount(tc.in)
		if err != nil {
			t.Fatalf("FormatAmount(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatAmount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetCurrency(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "TRY"},
		{"TL", "TRY"},
		{"usd", "USD"},
		{"GBP", "GBP"},
	} {
		got, err := GetCurrency(tc.in)
		if err != nil {
			t.Fatalf("GetCurrency(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("GetCurrency(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := GetCurrency("JPY"); err == nil || !strings.Contains(err.Error(), "JPY") {
		t.Errorf("GetCurrency(JPY) error = %v, want it to name JPY", err)
	}
}

func TestFormatExpiry(t *testing.T) {
	month, year, err := FormatExpiry("1", "30")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if month != "01" || year != "2030" {
		t.Errorf("FormatExpiry(1, 30) = %q/%q, want 01/2030", month, year)
	}
	month, year, err = FormatExpiry("12", "2031")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if month != "12" || year != "2031" {
		t.Errorf("FormatExpiry(12, 2031) = %q/%q, want 12/2031", month, year)
	}
	if _, _, err := FormatExpiry("13", "2030"); err == nil {
		t.Error("FormatExpiry(13, 2030) error = nil, want an error")
	}
}

func TestFormatInstallment(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 1},
		{6, 6},
	} {
		got, err := FormatInstallment(tc.in)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatInstallment(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if _, err := FormatInstallment(13); err == nil {
		t.Error("FormatInstallment(13) error = nil, want an error")
	}
}

func TestGetLanguage(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"en-US", "en"},
		{"tr", "tr"},
		{"", "tr"},
	} {
		if got := GetLanguage(tc.in); got != tc.want {
			t.Errorf("GetLanguage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildItemsSendsBasketWhenItAddsUp(t *testing.T) {
	items, err := BuildItems(mockBasketItems(), "1.00", "Order X")
	if err != nil {
		t.Fatalf("BuildItems() error = %v", err)
	}
	want := []Item{
		{Name: "Binocular", Price: "0.30", Quantity: 1, Description: "Collectibles"},
		{Name: "Game code", Price: "0.50", Quantity: 1, Description: "Game"},
		{Name: "Usb", Price: "0.20", Quantity: 1, Description: "Electronics"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("BuildItems() = %+v, want %+v", items, want)
	}
}

func TestBuildItemsSendsOneItemOtherwise(t *testing.T) {
	items, err := BuildItems(mockBasketItems(), "1.20", "Order X")
	if err != nil {
		t.Fatalf("BuildItems() error = %v", err)
	}
	want := []Item{{Name: "Order X", Price: "1.20", Quantity: 1, Description: "Order X"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("BuildItems() = %+v, want %+v", items, want)
	}
	items, err = BuildItems(nil, "5.00", "Order Y")
	if err != nil {
		t.Fatalf("BuildItems() error = %v", err)
	}
	if len(items) != 1 {
		t.Errorf("BuildItems(nil) = %d items, want 1", len(items))
	}
}

func TestParseFormReadsRejection(t *testing.T) {
	fields := ParseForm(threeDSRejectedHTML)
	for key, want := range map[string]string{
		"sipay_status":       "0",
		"invoice_id":         "ORDER3D",
		"status_code":        "68",
		"status_description": "Invalid hash key",
	} {
		if fields[key] != want {
			t.Errorf("ParseForm()[%q] = %q, want %q", key, fields[key], want)
		}
	}
}

func TestParseFormDecodesEntities(t *testing.T) {
	fields := ParseForm(`<input name="a&amp;b" value="&quot;x&#39;&#x3C;">`)
	want := map[string]string{"a&b": `"x'<`}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("ParseForm() = %v, want %v", fields, want)
	}
}

func TestParseFormDecodesEachEntityOnce(t *testing.T) {
	fields := ParseForm(`<input name="a" value="&amp;lt;&#x26;gt;&#38;amp;">`)
	want := map[string]string{"a": "&lt;&gt;&amp;"}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("ParseForm() = %v, want %v", fields, want)
	}
}

func TestMapTransactionStatus(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want core.PaymentStatus
	}{
		{"Completed", core.StatusSuccess},
		{"Partial Refund", core.StatusSuccess},
		{"Pre-Authorization", core.StatusSuccess},
		{"Refunded", core.StatusCancelled},
		{"Cancelled", core.StatusCancelled},
		{"Failed", core.StatusFailure},
		{"Pending", core.StatusPending},
		{"", core.StatusPending},
	} {
		if got := MapTransactionStatus(tc.in); got != tc.want {
			t.Errorf("MapTransactionStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

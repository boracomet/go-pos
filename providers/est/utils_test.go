package est

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestCompareKeys(t *testing.T) {
	t.Run("sorts case-insensitively", func(t *testing.T) {
		keys := []string{"oid", "Email", "amount", "TranType"}
		sort.Slice(keys, func(i, j int) bool { return CompareKeys(keys[i], keys[j]) < 0 })
		want := []string{"amount", "Email", "oid", "TranType"}
		for i := range want {
			if keys[i] != want[i] {
				t.Fatalf("sorted keys = %v, want %v", keys, want)
			}
		}
	})

	t.Run("sorts digit runs by value", func(t *testing.T) {
		keys := []string{"a10", "a2", "a1"}
		sort.Slice(keys, func(i, j int) bool { return CompareKeys(keys[i], keys[j]) < 0 })
		if strings.Join(keys, ",") != "a1,a2,a10" {
			t.Fatalf("sorted keys = %v", keys)
		}
	})

	t.Run(`puts "_" before letters, like the bank`, func(t *testing.T) {
		keys := []string{"ab", "a_b"}
		sort.Slice(keys, func(i, j int) bool { return CompareKeys(keys[i], keys[j]) < 0 })
		if strings.Join(keys, ",") != "a_b,ab" {
			t.Fatalf("sorted keys = %v", keys)
		}
	})
}

func TestCreateHash(t *testing.T) {
	storeKey := testConfig().StoreKey

	t.Run("matches the reference vector for a 3D form", func(t *testing.T) {
		hash, err := CreateHash(testFormFields, storeKey)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		if hash != testFormHash {
			t.Errorf("hash = %q, want %q", hash, testFormHash)
		}
	})

	t.Run("does not depend on field order", func(t *testing.T) {
		keys := make([]string, 0, len(testFormFields))
		for key := range testFormFields {
			keys = append(keys, key)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		reversed := map[string]string{}
		for _, key := range keys {
			reversed[key] = testFormFields[key]
		}
		hash, err := CreateHash(reversed, storeKey)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		if hash != testFormHash {
			t.Errorf("hash = %q, want %q", hash, testFormHash)
		}
	})

	t.Run(`escapes "\" and "|" in values and in the store key`, func(t *testing.T) {
		hash, err := CreateHash(map[string]string{"a": "x|y", "b": `c\d`}, `k|e\y`)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		want := "5lMnbi9fspFKYkvWgTCZWJhi/McRMhXM05sksD43DHr8U9+ZsUKG4+sDE5pubPkanDGyfGCXwPrRZTyNfBLiDA=="
		if hash != want {
			t.Errorf("hash = %q, want %q", hash, want)
		}
	})

	t.Run("ignores hash, encoding, countdown and nationalidno", func(t *testing.T) {
		withExtras := map[string]string{}
		for key, value := range testFormFields {
			withExtras[key] = value
		}
		withExtras["hash"] = "x"
		withExtras["Encoding"] = "UTF-8"
		withExtras["countdown"] = "5"
		withExtras["NationalIdNo"] = "1"
		hash, err := CreateHash(withExtras, storeKey)
		if err != nil {
			t.Fatalf("CreateHash() error = %v", err)
		}
		if hash != testFormHash {
			t.Errorf("hash = %q, want %q", hash, testFormHash)
		}
	})
}

func TestVerifyCallback(t *testing.T) {
	storeKey := testConfig().StoreKey

	t.Run("accepts the reference 3d and 3d_pay callbacks", func(t *testing.T) {
		if !VerifyCallback(test3DCallback(), storeKey) {
			t.Error("3d callback did not verify")
		}
		if !VerifyCallback(test3DPayCallback(), storeKey) {
			t.Error("3d_pay callback did not verify")
		}
	})

	t.Run("rejects a tampered field", func(t *testing.T) {
		data := test3DPayCallback()
		data["amount"] = "0.01"
		if VerifyCallback(data, storeKey) {
			t.Error("tampered amount verified")
		}
	})

	t.Run("rejects an added field", func(t *testing.T) {
		data := test3DPayCallback()
		data["extra"] = "x"
		if VerifyCallback(data, storeKey) {
			t.Error("added field verified")
		}
	})

	t.Run("rejects a wrong store key or a missing HASH", func(t *testing.T) {
		if VerifyCallback(test3DPayCallback(), "other") {
			t.Error("wrong store key verified")
		}
		data := test3DPayCallback()
		delete(data, "HASH")
		if VerifyCallback(data, storeKey) {
			t.Error("missing HASH verified")
		}
	})
}

func TestGenerateRandom(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	first, err := GenerateRandom()
	if err != nil {
		t.Fatalf("GenerateRandom() error = %v", err)
	}
	second, err := GenerateRandom()
	if err != nil {
		t.Fatalf("GenerateRandom() error = %v", err)
	}
	if !pattern.MatchString(first) {
		t.Errorf("random = %q, want 32 hex characters", first)
	}
	if first == second {
		t.Error("two random values are equal")
	}
}

func TestFormatAmount(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"1.2", "1.20"}, {"100", "100.00"}} {
		got, err := FormatAmount(tc.in)
		if err != nil {
			t.Fatalf("FormatAmount(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatAmount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCurrencyCode(t *testing.T) {
	t.Run("maps supported currencies", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"", "949"}, {"usd", "840"}, {"EUR", "978"}, {"GBP", "826"},
			{"pln", "985"}, {"SEK", "752"}, {"MKD", "807"},
		} {
			got, err := CurrencyCode(tc.in)
			if err != nil {
				t.Fatalf("CurrencyCode(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("CurrencyCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("rejects unsupported currencies instead of falling back to TRY", func(t *testing.T) {
		if _, err := CurrencyCode("JPY"); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("CurrencyCode(JPY) error = %v, want a 'not supported' error", err)
		}
	})
}

func TestFormatInstallment(t *testing.T) {
	// Zero means unset in the Go request type, so it renders like a single payment.
	for _, tc := range []struct {
		in   int
		want string
	}{{0, ""}, {1, ""}, {6, "6"}} {
		got, err := FormatInstallment(tc.in)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatInstallment(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := FormatInstallment(-1); err == nil {
		t.Error("FormatInstallment(-1) did not fail")
	}
}

func TestFormatExpiry(t *testing.T) {
	expiry, err := FormatExpiry("1", "2030")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if expiry.Month != "01" || expiry.Year != "30" {
		t.Errorf("expiry = %+v, want {01 30}", expiry)
	}
	if _, err := FormatExpiry("13", "30"); err == nil || !strings.Contains(err.Error(), "Invalid card expiry") {
		t.Errorf("FormatExpiry(13, 30) error = %v, want an invalid expiry error", err)
	}
}

func TestCardType(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"4355084355084358", "visa"},
		{"5528790000000008", "mastercard"},
		{"2223000048400011", "mastercard"},
		{"375987000000088", "amex"},
		{"9792030000000000", "visa"},
	} {
		if got := CardType(tc.in); got != tc.want {
			t.Errorf("CardType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsHostedStoreType(t *testing.T) {
	for _, tc := range []struct {
		in   StoreType
		want bool
	}{{Store3DHost, true}, {Store3DPayHosting, true}, {Store3D, false}, {Store3DPay, false}} {
		if got := IsHostedStoreType(tc.in); got != tc.want {
			t.Errorf("IsHostedStoreType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBuildXML(t *testing.T) {
	t.Run("builds a CC5Request with nested elements", func(t *testing.T) {
		xml := BuildXML([]XMLField{
			{Name: "Name", Value: "u"},
			{Name: "Type", Value: "Auth"},
			{Name: "Extra", Children: []XMLField{{Name: "ORDERSTATUS", Value: "SOR"}}},
		})
		want := `<?xml version="1.0" encoding="ISO-8859-9"?><CC5Request><Name>u</Name><Type>Auth</Type>` +
			`<Extra><ORDERSTATUS>SOR</ORDERSTATUS></Extra></CC5Request>`
		if xml != want {
			t.Errorf("xml = %q, want %q", xml, want)
		}
	})

	t.Run("escapes markup and sends non-ASCII characters as references", func(t *testing.T) {
		xml := BuildXML([]XMLField{{Name: "Name", Value: "Ayşe & <Ç>"}})
		if !strings.Contains(xml, "<Name>Ay&#351;e &amp; &lt;&#199;&gt;</Name>") {
			t.Errorf("xml = %q", xml)
		}
		if regexp.MustCompile(`[^\x20-\x7e]`).MatchString(xml) {
			t.Errorf("xml carries non-ASCII characters: %q", xml)
		}
	})
}

func TestParseXMLResponse(t *testing.T) {
	t.Run("reads the top-level fields and Extra separately", func(t *testing.T) {
		response, err := ParseXMLResponse(testApprovedXML)
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		expectFields(t, response.Fields, map[string]string{
			"OrderId": "ORDER3D", "Response": "Approved", "ProcReturnCode": "00",
			"TransId": "26011AAAA1234", "ErrMsg": "",
		})
		expectFields(t, response.Extra, map[string]string{"HOSTMSG": "ONAY", "SETTLEID": ""})
		if _, ok := response.Fields["HOSTMSG"]; ok {
			t.Error("HOSTMSG leaked into the top-level fields")
		}
	})

	t.Run("reads self-closing elements and entities", func(t *testing.T) {
		response, err := ParseXMLResponse("<CC5Response><ErrMsg>A &amp; B</ErrMsg><AuthCode/></CC5Response>")
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		if len(response.Fields) != 2 || response.Fields["ErrMsg"] != "A & B" || response.Fields["AuthCode"] != "" {
			t.Errorf("fields = %v", response.Fields)
		}
	})

	t.Run("throws on a body that is not a CC5Response", func(t *testing.T) {
		if _, err := ParseXMLResponse("<html>error</html>"); err == nil || !strings.Contains(err.Error(), "unexpected response") {
			t.Errorf("error = %v, want an unexpected response error", err)
		}
	})
}

func TestIsApproved(t *testing.T) {
	for _, tc := range []struct {
		fields map[string]string
		want   bool
	}{
		{map[string]string{"ProcReturnCode": "00", "Response": "Approved"}, true},
		{map[string]string{"ProcReturnCode": "00", "Response": "Declined"}, false},
		{map[string]string{"ProcReturnCode": "05", "Response": "Approved"}, false},
		{map[string]string{}, false},
	} {
		if got := IsApproved(tc.fields); got != tc.want {
			t.Errorf("IsApproved(%v) = %v, want %v", tc.fields, got, tc.want)
		}
	}
}

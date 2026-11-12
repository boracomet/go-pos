package denizbank

import (
	"strings"
	"testing"
)

const (
	testShopCode     = "9999"
	testUserCode     = "apiuser"
	testUserPass     = "apipass"
	testMerchantPass = "TESTSTOREKEY"
	testBaseURL      = SandboxBaseURL
)

func testConfig() Config {
	return Config{
		ShopCode:     testShopCode,
		UserCode:     testUserCode,
		UserPass:     testUserPass,
		MerchantPass: testMerchantPass,
	}
}

func callbackData() CallbackData {
	return CallbackData{
		"Version3D":               "2.0",
		"PurchAmount":             "1.20",
		"TotalAmount":             "1.20",
		"InstallmentCount":        "",
		"Currency":                "949",
		"OrderId":                 "ORDER3D",
		"TxnType":                 "Auth",
		"ShopCode":                testShopCode,
		"3DStatus":                "1",
		"MD":                      "MDVALUE123",
		"PayerTxnId":              "XIDVALUE=",
		"Eci":                     "05",
		"PayerAuthenticationCode": "CAVVVALUE=",
		"HASHPARAMS":              "Version3D:PurchAmount:TotalAmount:InstallmentCount:Currency:OrderId:TxnType:ShopCode:3DStatus:MD",
		"HASHPARAMSVAL":           "2.01.201.20949ORDER3DAuth99991MDVALUE123",
		"HASH":                    "cV+UFR1qPHiX/I8VFi2DNpn9G20=",
	}
}

func answer(overrides Fields) string {
	fields := mergeFields(Fields{
		{Key: "OrderId", Value: ""},
		{Key: "HostRefNum", Value: ""},
		{Key: "AuthCode", Value: ""},
		{Key: "TxnResult", Value: ""},
	}, overrides)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f.Key+"="+f.Value)
	}
	return strings.Join(parts, ";;") + ";;"
}

func approvedAnswer() string {
	return answer(Fields{
		{Key: "ProcReturnCode", Value: "00"},
		{Key: "ErrorMessage", Value: ""},
		{Key: "OrderId", Value: "ORDER3D"},
		{Key: "AuthCode", Value: "123456"},
		{Key: "TxnResult", Value: "Success"},
		{Key: "TransId", Value: "T1"},
	})
}

func declinedAnswer() string {
	return answer(Fields{
		{Key: "ProcReturnCode", Value: "51"},
		{Key: "ErrorCode", Value: "51"},
		{Key: "ErrorMessage", Value: "Yetersiz bakiye"},
		{Key: "TxnResult", Value: "Failed"},
	})
}

// mergeFields replaces values in place, keeping the original parameter order.
func mergeFields(base, overrides Fields) Fields {
	merged := make(Fields, 0, len(base)+len(overrides))
	for _, f := range base {
		value := f.Value
		for _, o := range overrides {
			if o.Key == f.Key {
				value = o.Value
			}
		}
		merged = append(merged, Field{Key: f.Key, Value: value})
	}
	for _, o := range overrides {
		known := false
		for _, f := range base {
			if f.Key == o.Key {
				known = true
				break
			}
		}
		if !known {
			merged = append(merged, o)
		}
	}
	return merged
}

func TestCreate3DHashReferenceVector(t *testing.T) {
	got := Create3DHash(HashFields{
		ShopCode:     testShopCode,
		OrderID:      "ORDER3D",
		Amount:       "1.20",
		OkURL:        "https://example.com/callback",
		FailURL:      "https://example.com/callback",
		TxnType:      "Auth",
		Installment:  "",
		Rnd:          "0123456789abcdef0123456789abcdef",
		MerchantPass: testMerchantPass,
	})
	if want := "iOJGlbU8NMSpo5xow2StNX9p36w="; got != want {
		t.Fatalf("Create3DHash = %q, want %q", got, want)
	}
}

func TestVerifyCallbackAcceptsReference(t *testing.T) {
	if !VerifyCallback(callbackData(), testMerchantPass) {
		t.Fatal("reference callback rejected")
	}
}

func TestVerifyCallbackRebuildsValueIgnoringForgedHashParamsVal(t *testing.T) {
	// The genuine HASH and HASHPARAMSVAL, with the order id changed.
	data := callbackData()
	data["OrderId"] = "OTHER"
	if VerifyCallback(data, testMerchantPass) {
		t.Fatal("a changed OrderId was accepted")
	}
}

func TestVerifyCallbackRequiresOrderIDInHashParams(t *testing.T) {
	data := callbackData()
	data["HASHPARAMS"] = "Version3D:MD"
	if VerifyCallback(data, testMerchantPass) {
		t.Fatal("HASHPARAMS without OrderId was accepted")
	}
}

func TestVerifyCallbackRejectsWrongStoreKeyOrMissingHash(t *testing.T) {
	if VerifyCallback(callbackData(), "other") {
		t.Error("a wrong store key was accepted")
	}
	data := callbackData()
	delete(data, "HASH")
	if VerifyCallback(data, testMerchantPass) {
		t.Error("a callback without HASH was accepted")
	}
}

func TestFormatters(t *testing.T) {
	if got, err := FormatAmount("1.2"); err != nil || got != "1.20" {
		t.Errorf("FormatAmount(1.2) = %q, %v", got, err)
	}
	if got, err := CurrencyCode("usd"); err != nil || got != "840" {
		t.Errorf("CurrencyCode(usd) = %q, %v", got, err)
	}
	if _, err := CurrencyCode("JPY"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(JPY) error = %v", err)
	}
	if got, err := FormatInstallment(1); err != nil || got != "" {
		t.Errorf("FormatInstallment(1) = %q, %v", got, err)
	}
	if got, err := FormatInstallment(3); err != nil || got != "3" {
		t.Errorf("FormatInstallment(3) = %q, %v", got, err)
	}
	if got, err := FormatExpiry("1", "2030"); err != nil || got != "0130" {
		t.Errorf("FormatExpiry(1, 2030) = %q, %v", got, err)
	}
	for number, want := range map[string]string{
		"4111111111111111": "visa",
		"5528790000000008": "mastercard",
		"375987000000088":  "amex",
	} {
		if got := CardType(number); got != want {
			t.Errorf("CardType(%s) = %q, want %q", number, got, want)
		}
	}
}

func TestParseResponse(t *testing.T) {
	parsed, err := ParseResponse("ProcReturnCode=96;;ErrorCode=031;;ErrorMessage=Gecersiz Islem Tipi;;OrderId=;;HostRefNum=hostid;;;TRXDATE=01.01.0001 00:00:00;;")
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	for key, want := range map[string]string{
		"ProcReturnCode": "96",
		"ErrorCode":      "031",
		"ErrorMessage":   "Gecersiz Islem Tipi",
		"OrderId":        "",
		"TRXDATE":        "01.01.0001 00:00:00",
	} {
		if parsed[key] != want {
			t.Errorf("%s = %q, want %q", key, parsed[key], want)
		}
	}
	if _, err := ParseResponse("<html/>"); err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("ParseResponse(<html/>) error = %v", err)
	}
}

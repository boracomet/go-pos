package qnb

import (
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

const (
	testMerchantID   = "085300000009597"
	testUserCode     = "QNB_API_KULLANICI"
	testUserPass     = "FwCX2"
	testMerchantPass = "12345678"
	testBaseURL      = SandboxBaseURL
	testFormHash     = "Zf29qjUJV9sxtrOZspFcjvecWxc="
)

func testConfig() Config {
	return Config{
		MerchantID:   testMerchantID,
		UserCode:     testUserCode,
		UserPass:     testUserPass,
		MerchantPass: testMerchantPass,
	}
}

func testCredentials() Credentials {
	return Credentials{MerchantID: testMerchantID, MerchantPass: testMerchantPass, UserCode: testUserCode}
}

// threeDModelCallback: card verified (V033), charged later with 3DModelPayment.
func threeDModelCallback() CallbackData {
	return CallbackData{
		"OrderId":        "ORDER3D",
		"MerchantID":     testMerchantID,
		"SecureType":     "3DModel",
		"3DStatus":       "1",
		"RequestGuid":    "1000000128757956",
		"PurchAmount":    "1.20",
		"Currency":       "949",
		"ProcReturnCode": "V033",
		"TxnResult":      "Failed",
		"ErrMsg":         "3D Kullanıcı Doğrulama Adımı Başarılı",
		"AuthCode":       "",
		"HostRefNum":     "",
		"Eci":            "05",
		"ResponseRnd":    "PF639273336051392939",
		"ResponseHash":   "gZrDp47gIQ8N0nCWSYNW80NZoe0=",
		"HashType":       "Sha1",
	}
}

// threeDPayCallback: the bank charged the card.
func threeDPayCallback() CallbackData {
	return CallbackData{
		"OrderId":        "ORDERPAY",
		"MerchantID":     testMerchantID,
		"SecureType":     "3DPay",
		"3DStatus":       "1",
		"RequestGuid":    "1000000128757960",
		"PurchAmount":    "1.00",
		"Currency":       "949",
		"ProcReturnCode": "00",
		"TxnResult":      "Success",
		"ErrMsg":         "Onaylandı",
		"AuthCode":       "S59045",
		"HostRefNum":     "628416660077",
		"Eci":            "05",
		"ResponseRnd":    "PF639273336051392940",
		"ResponseHash":   "3Yllopti5IiA/HlqGAZkwGQw7Oo=",
		"HashType":       "Sha1",
	}
}

func qnbXML(fields Fields) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><PayforResponse>`)
	for _, f := range fields {
		b.WriteString("<" + f.Key + ">" + f.Value + "</" + f.Key + ">")
	}
	b.WriteString(`</PayforResponse>`)
	return b.String()
}

func approvedXML() string {
	return qnbXML(Fields{
		{Key: "OrderId", Value: "ORDER3D"},
		{Key: "ProcReturnCode", Value: "00"},
		{Key: "ErrMsg", Value: "Onaylandı"},
		{Key: "AuthCode", Value: "S70439"},
		{Key: "HostRefNum", Value: "628416660070"},
	})
}

func inquiryXML(overrides Fields) string {
	return qnbXML(mergeFields(Fields{
		{Key: "OrderId", Value: "ORDERPAY"},
		{Key: "TxnResult", Value: "Success"},
		{Key: "ProcReturnCode", Value: "00"},
		{Key: "ErrMsg", Value: "Onaylandı"},
		{Key: "AuthCode", Value: "S59045"},
		{Key: "HostRefNum", Value: "\r\n  "},
		{Key: "TxnStatus", Value: "Y"},
		{Key: "IsVoided", Value: "false"},
		{Key: "IsRefunded", Value: "false"},
		{Key: "PurchAmount", Value: "1.00"},
		{Key: "TxnType", Value: "Auth"},
	}, overrides))
}

// mergeFields replaces values in place, keeping the original element order.
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
		OrderID:      "ORDER3D",
		Amount:       "1.20",
		OkURL:        "https://example.com/callback",
		FailURL:      "https://example.com/callback",
		TxnType:      "Auth",
		Installment:  "0",
		Rnd:          "0123456789abcdef0123",
		MerchantPass: testMerchantPass,
	})
	if got != testFormHash {
		t.Fatalf("Create3DHash = %q, want %q", got, testFormHash)
	}
}

func TestVerifyCallbackAcceptsReferenceCallbacks(t *testing.T) {
	if !VerifyCallback(threeDModelCallback(), testCredentials()) {
		t.Error("3DModel callback rejected")
	}
	if !VerifyCallback(threeDPayCallback(), testCredentials()) {
		t.Error("3DPay callback rejected")
	}
}

func TestVerifyCallbackRejectsChangedFields(t *testing.T) {
	for _, field := range []string{"OrderId", "ProcReturnCode", "3DStatus", "AuthCode", "ResponseRnd"} {
		t.Run(field, func(t *testing.T) {
			data := threeDPayCallback()
			data[field] = "X"
			if VerifyCallback(data, testCredentials()) {
				t.Errorf("changed %s accepted", field)
			}
		})
	}
}

func TestVerifyCallbackUsesConfiguredCredentials(t *testing.T) {
	credentials := testCredentials()
	credentials.UserCode = "OTHER"
	if VerifyCallback(threeDPayCallback(), credentials) {
		t.Error("callback accepted with a different userCode")
	}
	credentials = testCredentials()
	credentials.MerchantPass = "x"
	if VerifyCallback(threeDPayCallback(), credentials) {
		t.Error("callback accepted with a different merchantPass")
	}
}

func TestVerifyCallbackRejectsMissingHash(t *testing.T) {
	data := threeDPayCallback()
	delete(data, "ResponseHash")
	if VerifyCallback(data, testCredentials()) {
		t.Error("callback without ResponseHash accepted")
	}
}

func TestFormatters(t *testing.T) {
	amount, err := FormatAmount("1.2")
	if err != nil || amount != "1.20" {
		t.Errorf("FormatAmount(1.2) = %q, %v", amount, err)
	}
	if code, err := CurrencyCode(""); err != nil || code != "949" {
		t.Errorf("CurrencyCode(empty) = %q, %v", code, err)
	}
	if code, err := CurrencyCode("eur"); err != nil || code != "978" {
		t.Errorf("CurrencyCode(eur) = %q, %v", code, err)
	}
	if _, err := CurrencyCode("GBP"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(GBP) error = %v", err)
	}
	if got, err := FormatInstallment(1); err != nil || got != "0" {
		t.Errorf("FormatInstallment(1) = %q, %v", got, err)
	}
	if got, err := FormatInstallment(6); err != nil || got != "6" {
		t.Errorf("FormatInstallment(6) = %q, %v", got, err)
	}
	if got, err := FormatExpiry("2", "2030"); err != nil || got != "0230" {
		t.Errorf("FormatExpiry(2, 2030) = %q, %v", got, err)
	}
	if got, err := ValidateOrderID("ORDER_1-A"); err != nil || got != "ORDER_1-A" {
		t.Errorf("ValidateOrderID = %q, %v", got, err)
	}
	if _, err := ValidateOrderID("SİPARİŞ"); err == nil {
		t.Error("ValidateOrderID accepted Turkish characters")
	}
}

func TestBuildAndParseXML(t *testing.T) {
	want := `<?xml version="1.0" encoding="utf-8"?><PayforRequest><MbrId>5</MbrId><OrderId>A&amp;B</OrderId></PayforRequest>`
	if got := BuildXML(Fields{{Key: "MbrId", Value: "5"}, {Key: "OrderId", Value: "A&B"}}); got != want {
		t.Errorf("BuildXML = %q, want %q", got, want)
	}
	fields, err := ParseXMLResponse(inquiryXML(nil))
	if err != nil {
		t.Fatalf("ParseXMLResponse: %v", err)
	}
	if fields["TxnStatus"] != "Y" {
		t.Errorf("TxnStatus = %q, want Y", fields["TxnStatus"])
	}
	if fields["HostRefNum"] != "" {
		t.Errorf("HostRefNum = %q, want empty", fields["HostRefNum"])
	}
	if _, err := ParseXMLResponse("<html/>"); err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("ParseXMLResponse(<html/>) error = %v", err)
	}
}

func TestMapOrderStatus(t *testing.T) {
	cases := []struct {
		name      string
		overrides Fields
		want      core.PaymentStatus
	}{
		{"approved", nil, core.StatusSuccess},
		{"voided", Fields{{Key: "TxnStatus", Value: "V"}, {Key: "IsVoided", Value: "true"}}, core.StatusCancelled},
		{"refunded", Fields{{Key: "IsRefunded", Value: "true"}}, core.StatusCancelled},
		{"failed", Fields{{Key: "TxnResult", Value: "Failed"}, {Key: "TxnStatus", Value: "N"}}, core.StatusFailure},
		{"pending", Fields{{Key: "TxnStatus", Value: "P"}, {Key: "TxnResult", Value: ""}}, core.StatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := ParseXMLResponse(inquiryXML(tc.overrides))
			if err != nil {
				t.Fatalf("ParseXMLResponse: %v", err)
			}
			if got := MapOrderStatus(fields); got != tc.want {
				t.Errorf("MapOrderStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

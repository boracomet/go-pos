package vakifbank

import (
	"net/url"
	"strings"
	"testing"
)

const (
	testMerchantID = "000000000111111"
	testPassword   = "apipass"
	testTerminalNo = "VP000001"
	testBaseURL    = SandboxBaseURL
)

func testConfig() Config {
	return Config{
		MerchantID: testMerchantID,
		Password:   testPassword,
		TerminalNo: testTerminalNo,
		TestMode:   true,
	}
}

func vposXML(fields Fields) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><VposResponse>`)
	for _, f := range fields {
		b.WriteString("<" + f.Key + ">" + f.Value + "</" + f.Key + ">")
	}
	b.WriteString(`</VposResponse>`)
	return b.String()
}

func approvedXML() string {
	return vposXML(Fields{
		{Key: "MerchantId", Value: testMerchantID},
		{Key: "TransactionType", Value: "Sale"},
		{Key: "TransactionId", Value: "ORDER3D"},
		{Key: "ResultCode", Value: "0000"},
		{Key: "ResultDetail", Value: "İŞLEM BAŞARILI"},
		{Key: "AuthCode", Value: "985347"},
		{Key: "Rrn", Value: "918911726582"},
		{Key: "CurrencyAmount", Value: "1.20"},
		{Key: "CurrencyCode", Value: "949"},
	})
}

func declinedXML() string {
	return vposXML(Fields{
		{Key: "ResultCode", Value: "0051"},
		{Key: "ResultDetail", Value: "YETERSIZ BAKIYE"},
	})
}

const enrolledXML = `<?xml version="1.0" encoding="utf-8" ?><IPaySecure><Message ID="m1"><VERes><Version>1.0.2</Version><Status>Y</Status><PaReq>PAREQVALUE</PaReq><ACSUrl>https://acs.example-bank.test/pareq</ACSUrl><TermUrl>https://3dsecuretest.vakifbank.com.tr/MPIAPI/MPI_PARes.aspx</TermUrl><MD>MDVALUE</MD><ACTUALBRAND>200</ACTUALBRAND></VERes></Message><VerifyEnrollmentRequestId>ORDER3D</VerifyEnrollmentRequestId><MessageErrorCode>200</MessageErrorCode></IPaySecure>`

const notFoundXML = `<?xml version="1.0" encoding="utf-8"?><IPaySecure><Message><VERes><Status>E</Status></VERes></Message><VerifyEnrollmentRequestId>ORDER3D</VerifyEnrollmentRequestId><MessageErrorCode>9093</MessageErrorCode><ErrorMessage>Aranan İşyeri Kaydı Bulunamadı.</ErrorMessage></IPaySecure>`

const notEnrolledXML = `<IPaySecure><Message ID="x"/><VERes><Version>1.0.2</Version><Status>N</Status></VERes></IPaySecure>`

func callbackData() CallbackData {
	return CallbackData{
		"MerchantId":                testMerchantID,
		"VerifyEnrollmentRequestId": "ORDER3D",
		"Status":                    "Y",
		"Xid":                       "XIDVALUE",
		"PurchAmount":               "120",
		"PurchCurrency":             "949",
		"ExpiryDate":                "3012",
		"Eci":                       "02",
		"Cavv":                      "CAVVVALUE=",
		"SessionInfo":               "85.34.78.112",
	}
}

func TestFormatters(t *testing.T) {
	if got, err := FormatAmount("1.2"); err != nil || got != "1.20" {
		t.Errorf("FormatAmount(1.2) = %q, %v", got, err)
	}
	if got, err := FormatAmount("1234.5"); err != nil || got != "1234.50" {
		t.Errorf("FormatAmount(1234.5) = %q, %v", got, err)
	}
	if got, err := CurrencyCode(""); err != nil || got != "949" {
		t.Errorf("CurrencyCode(empty) = %q, %v", got, err)
	}
	if got, err := CurrencyCode("usd"); err != nil || got != "840" {
		t.Errorf("CurrencyCode(usd) = %q, %v", got, err)
	}
	if _, err := CurrencyCode("CHF"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(CHF) error = %v", err)
	}
	if got, err := FormatInstallment(1); err != nil || got != 0 {
		t.Errorf("FormatInstallment(1) = %d, %v", got, err)
	}
	if got, err := FormatInstallment(6); err != nil || got != 6 {
		t.Errorf("FormatInstallment(6) = %d, %v", got, err)
	}
	if got, err := FormatMPIExpiry("3", "2025"); err != nil || got != "2503" {
		t.Errorf("FormatMPIExpiry(3, 2025) = %q, %v", got, err)
	}
	if got, err := FormatVPOSExpiry("3", "25"); err != nil || got != "202503" {
		t.Errorf("FormatVPOSExpiry(3, 25) = %q, %v", got, err)
	}
	if _, err := FormatVPOSExpiry("13", "2025"); err == nil || !strings.Contains(err.Error(), "Invalid card expiry") {
		t.Errorf("FormatVPOSExpiry(13, 2025) error = %v", err)
	}
	for number, want := range map[string]string{
		"4289450189088488": "100",
		"5528790000000008": "200",
		"9792030000000000": "300",
		"375987000000088":  "400",
	} {
		if got := BrandName(number); got != want {
			t.Errorf("BrandName(%s) = %q, want %q", number, got, want)
		}
	}
}

func TestValidateOrderID(t *testing.T) {
	if got, err := ValidateOrderID("ORDER_3D-1"); err != nil || got != "ORDER_3D-1" {
		t.Errorf("ValidateOrderID = %q, %v", got, err)
	}
	if _, err := ValidateOrderID(strings.Repeat("A", 41)); err == nil {
		t.Error("ValidateOrderID accepted 41 characters")
	}
	if _, err := ValidateOrderID("ORDER 1"); err == nil {
		t.Error("ValidateOrderID accepted a space")
	}
}

func TestBuildRequestBodyPostsPrmstr(t *testing.T) {
	body := BuildRequestBody("VposRequest", Fields{
		{Key: "MerchantId", Value: "1"},
		{Key: "ClientIp", Value: "1.1.1.1"},
	})
	if !strings.HasPrefix(body, "prmstr=") {
		t.Fatalf("body = %q, want a prmstr prefix", body)
	}
	xml, err := url.PathUnescape(strings.TrimPrefix(body, "prmstr="))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := `<?xml version="1.0" encoding="utf-8"?><VposRequest><MerchantId>1</MerchantId><ClientIp>1.1.1.1</ClientIp></VposRequest>`
	if xml != want {
		t.Errorf("xml = %q, want %q", xml, want)
	}
}

func TestParseXML(t *testing.T) {
	fields, err := ParseXML(approvedXML(), "VposResponse")
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	if fields["ResultCode"] != "0000" || fields["AuthCode"] != "985347" {
		t.Errorf("fields = %v", fields)
	}
	if _, err := ParseXML("<html/>", "VposResponse"); err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("ParseXML(<html/>) error = %v", err)
	}
}

func TestIsIP(t *testing.T) {
	for _, value := range []string{"85.34.78.112", "127.0.0.1", "::1"} {
		if !IsIP(value) {
			t.Errorf("IsIP(%q) = false", value)
		}
	}
	for _, value := range []string{"", "not-an-ip", "1.1.1.1;drop"} {
		if IsIP(value) {
			t.Errorf("IsIP(%q) = true", value)
		}
	}
}

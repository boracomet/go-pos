package parampos

import (
	"bytes"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func TestGeneratePaymentHashReferenceVector(t *testing.T) {
	got, err := GeneratePaymentHash("10738", testGUID, 3, "1.000,01", "1.000,01", "202412293F4E")
	if err != nil {
		t.Fatalf("GeneratePaymentHash() error = %v", err)
	}
	if want := "jsLYSB3lJ81leFgDLw4D8PbXURs="; got != want {
		t.Errorf("GeneratePaymentHash() = %q, want %q", got, want)
	}
}

func TestGenerate3DSVerificationHashReferenceVector(t *testing.T) {
	got, err := Generate3DSVerificationHash(
		"35513153-9902-4a4c-a256-af6ed9cadc52",
		"581877:A65A349B0BAE27FC6567294215158DD8AE223843B5C96462F04A750CA7E8B165:3680:##500100000",
		"1",
		"2025011749D1",
		testGUID,
	)
	if err != nil {
		t.Fatalf("Generate3DSVerificationHash() error = %v", err)
	}
	if want := "b1D7+nI3j4k3WGJuhW5IuPOFpEE="; got != want {
		t.Errorf("Generate3DSVerificationHash() = %q, want %q", got, want)
	}
}

func TestVerify3DSCallback(t *testing.T) {
	callback := referenceCallback()

	if !Verify3DSCallback(callback, testGUID) {
		t.Error("Verify3DSCallback() = false for a valid callback, want true")
	}

	tampered := referenceCallback()
	tampered["mdStatus"] = "0"
	if Verify3DSCallback(tampered, testGUID) {
		t.Error("Verify3DSCallback() = true for a tampered mdStatus, want false")
	}

	attackerGUID := "11111111-1111-1111-1111-111111111111"
	forged := referenceCallback()
	forged["GUID"] = attackerGUID
	forgedHash, err := Generate3DSVerificationHash(
		callback["islemGUID"], callback["md"], "1", callback["orderId"], attackerGUID)
	if err != nil {
		t.Fatalf("Generate3DSVerificationHash() error = %v", err)
	}
	forged["islemHash"] = forgedHash
	if Verify3DSCallback(forged, testGUID) {
		t.Error("Verify3DSCallback() = true for a callback signed with another GUID, want false")
	}

	missingHash := referenceCallback()
	delete(missingHash, "islemHash")
	if Verify3DSCallback(missingHash, testGUID) {
		t.Error("Verify3DSCallback() = true without islemHash, want false")
	}
	if Verify3DSCallback(map[string]string{}, testGUID) {
		t.Error("Verify3DSCallback() = true for an empty callback, want false")
	}
	if Verify3DSCallback(nil, testGUID) {
		t.Error("Verify3DSCallback() = true for a nil callback, want false")
	}
}

func TestFormatAmount(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"100", "100,00"},
		{"10.5", "10,50"},
		{"1.006", "1,01"},
	} {
		got, err := FormatAmount(tc.in)
		if err != nil {
			t.Fatalf("FormatAmount(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatAmount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, invalid := range []string{"abc", "-1"} {
		if _, err := FormatAmount(invalid); err == nil {
			t.Errorf("FormatAmount(%q) error = nil, want an error", invalid)
		}
	}
}

func TestFormatRefundAmount(t *testing.T) {
	got, err := FormatRefundAmount("10.5")
	if err != nil {
		t.Fatalf("FormatRefundAmount() error = %v", err)
	}
	if got != "10.50" {
		t.Errorf("FormatRefundAmount() = %q, want %q", got, "10.50")
	}
}

func TestFormatExpiry(t *testing.T) {
	month, err := FormatExpiryMonth("3")
	if err != nil {
		t.Fatalf("FormatExpiryMonth(3) error = %v", err)
	}
	if month != "03" {
		t.Errorf("FormatExpiryMonth(3) = %q, want %q", month, "03")
	}
	if _, err := FormatExpiryMonth("13"); err == nil {
		t.Error("FormatExpiryMonth(13) error = nil, want an error")
	}
	year, err := FormatExpiryYear("30")
	if err != nil {
		t.Fatalf("FormatExpiryYear(30) error = %v", err)
	}
	if year != "2030" {
		t.Errorf("FormatExpiryYear(30) = %q, want %q", year, "2030")
	}
	year, err = FormatExpiryYear("2031")
	if err != nil {
		t.Fatalf("FormatExpiryYear(2031) error = %v", err)
	}
	if year != "2031" {
		t.Errorf("FormatExpiryYear(2031) = %q, want %q", year, "2031")
	}
	if _, err := FormatExpiryYear("203"); err == nil {
		t.Error("FormatExpiryYear(203) error = nil, want an error")
	}
}

func TestFormatGSM(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"+90 535 000 00 00", "5350000000"},
		{"05350000000", "5350000000"},
		{"", ""},
	} {
		if got := FormatGSM(tc.in); got != tc.want {
			t.Errorf("FormatGSM(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEncodeISO88599(t *testing.T) {
	got := EncodeISO88599("ğüşİıç")
	want := []byte{0xf0, 0xfc, 0xfe, 0xdd, 0xfd, 0xe7}
	if !bytes.Equal(got, want) {
		t.Errorf("EncodeISO88599() = % x, want % x", got, want)
	}
}

func TestEscapeAndUnescapeXML(t *testing.T) {
	if got, want := EscapeXML(`<a & "b">`), "&lt;a &amp; &quot;b&quot;&gt;"; got != want {
		t.Errorf("EscapeXML() = %q, want %q", got, want)
	}
	if got, want := UnescapeXML("&lt;form action=&quot;x&quot;&gt;&#x130;&#252;"), `<form action="x">İü`; got != want {
		t.Errorf("UnescapeXML() = %q, want %q", got, want)
	}
	if got := EscapeXML(""); got != "" {
		t.Errorf("EscapeXML(\"\") = %q, want empty", got)
	}
}

func TestBuildSOAPEnvelope(t *testing.T) {
	xml := BuildSOAPEnvelope("TP_WMD_Pay", Fields{
		NewGroup("G", fields("CLIENT_CODE", "1")),
		NewField("Siparis_ID", "a<b"),
	})
	for _, want := range []string{
		`<TP_WMD_Pay xmlns="https://turkpos.com.tr/">`,
		`<G><CLIENT_CODE>1</CLIENT_CODE></G>`,
		`<Siparis_ID>a&lt;b</Siparis_ID>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("envelope missing %q:\n%s", want, xml)
		}
	}
}

func TestParseSOAPResponse(t *testing.T) {
	xml := soapResponse("TP_Islem_Sorgulama4", Fields{
		NewGroup("DT_Bilgi", fields("Durum", "SUCCESS", "Toplam_Tutar", "10.01")),
		NewField("Sonuc", "1"),
		NewField("Sonuc_Str", "Başarılı"),
	})
	result, err := ParseSOAPResponse(xml, "TP_Islem_Sorgulama4Result")
	if err != nil {
		t.Fatalf("ParseSOAPResponse() error = %v", err)
	}
	for key, want := range map[string]string{"Sonuc": "1", "Durum": "SUCCESS", "Toplam_Tutar": "10.01", "Sonuc_Str": "Başarılı"} {
		if result[key] != want {
			t.Errorf("result[%q] = %q, want %q", key, result[key], want)
		}
	}

	htmlXML := soapResponse("TP_WMD_UCD", fields("UCD_HTML", `<form id="x"></form>`))
	html, err := ParseSOAPResponse(htmlXML, "TP_WMD_UCDResult")
	if err != nil {
		t.Fatalf("ParseSOAPResponse() error = %v", err)
	}
	if got, want := html["UCD_HTML"], `<form id="x"></form>`; got != want {
		t.Errorf("UCD_HTML = %q, want %q", got, want)
	}
}

func TestParseSOAPResponseErrors(t *testing.T) {
	_, err := ParseSOAPResponse("<soap:Fault><faultstring>Server was unable</faultstring></soap:Fault>", "X")
	if err == nil || !strings.Contains(err.Error(), "Server was unable") {
		t.Errorf("fault error = %v, want it to mention the faultstring", err)
	}
	if _, err := ParseSOAPResponse("<a/>", "TP_WMD_UCDResult"); err == nil {
		t.Error("missing result error = nil, want an error")
	}
}

func TestIsSuccess(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"0", false},
		{"-102", false},
		{"", false},
	} {
		if got := IsSuccess(tc.in); got != tc.want {
			t.Errorf("IsSuccess(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMapOrderStatus(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want core.PaymentStatus
	}{
		{"SUCCESS", core.StatusSuccess},
		{"REFUND", core.StatusCancelled},
		{"CANCEL", core.StatusCancelled},
		{"FAIL", core.StatusFailure},
		{"", core.StatusPending},
	} {
		if got := MapOrderStatus(tc.in); got != tc.want {
			t.Errorf("MapOrderStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateTurkishIdentityNumberAndMaskCard(t *testing.T) {
	if !ValidateTurkishIdentityNumber("10000000146") {
		t.Error("ValidateTurkishIdentityNumber(10000000146) = false, want true")
	}
	if ValidateTurkishIdentityNumber("12345678901") {
		t.Error("ValidateTurkishIdentityNumber(12345678901) = true, want false")
	}
	if got, want := MaskCardNumber("4446763125813623"), "4446********3623"; got != want {
		t.Errorf("MaskCardNumber() = %q, want %q", got, want)
	}
}

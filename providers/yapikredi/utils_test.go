package yapikredi

import (
	"net/url"
	"strings"
	"testing"
)

func TestCreateMac(t *testing.T) {
	got, err := CreateMac(MACFields{
		XID:        "0000000000000ORDER3D",
		Amount:     "120",
		Currency:   "TL",
		MerchantID: "6706598320",
		TerminalID: "67005551",
		EncKey:     "10,10,10,10,10,10,10,10",
	})
	if err != nil {
		t.Fatalf("CreateMac() error = %v", err)
	}
	wantString(t, got, ykbMAC, "mac")
}

func TestCreateMacGuideSample(t *testing.T) {
	got, err := CreateMac(MACFields{
		XID:        "YKB_TST_190620093100_024",
		Amount:     "175",
		Currency:   "TL",
		MerchantID: "6706598320",
		TerminalID: "67005551",
		EncKey:     "10,10,10,10,10,10,10,10",
	})
	if err != nil {
		t.Fatalf("CreateMac() error = %v", err)
	}
	wantString(t, got, guideMAC, "mac")
}

func TestCreateResponseMac(t *testing.T) {
	fields := MACFields{
		XID:        "0000000000000ORDER3D",
		Amount:     "120",
		Currency:   "TL",
		MerchantID: "6706598320",
		TerminalID: "67005551",
		EncKey:     "10,10,10,10,10,10,10,10",
	}
	resolve, err := CreateResponseMac("1", fields)
	if err != nil {
		t.Fatalf("CreateResponseMac() error = %v", err)
	}
	wantString(t, resolve, resolveMAC, "resolve mac")

	charge, err := CreateResponseMac("021000000000000123", fields)
	if err != nil {
		t.Fatalf("CreateResponseMac() error = %v", err)
	}
	wantString(t, charge, chargeMAC, "charge mac")
}

func TestFormatAmount(t *testing.T) {
	for _, item := range []struct{ amount, want string }{
		{"1.2", "120"},
		{"1.20", "120"},
		{"0.5", "50"},
		{"1", "100"},
		{"1.005", "101"},
	} {
		got, err := FormatAmount(item.amount)
		if err != nil {
			t.Fatalf("FormatAmount(%q) error = %v", item.amount, err)
		}
		wantString(t, got, item.want, "amount "+item.amount)
	}
	if _, err := FormatAmount("abc"); err == nil {
		t.Error("FormatAmount(\"abc\") succeeded, want a validation error")
	}
}

func TestCurrencyCode(t *testing.T) {
	for _, item := range []struct{ currency, want string }{
		{"", "TL"},
		{"TRY", "TL"},
		{"TL", "TL"},
		{"usd", "US"},
		{"EUR", "EU"},
	} {
		got, err := CurrencyCode(item.currency)
		if err != nil {
			t.Fatalf("CurrencyCode(%q) error = %v", item.currency, err)
		}
		wantString(t, got, item.want, "currency "+item.currency)
	}
	for _, currency := range []string{"CHF", "GBP"} {
		if _, err := CurrencyCode(currency); err == nil {
			t.Errorf("CurrencyCode(%q) succeeded, want a validation error", currency)
		} else if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("CurrencyCode(%q) error = %v, want not supported", currency, err)
		}
	}
}

func TestFormatInstallment(t *testing.T) {
	for _, item := range []struct {
		count int
		want  string
	}{
		{0, "00"},
		{1, "00"},
		{3, "03"},
		{12, "12"},
		{99, "99"},
	} {
		got, err := FormatInstallment(item.count)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", item.count, err)
		}
		wantString(t, got, item.want, "installment")
	}
	for _, count := range []int{-1, 100} {
		if _, err := FormatInstallment(count); err == nil {
			t.Errorf("FormatInstallment(%d) succeeded, want a validation error", count)
		}
	}
}

func TestFormatExpiry(t *testing.T) {
	for _, item := range []struct{ month, year, want string }{
		{"1", "2030", "3001"},
		{"12", "2030", "3012"},
		{"12", "30", "3012"},
		{"1", "30", "3001"},
	} {
		got, err := FormatExpiry(item.month, item.year)
		if err != nil {
			t.Fatalf("FormatExpiry(%q, %q) error = %v", item.month, item.year, err)
		}
		wantString(t, got, item.want, "expiry")
	}
	for _, item := range [][2]string{{"13", "2030"}, {"0", "2030"}, {"12", "203"}, {"", "2030"}} {
		if _, err := FormatExpiry(item[0], item[1]); err == nil {
			t.Errorf("FormatExpiry(%q, %q) succeeded, want a validation error", item[0], item[1])
		}
	}
}

func TestOrderIDs(t *testing.T) {
	for _, item := range []struct{ orderID, want string }{
		{"ORDER3D", "0000000000000ORDER3D"},
		{"YKB_TST_1", "00000000000YKB_TST_1"},
		{strings.Repeat("A", 20), strings.Repeat("A", 20)},
	} {
		got, err := ToXID(item.orderID)
		if err != nil {
			t.Fatalf("ToXID(%q) error = %v", item.orderID, err)
		}
		wantString(t, got, item.want, "xid "+item.orderID)
	}
	got, err := ToOrderID("ORDER1")
	if err != nil {
		t.Fatalf("ToOrderID() error = %v", err)
	}
	wantString(t, got, "000000000000000000ORDER1", "order id")

	threeDS, err := ToThreeDSOrderID("ORDER1")
	if err != nil {
		t.Fatalf("ToThreeDSOrderID() error = %v", err)
	}
	wantString(t, threeDS, "TDS_00000000000000ORDER1", "3ds order id")

	for _, orderID := range []string{"ORDER-3D", strings.Repeat("A", 21), ""} {
		_, err := ToXID(orderID)
		if err == nil {
			t.Fatalf("ToXID(%q) succeeded, want a validation error", orderID)
		}
		if !strings.Contains(err.Error(), `letters, digits or "_"`) {
			t.Errorf("ToXID(%q) error = %v, want the order id rule", orderID, err)
		}
	}
	if _, err := ToOrderID(strings.Repeat("A", 25)); err == nil {
		t.Error("ToOrderID() accepted 25 characters, want a validation error")
	}
}

func TestBuildRequestBody(t *testing.T) {
	body := BuildRequestBody([]XMLField{
		{Name: "mid", Value: "1"},
		{Name: "sale", Children: []XMLField{{Name: "cardHolderName", Value: "Ayşe & Co"}}},
	})
	if !strings.HasPrefix(body, "xmldata=") {
		t.Fatalf("body = %q, want the xmldata form field", body)
	}
	for i := 0; i < len(body); i++ {
		if body[i] < 0x20 || body[i] > 0x7e {
			t.Fatalf("body holds a non-ASCII byte at %d", i)
		}
	}
	xml, err := url.PathUnescape(strings.TrimPrefix(body, "xmldata="))
	if err != nil {
		t.Fatalf("body is not form-encoded: %v", err)
	}
	wantString(t, xml,
		`<?xml version="1.0" encoding="ISO-8859-9"?><posnetRequest><mid>1</mid>`+
			`<sale><cardHolderName>Ay&#351;e &amp; Co</cardHolderName></sale></posnetRequest>`,
		"xml")
}

func TestEncodeURIComponent(t *testing.T) {
	wantString(t, encodeURIComponent("aA0-_.!~*'()"), "aA0-_.!~*'()", "unreserved")
	wantString(t, encodeURIComponent("a b+c&d=e"), "a%20b%2Bc%26d%3De", "reserved")
	wantString(t, encodeURIComponent("<>"), "%3C%3E", "angle brackets")
}

func TestParseXMLResponse(t *testing.T) {
	parsed, err := ParseXMLResponse(ykbResolveXML(nil))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if len(parsed.Fields) != 1 || parsed.Fields["approved"] != "1" {
		t.Errorf("fields = %v, want only approved 1", parsed.Fields)
	}
	wantString(t, parsed.OOSResolveMerchantData["xid"], "0000000000000ORDER3D", "xid")
	wantString(t, parsed.OOSResolveMerchantData["mdStatus"], "1", "mdStatus")
	if len(parsed.Transactions) != 0 {
		t.Errorf("transactions = %v, want none", parsed.Transactions)
	}

	request, err := ParseXMLResponse(oosRequestXML)
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if len(request.OOSRequestData) != 3 ||
		request.OOSRequestData["data1"] != "DATA1VALUE" ||
		request.OOSRequestData["data2"] != "DATA2VALUE" ||
		request.OOSRequestData["sign"] != "SIGNVALUE" {
		t.Errorf("oosRequestData = %v, want data1, data2 and sign", request.OOSRequestData)
	}

	if _, err := ParseXMLResponse("<html/>"); err == nil {
		t.Error("ParseXMLResponse(\"<html/>\") succeeded, want an error")
	} else if !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("error = %v, want unexpected response", err)
	}
}

func TestParseXMLResponseTransactions(t *testing.T) {
	parsed, err := ParseXMLResponse(ykbAgreementXML([]map[string]string{
		{},
		{"state": "Sale_Reverse", "hostLogKey": "021450428990000192"},
	}))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if len(parsed.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(parsed.Transactions))
	}
	wantString(t, parsed.Transactions[0]["hostLogKey"], "021450428990000191", "hostLogKey")
	wantString(t, parsed.Transactions[1]["state"], "Sale_Reverse", "state")
	if len(parsed.Fields) != 1 || parsed.Fields["approved"] != "1" {
		t.Errorf("fields = %v, want only approved 1", parsed.Fields)
	}
}

func TestParseXMLResponseUTF16(t *testing.T) {
	body := strings.Join(strings.Split(ykbAgreementXML([]map[string]string{{}}), ""), "\x00")
	parsed, err := ParseXMLResponse(body)
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if !IsApproved(parsed) || len(parsed.Transactions) != 1 {
		t.Errorf("parsed = %+v, want an approved answer with one transaction", parsed)
	}
}

func TestParseXMLResponseEscapes(t *testing.T) {
	parsed, err := ParseXMLResponse(ykbXML([]field{
		{"approved", "1"},
		{"respText", "Ay&#351;e &amp; Co &lt;1&gt; &quot;x&quot; &apos;y&apos;"},
	}, nil))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	wantString(t, parsed.Fields["respText"], "Ayşe & Co <1> \"x\" 'y'", "respText")
}

func TestParseXMLResponseFirstTagWins(t *testing.T) {
	parsed, err := ParseXMLResponse(ykbXML([]field{
		{"approved", "1"},
		{"respText", "first"},
		{"respText", "second"},
	}, nil))
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	wantString(t, parsed.Fields["respText"], "first", "respText")
}

func TestIsApproved(t *testing.T) {
	approved, err := ParseXMLResponse(approvedXML)
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if !IsApproved(approved) {
		t.Error("IsApproved() = false, want true")
	}
	declined, err := ParseXMLResponse(declinedXML)
	if err != nil {
		t.Fatalf("ParseXMLResponse() error = %v", err)
	}
	if IsApproved(declined) {
		t.Error("IsApproved() = true, want false")
	}
}

package garanti

import (
	"strings"
	"testing"
)

func TestCreateSecurityData(t *testing.T) {
	t.Run("SHA1 of the password and the terminal id padded to 9 digits", func(t *testing.T) {
		cfg := testConfig()
		got, err := CreateSecurityData(cfg.Password, cfg.TerminalID)
		if err != nil {
			t.Fatalf("CreateSecurityData() error = %v", err)
		}
		if got != testSecurityData {
			t.Errorf("CreateSecurityData() = %q, want %q", got, testSecurityData)
		}
	})

	t.Run("hashes Turkish characters as ISO-8859-9", func(t *testing.T) {
		got, err := CreateSecurityData("şifreĞİ", testConfig().TerminalID)
		if err != nil {
			t.Fatalf("CreateSecurityData() error = %v", err)
		}
		if got != "BCC9B0D0F9453C13E524916372AA007A69161F09" {
			t.Errorf("CreateSecurityData() = %q", got)
		}
	})
}

func TestCreateHashData(t *testing.T) {
	cfg := testConfig()
	base := HashDataFields{
		OrderID:      "ORDER1",
		TerminalID:   cfg.TerminalID,
		CurrencyCode: "949",
		SecurityData: testSecurityData,
	}

	t.Run("with a card number", func(t *testing.T) {
		fields := base
		fields.CardNumber, fields.Amount = "5549603469426017", "120"
		got, err := CreateHashData(fields)
		if err != nil {
			t.Fatalf("CreateHashData() error = %v", err)
		}
		want := "18657F3D4D1050B859E84CAF55E143AADFB7D0C9C601636F8238A995A03E8190B97CD8B303BC3F4CC29A2F481787D978C4CC3EB5F0B828818AA13E6FD79534ED"
		if got != want {
			t.Errorf("CreateHashData() = %q, want %q", got, want)
		}
	})

	t.Run("without a card number", func(t *testing.T) {
		fields := base
		fields.Amount = "1"
		got, err := CreateHashData(fields)
		if err != nil {
			t.Fatalf("CreateHashData() error = %v", err)
		}
		want := "6FA85DCF3C00DBFEED916BDADD37FBE30A6EF692B7D55919388C4513DA9689C031971BA744E896796B8A1FEC45B3673B6488C0BD3FB5F43DA271F0C1F8865FB7"
		if got != want {
			t.Errorf("CreateHashData() = %q, want %q", got, want)
		}
	})
}

func TestCreate3DHash(t *testing.T) {
	cfg := testConfig()
	got, err := Create3DHash(ThreeDHashFields{
		TerminalID:   cfg.TerminalID,
		OrderID:      "ORDER3D",
		Amount:       "120",
		CurrencyCode: "949",
		SuccessURL:   "https://example.com/callback",
		ErrorURL:     "https://example.com/callback",
		Type:         "sales",
		Installment:  "",
		StoreKey:     cfg.StoreKey,
		SecurityData: testSecurityData,
	})
	if err != nil {
		t.Fatalf("Create3DHash() error = %v", err)
	}
	want := "88A915F7BF4F0BBE355EA7F34A7059D763A8406CDC8FDE9B2DF02C80BC1186F5C9E3FED0C322BE4E23466FED2067245BC7D002BADAE822AD3B28D1CAB3E6BB2D"
	if got != want {
		t.Errorf("Create3DHash() = %q, want %q", got, want)
	}
}

func TestVerifyCallback(t *testing.T) {
	storeKey := testConfig().StoreKey

	t.Run("accepts the reference 3D and 3D_PAY callbacks (SHA-512)", func(t *testing.T) {
		if !VerifyCallback(test3DCallback(), storeKey) {
			t.Error("VerifyCallback(3D) = false, want true")
		}
		if !VerifyCallback(test3DPayCallback(), storeKey) {
			t.Error("VerifyCallback(3D_PAY) = false, want true")
		}
	})

	t.Run("accepts a SHA-1 hash from older terminals", func(t *testing.T) {
		data := test3DCallback()
		data["hash"] = "41BD05D37CE5E07D8425746C61F59F7DF2DDE008"
		if !VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(SHA-1) = false, want true")
		}
	})

	t.Run("rebuilds the value from the fields, ignoring a forged hashparamsval", func(t *testing.T) {
		data := test3DCallback()
		data["oid"], data["orderid"] = "OTHER", "OTHER"
		if VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(changed order id) = true, want false")
		}
	})

	t.Run("rejects a tampered covered field", func(t *testing.T) {
		data := test3DCallback()
		data["mdstatus"] = "0"
		if VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(tampered mdstatus) = true, want false")
		}
	})

	t.Run("requires hashparams to cover the order id and mdstatus", func(t *testing.T) {
		data := test3DCallback()
		data["hashparams"] = "clientid:authcode:"
		if VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(short hashparams) = true, want false")
		}
	})

	t.Run("rejects a wrong store key or a missing hash", func(t *testing.T) {
		if VerifyCallback(test3DCallback(), "other") {
			t.Error("VerifyCallback(wrong store key) = true, want false")
		}
		data := test3DCallback()
		delete(data, "hash")
		if VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(no hash) = true, want false")
		}
	})

	t.Run("rejects a hashparamsval that does not match the rebuilt value", func(t *testing.T) {
		data := test3DCallback()
		data["hashparamsval"] = "30691297OTHER1CAVVVALUE=02MDVALUE123RNDVALUE"
		if VerifyCallback(data, storeKey) {
			t.Error("VerifyCallback(forged hashparamsval) = true, want false")
		}
	})
}

func TestFormatAmount(t *testing.T) {
	for _, tc := range []struct{ amount, want string }{
		{"1.2", "120"},
		{"11.22", "1122"},
		{"0.01", "1"},
	} {
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
	for _, tc := range []struct{ currency, want string }{
		{"", "949"},
		{"usd", "840"},
		{"JPY", "392"},
	} {
		got, err := CurrencyCode(tc.currency)
		if err != nil {
			t.Fatalf("CurrencyCode(%q) error = %v", tc.currency, err)
		}
		if got != tc.want {
			t.Errorf("CurrencyCode(%q) = %q, want %q", tc.currency, got, tc.want)
		}
	}
	if _, err := CurrencyCode("CHF"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("CurrencyCode(CHF) error = %v, want a not supported error", err)
	}
}

func TestFormatInstallment(t *testing.T) {
	for _, tc := range []struct {
		installment int
		want        string
	}{
		{0, ""},
		{1, ""},
		{3, "3"},
	} {
		got, err := FormatInstallment(tc.installment)
		if err != nil {
			t.Fatalf("FormatInstallment(%d) error = %v", tc.installment, err)
		}
		if got != tc.want {
			t.Errorf("FormatInstallment(%d) = %q, want %q", tc.installment, got, tc.want)
		}
	}
	if _, err := FormatInstallment(-1); err == nil {
		t.Error("FormatInstallment(-1) error = nil, want an error")
	}
}

func TestFormatExpiry(t *testing.T) {
	got, err := FormatExpiry("1", "2027")
	if err != nil {
		t.Fatalf("FormatExpiry() error = %v", err)
	}
	if got != (Expiry{Month: "01", Year: "27"}) {
		t.Errorf("FormatExpiry() = %+v", got)
	}
	if _, err := FormatExpiry("13", "27"); err == nil || !strings.Contains(err.Error(), "Invalid card expiry") {
		t.Errorf("FormatExpiry(13, 27) error = %v", err)
	}
}

func TestBuildXML(t *testing.T) {
	got := BuildXML([]XMLField{
		{Name: "Mode", Value: "TEST"},
		{Name: "Order", Children: []XMLField{{Name: "OrderID", Value: "A&B"}}},
	})
	want := `<?xml version="1.0" encoding="UTF-8"?><GVPSRequest><Mode>TEST</Mode>` +
		`<Order><OrderID>A&amp;B</OrderID></Order></GVPSRequest>`
	if got != want {
		t.Errorf("BuildXML() = %q, want %q", got, want)
	}
}

func TestParseXMLResponse(t *testing.T) {
	t.Run("separates the response, transaction and order inquiry fields", func(t *testing.T) {
		parsed, err := ParseXMLResponse(testApprovedXML)
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		expectFields(t, parsed.Response, map[string]string{"Source": "HOST", "Code": "00", "Message": "Approved"})
		expectFields(t, parsed.Transaction, map[string]string{"RetrefNum": "628408842186", "AuthCode": "405119"})
		if _, ok := parsed.Transaction["Code"]; ok {
			t.Error("transaction must not carry the response Code")
		}
		if !IsApproved(parsed) {
			t.Error("IsApproved() = false, want true")
		}
	})

	t.Run("keeps the inquiry result Code apart from the response Code", func(t *testing.T) {
		parsed, err := ParseXMLResponse(garantiInquiryXML([]xmlPair{
			{"Status", "DECLINED"}, {"Code", "53"}, {"ReasonCode", "53"},
		}))
		if err != nil {
			t.Fatalf("ParseXMLResponse() error = %v", err)
		}
		if parsed.Response["Code"] != "00" {
			t.Errorf("response Code = %q, want 00", parsed.Response["Code"])
		}
		expectFields(t, parsed.OrderInquiry, map[string]string{"Status": "DECLINED", "Code": "53"})
	})

	t.Run("fails on a body that is not a GVPSResponse", func(t *testing.T) {
		if _, err := ParseXMLResponse("<html/>"); err == nil || !strings.Contains(err.Error(), "unexpected response") {
			t.Errorf("ParseXMLResponse() error = %v", err)
		}
	})
}

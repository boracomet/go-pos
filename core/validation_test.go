package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// validationTestNow is the fixed clock the expiry cases are written against.
var validationTestNow = time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)

func validationTestCard() PaymentCard {
	return PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2099",
		CVC:            "123",
	}
}

func validationTestBuyer() Buyer {
	return Buyer{
		ID:                  "BY789",
		Name:                "John",
		Surname:             "Doe",
		Email:               "john.doe@example.com",
		IdentityNumber:      "74300864791",
		RegistrationAddress: "Nidakule Goztepe, Merdivenkoy Mah. Bora Sok. No:1",
		City:                "Istanbul",
		Country:             "Turkey",
		IP:                  "85.34.78.112",
		GSMNumber:           "+905350000000",
	}
}

func validationTestRequest() *PaymentRequest {
	card := validationTestCard()
	return &PaymentRequest{
		Price:       "1",
		PaidPrice:   "1.2",
		Currency:    string(CurrencyTRY),
		BasketID:    "B67832",
		PaymentCard: &card,
		Buyer:       validationTestBuyer(),
		ShippingAddress: Address{
			ContactName: "Jane Doe", City: "Istanbul", Country: "Turkey", Address: "Nidakule Goztepe",
		},
		BillingAddress: Address{
			ContactName: "John Doe", City: "Istanbul", Country: "Turkey", Address: "Nidakule Goztepe",
		},
		BasketItems: []BasketItem{
			{ID: "BI101", Name: "Binocular", Category1: "Collectibles", ItemType: string(BasketItemPhysical), Price: "0.3"},
			{ID: "BI102", Name: "Game code", Category1: "Game", ItemType: string(BasketItemVirtual), Price: "0.5"},
			{ID: "BI103", Name: "Usb", Category1: "Electronics", ItemType: string(BasketItemPhysical), Price: "0.2"},
		},
		ConversationID: "123456789",
	}
}

// validationTestIssues returns the paths of the issues a validation produced.
func validationTestIssues(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T (%v), want a ValidationError", err, err)
	}
	paths := make([]string, 0, len(validation.Issues))
	for _, issue := range validation.Issues {
		paths = append(paths, issue.Path)
	}
	return paths
}

func validationTestWantPaths(t *testing.T, err error, want []string) {
	t.Helper()
	got := validationTestIssues(t, err)
	if len(got) != len(want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("issues = %v, want %v", got, want)
		}
	}
}

func TestLuhn(t *testing.T) {
	cases := []struct {
		digits string
		want   bool
	}{
		{"5528790000000008", true},
		{"4111111111111111", true},
		{"4242424242424242", true},
		{"5555555555554444", true},
		{"4000000000000002", true},
		{"4111111111111112", false},
		{"5528790000000009", false},
		{"1234", false},
		{"0", true},
		{"00", true},
		{"18", true},
		{"411111111111111a", false},
		{"4111 1111 1111 1111", false},
	}
	for _, tc := range cases {
		t.Run(tc.digits, func(t *testing.T) {
			if got := Luhn(tc.digits); got != tc.want {
				t.Errorf("Luhn(%q) = %v, want %v", tc.digits, got, tc.want)
			}
		})
	}
}

func TestRequestValidatorCard(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*PaymentCard)
		wantPath string
	}{
		{"valid", func(*PaymentCard) {}, ""},
		{"spaces in the number", func(c *PaymentCard) { c.CardNumber = "5528 7900 0000 0008" }, ""},
		{"dashes in the number", func(c *PaymentCard) { c.CardNumber = "5528-7900-0000-0008" }, ""},
		{"too short", func(c *PaymentCard) { c.CardNumber = "1234" }, "paymentCard.cardNumber"},
		{"too long", func(c *PaymentCard) { c.CardNumber = "55287900000000081234" }, "paymentCard.cardNumber"},
		{"luhn failure", func(c *PaymentCard) { c.CardNumber = "5528790000000009" }, "paymentCard.cardNumber"},
		{"letters", func(c *PaymentCard) { c.CardNumber = "552879000000000a" }, "paymentCard.cardNumber"},
		{"empty number", func(c *PaymentCard) { c.CardNumber = "" }, "paymentCard.cardNumber"},
		{"blank holder", func(c *PaymentCard) { c.CardHolderName = " " }, "paymentCard.cardHolderName"},
		{"empty holder", func(c *PaymentCard) { c.CardHolderName = "" }, "paymentCard.cardHolderName"},
		{"month 13", func(c *PaymentCard) { c.ExpireMonth = "13" }, "paymentCard.expireMonth"},
		{"month 0", func(c *PaymentCard) { c.ExpireMonth = "0" }, "paymentCard.expireMonth"},
		{"month text", func(c *PaymentCard) { c.ExpireMonth = "ab" }, "paymentCard.expireMonth"},
		{"three digit year", func(c *PaymentCard) { c.ExpireYear = "203" }, "paymentCard.expireYear"},
		{"one digit year", func(c *PaymentCard) { c.ExpireYear = "9" }, "paymentCard.expireYear"},
		{"year text", func(c *PaymentCard) { c.ExpireYear = "abcd" }, "paymentCard.expireYear"},
		{"past month", func(c *PaymentCard) { c.ExpireMonth = "08"; c.ExpireYear = "2026" }, "paymentCard.expireYear"},
		{"past year", func(c *PaymentCard) { c.ExpireMonth = "12"; c.ExpireYear = "2025" }, "paymentCard.expireYear"},
		{"current month four digit year", func(c *PaymentCard) { c.ExpireMonth = "09"; c.ExpireYear = "2026" }, ""},
		{"current month two digit year", func(c *PaymentCard) { c.ExpireMonth = "09"; c.ExpireYear = "26" }, ""},
		{"next month two digit year", func(c *PaymentCard) { c.ExpireMonth = "10"; c.ExpireYear = "26" }, ""},
		{"short cvc", func(c *PaymentCard) { c.CVC = "12" }, "paymentCard.cvc"},
		{"long cvc", func(c *PaymentCard) { c.CVC = "12345" }, "paymentCard.cvc"},
		{"non numeric cvc", func(c *PaymentCard) { c.CVC = "12a" }, "paymentCard.cvc"},
		{"empty cvc", func(c *PaymentCard) { c.CVC = "" }, "paymentCard.cvc"},
		{"four digit cvc", func(c *PaymentCard) { c.CVC = "1234" }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := validationTestCard()
			tc.mutate(&card)
			v := &RequestValidator{}
			v.Card(&card, "paymentCard", validationTestNow)
			var want []string
			if tc.wantPath != "" {
				want = []string{tc.wantPath}
			}
			validationTestWantPaths(t, v.Assert("test-pos"), want)
		})
	}
}

func TestRequestValidatorCardValidThroughTheLastDayOfTheMonth(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"first day of the expiry month", time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC), true},
		{"last day of the expiry month", time.Date(2026, time.September, 30, 23, 59, 59, 0, time.UTC), true},
		{"first day of the next month", time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC), false},
		{"local time is normalized to UTC", time.Date(2026, time.September, 30, 23, 0, 0, 0, time.FixedZone("UTC+3", 3*3600)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := validationTestCard()
			card.ExpireMonth, card.ExpireYear = "09", "2026"
			v := &RequestValidator{}
			v.Card(&card, "paymentCard", tc.now)
			expired := len(v.Issues) > 0
			if expired == tc.want {
				t.Errorf("expired = %v at %s, want %v (issues %v)", expired, tc.now, !tc.want, v.Issues)
			}
		})
	}
}

func TestRequestValidatorCardRequiresACard(t *testing.T) {
	v := &RequestValidator{}
	v.Card(nil, "", validationTestNow)
	validationTestWantPaths(t, v.Assert("test-pos"), []string{"paymentCard"})
}

func TestRequestValidatorAmount(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		required bool
		want     int64
		wantOK   bool
		wantPath string
	}{
		{"integer", "10", true, 1000, true, ""},
		{"one decimal", "10.5", true, 1050, true, ""},
		{"two decimals", "10.55", true, 1055, true, ""},
		{"trimmed", "  10.55  ", true, 1055, true, ""},
		{"zero is not positive", "0", true, 0, true, "price"},
		{"blank required", "", true, 0, false, "price"},
		{"blank optional", "", false, 0, false, ""},
		{"whitespace optional", "   ", false, 0, false, ""},
		{"three decimals", "10.555", true, 0, false, "price"},
		{"negative", "-1", true, 0, false, "price"},
		{"text", "abc", true, 0, false, "price"},
		{"comma separator", "10,55", true, 0, false, "price"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &RequestValidator{}
			got, ok := v.Amount(tc.value, "price", tc.required)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("Amount(%q) = (%d, %v), want (%d, %v)", tc.value, got, ok, tc.want, tc.wantOK)
			}
			var want []string
			if tc.wantPath != "" {
				want = []string{tc.wantPath}
			}
			validationTestWantPaths(t, v.Assert("test-pos"), want)
		})
	}
}

// validationTestContact is reached through an embedded pointer.
type validationTestContact struct {
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type validationTestAccount struct {
	*validationTestContact
	Name string `json:"name"`
}

type validationTestEnvelope struct {
	Account  *validationTestAccount `json:"account"`
	Untagged string
}

func TestRequestValidatorRequiredResolvesDottedPaths(t *testing.T) {
	envelope := validationTestEnvelope{
		Account: &validationTestAccount{
			validationTestContact: &validationTestContact{Email: "a@b.com", Phone: "555"},
			Name:                  "John",
		},
	}
	v := &RequestValidator{}
	v.Required(envelope, []string{"account.email", "account.name", "account.phone"})
	if err := v.Assert("test-pos"); err != nil {
		t.Errorf("Assert = %v, want no issues", err)
	}

	// A pointer to the source works the same way.
	v = &RequestValidator{}
	v.Required(&envelope, []string{"account.email"})
	if err := v.Assert("test-pos"); err != nil {
		t.Errorf("Assert = %v, want no issues", err)
	}
}

func TestRequestValidatorRequiredReportsMissingPaths(t *testing.T) {
	cases := []struct {
		name   string
		source any
		paths  []string
		want   []string
	}{
		{
			name:   "missing leaf",
			source: validationTestEnvelope{Account: &validationTestAccount{Name: "John"}},
			paths:  []string{"account.email"},
			want:   []string{"account.email"},
		},
		{
			name:   "nil embedded pointer",
			source: validationTestEnvelope{Account: &validationTestAccount{Name: "John"}},
			paths:  []string{"account.phone", "account.name"},
			want:   []string{"account.phone"},
		},
		{
			name:   "nil pointer field",
			source: validationTestEnvelope{},
			paths:  []string{"account.email"},
			want:   []string{"account.email"},
		},
		{
			name:   "unknown path",
			source: validationTestEnvelope{},
			paths:  []string{"account.unknown", "missing"},
			want:   []string{"account.unknown", "missing"},
		},
		{
			name:   "blank value counts as missing",
			source: validationTestEnvelope{Account: &validationTestAccount{Name: "   "}},
			paths:  []string{"account.name"},
			want:   []string{"account.name"},
		},
		{
			name:   "fields are matched by json tag, not by Go name",
			source: validationTestEnvelope{Untagged: "set"},
			paths:  []string{"Untagged"},
			want:   []string{"Untagged"},
		},
		{
			name:   "map source",
			source: map[string]any{"buyer": map[string]any{"email": "a@b.com"}},
			paths:  []string{"buyer.email", "buyer.id"},
			want:   []string{"buyer.id"},
		},
		{
			name:   "nil source",
			source: nil,
			paths:  []string{"buyer.email"},
			want:   []string{"buyer.email"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &RequestValidator{}
			v.Required(tc.source, tc.paths)
			validationTestWantPaths(t, v.Assert("test-pos"), tc.want)
		})
	}
}

func TestRequestValidatorRequiredThroughEmbeddedPaymentRequest(t *testing.T) {
	request := ThreeDSPaymentRequest{PaymentRequest: PaymentRequest{CallbackURL: "https://example.com/cb"}}
	v := &RequestValidator{}
	v.Required(request, []string{"callbackUrl", "price"})
	validationTestWantPaths(t, v.Assert("test-pos"), []string{"price"})

	v = &RequestValidator{}
	v.Required(&request, []string{"callbackUrl"})
	if err := v.Assert("test-pos"); err != nil {
		t.Errorf("Assert = %v, want no issues", err)
	}
}

func TestRequestValidatorBuyerFormats(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Buyer)
		want   []string
	}{
		{"valid fixture", func(*Buyer) {}, nil},
		{"empty values are skipped", func(b *Buyer) { b.Email, b.IP, b.GSMNumber = "", "", "" }, nil},
		{"bad email", func(b *Buyer) { b.Email = "nope" }, []string{"buyer.email"}},
		{"email without a domain dot", func(b *Buyer) { b.Email = "a@b" }, []string{"buyer.email"}},
		{"email with a trailing dot", func(b *Buyer) { b.Email = "a@b." }, []string{"buyer.email"}},
		{"email with a leading domain dot", func(b *Buyer) { b.Email = "a@.b" }, []string{"buyer.email"}},
		{"email with an empty label", func(b *Buyer) { b.Email = "a@b..c" }, []string{"buyer.email"}},
		{"email with spaces", func(b *Buyer) { b.Email = "a b@c.com" }, []string{"buyer.email"}},
		{"email with two at signs", func(b *Buyer) { b.Email = "a@@c.com" }, []string{"buyer.email"}},
		{"subdomain email", func(b *Buyer) { b.Email = "first.last@mail.example.com.tr" }, nil},
		{"bad ipv4", func(b *Buyer) { b.IP = "999.1.1.1" }, []string{"buyer.ip"}},
		{"ipv4 text", func(b *Buyer) { b.IP = "not-an-ip" }, []string{"buyer.ip"}},
		{"ipv4 with a port", func(b *Buyer) { b.IP = "1.1.1.1:80" }, []string{"buyer.ip"}},
		{"ipv6", func(b *Buyer) { b.IP = "2001:db8::1" }, nil},
		{"ipv6 with a zone", func(b *Buyer) { b.IP = "fe80::1%eth0" }, nil},
		{"short gsm", func(b *Buyer) { b.GSMNumber = "12" }, []string{"buyer.gsmNumber"}},
		{"gsm with letters", func(b *Buyer) { b.GSMNumber = "555abc5555" }, []string{"buyer.gsmNumber"}},
		{"gsm with separators", func(b *Buyer) { b.GSMNumber = "+90 (535) 000-00-00" }, nil},
		{"ten digit gsm", func(b *Buyer) { b.GSMNumber = "5350000000" }, nil},
		{"all three invalid", func(b *Buyer) { b.Email, b.IP, b.GSMNumber = "nope", "999.1.1.1", "12" },
			[]string{"buyer.email", "buyer.ip", "buyer.gsmNumber"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buyer := validationTestBuyer()
			tc.mutate(&buyer)
			v := &RequestValidator{}
			v.BuyerFormats(buyer)
			validationTestWantPaths(t, v.Assert("test-pos"), tc.want)
		})
	}
}

func TestRequestValidatorEmailPatternIsLinear(t *testing.T) {
	// A pathological domain must not make the pattern backtrack.
	hostile := "!@!." + strings.Repeat("!.", 1000)
	buyer := validationTestBuyer()
	buyer.Email = hostile
	v := &RequestValidator{}
	v.BuyerFormats(buyer)
	validationTestWantPaths(t, v.Assert("test-pos"), []string{"buyer.email"})
}

func TestRequestValidatorAssert(t *testing.T) {
	v := &RequestValidator{}
	if err := v.Assert("test-pos"); err != nil {
		t.Errorf("Assert with no issues = %v, want nil", err)
	}

	v.Add("price", "is required").Add("buyer.email", "is not a valid email address")
	err := v.Assert("test-pos")
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Assert = %T, want a ValidationError", err)
	}
	if validation.Field() != "price" {
		t.Errorf("Field() = %q, want price", validation.Field())
	}
	if validation.Provider != "test-pos" {
		t.Errorf("Provider = %q, want test-pos", validation.Provider)
	}
	if validation.Code != "VALIDATION_ERROR" {
		t.Errorf("Code = %q, want VALIDATION_ERROR", validation.Code)
	}
	for _, want := range []string{"price is required", "buyer.email is not a valid email address"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
	if len(validation.Issues) != 2 {
		t.Errorf("Issues = %v, want 2 entries", validation.Issues)
	}
}

func TestValidatePaymentRequestAcceptsTheFixtureWithEveryRule(t *testing.T) {
	rules := PaymentValidationRules{
		Card:               true,
		BasketRequired:     true,
		BasketMatchesPrice: true,
		Required:           []string{"buyer.email", "buyer.identityNumber", "billingAddress.city"},
	}
	if err := ValidatePaymentRequest(validationTestRequest(), rules, "test-pos"); err != nil {
		t.Errorf("ValidatePaymentRequest = %v, want no issues", err)
	}
}

func TestValidatePaymentRequestChecksAmounts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PaymentRequest)
		want   []string
	}{
		{"zero price", func(r *PaymentRequest) { r.Price = "0" }, []string{"price"}},
		{"three decimals", func(r *PaymentRequest) { r.Price = "10.555" }, []string{"price"}},
		{"negative paid price", func(r *PaymentRequest) { r.PaidPrice = "-1" }, []string{"paidPrice"}},
		{"missing price", func(r *PaymentRequest) { r.Price = "" }, []string{"price"}},
		{"missing paid price is allowed", func(r *PaymentRequest) { r.PaidPrice = "" }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validationTestRequest()
			tc.mutate(request)
			validationTestWantPaths(t, ValidatePaymentRequest(request, PaymentValidationRules{}, "test-pos"), tc.want)
		})
	}
}

func TestValidatePaymentRequestBasketRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PaymentRequest)
		rules  PaymentValidationRules
		want   []string
	}{
		{
			name:   "total matches without the rule",
			mutate: func(r *PaymentRequest) { r.Price, r.PaidPrice = "2", "2" },
			rules:  PaymentValidationRules{},
			want:   nil,
		},
		{
			name:   "total does not match",
			mutate: func(r *PaymentRequest) { r.Price, r.PaidPrice = "2", "2" },
			rules:  PaymentValidationRules{BasketMatchesPrice: true},
			want:   []string{"basketItems"},
		},
		{
			name:   "total matches",
			mutate: func(r *PaymentRequest) {},
			rules:  PaymentValidationRules{BasketMatchesPrice: true},
			want:   nil,
		},
		{
			name:   "basket required and empty",
			mutate: func(r *PaymentRequest) { r.BasketItems = nil },
			rules:  PaymentValidationRules{BasketRequired: true},
			want:   []string{"basketItems"},
		},
		{
			name:   "basket not required and empty",
			mutate: func(r *PaymentRequest) { r.BasketItems = nil },
			rules:  PaymentValidationRules{},
			want:   nil,
		},
		{
			name: "item price is invalid",
			mutate: func(r *PaymentRequest) {
				r.BasketItems[0].Price = "x"
			},
			rules: PaymentValidationRules{BasketMatchesPrice: true},
			want:  []string{"basketItems[0].price"},
		},
		{
			name:   "item price is zero",
			mutate: func(r *PaymentRequest) { r.BasketItems[0].Price = "0" },
			rules:  PaymentValidationRules{BasketMatchesPrice: true},
			want:   []string{"basketItems[0].price"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validationTestRequest()
			tc.mutate(request)
			validationTestWantPaths(t, ValidatePaymentRequest(request, tc.rules, "test-pos"), tc.want)
		})
	}
}

func TestValidatePaymentRequestBasketMismatchMessage(t *testing.T) {
	request := validationTestRequest()
	request.Price, request.PaidPrice = "2", "2"
	err := ValidatePaymentRequest(request, PaymentValidationRules{BasketMatchesPrice: true}, "test-pos")
	if err == nil {
		t.Fatal("want a basket mismatch")
	}
	for _, want := range []string{"basketItems", "1.00", "2.00"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestValidatePaymentRequestRequiredAndBuyerFormats(t *testing.T) {
	request := validationTestRequest()
	request.Buyer.Email, request.Buyer.IP, request.Buyer.GSMNumber = "nope", "999.1.1.1", "12"
	rules := PaymentValidationRules{Required: []string{"buyer.identityNumber"}}
	validationTestWantPaths(t, ValidatePaymentRequest(request, rules, "test-pos"),
		[]string{"buyer.email", "buyer.ip", "buyer.gsmNumber"})

	request = validationTestRequest()
	request.Buyer.IdentityNumber = ""
	validationTestWantPaths(t, ValidatePaymentRequest(request, rules, "test-pos"), []string{"buyer.identityNumber"})
}

func TestValidatePaymentRequestStoredCard(t *testing.T) {
	cases := []struct {
		name   string
		stored *StoredCardReference
		rules  PaymentValidationRules
		want   []string
	}{
		{
			name:   "unsupported provider",
			stored: &StoredCardReference{CustomerToken: "C1", CardToken: "T1"},
			rules:  PaymentValidationRules{Card: true},
			want:   []string{"storedCard"},
		},
		{
			name:   "supported provider",
			stored: &StoredCardReference{CustomerToken: "C1", CardToken: "T1"},
			rules:  PaymentValidationRules{Card: true, StoredCard: true},
			want:   nil,
		},
		{
			name:   "missing tokens",
			stored: &StoredCardReference{},
			rules:  PaymentValidationRules{StoredCard: true},
			want:   []string{"storedCard.customerToken", "storedCard.cardToken"},
		},
		{
			name:   "stored card replaces the card check",
			stored: &StoredCardReference{CustomerToken: "C1", CardToken: "T1"},
			rules:  PaymentValidationRules{Card: true, StoredCard: true},
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validationTestRequest()
			request.PaymentCard = nil
			request.StoredCard = tc.stored
			validationTestWantPaths(t, ValidatePaymentRequest(request, tc.rules, "test-pos"), tc.want)
		})
	}
}

func TestValidatePaymentRequestCardRules(t *testing.T) {
	request := validationTestRequest()
	request.PaymentCard.CardNumber = "5528790000000009"
	validationTestWantPaths(t, ValidatePaymentRequest(request, PaymentValidationRules{Card: true}, "test-pos"),
		[]string{"paymentCard.cardNumber"})

	// Without the card rule the number is not checked.
	if err := ValidatePaymentRequest(request, PaymentValidationRules{}, "test-pos"); err != nil {
		t.Errorf("ValidatePaymentRequest without the card rule = %v, want no issues", err)
	}

	request = validationTestRequest()
	request.PaymentCard = nil
	validationTestWantPaths(t, ValidatePaymentRequest(request, PaymentValidationRules{Card: true}, "test-pos"),
		[]string{"paymentCard"})
}

func TestValidatePaymentRequestSaveCard(t *testing.T) {
	request := validationTestRequest()
	request.SaveCard = &SaveCardOption{Enabled: true}
	validationTestWantPaths(t, ValidatePaymentRequest(request, PaymentValidationRules{Card: true}, "test-pos"),
		[]string{"saveCard"})

	if err := ValidatePaymentRequest(request, PaymentValidationRules{Card: true, SaveCard: true}, "test-pos"); err != nil {
		t.Errorf("ValidatePaymentRequest with saveCard support = %v, want no issues", err)
	}

	request.SaveCard = &SaveCardOption{Enabled: false}
	if err := ValidatePaymentRequest(request, PaymentValidationRules{Card: true}, "test-pos"); err != nil {
		t.Errorf("ValidatePaymentRequest with saveCard disabled = %v, want no issues", err)
	}
}

func TestValidatePaymentRequestRequiresARequest(t *testing.T) {
	validationTestWantPaths(t, ValidatePaymentRequest(nil, PaymentValidationRules{}, "test-pos"), []string{"request"})
}

func TestValidatePaymentRequestReportsEveryIssue(t *testing.T) {
	request := validationTestRequest()
	request.Price = "x"
	err := ValidatePaymentRequest(request, PaymentValidationRules{
		Card:     true,
		Required: []string{"buyer.foo"},
	}, "test-pos")

	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T, want a ValidationError", err)
	}
	if validation.Field() != "price" {
		t.Errorf("Field() = %q, want price", validation.Field())
	}
	for _, want := range []string{"price must be a decimal amount", "buyer.foo is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestValidateRefundAmount(t *testing.T) {
	cases := []struct {
		price string
		want  []string
	}{
		{"0.5", nil},
		{"1", nil},
		{"1.00", nil},
		{"", []string{"price"}},
		{"-5", []string{"price"}},
		{"0", []string{"price"}},
		{"abc", []string{"price"}},
	}
	for _, tc := range cases {
		t.Run(tc.price, func(t *testing.T) {
			validationTestWantPaths(t, ValidateRefundAmount(tc.price, "test-pos"), tc.want)
		})
	}
}

func TestValidateCaptureRequest(t *testing.T) {
	cases := []struct {
		name    string
		request *CaptureRequest
		want    []string
	}{
		{"valid", &CaptureRequest{PaymentID: "P1", Amount: "1.00"}, nil},
		{"nil", nil, []string{"request"}},
		{"missing payment id", &CaptureRequest{Amount: "1.00"}, []string{"paymentId"}},
		{"missing amount", &CaptureRequest{PaymentID: "P1"}, []string{"amount"}},
		{"zero amount", &CaptureRequest{PaymentID: "P1", Amount: "0"}, []string{"amount"}},
		{"both missing", &CaptureRequest{}, []string{"paymentId", "amount"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validationTestWantPaths(t, ValidateCaptureRequest(tc.request, "test-pos"), tc.want)
		})
	}
}

func TestBaseProviderValidationCanBeDisabled(t *testing.T) {
	request := validationTestRequest()
	request.Price = "not-an-amount"

	enabled := NewBaseProvider("test-pos", ProviderConfig{})
	if err := enabled.ValidatePayment(request, PaymentValidationRules{}); err == nil {
		t.Error("ValidatePayment with validation enabled: want an error")
	}

	disabled := NewBaseProvider("test-pos", ProviderConfig{Validate: coreTestPtr(false)})
	if err := disabled.ValidatePayment(request, PaymentValidationRules{}); err != nil {
		t.Errorf("ValidatePayment with validation disabled = %v, want nil", err)
	}
	if err := disabled.ValidateRefund(&RefundRequest{Price: "-1"}); err != nil {
		t.Errorf("ValidateRefund with validation disabled = %v, want nil", err)
	}
	if err := disabled.ValidateCapture(&CaptureRequest{}); err != nil {
		t.Errorf("ValidateCapture with validation disabled = %v, want nil", err)
	}
	if !enabled.ValidationEnabled() {
		t.Error("ValidationEnabled() = false, want true by default")
	}
	if disabled.ValidationEnabled() {
		t.Error("ValidationEnabled() = true, want false")
	}
}

func TestBaseProviderCardOf(t *testing.T) {
	base := NewBaseProvider("test-pos", ProviderConfig{})
	request := validationTestRequest()
	card, err := base.CardOf(request)
	if err != nil {
		t.Fatalf("CardOf: %v", err)
	}
	if card != request.PaymentCard {
		t.Error("CardOf must return the request's card")
	}

	for _, missing := range []*PaymentRequest{nil, {}} {
		_, err := base.CardOf(missing)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("CardOf(%v) error = %T, want a ValidationError", missing, err)
		}
		if validation.Field() != "paymentCard" {
			t.Errorf("Field() = %q, want paymentCard", validation.Field())
		}
	}
}

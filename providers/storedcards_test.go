package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/providers/paytr"
	fixtures "github.com/boracomet/go-pos/testing"
)

// Card storage is the most unevenly supported feature across the gateways: only
// one stores cards on its own, two can list and delete them, and two can charge
// a card saved earlier. Everything else inherits the base provider's refusal.
var (
	saveCardSupport          = map[string]bool{"iyzico": true}
	cardListSupport          = map[string]bool{"iyzico": true, "paytr": true}
	storedCardPaymentSupport = map[string]bool{"iyzico": true, "paytr": true}
)

func TestSaveCardAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.SaveCard(context.Background(), saveCardRequest())

			if !saveCardSupport[name] {
				wantNotSupported(t, name, "SaveCard", err)
				if result != nil {
					t.Errorf("SaveCard() result = %+v, want nil", result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("SaveCard() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("SaveCard() = %+v, want success", result)
			}
			if result.CustomerToken == "" {
				t.Error("SaveCard() returned no customer token")
			}
			if result.Card == nil || result.Card.CardToken == "" {
				t.Errorf("SaveCard() card = %+v, want a stored card with a token", result.Card)
			}
			if g.count() == 0 {
				t.Error("SaveCard() sent no request")
			}
		})
	}
}

func TestListCardsAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.ListCards(context.Background(), listCardsRequest())

			if !cardListSupport[name] {
				wantNotSupported(t, name, "ListCards", err)
				if result != nil {
					t.Errorf("ListCards() result = %+v, want nil", result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("ListCards() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("ListCards() = %+v, want success", result)
			}
			if result.CustomerToken == "" {
				t.Error("ListCards() returned no customer token")
			}
			if len(result.Cards) == 0 {
				t.Fatal("ListCards() returned no cards")
			}
			if result.Cards[0].CardToken == "" {
				t.Errorf("ListCards() card = %+v, want a card token", result.Cards[0])
			}
			if g.count() == 0 {
				t.Error("ListCards() sent no request")
			}
		})
	}
}

func TestDeleteCardAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			result, err := g.provider.DeleteCard(context.Background(), deleteCardRequest())

			if !cardListSupport[name] {
				wantNotSupported(t, name, "DeleteCard", err)
				if result != nil {
					t.Errorf("DeleteCard() result = %+v, want nil", result)
				}
				wantNoCalls(t, g)
				return
			}
			if err != nil {
				t.Fatalf("DeleteCard() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("DeleteCard() = %+v, want success", result)
			}
			if g.count() == 0 {
				t.Error("DeleteCard() sent no request")
			}
		})
	}
}

// TestStoredCardPaymentAcrossProviders charges a card saved earlier. A gateway
// that cannot do it must refuse before sending anything to the bank.
func TestStoredCardPaymentAcrossProviders(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			g := newProviderGateway(t, name, approve(name))
			request := orderWithoutCard()
			request.StoredCard = storedCardReference()

			result, err := g.provider.CreatePayment(context.Background(), request)
			switch {
			case storedCardPaymentSupport[name]:
				if err != nil {
					t.Fatalf("CreatePayment() error = %v", err)
				}
				if result.Status != core.StatusSuccess {
					t.Fatalf("CreatePayment() = %+v, want success", result)
				}
				if g.count() == 0 {
					t.Error("CreatePayment() sent no request")
				}
			case name == "kuveytturk":
				// This gateway only processes 3D Secure payments, so it refuses
				// every direct payment before it looks at the card. Its message
				// names the 3D entry point rather than the words "not supported".
				var base *core.BaseError
				if !errors.As(err, &base) {
					t.Fatalf("CreatePayment() error = %v, want a NOT_SUPPORTED error", err)
				}
				if base.Code != "NOT_SUPPORTED" || base.Provider != name {
					t.Errorf("error = %+v, want NOT_SUPPORTED from %s", base, name)
				}
				if result != nil {
					t.Errorf("CreatePayment() result = %+v, want nil", result)
				}
				wantNoCalls(t, g)
			default:
				wantStoredCardRejected(t, name, result, err)
				wantNoCalls(t, g)
			}
		})
	}
}

// wantStoredCardRejected asserts a gateway refused a stored card locally: a
// FAILURE result naming the field, not a call to the bank.
func wantStoredCardRejected(t *testing.T, name string, result *core.PaymentResponse, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: CreatePayment() error = %v, want a failure result", name, err)
	}
	if result == nil {
		t.Fatalf("%s: CreatePayment() returned no result", name)
	}
	wantValidationFailure(t, result.Status, result.Code, result.ErrorMessage, "storedCard")
}

// TestIyzicoStoredCardPaymentSendsOnlyTokens checks that a stored card never
// puts card data on the wire: the two tokens replace the whole card object.
func TestIyzicoStoredCardPaymentSendsOnlyTokens(t *testing.T) {
	g := newProviderGateway(t, "iyzico", approve("iyzico"))
	request := orderWithoutCard()
	request.StoredCard = storedCardReference()

	result, err := g.provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("CreatePayment() = %+v, want success", result)
	}

	sent := g.last(t)
	if sent.Path != "/payment/auth" {
		t.Errorf("path = %q, want /payment/auth", sent.Path)
	}
	card, ok := sent.jsonBody(t)["paymentCard"].(map[string]any)
	if !ok {
		t.Fatalf("paymentCard is missing from %s", sent.Body)
	}
	if len(card) != 2 {
		t.Errorf("paymentCard = %v, want only cardUserKey and cardToken", card)
	}
	if card["cardUserKey"] != "USERKEY" {
		t.Errorf("cardUserKey = %v, want USERKEY", card["cardUserKey"])
	}
	if card["cardToken"] != "CARDTOKEN" {
		t.Errorf("cardToken = %v, want CARDTOKEN", card["cardToken"])
	}
}

// TestIyzicoSaveCardDuringPaymentReturnsTokens covers both ways of asking for a
// card to be stored while it is charged.
func TestIyzicoSaveCardDuringPaymentReturnsTokens(t *testing.T) {
	tests := []struct {
		name        string
		option      core.SaveCardOption
		wantUserKey string
	}{
		{
			name:   "new customer",
			option: core.SaveCardOption{Enabled: true, Alias: "My card"},
		},
		{
			name:        "existing customer",
			option:      core.SaveCardOption{Enabled: true, CustomerToken: "U1", Alias: "My card"},
			wantUserKey: "U1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newProviderGateway(t, "iyzico", approve("iyzico"))
			request := orderRequest()
			request.SaveCard = &test.option

			result, err := g.provider.CreatePayment(context.Background(), request)
			if err != nil {
				t.Fatalf("CreatePayment() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("CreatePayment() = %+v, want success", result)
			}
			if result.StoredCard == nil {
				t.Fatal("CreatePayment() returned no stored card tokens")
			}
			if result.StoredCard.CustomerToken != "U1" || result.StoredCard.CardToken != "C1" {
				t.Errorf("storedCard = %+v, want {U1 C1}", result.StoredCard)
			}

			card, ok := g.last(t).jsonBody(t)["paymentCard"].(map[string]any)
			if !ok {
				t.Fatal("paymentCard is missing from the request")
			}
			if card["registerCard"] != float64(1) {
				t.Errorf("registerCard = %v, want 1", card["registerCard"])
			}
			if card["cardAlias"] != "My card" {
				t.Errorf("cardAlias = %v, want My card", card["cardAlias"])
			}
			if card["cardNumber"] != fixtures.FixtureCard().CardNumber {
				t.Errorf("cardNumber = %v, want the card being stored", card["cardNumber"])
			}
			if test.wantUserKey == "" {
				if _, present := card["cardUserKey"]; present {
					t.Errorf("cardUserKey = %v, want it absent for a new customer", card["cardUserKey"])
				}
			} else if card["cardUserKey"] != test.wantUserKey {
				t.Errorf("cardUserKey = %v, want %q", card["cardUserKey"], test.wantUserKey)
			}
		})
	}
}

// TestIyzicoCardStorageEndpoints walks the three card storage calls and the
// normalized card they return.
func TestIyzicoCardStorageEndpoints(t *testing.T) {
	ctx := context.Background()

	t.Run("save", func(t *testing.T) {
		g := newProviderGateway(t, "iyzico", approve("iyzico"))
		result, err := g.provider.SaveCard(ctx, saveCardRequest())
		if err != nil {
			t.Fatalf("SaveCard() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.CustomerToken != "U1" {
			t.Fatalf("SaveCard() = %+v, want success with customer token U1", result)
		}
		if result.Card == nil {
			t.Fatal("SaveCard() returned no card")
		}
		want := core.StoredCard{
			CardToken: "C1", Alias: "Work", BinNumber: "552879",
			LastFourDigits: "0008", BankName: "Halkbank",
		}
		if *result.Card != want {
			t.Errorf("card = %+v, want %+v", *result.Card, want)
		}

		sent := g.last(t)
		if sent.Method != http.MethodPost || sent.Path != "/cardstorage/card" {
			t.Errorf("request = %s %s, want POST /cardstorage/card", sent.Method, sent.Path)
		}
		body := sent.jsonBody(t)
		if body["locale"] != "tr" {
			t.Errorf("locale = %v, want tr", body["locale"])
		}
		if body["email"] != "john.doe@example.com" {
			t.Errorf("email = %v, want the buyer's address", body["email"])
		}
		if _, present := body["cardUserKey"]; present {
			t.Errorf("cardUserKey = %v, want it absent for a new customer", body["cardUserKey"])
		}
		card, ok := body["card"].(map[string]any)
		if !ok {
			t.Fatalf("card is missing from %s", sent.Body)
		}
		if card["cardAlias"] != "Work" {
			t.Errorf("cardAlias = %v, want Work", card["cardAlias"])
		}
		if card["cardNumber"] != fixtures.FixtureCard().CardNumber {
			t.Errorf("cardNumber = %v, want the fixture card", card["cardNumber"])
		}
		if card["expireMonth"] != "12" || card["expireYear"] != "2030" {
			t.Errorf("expiry = %v/%v, want 12/2030", card["expireMonth"], card["expireYear"])
		}
	})

	t.Run("list", func(t *testing.T) {
		g := newProviderGateway(t, "iyzico", approve("iyzico"))
		result, err := g.provider.ListCards(ctx, listCardsRequest())
		if err != nil {
			t.Fatalf("ListCards() error = %v", err)
		}
		if result.Status != core.StatusSuccess || result.CustomerToken != "U1" {
			t.Fatalf("ListCards() = %+v, want success with customer token U1", result)
		}
		if len(result.Cards) != 1 {
			t.Fatalf("ListCards() returned %d cards, want 1", len(result.Cards))
		}
		card := result.Cards[0]
		if card.CardToken != "C1" || card.Alias != "Work" || card.BankName != "Halkbank" {
			t.Errorf("card = %+v, want the stored card's details", card)
		}
		if card.LastFourDigits != "0008" {
			t.Errorf("lastFourDigits = %q, want 0008", card.LastFourDigits)
		}

		sent := g.last(t)
		if sent.Method != http.MethodPost || sent.Path != "/cardstorage/cards" {
			t.Errorf("request = %s %s, want POST /cardstorage/cards", sent.Method, sent.Path)
		}
		if body := sent.jsonBody(t); body["cardUserKey"] != "U1" {
			t.Errorf("cardUserKey = %v, want U1", body["cardUserKey"])
		}
	})

	t.Run("delete", func(t *testing.T) {
		g := newProviderGateway(t, "iyzico", approve("iyzico"))
		result, err := g.provider.DeleteCard(ctx, deleteCardRequest())
		if err != nil {
			t.Fatalf("DeleteCard() error = %v", err)
		}
		if result.Status != core.StatusSuccess {
			t.Fatalf("DeleteCard() = %+v, want success", result)
		}

		sent := g.last(t)
		if sent.Method != http.MethodDelete || sent.Path != "/cardstorage/card" {
			t.Errorf("request = %s %s, want DELETE /cardstorage/card", sent.Method, sent.Path)
		}
		body := sent.jsonBody(t)
		if body["cardUserKey"] != "U1" || body["cardToken"] != "C1" {
			t.Errorf("body = %v, want both tokens", body)
		}
	})
}

// TestIyzicoStoredCardRequiresBothTokens checks that an incomplete reference is
// rejected locally, naming the missing field.
func TestIyzicoStoredCardRequiresBothTokens(t *testing.T) {
	for _, field := range []string{"customerToken", "cardToken"} {
		t.Run(field, func(t *testing.T) {
			g := newProviderGateway(t, "iyzico", approve("iyzico"))
			request := orderWithoutCard()
			request.StoredCard = storedCardReference()
			if field == "customerToken" {
				request.StoredCard.CustomerToken = ""
			} else {
				request.StoredCard.CardToken = ""
			}

			result, err := g.provider.CreatePayment(context.Background(), request)
			if err != nil {
				t.Fatalf("CreatePayment() error = %v", err)
			}
			wantValidationFailure(t, result.Status, result.Code, result.ErrorMessage, "storedCard."+field)
			wantNoCalls(t, g)
		})
	}
}

// TestPayTRListCardsSignsToken checks the card list signature: the customer
// token followed by the merchant salt, HMAC-SHA256 with the merchant key.
func TestPayTRListCardsSignsToken(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))
	request := listCardsRequest()

	result, err := g.provider.ListCards(context.Background(), request)
	if err != nil {
		t.Fatalf("ListCards() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("ListCards() = %+v, want success", result)
	}
	if result.CustomerToken != request.CustomerToken {
		t.Errorf("customerToken = %q, want %q", result.CustomerToken, request.CustomerToken)
	}
	if len(result.Cards) != 1 {
		t.Fatalf("ListCards() returned %d cards, want 1", len(result.Cards))
	}
	card := result.Cards[0]
	if card.CardToken != "CT1" || card.LastFourDigits != "4358" {
		t.Errorf("card = %+v, want token CT1 ending 4358", card)
	}
	if card.ExpireMonth != "12" || card.ExpireYear != "30" {
		t.Errorf("expiry = %s/%s, want 12/30", card.ExpireMonth, card.ExpireYear)
	}
	if card.BankName != "Axess" || card.CardFamily != "axess" || card.CardAssociation != "VISA" {
		t.Errorf("card = %+v, want the issuer details", card)
	}
	if card.RequiresCVC == nil || !*card.RequiresCVC {
		t.Errorf("requiresCvc = %v, want true", card.RequiresCVC)
	}

	sent := g.last(t)
	if sent.Method != http.MethodPost || sent.Path != "/odeme/capi/list" {
		t.Errorf("request = %s %s, want POST /odeme/capi/list", sent.Method, sent.Path)
	}
	form := sent.formBody(t)
	if form["merchant_id"] != paytrTestMerchantID {
		t.Errorf("merchant_id = %q, want %q", form["merchant_id"], paytrTestMerchantID)
	}
	if form["utoken"] != request.CustomerToken {
		t.Errorf("utoken = %q, want %q", form["utoken"], request.CustomerToken)
	}
	wantToken, err := paytr.Sign(request.CustomerToken+paytrTestSalt, paytrTestKey)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if form["paytr_token"] != wantToken {
		t.Errorf("paytr_token = %q, want %q", form["paytr_token"], wantToken)
	}
}

// TestPayTRListCardsReportsError covers the error object the API answers with
// instead of an array: it must not be read as an empty card list.
func TestPayTRListCardsReportsError(t *testing.T) {
	g := newProviderGateway(t, "paytr", func(call) (int, string) {
		return http.StatusOK, `{"status":"error","err_no":"1","err_msg":"utoken gecersiz"}`
	})

	result, err := g.provider.ListCards(context.Background(), listCardsRequest())
	if err != nil {
		t.Fatalf("ListCards() error = %v", err)
	}
	if result.Status != core.StatusFailure {
		t.Fatalf("ListCards() = %+v, want failure", result)
	}
	if len(result.Cards) != 0 {
		t.Errorf("ListCards() returned %d cards, want none", len(result.Cards))
	}
	if result.ErrorMessage != "utoken gecersiz" {
		t.Errorf("errorMessage = %q, want the provider's message", result.ErrorMessage)
	}
	if result.CustomerToken != "U1" {
		t.Errorf("customerToken = %q, want U1", result.CustomerToken)
	}
}

// TestPayTRDeleteCardSignsToken checks the delete signature: card token,
// customer token, then the merchant salt.
func TestPayTRDeleteCardSignsToken(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))
	request := deleteCardRequest()

	result, err := g.provider.DeleteCard(context.Background(), request)
	if err != nil {
		t.Fatalf("DeleteCard() error = %v", err)
	}
	if result.Status != core.StatusSuccess {
		t.Fatalf("DeleteCard() = %+v, want success", result)
	}

	sent := g.last(t)
	if sent.Method != http.MethodPost || sent.Path != "/odeme/capi/delete" {
		t.Errorf("request = %s %s, want POST /odeme/capi/delete", sent.Method, sent.Path)
	}
	form := sent.formBody(t)
	if form["merchant_id"] != paytrTestMerchantID {
		t.Errorf("merchant_id = %q, want %q", form["merchant_id"], paytrTestMerchantID)
	}
	if form["ctoken"] != request.CardToken || form["utoken"] != request.CustomerToken {
		t.Errorf("tokens = %q/%q, want %q/%q",
			form["ctoken"], form["utoken"], request.CardToken, request.CustomerToken)
	}
	wantToken, err := paytr.Sign(request.CardToken+request.CustomerToken+paytrTestSalt, paytrTestKey)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if form["paytr_token"] != wantToken {
		t.Errorf("paytr_token = %q, want %q", form["paytr_token"], wantToken)
	}
}

// TestPayTRStoredCardPaymentOmitsCardData checks that a stored card is sent as
// tokens only, and that the CVC flag follows whether one was supplied.
func TestPayTRStoredCardPaymentOmitsCardData(t *testing.T) {
	tests := []struct {
		name        string
		cvc         string
		wantRequire string
	}{
		{name: "with CVC", cvc: "000", wantRequire: "1"},
		{name: "without CVC", wantRequire: "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newProviderGateway(t, "paytr", approve("paytr"))
			request := orderWithoutCard()
			request.StoredCard = &core.StoredCardReference{
				CustomerToken: "UT", CardToken: "CT1", CVC: test.cvc,
			}

			result, err := g.provider.CreatePayment(context.Background(), request)
			if err != nil {
				t.Fatalf("CreatePayment() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("CreatePayment() = %+v, want success", result)
			}

			sent := g.last(t)
			if sent.Path != "/odeme" {
				t.Errorf("path = %q, want /odeme", sent.Path)
			}
			form := sent.formBody(t)
			if form["utoken"] != "UT" || form["ctoken"] != "CT1" {
				t.Errorf("tokens = %q/%q, want UT/CT1", form["utoken"], form["ctoken"])
			}
			if form["require_cvv"] != test.wantRequire {
				t.Errorf("require_cvv = %q, want %q", form["require_cvv"], test.wantRequire)
			}
			if form["cvv"] != test.cvc {
				t.Errorf("cvv = %q, want %q", form["cvv"], test.cvc)
			}
			for _, field := range []string{"card_number", "cc_owner", "expiry_month", "expiry_year", "store_card"} {
				if _, present := form[field]; present {
					t.Errorf("%s = %q, want it absent for a stored card", field, form[field])
				}
			}
			if form["non_3d"] != "1" || form["sync_mode"] != "1" {
				t.Errorf("non_3d/sync_mode = %q/%q, want 1/1", form["non_3d"], form["sync_mode"])
			}
			if form["payment_amount"] != "120" || form["currency"] != "TL" {
				t.Errorf("amount/currency = %q/%q, want 120/TL", form["payment_amount"], form["currency"])
			}
		})
	}
}

// TestPayTRSaveCardDuringPaymentReturnsUtoken covers storing a card while it is
// charged, which is the only way this gateway stores cards.
func TestPayTRSaveCardDuringPaymentReturnsUtoken(t *testing.T) {
	tests := []struct {
		name       string
		option     core.SaveCardOption
		wantUtoken string
	}{
		{name: "boolean form", option: core.SaveCardOption{Enabled: true}},
		{name: "object form", option: core.SaveCardOption{CustomerToken: "UT"}, wantUtoken: "UT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newProviderGateway(t, "paytr", approve("paytr"))
			request := orderRequest()
			request.SaveCard = &test.option

			result, err := g.provider.CreatePayment(context.Background(), request)
			if err != nil {
				t.Fatalf("CreatePayment() error = %v", err)
			}
			if result.Status != core.StatusSuccess {
				t.Fatalf("CreatePayment() = %+v, want success", result)
			}
			if result.StoredCard == nil || result.StoredCard.CustomerToken != "NEWUT" {
				t.Errorf("storedCard = %+v, want the customer token NEWUT", result.StoredCard)
			}

			form := g.last(t).formBody(t)
			if form["store_card"] != "1" {
				t.Errorf("store_card = %q, want 1", form["store_card"])
			}
			if form["card_number"] != fixtures.FixtureCard().CardNumber {
				t.Errorf("card_number = %q, want the card being stored", form["card_number"])
			}
			if form["utoken"] != test.wantUtoken {
				t.Errorf("utoken = %q, want %q", form["utoken"], test.wantUtoken)
			}
		})
	}
}

// TestPayTRThreeDSStoredCardUsesDirectForm checks that a stored card skips the
// hosted iFrame: the gateway builds an auto-submitting form instead, without
// calling the API first.
func TestPayTRThreeDSStoredCardUsesDirectForm(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))
	request := orderThreeDSRequest()
	request.PaymentCard = nil
	request.StoredCard = storedCardReference()

	result, err := g.provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	if result.Status != core.StatusPending {
		t.Fatalf("InitThreeDSPayment() = %+v, want pending", result)
	}
	wantNoCalls(t, g)

	form := result.ThreeDSHTMLContent
	for _, want := range []string{
		`action="` + g.server.URL + `/odeme"`,
		`name="utoken" value="USERKEY"`,
		`name="ctoken" value="CARDTOKEN"`,
		`name="require_cvv" value="0"`,
		`name="merchant_ok_url" value="https://example.com/callback"`,
		`name="non_3d" value="0"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("form is missing %s\n%s", want, form)
		}
	}
	if strings.Contains(form, `name="card_number"`) {
		t.Errorf("form carries card data for a stored card:\n%s", form)
	}
	if result.PaymentID == "" {
		t.Error("InitThreeDSPayment() returned no payment id")
	}
}

// TestPayTRIframeCannotSaveCards checks that the hosted iFrame refuses saveCard:
// only the Direct API can store a card.
func TestPayTRIframeCannotSaveCards(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))
	request := orderThreeDSRequest()
	request.SaveCard = &core.SaveCardOption{Enabled: true}

	result, err := g.provider.InitThreeDSPayment(context.Background(), request)
	if err != nil {
		t.Fatalf("InitThreeDSPayment() error = %v", err)
	}
	wantValidationFailure(t, result.Status, result.Code, result.ErrorMessage, "saveCard")
	wantNoCalls(t, g)
}

// TestPayTRSaveCardWithoutPaymentIsNotSupported checks the deliberate stub: it
// explains where card storage does happen instead of failing silently.
func TestPayTRSaveCardWithoutPaymentIsNotSupported(t *testing.T) {
	g := newProviderGateway(t, "paytr", approve("paytr"))

	result, err := g.provider.SaveCard(context.Background(), saveCardRequest())
	wantNotSupported(t, "paytr", "SaveCard", err)
	if result != nil {
		t.Errorf("SaveCard() result = %+v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Direct API") {
		t.Errorf("error = %q, want it to point at the Direct API", err)
	}
	wantNoCalls(t, g)
}

// TestMockProviderCardStorageLifecycle checks the in-memory provider against the
// same contract: a card is stored, listed, charged and deleted.
func TestMockProviderCardStorageLifecycle(t *testing.T) {
	provider := fixtures.NewMockProvider()
	ctx := context.Background()

	saved, err := provider.SaveCard(ctx, saveCardRequest())
	if err != nil {
		t.Fatalf("SaveCard() error = %v", err)
	}
	if saved.Status != core.StatusSuccess || saved.CustomerToken == "" || saved.Card == nil {
		t.Fatalf("SaveCard() = %+v, want a stored card", saved)
	}
	if saved.Card.LastFourDigits != "0008" || saved.Card.BinNumber != "552879" {
		t.Errorf("card = %+v, want the fixture card's BIN and last four digits", saved.Card)
	}
	if saved.Card.Alias != "Work" {
		t.Errorf("alias = %q, want Work", saved.Card.Alias)
	}

	listed, err := provider.ListCards(ctx, &core.ListCardsRequest{CustomerToken: saved.CustomerToken})
	if err != nil {
		t.Fatalf("ListCards() error = %v", err)
	}
	if len(listed.Cards) != 1 || listed.Cards[0].CardToken != saved.Card.CardToken {
		t.Fatalf("ListCards() = %+v, want the card just stored", listed)
	}

	request := orderWithoutCard()
	request.StoredCard = &core.StoredCardReference{
		CustomerToken: saved.CustomerToken, CardToken: saved.Card.CardToken,
	}
	paid, err := provider.CreatePayment(ctx, request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if paid.Status != core.StatusSuccess {
		t.Fatalf("CreatePayment() = %+v, want success", paid)
	}

	deleted, err := provider.DeleteCard(ctx, &core.DeleteCardRequest{
		CustomerToken: saved.CustomerToken, CardToken: saved.Card.CardToken,
	})
	if err != nil {
		t.Fatalf("DeleteCard() error = %v", err)
	}
	if deleted.Status != core.StatusSuccess {
		t.Fatalf("DeleteCard() = %+v, want success", deleted)
	}

	empty, err := provider.ListCards(ctx, &core.ListCardsRequest{CustomerToken: saved.CustomerToken})
	if err != nil {
		t.Fatalf("ListCards() error = %v", err)
	}
	if len(empty.Cards) != 0 {
		t.Errorf("ListCards() after delete = %+v, want no cards", empty)
	}

	// A deleted card can no longer be charged or deleted again.
	if _, err := provider.CreatePayment(ctx, request); err == nil {
		t.Error("CreatePayment() with a deleted card returned no error")
	}
	again, err := provider.DeleteCard(ctx, &core.DeleteCardRequest{
		CustomerToken: saved.CustomerToken, CardToken: saved.Card.CardToken,
	})
	if err != nil {
		t.Fatalf("DeleteCard() error = %v", err)
	}
	if again.Status != core.StatusFailure {
		t.Errorf("DeleteCard() of an unknown card = %+v, want failure", again)
	}
}

package testing

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// fixtureRules are the strictest rules any gateway applies: a card, a basket
// whose prices add up to the order, and every buyer and address field. A fixture
// that passes them works with every provider.
var fixtureRules = core.PaymentValidationRules{
	Card:               true,
	StoredCard:         true,
	SaveCard:           true,
	BasketRequired:     true,
	BasketMatchesPrice: true,
	Required: []string{
		"buyer.id",
		"buyer.name",
		"buyer.surname",
		"buyer.email",
		"buyer.identityNumber",
		"buyer.registrationAddress",
		"buyer.city",
		"buyer.country",
		"buyer.ip",
		"buyer.gsmNumber",
		"billingAddress.contactName",
		"billingAddress.city",
		"billingAddress.country",
		"billingAddress.address",
		"shippingAddress.contactName",
		"shippingAddress.city",
		"shippingAddress.country",
		"shippingAddress.address",
	},
}

func TestPaymentFixturesPassTheStrictestValidation(t *testing.T) {
	t.Run("direct payment", func(t *testing.T) {
		request := FixturePaymentRequest()
		if err := core.ValidatePaymentRequest(&request, fixtureRules, "fixture"); err != nil {
			t.Errorf("FixturePaymentRequest() is invalid: %v", err)
		}
	})

	t.Run("3D Secure payment", func(t *testing.T) {
		request := FixtureThreeDSPaymentRequest()
		if err := core.ValidatePaymentRequest(&request.PaymentRequest, fixtureRules, "fixture"); err != nil {
			t.Errorf("FixtureThreeDSPaymentRequest() is invalid: %v", err)
		}
		if request.CallbackURL == "" {
			t.Error("FixtureThreeDSPaymentRequest() has no callbackUrl, which 3D Secure requires")
		}
	})

	t.Run("hosted checkout form", func(t *testing.T) {
		form := FixtureCheckoutFormRequest()
		// A checkout form has no card: the customer enters it on the bank's page.
		rules := fixtureRules
		rules.Card = false
		request := core.PaymentRequest{
			Price:           form.Price,
			PaidPrice:       form.PaidPrice,
			Currency:        form.Currency,
			BasketID:        form.BasketID,
			Buyer:           form.Buyer,
			ShippingAddress: form.ShippingAddress,
			BillingAddress:  form.BillingAddress,
			BasketItems:     form.BasketItems,
			CallbackURL:     form.CallbackURL,
			ConversationID:  form.ConversationID,
		}
		if err := core.ValidatePaymentRequest(&request, rules, "fixture"); err != nil {
			t.Errorf("FixtureCheckoutFormRequest() is invalid: %v", err)
		}
		if form.CallbackURL == "" {
			t.Error("FixtureCheckoutFormRequest() has no callbackUrl")
		}
		seen := make(map[int]bool, len(form.EnabledInstallments))
		for _, count := range form.EnabledInstallments {
			if count < 2 {
				t.Errorf("enabledInstallments contains %d, want 2 or more", count)
			}
			if seen[count] {
				t.Errorf("enabledInstallments repeats %d", count)
			}
			seen[count] = true
		}
	})
}

// TestFixtureBasketAddsUpToThePrice checks the basket the payment fixtures carry,
// item by item, so a provider that validates line prices accepts it.
func TestFixtureBasketAddsUpToThePrice(t *testing.T) {
	items := FixtureBasketItems()
	if len(items) == 0 {
		t.Fatal("FixtureBasketItems() is empty")
	}
	var total int64
	for i, item := range items {
		if item.ID == "" || item.Name == "" || item.Category1 == "" || item.Price == "" {
			t.Errorf("item %d = %+v, want every field filled in", i, item)
		}
		switch item.ItemType {
		case string(core.BasketItemPhysical), string(core.BasketItemVirtual):
		default:
			t.Errorf("item %d itemType = %q, want a known basket item type", i, item.ItemType)
		}
		price, err := strconv.ParseFloat(item.Price, 64)
		if err != nil {
			t.Fatalf("item %d price = %q, want a number: %v", i, item.Price, err)
		}
		if price <= 0 {
			t.Errorf("item %d price = %q, want a positive amount", i, item.Price)
		}
		total += int64(math.Round(price * 100))
	}

	request := FixturePaymentRequest()
	orderPrice, err := strconv.ParseFloat(request.Price, 64)
	if err != nil {
		t.Fatalf("price = %q, want a number: %v", request.Price, err)
	}
	if want := int64(math.Round(orderPrice * 100)); total != want {
		t.Errorf("basket totals %d minor units, want the order price %d", total, want)
	}
	if request.BasketID == "" {
		t.Error("FixturePaymentRequest() has no basketId")
	}
}

// TestFixturesReturnFreshValues checks the documented contract: changing one
// fixture never changes the next, so tests cannot leak state into each other.
func TestFixturesReturnFreshValues(t *testing.T) {
	first := FixturePaymentRequest()
	second := FixturePaymentRequest()
	if first.PaymentCard == second.PaymentCard {
		t.Error("two fixtures share one card pointer")
	}
	if &first.BasketItems[0] == &second.BasketItems[0] {
		t.Error("two fixtures share one basket slice")
	}

	first.PaymentCard.CardNumber = "4242424242424242"
	*first.PaymentCard.RegisterCard = false
	first.BasketItems[0].Price = "99"
	first.Buyer.Email = "changed@example.com"
	first.ShippingAddress.City = "Ankara"
	first.BillingAddress.ContactName = "Changed"

	third := FixturePaymentRequest()
	if third.PaymentCard.CardNumber != second.PaymentCard.CardNumber {
		t.Errorf("card number = %q, want the fixture card", third.PaymentCard.CardNumber)
	}
	if third.PaymentCard.RegisterCard == nil || !*third.PaymentCard.RegisterCard {
		t.Error("registerCard was changed by an earlier fixture")
	}
	if third.BasketItems[0].Price != second.BasketItems[0].Price {
		t.Errorf("basket price = %q, want the fixture price", third.BasketItems[0].Price)
	}
	if third.Buyer.Email != second.Buyer.Email {
		t.Errorf("buyer email = %q, want the fixture address", third.Buyer.Email)
	}
	if third.ShippingAddress.City != second.ShippingAddress.City {
		t.Errorf("shipping city = %q, want the fixture city", third.ShippingAddress.City)
	}
	if third.BillingAddress.ContactName != second.BillingAddress.ContactName {
		t.Errorf("billing contact = %q, want the fixture name", third.BillingAddress.ContactName)
	}

	// The card and address fixtures are independent of the payment fixture too.
	card := FixtureCard()
	card.CardHolderName = "Changed"
	if FixtureCard().CardHolderName != "John Doe" {
		t.Error("FixtureCard() handed out a shared value")
	}
	address := FixtureShippingAddress()
	address.Address = "Changed"
	if FixtureShippingAddress().Address == "Changed" {
		t.Error("FixtureShippingAddress() handed out a shared value")
	}
	billing := FixtureBillingAddress()
	billing.ZipCode = "00000"
	if FixtureBillingAddress().ZipCode == "00000" {
		t.Error("FixtureBillingAddress() handed out a shared value")
	}
}

// TestFixtureThreeDSRequestIsThePaymentFixturePlusACallback checks that the two
// payment fixtures stay in step.
func TestFixtureThreeDSRequestIsThePaymentFixturePlusACallback(t *testing.T) {
	threeDS := FixtureThreeDSPaymentRequest()
	direct := FixturePaymentRequest()
	if threeDS.CallbackURL == "" {
		t.Fatal("FixtureThreeDSPaymentRequest() has no callbackUrl")
	}
	threeDS.PaymentRequest.CallbackURL = ""
	if !reflect.DeepEqual(threeDS.PaymentRequest, direct) {
		t.Errorf("3D Secure fixture = %+v, want the direct fixture plus a callbackUrl", threeDS.PaymentRequest)
	}
}

// TestFixtureRefundRequestIsAPartialRefund checks the refund fixture against the
// payment it refunds.
func TestFixtureRefundRequestIsAPartialRefund(t *testing.T) {
	refund := FixtureRefundRequest()
	if err := core.ValidateRefundAmount(refund.Price, "fixture"); err != nil {
		t.Fatalf("FixtureRefundRequest() price is invalid: %v", err)
	}
	if refund.PaymentID == "" || refund.IP == "" {
		t.Errorf("FixtureRefundRequest() = %+v, want a payment id and an IP", refund)
	}
	if refund.Currency != string(core.CurrencyTRY) {
		t.Errorf("currency = %q, want %q", refund.Currency, core.CurrencyTRY)
	}

	payment := FixturePaymentRequest()
	refundPrice, _ := strconv.ParseFloat(refund.Price, 64)
	orderPrice, _ := strconv.ParseFloat(payment.Price, 64)
	if refundPrice >= orderPrice {
		t.Errorf("refund price %q is not below the order price %q", refund.Price, payment.Price)
	}
	if refund.ConversationID != payment.ConversationID {
		t.Errorf("conversationId = %q, want the payment fixture's %q", refund.ConversationID, payment.ConversationID)
	}
}

// TestFixtureCancelRequestNamesThePayment checks the cancellation fixture.
func TestFixtureCancelRequestNamesThePayment(t *testing.T) {
	cancel := FixtureCancelRequest()
	if cancel.PaymentID == "" || cancel.IP == "" {
		t.Errorf("FixtureCancelRequest() = %+v, want a payment id and an IP", cancel)
	}
	payment := FixturePaymentRequest()
	if cancel.ConversationID != payment.ConversationID {
		t.Errorf("conversationId = %q, want the payment fixture's %q", cancel.ConversationID, payment.ConversationID)
	}
	if cancel.Price != "" {
		t.Errorf("price = %q, want it unset so a full cancellation is requested", cancel.Price)
	}
}

// TestFixtureCardSucceedsWithTheMockProvider checks that the default fixture is
// the happy path: it is a valid card that no magic scenario intercepts.
func TestFixtureCardSucceedsWithTheMockProvider(t *testing.T) {
	card := FixtureCard()
	for scenario, number := range MagicCards() {
		if number == card.CardNumber {
			t.Fatalf("the fixture card is the magic %s card, so it would not simply succeed", scenario)
		}
	}

	request := FixturePaymentRequest()
	paid, err := NewMockProvider().CreatePayment(context.Background(), &request)
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	if paid.Status != core.StatusSuccess {
		t.Errorf("CreatePayment() = %+v, want the fixture card to succeed", paid)
	}
}

// TestPaymentFixtureUsesTheWireFieldNames guards the JSON contract the fixtures
// are sent over: a renamed field would silently drop out of every request.
func TestPaymentFixtureUsesTheWireFieldNames(t *testing.T) {
	raw, err := json.Marshal(FixturePaymentRequest())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{
		"price", "paidPrice", "currency", "basketId", "paymentCard",
		"buyer", "shippingAddress", "billingAddress", "basketItems", "conversationId",
	} {
		if _, present := body[key]; !present {
			t.Errorf("serialized fixture is missing %q", key)
		}
	}
	for _, key := range []string{"storedCard", "saveCard", "callbackUrl"} {
		if _, present := body[key]; present {
			t.Errorf("serialized fixture carries %q, want it omitted when unset", key)
		}
	}

	card, ok := body["paymentCard"].(map[string]any)
	if !ok {
		t.Fatalf("paymentCard = %v, want an object", body["paymentCard"])
	}
	for _, key := range []string{"cardHolderName", "cardNumber", "expireMonth", "expireYear", "cvc", "registerCard"} {
		if _, present := card[key]; !present {
			t.Errorf("serialized card is missing %q", key)
		}
	}

	raw, err = json.Marshal(FixtureCheckoutFormRequest())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body = nil
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{"callbackUrl", "enabledInstallments", "basketId", "paidPrice"} {
		if _, present := body[key]; !present {
			t.Errorf("serialized checkout form is missing %q", key)
		}
	}
}

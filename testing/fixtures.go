package testing

import "github.com/boracomet/go-pos/core"

// FixtureCard returns a valid test card. Every call returns a fresh value.
func FixtureCard() core.PaymentCard {
	register := true
	return core.PaymentCard{
		CardHolderName: "John Doe",
		CardNumber:     "5528790000000008",
		ExpireMonth:    "12",
		ExpireYear:     "2030",
		CVC:            "123",
		RegisterCard:   &register,
	}
}

// FixtureBuyer returns a test customer.
func FixtureBuyer() core.Buyer {
	return core.Buyer{
		ID:                  "BY789",
		Name:                "John",
		Surname:             "Doe",
		GSMNumber:           "+905350000000",
		Email:               "john.doe@example.com",
		IdentityNumber:      "74300864791",
		RegistrationAddress: "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		IP:                  "85.34.78.112",
		City:                "Istanbul",
		Country:             "Turkey",
		ZipCode:             "34732",
	}
}

// FixtureShippingAddress returns the test shipping address.
func FixtureShippingAddress() core.Address {
	return core.Address{
		ContactName: "Jane Doe",
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

// FixtureBillingAddress returns the test billing address.
func FixtureBillingAddress() core.Address {
	return core.Address{
		ContactName: "John Doe",
		City:        "Istanbul",
		Country:     "Turkey",
		Address:     "Nidakule Göztepe, Merdivenköy Mah. Bora Sok. No:1",
		ZipCode:     "34732",
	}
}

// FixtureBasketItems returns the test basket: three items adding up to 1.00.
func FixtureBasketItems() []core.BasketItem {
	return []core.BasketItem{
		{
			ID:        "BI101",
			Name:      "Binocular",
			Category1: "Collectibles",
			Category2: "Accessories",
			ItemType:  string(core.BasketItemPhysical),
			Price:     "0.3",
		},
		{
			ID:        "BI102",
			Name:      "Game code",
			Category1: "Game",
			Category2: "Online Game Items",
			ItemType:  string(core.BasketItemVirtual),
			Price:     "0.5",
		},
		{
			ID:        "BI103",
			Name:      "Usb",
			Category1: "Electronics",
			Category2: "Usb / Cable",
			ItemType:  string(core.BasketItemPhysical),
			Price:     "0.2",
		},
	}
}

// FixturePaymentRequest returns a valid payment request of 1.00 TRY.
func FixturePaymentRequest() core.PaymentRequest {
	card := FixtureCard()
	return core.PaymentRequest{
		Price:           "1",
		PaidPrice:       "1.2",
		Currency:        string(core.CurrencyTRY),
		BasketID:        "B67832",
		PaymentCard:     &card,
		Buyer:           FixtureBuyer(),
		ShippingAddress: FixtureShippingAddress(),
		BillingAddress:  FixtureBillingAddress(),
		BasketItems:     FixtureBasketItems(),
		ConversationID:  "123456789",
	}
}

// FixtureThreeDSPaymentRequest returns a valid 3D Secure payment request.
func FixtureThreeDSPaymentRequest() core.ThreeDSPaymentRequest {
	request := FixturePaymentRequest()
	request.CallbackURL = "https://example.com/callback"
	return core.ThreeDSPaymentRequest{PaymentRequest: request}
}

// FixtureRefundRequest returns a refund request of 0.50 TRY.
func FixtureRefundRequest() core.RefundRequest {
	return core.RefundRequest{
		PaymentID:      "12345678",
		Price:          "0.5",
		Currency:       string(core.CurrencyTRY),
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

// FixtureCancelRequest returns a cancellation request.
func FixtureCancelRequest() core.CancelRequest {
	return core.CancelRequest{
		PaymentID:      "12345678",
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

// FixtureCheckoutFormRequest returns a hosted checkout form request.
func FixtureCheckoutFormRequest() core.CheckoutFormRequest {
	return core.CheckoutFormRequest{
		Price:               "1",
		PaidPrice:           "1.2",
		Currency:            string(core.CurrencyTRY),
		BasketID:            "B67832",
		CallbackURL:         "https://example.com/callback",
		EnabledInstallments: []int{2, 3, 6, 9},
		Buyer:               FixtureBuyer(),
		ShippingAddress:     FixtureShippingAddress(),
		BillingAddress:      FixtureBillingAddress(),
		BasketItems:         FixtureBasketItems(),
		ConversationID:      "123456789",
	}
}

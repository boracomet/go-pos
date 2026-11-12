// Command pwi-payment takes a protected bank transfer (Korumalı Havale/EFT) with
// iyzico: start the payment, show the IBAN, poll the state, and handle the
// callback the bank sends once the customer transfers the money.
//
//	IYZICO_API_KEY=... IYZICO_SECRET_KEY=... go run ./examples/pwi-payment
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
)

func newPWI() (*pos.Payment, core.PWIProvider) {
	baseURL := os.Getenv("IYZICO_BASE_URL")
	if baseURL == "" {
		baseURL = "https://sandbox-api.iyzipay.com"
	}
	cfg := pos.IyzicoConfig{
		APIKey:    os.Getenv("IYZICO_API_KEY"),
		SecretKey: os.Getenv("IYZICO_SECRET_KEY"),
	}
	cfg.BaseURL = baseURL
	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{"iyzico": cfg},
	})
	if err != nil {
		log.Fatal(err)
	}
	pwi, err := payment.PWIProvider("iyzico")
	if err != nil {
		log.Fatal(err)
	}
	return payment, pwi
}

// initializePWIPayment starts the transfer and returns its token.
func initializePWIPayment(ctx context.Context, pwi core.PWIProvider) string {
	result, err := pwi.InitPWIPayment(ctx, &core.PWIPaymentRequest{
		Price:       "100.00",
		PaidPrice:   "100.00",
		Currency:    string(core.CurrencyTRY),
		BasketID:    "B67832",
		CallbackURL: "https://your-site.com/payment/callback",
		Buyer: core.Buyer{
			ID:                  "BY789",
			Name:                "John",
			Surname:             "Doe",
			Email:               "john.doe@example.com",
			IdentityNumber:      "11111111110",
			RegistrationAddress: "Nidakule Göztepe, Merdivenköy Mah.",
			City:                "Istanbul",
			Country:             "Turkey",
			IP:                  "85.34.78.112",
			GSMNumber:           "+905350000000",
		},
		ShippingAddress: core.Address{
			ContactName: "John Doe",
			City:        "Istanbul",
			Country:     "Turkey",
			Address:     "Nidakule Göztepe, Merdivenköy Mah.",
			ZipCode:     "34732",
		},
		BillingAddress: core.Address{
			ContactName: "John Doe",
			City:        "Istanbul",
			Country:     "Turkey",
			Address:     "Nidakule Göztepe, Merdivenköy Mah.",
			ZipCode:     "34732",
		},
		BasketItems: []core.BasketItem{{
			ID:        "BI101",
			Name:      "Premium Üyelik",
			Category1: "Membership",
			ItemType:  string(core.BasketItemVirtual),
			Price:     "100.00",
		}},
		ConversationID: "pwi-payment-001",
	})
	if err != nil {
		log.Println("PWI hatası:", err)
		return ""
	}
	if result.Status != core.StatusSuccess {
		log.Println("PWI başlatma hatası:", result.ErrorMessage)
		return ""
	}

	fmt.Println("PWI ödemesi başlatıldı")
	fmt.Println("Token:", result.Token)
	fmt.Println("Token geçerlilik:", time.UnixMilli(result.TokenExpireTime).Format(time.RFC3339))

	// Either show the HTML (it carries the IBAN and the transfer details)...
	fmt.Println("\nHTML içeriği (IBAN bilgileri):")
	fmt.Println(result.HTMLContent)
	// ...or send the customer to iyzico's payment page.
	fmt.Println("\nÖdeme sayfası:", result.PaymentPageURL)

	saveToken(result.Token, result.ConversationID)
	return result.Token
}

// checkPWIPaymentStatus asks iyzico whether the transfer arrived.
func checkPWIPaymentStatus(ctx context.Context, pwi core.PWIProvider, token string) *core.PWIPaymentRetrieveResponse {
	result, err := pwi.RetrievePWIPayment(ctx, token, "")
	if err != nil {
		log.Println("sorgulama hatası:", err)
		return nil
	}
	if result.PaymentStatus == "" {
		log.Println("ödeme durumu alınamadı:", result.ErrorMessage)
		return nil
	}

	fmt.Println("\nÖdeme durumu:", result.PaymentStatus)
	switch core.PWIPaymentStatus(result.PaymentStatus) {
	case core.PWIWaiting:
		fmt.Println("\nHavale bilgileri:")
		fmt.Println("IBAN:", result.IBAN)
		fmt.Println("Banka:", result.BankName)
		fmt.Println("Tutar:", amount(result.PaidPrice), result.Currency)
		fmt.Println("Alıcı:", result.BuyerName, result.BuyerSurname)
		fmt.Println("\nHavale yapılmasını bekliyoruz...")
	case core.PWISuccess:
		fmt.Println("\nÖdeme başarılı")
		fmt.Println("Ödeme ID:", result.PaymentID)
		fmt.Println("Ödenen tutar:", amount(result.PaidPrice), result.Currency)
		fmt.Println("Komisyon:", amount(result.MerchantCommissionRateAmount))
		completeOrder(result.PaymentID)
	case core.PWIFailure:
		fmt.Println("\nÖdeme başarısız:", result.ErrorMessage)
		cancelOrder(token)
	}
	return result
}

// callbackHandler is the endpoint iyzico notifies after the transfer.
func callbackHandler(pwi core.PWIProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"Token gerekli"}`)
			return
		}

		result := checkPWIPaymentStatus(r.Context(), pwi, body.Token)
		if result == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"Ödeme durumu sorgulanamadı"}`)
			return
		}

		switch core.PWIPaymentStatus(result.PaymentStatus) {
		case core.PWISuccess:
			http.Redirect(w, r, "/payment/success?paymentId="+result.PaymentID, http.StatusSeeOther)
		case core.PWIWaiting:
			http.Redirect(w, r, "/payment/waiting?token="+body.Token, http.StatusSeeOther)
		default:
			http.Redirect(w, r, "/payment/failed", http.StatusSeeOther)
		}
	}
}

// periodicPaymentCheck settles transfers that never sent a callback.
func periodicPaymentCheck(ctx context.Context, pwi core.PWIProvider) {
	for _, pending := range pendingPayments() {
		result := checkPWIPaymentStatus(ctx, pwi, pending.token)
		if result == nil {
			continue
		}
		switch core.PWIPaymentStatus(result.PaymentStatus) {
		case core.PWISuccess:
			completeOrder(result.PaymentID)
			updatePaymentStatus(pending.token, "completed")
		case core.PWIFailure:
			cancelOrder(pending.token)
			updatePaymentStatus(pending.token, "failed")
		}
	}
}

// The helpers below stand in for your own database.
func saveToken(token, conversationID string) {
	fmt.Println("Token kaydedildi:", token, conversationID)
}

func pendingPayments() []struct{ token string } { return nil }

func completeOrder(paymentID string) { fmt.Println("Sipariş tamamlandı:", paymentID) }

func cancelOrder(token string) { fmt.Println("Sipariş iptal edildi:", token) }

func updatePaymentStatus(token, status string) {
	fmt.Println("Ödeme durumu güncellendi:", token, status)
}

func amount(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *value)
}

func main() {
	if os.Getenv("IYZICO_API_KEY") == "" || os.Getenv("IYZICO_SECRET_KEY") == "" {
		log.Fatal("IYZICO_API_KEY ve IYZICO_SECRET_KEY gerekli")
	}
	_, pwi := newPWI()
	ctx := context.Background()

	fmt.Println("PWI ödeme örneği")
	token := initializePWIPayment(ctx, pwi)
	if token == "" {
		return
	}

	fmt.Println("\n---")
	checkPWIPaymentStatus(ctx, pwi, token)

	fmt.Println("\n---")
	fmt.Println("Kullanıcı havale yaptıktan sonra callback endpoint'ine bildirim gelir.")
	fmt.Println("Ayrıca periyodik olarak ödeme durumunu sorgulayabilirsin:")

	http.HandleFunc("/payment/callback", callbackHandler(pwi))
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			periodicPaymentCheck(ctx, pwi)
		}
	}()
	log.Fatal(http.ListenAndServe(":8080", nil))
}

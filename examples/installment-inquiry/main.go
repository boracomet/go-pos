// Command installment-inquiry queries installment options for a card BIN with
// iyzico: a single query, a comparison across cards, checkout options, interest
// analysis, and a live card-entry flow.
//
//	IYZICO_API_KEY=... IYZICO_SECRET_KEY=... go run ./examples/installment-inquiry
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
)

func newPayment() (*pos.Payment, core.Provider) {
	cfg := pos.IyzicoConfig{
		APIKey:    os.Getenv("IYZICO_API_KEY"),
		SecretKey: os.Getenv("IYZICO_SECRET_KEY"),
	}
	cfg.BaseURL = baseURL()
	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{"iyzico": cfg},
	})
	if err != nil {
		log.Fatal(err)
	}
	provider, err := payment.Use("iyzico")
	if err != nil {
		log.Fatal(err)
	}
	return payment, provider
}

func baseURL() string {
	if url := os.Getenv("IYZICO_BASE_URL"); url != "" {
		return url
	}
	return "https://sandbox-api.iyzipay.com"
}

func query(ctx context.Context, provider core.Provider, bin, price string) *core.InstallmentInfoResponse {
	result, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{
		BinNumber: bin, // kart numarasının ilk 6 hanesi
		Price:     price,
	})
	if err != nil {
		log.Println("hata:", err)
		return nil
	}
	if result.Status != core.StatusSuccess || len(result.InstallmentDetails) == 0 {
		log.Println("taksit bilgisi alınamadı:", result.ErrorMessage)
		return nil
	}
	return result
}

// 1. Basit taksit sorgulama
func simpleInstallmentInquiry(ctx context.Context, provider core.Provider) {
	result := query(ctx, provider, "552879", "100.00")
	if result == nil {
		return
	}
	fmt.Println("Taksit bilgileri alındı")

	for _, detail := range result.InstallmentDetails {
		fmt.Println("\nBanka:", detail.BankName)
		fmt.Println("Kart ailesi:", detail.CardFamilyName)
		fmt.Println("Kart tipi:", detail.CardType)
		fmt.Println("Kart kuruluşu:", detail.CardAssociation)
		fmt.Println("Ticari kart:", yesNo(detail.Commercial == 1))
		if detail.Force3DS != nil && *detail.Force3DS == 1 {
			fmt.Println("3D Secure zorunlu")
		}

		fmt.Println("\nTaksit seçenekleri:")
		fmt.Println(strings.Repeat("-", 60))
		for _, installment := range detail.InstallmentPrices {
			if installment.InstallmentNumber == 1 {
				fmt.Printf("Tek çekim: %.2f TL\n", installment.TotalPrice)
				continue
			}
			fmt.Printf("%d taksit: %.2f TL (aylık %.2f TL, faiz %%%s)\n",
				installment.InstallmentNumber,
				installment.TotalPrice,
				installment.InstallmentPrice,
				interestRate(installment.TotalPrice, detail.Price))
		}
		fmt.Println(strings.Repeat("=", 60))
	}
}

// 2. Birden fazla kart için taksit karşılaştırma
func compareInstallmentOptions(ctx context.Context, provider core.Provider) {
	cards := []struct{ bin, name string }{
		{"552879", "Akbank Bonus"},
		{"454360", "Garanti BBVA"},
		{"540668", "İş Bankası Maximum"},
	}
	const amount = "1000.00"
	fmt.Printf("%s TL için taksit karşılaştırması\n\n", amount)

	for _, card := range cards {
		result := query(ctx, provider, card.bin, amount)
		if result == nil {
			continue
		}
		detail := result.InstallmentDetails[0]
		fmt.Printf("%s (%s)\n   Banka: %s\n", card.name, card.bin, detail.BankName)

		max := detail.InstallmentPrices[0]
		for _, installment := range detail.InstallmentPrices {
			if installment.InstallmentNumber > max.InstallmentNumber {
				max = installment
			}
		}
		fmt.Printf("   Maksimum taksit: %d ay\n", max.InstallmentNumber)

		for _, installment := range detail.InstallmentPrices {
			if installment.InstallmentNumber == 6 {
				fmt.Printf("   6 taksit: %.2f TL (aylık %.2f TL)\n",
					installment.TotalPrice, installment.InstallmentPrice)
			}
		}
		fmt.Println()
	}
}

type checkoutOption struct {
	BankName          string  `json:"bankName"`
	CardFamily        string  `json:"cardFamily"`
	InstallmentNumber int     `json:"installmentNumber"`
	MonthlyPayment    float64 `json:"monthlyPayment"`
	TotalPayment      float64 `json:"totalPayment"`
	InstallmentText   string  `json:"installmentText"`
	DisplayText       string  `json:"displayText"`
}

// 3. E-ticaret sitesi için taksit hesaplama
func calculateInstallmentForCheckout(ctx context.Context, provider core.Provider, bin, cartTotal string) []checkoutOption {
	result, err := provider.InstallmentInfo(ctx, &core.InstallmentInfoRequest{
		BinNumber:      bin,
		Price:          cartTotal,
		ConversationID: fmt.Sprintf("checkout-%d", time.Now().UnixMilli()),
	})
	if err != nil {
		log.Println("hata:", err)
		return nil
	}
	if result.Status != core.StatusSuccess {
		log.Println("taksit bilgisi alınamadı:", result.ErrorMessage)
		return nil
	}

	var options []checkoutOption
	for _, detail := range result.InstallmentDetails {
		for _, installment := range detail.InstallmentPrices {
			text := fmt.Sprintf("%d taksit", installment.InstallmentNumber)
			display := fmt.Sprintf("%d taksit - aylık %.2f TL (toplam %.2f TL)",
				installment.InstallmentNumber, installment.InstallmentPrice, installment.TotalPrice)
			if installment.InstallmentNumber == 1 {
				text = "Tek çekim"
				display = fmt.Sprintf("Tek çekim - %.2f TL", installment.TotalPrice)
			}
			options = append(options, checkoutOption{
				BankName:          detail.BankName,
				CardFamily:        detail.CardFamilyName,
				InstallmentNumber: installment.InstallmentNumber,
				MonthlyPayment:    installment.InstallmentPrice,
				TotalPayment:      installment.TotalPrice,
				InstallmentText:   text,
				DisplayText:       display,
			})
		}
	}

	fmt.Println("Sepet taksit seçenekleri:")
	encoded, err := json.MarshalIndent(options, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
	return options
}

// 4. Taksit faiz oranlarını hesaplama
func calculateInterestRates(ctx context.Context, provider core.Provider, bin, price string) {
	result := query(ctx, provider, bin, price)
	if result == nil {
		return
	}
	fmt.Println("Taksit faiz oranları analizi")

	original, err := core.ParseAmount(price, "price")
	if err != nil {
		log.Fatal(err)
	}
	for _, detail := range result.InstallmentDetails {
		fmt.Printf("\n%s - %s\n%s\n", detail.BankName, detail.CardFamilyName, strings.Repeat("-", 50))
		for _, installment := range detail.InstallmentPrices {
			if installment.InstallmentNumber == 1 {
				fmt.Println("Tek çekim: faizsiz")
				continue
			}
			rate := interestRate(installment.TotalPrice, original)
			monthly := 0.0
			if parsed, err := core.ParseAmount(rate, "rate"); err == nil {
				monthly = parsed / float64(installment.InstallmentNumber)
			}
			fmt.Printf("%d taksit: %%%s toplam faiz (%%%.2f aylık)\n",
				installment.InstallmentNumber, rate, monthly)
		}
	}
}

// 5. Canlı kart girişi: kullanıcı ilk 6 haneyi yazınca sorgula
func handleCardInput(ctx context.Context, provider core.Provider, cardNumber, amount string) *core.InstallmentPrice {
	if len(cardNumber) < 6 {
		return nil
	}
	bin := cardNumber[:6]
	fmt.Printf("%s BIN numarası için taksit seçenekleri sorgulanıyor...\n\n", bin)

	result := query(ctx, provider, bin, amount)
	if result == nil {
		fmt.Println("Bu kart için taksit bilgisi bulunamadı")
		return nil
	}
	detail := result.InstallmentDetails[0]

	fmt.Println("Kart bilgileri:")
	fmt.Println("   Banka:", detail.BankName)
	fmt.Println("   Kart tipi:", detail.CardType)
	fmt.Println("   Kart ailesi:", detail.CardFamilyName)
	if detail.Commercial == 1 {
		fmt.Println("   Bu bir ticari karttır")
	}
	if detail.Force3DS != nil && *detail.Force3DS == 1 {
		fmt.Println("   3D Secure zorunludur")
	}

	fmt.Println("\nMevcut taksit seçenekleri:")
	for _, installment := range detail.InstallmentPrices {
		label := fmt.Sprintf("%d taksit", installment.InstallmentNumber)
		if installment.InstallmentNumber == 1 {
			label = "Tek çekim"
		}
		fmt.Printf("   %s: %.2f TL\n", label, installment.TotalPrice)
	}
	return &detail
}

func interestRate(total, original float64) string {
	if original == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%.2f", (total-original)/original*100)
}

func yesNo(value bool) string {
	if value {
		return "Evet"
	}
	return "Hayır"
}

func main() {
	if os.Getenv("IYZICO_API_KEY") == "" || os.Getenv("IYZICO_SECRET_KEY") == "" {
		log.Fatal("IYZICO_API_KEY ve IYZICO_SECRET_KEY gerekli")
	}
	_, provider := newPayment()
	ctx := context.Background()

	separator := strings.Repeat("=", 60)

	fmt.Println("ÖRNEK 1: Basit taksit sorgulama")
	simpleInstallmentInquiry(ctx, provider)

	fmt.Println("\n" + separator + "\n\nÖRNEK 2: Kart karşılaştırma")
	compareInstallmentOptions(ctx, provider)

	fmt.Println("\n" + separator + "\n\nÖRNEK 3: E-ticaret checkout")
	calculateInstallmentForCheckout(ctx, provider, "552879", "299.99")

	fmt.Println("\n" + separator + "\n\nÖRNEK 4: Faiz oranları analizi")
	calculateInterestRates(ctx, provider, "552879", "500.00")

	fmt.Println("\n" + separator + "\n\nÖRNEK 5: Canlı kart girişi")
	handleCardInput(ctx, provider, "5528790000000008", "150.00")
}

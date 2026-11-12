# go-pos

Türkiye'deki sanal POS'ların hepsi, tek bir Go API'sinde.

`go-pos`; iyzico, PayTR, Garanti BBVA, İş Bankası, Ziraat, Akbank, Yapı Kredi, QNB, VakıfBank, Denizbank, Kuveyt Türk, Albaraka ve ParamPOS gibi sağlayıcıları tek bir `Payment` nesnesi arkasında toplar. Sağlayıcıyı değiştirirsin, iş kodunu değiştirmezsin.

Kart verisi, imza, callback doğrulama, hata kodu normalizasyonu ve yeniden deneme politikası kütüphanenin işi; siparişini güncellemek senin.

## Öne çıkanlar

- **20 sağlayıcı kimliği**, 13 sağlayıcı paketi. EST (Asseco) tabanlı bankalar tek implementasyonu paylaşır.
- **Tek API:** ödeme, 3D Secure, ön provizyon, capture, void, iade, iptal, kart saklama, BIN sorgulama, taksit bilgisi.
- **Normalleştirilmiş sonuçlar:** her sağlayıcıdan aynı `PaymentResponse`, aynı `PaymentErrorCode`. Bankanın ham hata kodu da yanında durur.
- **16 eklenti:** rate limit, fraud kuralları, redaksiyon, bildirim, webhook yönlendirme, failover, iade onayı, telemetri, i18n, captcha, chaos, MCP, OpenAPI ve daha fazlası.
- **Akıllı yönlendirme:** BIN'e, taksite, tutara ve ağırlığa göre sağlayıcı seçimi; komisyon oranına göre en ucuz sağlayıcıyı bulma.
- **Hazır HTTP handler:** `net/http` ile doğrudan, framework bağımlılığı yok. Callback doğrulama, idempotency ve yetkilendirme dahil.
- **Test araçları:** ağa çıkmayan mock sağlayıcı, sihirli kart numaraları, hazır fixture'lar.
- **Context-first:** her operasyon `context.Context` alır. İptal ve timeout doğal çalışır.
- **Bağımlılıksız:** sadece Go standart kütüphanesi. `go 1.23+`.

## Kurulum

```bash
go get github.com/boracomet/go-pos
```

## Hızlı başlangıç

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
)

func main() {
	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{
			"iyzico": pos.IyzicoConfig{
				APIKey:    os.Getenv("IYZICO_API_KEY"),
				SecretKey: os.Getenv("IYZICO_SECRET_KEY"),
			},
		},
		Mode: core.ModeSandbox, // üretimde core.ModeProduction (varsayılan)
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := payment.CreatePayment(context.Background(), &core.PaymentRequest{
		Price:     "100.00",
		PaidPrice: "100.00",
		Currency:  "TRY",
		BasketID:  "SIPARIS-1001",
		PaymentCard: &core.PaymentCard{
			CardHolderName: "AHMET YILMAZ",
			CardNumber:     "5528790000000008",
			ExpireMonth:    "12",
			ExpireYear:     "2030",
			CVC:            "123",
		},
		Buyer: core.Buyer{
			ID:                  "MUSTERI-1",
			Name:                "Ahmet",
			Surname:             "Yılmaz",
			Email:               "ahmet@example.com",
			IdentityNumber:      "11111111111",
			RegistrationAddress: "Atatürk Cad. No:1",
			City:                "İstanbul",
			Country:             "Türkiye",
			IP:                  "203.0.113.10",
			GSMNumber:           "+905321234567",
		},
		BillingAddress: core.Address{
			ContactName: "Ahmet Yılmaz",
			City:        "İstanbul",
			Country:     "Türkiye",
			Address:     "Atatürk Cad. No:1",
		},
		ShippingAddress: core.Address{
			ContactName: "Ahmet Yılmaz",
			City:        "İstanbul",
			Country:     "Türkiye",
			Address:     "Atatürk Cad. No:1",
		},
		BasketItems: []core.BasketItem{
			{ID: "URUN-1", Name: "Kahve", Category1: "Gıda", ItemType: "PHYSICAL", Price: "100.00"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if result.Status == core.StatusSuccess {
		log.Println("ödeme alındı:", result.PaymentID)
	} else {
		log.Println("ödeme başarısız:", result.Code, result.ErrorMessage)
	}
}
```

Tutarlar **string** ve en fazla iki ondalık basamaklıdır (`"100.00"`). Float kullanılmaz; para hesabı `math/big` ile tam yapılır.

### Birden fazla sağlayıcı

```go
payment, err := pos.New(pos.Config{
	Providers: map[string]core.ProviderEntry{
		"iyzico": pos.IyzicoConfig{APIKey: key, SecretKey: secret},
		"paytr":  pos.PayTRConfig{MerchantID: id, MerchantKey: key, MerchantSalt: salt},
		"ziraat": pos.EstConfig{ClientID: clientID, Username: user, Password: pass},
	},
	DefaultProvider: "iyzico",
})

result, err := payment.CreatePayment(ctx, request) // iyzico
paytr, err := payment.Use("paytr")                 // hook'lardan geçen sağlayıcı
result, err := paytr.CreatePayment(ctx, request)
```

Map anahtarı serbesttir: `"odeme"`, `"tr"` gibi kendi kimliğini de kullanabilirsin. O kimlik handler URL'lerinde ve eklentilerde görünür. EST bankaları için anahtar bankayı seçer: `isbank`, `ziraat`, `halkbank`, `teb`, `sekerbank`, `akbankest`, `halkbankmkd`.

## 3D Secure

```go
init, err := payment.InitThreeDSPayment(ctx, &core.ThreeDSPaymentRequest{
	PaymentRequest: request, // CallbackURL zorunlu
})
// init.ThreeDSHTMLContent → tarayıcıya basılacak HTML
// init.RedirectURL        → sağlayıcı yönlendirmesi (varsa)

// Banka CallbackURL'e POST ettiğinde:
result, err := payment.CompleteThreeDSPayment(ctx, callbackData)
```

`callbackData` sağlayıcının gönderdiği form alanlarıdır (`map[string]string`, `url.Values` veya sağlayıcının kendi callback tipi). İmza/hash doğrulaması burada yapılır; sahte callback `INVALID_HASH` ile reddedilir.

## HTTP handler

```go
payment, err := pos.New(pos.Config{
	Providers: map[string]core.ProviderEntry{"iyzico": pos.IyzicoConfig{...}},
	Handler: &core.HandlerOptions{
		BasePath: "/api/pay",
		OnCallback: func(ctx context.Context, result *core.PaymentResponse, cctx core.CallbackContext) error {
			// Siparişini burada güncelle. Hata dönersen 500 döner ve
			// sağlayıcı bildirimi tekrar gönderir.
			return orders.MarkPaid(ctx, result.ConversationID, result.PaymentID)
		},
		CallbackRedirect: func(ctx context.Context, result *core.PaymentResponse, cctx core.CallbackContext) (string, error) {
			return "/siparis/" + result.ConversationID, nil // 303
		},
	},
})

http.Handle("/api/pay/", adapters.HTTPHandler(payment))
log.Fatal(http.ListenAndServe(":8080", nil))
```

Varsayılan olarak **yalnızca sağlayıcı callback'leri ve salt-okunur sorgular** açıktır (`payment/complete-3ds`, `callback`, `installment`, `bin-check`). İade, iptal, capture, kart yönetimi gibi ayrıcalıklı uçlar `AllowedActions` ile açılır ve bir `Authorize` hook'u zorunludur.

Tutarı, sepeti ve alıcıyı istemciden gelen body'ye güvenerek değil, sunucuda `TransformRequest` ile koy.

## Sağlayıcılar ve yetenekler

Her sağlayıcı ödeme, 3D Secure, iade ve iptali aynı API'den sunar. Aşağıdaki tablo **ek** yetenekleri ve bilinen sınırları gösterir; `—` o operasyonun `NOT_SUPPORTED` döndüğü anlamına gelir.

| Sağlayıcı | Ön provizyon¹ | Kart saklama | BIN | Taksit | Sorgulama |
|---|---|---|---|---|---|
| iyzico | ✅ | ✅ | ✅ | ✅ | ✅ |
| PayTR | —² | listele/sil | ✅ | ✅ | ✅ |
| ParamPOS | ✅ | — | ✅ | ✅ | ✅ |
| Sipay / QNBpay | ✅ | — | ✅ | ✅ | ✅ |
| Akbank (JSON) | ✅ | — | — | —⁴ | ✅ |
| Albaraka Türk | ✅ | — | — | — | ✅ |
| İş Bankası (EST) | — | — | — | — | ✅ |
| Ziraat (EST) | — | — | — | — | ✅ |
| Halkbank (EST) | — | — | — | — | ✅ |
| TEB (EST) | — | — | — | — | ✅ |
| Şekerbank (EST) | — | — | — | — | ✅ |
| Akbank EST | — | — | — | — | ✅ |
| Halkbank MKD | — | — | — | — | ✅ |
| Garanti BBVA | — | — | — | — | ✅ |
| Kuveyt Türk⁵ | — | — | — | — | ✅ |
| Yapı Kredi | — | — | — | — | ✅ |
| QNB Finansbank | — | — | — | — | ✅ |
| VakıfBank | — | — | — | — | —³ |
| Denizbank | — | — | — | — | —⁶ |

¹ `Authorize`, `InitThreeDSAuthorize`, `Capture`, `VoidAuthorization`.
² PayTR'nin ön provizyon API'si yoktur; bu metotlar `NOT_SUPPORTED` döner.
³ VakıfBank'ta ödeme sorgulama yoktur; `GetPayment` `NOT_SUPPORTED` döner.
⁴ Akbank Sanal POS API'sinde taksit oranı sorgusu yoktur. Taksit planı kendi Akbank sözleşmenden gelir: ödeme isteğinde `installment` gönder, `paidPrice`'ı sözleşmeli oranından hesapla.
⁵ Kuveyt Türk KT Pay Gate **yalnızca 3D Secure** işler. `CreatePayment` `NOT_SUPPORTED` döner; `InitThreeDSPayment` kullan.
⁶ Denizbank'ta sipariş durumu sorgusu henüz yoktur.

Desteklenmeyen bir operasyon sessizce başarılı olmaz, her zaman `NOT_SUPPORTED` hatası döner.

**Ek arayüzler:** iyzico checkout form, PWI ve abonelik (7 metot) sunar; PayTR token ile ödeme ve düz metin callback sunar. Bunlara `payment.CheckoutFormProvider("iyzico")`, `payment.SubscriptionProvider("iyzico")`, `payment.TokenPaymentProvider("paytr")` ile erişilir.

## Eklentiler

```go
rateLimit, err := plugins.RateLimit(plugins.RateLimitOptions{
	Key: func(r *core.HandlerRequest) (string, error) { return r.Headers["x-api-key"], nil },
})

payment, err := pos.New(pos.Config{
	Providers: map[string]core.ProviderEntry{...},
	Plugins:   []core.Plugin{rateLimit, plugins.Redact(plugins.RedactOptions{})},
})
```

| Eklenti | Ne yapar |
|---|---|
| `rate-limit` | Pencere başına istek sınırı, route bazlı kurallar |
| `fraud-rules` | Tutar, BIN, ülke, hız bazlı dolandırıcılık kuralları |
| `redact` | Kart ve kimlik verisini loglardan ve yanıtlardan maskeler |
| `notifications` | Ödeme/iade olaylarında bildirim gönderir |
| `webhook-forwarding` | Doğrulanmış callback'i kendi sistemine iletir |
| `pending-resolver` | Askıda kalan ödemeyi sorgulayıp sonuçlandırır |
| `three-ds-timeout` | Tamamlanmayan 3D Secure akışını süresi dolmuş sayar |
| `failover` | Sağlayıcı hatasında sıradaki sağlayıcıya geçer |
| `refund-approval` | İadeyi onay akışına bağlar |
| `currency-conversion` | Tutarı başka bir para birimine çevirir |
| `telemetry` | Operasyon süresi ve sonuç metrikleri |
| `i18n` | Türkçe/İngilizce hata ve sağlayıcı mesajları |
| `captcha` | Ödeme öncesi captcha doğrulaması |
| `chaos` | Test için gecikme ve hata enjekte eder |
| `mcp` | Ödeme API'sini MCP aracı olarak açar |
| `openapi` | Handler uçları için OpenAPI şeması üretir |

Eklentiler kimlikle de kurulabilir: `plugins.BuildPlugin("rate-limit", options)`.

## Yönlendirme

BIN, kart markası, taksit, tutar aralığı ve ağırlığa göre sağlayıcı seçer; aynı siparişin devam operasyonlarını (iade, capture, iptal) ilk ödemeyi yapan sağlayıcıya bağlar.

```go
router, err := routing.New(routing.Options{ /* kurallar */ })
payment, err := pos.New(pos.Config{
	Providers: map[string]core.ProviderEntry{...},
	Plugins:   []core.Plugin{router.Plugin()},
})
```

`commissionrouter` ise komisyon oranı tablosundan en ucuz sağlayıcıyı hesaplar. Yüzde hesabı `big.Int` ile 10⁴ ölçekli ve yarı-yukarı yuvarlamalıdır; kuruş kaybı olmaz.

## Test araçları

```go
mock := testing.NewMockProvider()
payment, err := pos.New(pos.Config{
	Providers: map[string]core.ProviderEntry{"mock": mock},
})

req := testing.FixturePaymentRequest()
req.PaymentCard.CardNumber = testing.CardInsufficientFunds
result, err := payment.CreatePayment(ctx, &req)
// result.Code == core.CodeInsufficientFunds
```

Mock sağlayıcı ağa çıkmaz, callback'leri kendi rastgele anahtarıyla imzalar (başka instance'tan gelen callback sahte sayılır). Sihirli kartlar: başarı, reddedildi, yetersiz bakiye, süresi dolmuş, hatalı CVC, fraud, limit, 3D Secure zorunlu, 3D Secure başarısız, ağ hatası, iade başarısız.

## Hatalar

```go
result, err := payment.CreatePayment(ctx, request)

var validation *core.ValidationError
if errors.As(err, &validation) {
	for _, issue := range validation.Issues {
		log.Printf("%s: %s", issue.Path, issue.Message)
	}
}

var base *core.BaseError
if errors.As(err, &base) {
	log.Println(base.Code, base.Provider)
}
```

Sağlayıcı başarısızlığı **hata değil, sonuçtur**: `err == nil` ve `result.Status == core.StatusFailure`. `result.Code` normalize edilmiş koddur (`INSUFFICIENT_FUNDS`, `THREEDS_FAILED`, `INVALID_HASH`, …), `result.ErrorCode` bankanın ham kodu. ISO 8583 yanıt kodları da aynı tabloya eşlenir.

## Güvenlik

- İmza ve hash karşılaştırması **sabit zamanlıdır** (`core.SafeEqual`).
- Logger sözleşmesi istek gövdesi ve header loglamayı yasaklar: kart verisi ve kimlik bilgisi taşır. `redact` eklentisi maskelemeyi üstlenir.
- **Ödeme, iade ve iptal asla yeniden denenmez.** Yalnızca okuma sorguları retry edilir.
- Callback'ler doğrulanmadan hiçbir sonuç üretilmez; sahte bildirim `INVALID_HASH` ile düşer.
- Handler'da ayrıcalıklı uçlar `Authorize` hook'u olmadan açılamaz.
- `rawResponse` varsayılan olarak HTTP yanıtına konmaz (komisyon oranı, fraud durumu ve saklı kart anahtarı içerebilir). Hook'lar ve olaylar onu her zaman görür.
- `Idempotency-Key` ve callback tekilleştirmesi varsayılan olarak açıktır.

## Paketler

| Paket | İçerik |
|---|---|
| `pos` | `Payment` nesnesi, kurulum, operasyonlar, sağlayıcı fabrikaları |
| `core` | Tipler, hatalar, hata kodları, HTTP, doğrulama, kripto, handler, olaylar |
| `providers/*` | 13 sağlayıcı implementasyonu |
| `plugins` | 16 eklenti ve kimlik tabanlı kayıt |
| `routing` | Kural tabanlı sağlayıcı yönlendirme |
| `commissionrouter` | Komisyon oranına göre yönlendirme |
| `adapters` | `net/http` bağdaştırıcıları |
| `client` | Handler API'sini çağıran istemci; üye işyeri kimlik bilgilerini görmez |
| `testing` | Mock sağlayıcı, sihirli kartlar, fixture'lar, test sunucusu |

## Örnekler

```bash
go run ./examples/plugins
```

| Örnek | Gösterdiği | Ağ |
|---|---|---|
| `custom-provider` | Kendi sağlayıcını ekleme: `BaseProvider`, imza, hata kodu tablosu, ağ hatasında `PENDING` | yok |
| `plugins` | Before hook ile yönlendirme, olay dinleyici, eklenti üyeleri ve hata kodları | yok |
| `i18n` | Çok dilli hata mesajları; cookie, header ve URL'den dil algılama | yok |
| `frameworks` | Handler'ı `net/http`'ye ve middleware olarak bağlama | sunucu başlatır |
| `installment-inquiry` | BIN'e göre taksit sorgulama, kart karşılaştırma, faiz analizi | iyzico sandbox |
| `pwi-payment` | Korumalı havale/EFT: başlatma, IBAN, durum sorgulama, callback | iyzico sandbox |

Son ikisi `IYZICO_API_KEY` ve `IYZICO_SECRET_KEY` ister. İlk üçü çevrimdışı çalışır.

## Lisans

MIT — bkz. [LICENSE](LICENSE).
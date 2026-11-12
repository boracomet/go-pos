// Command i18n shows customer-facing error messages in the customer's language:
// the built-in dictionaries, an override, an extra language, and detection from
// a cookie, the Accept-Language header or the request URL.
//
//	go run ./examples/i18n
package main

import (
	"fmt"
	"log"
	"net/url"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/core"
	"github.com/boracomet/go-pos/plugins"
)

func main() {
	// The built-in dictionaries are copies, so overriding one is safe.
	translations := plugins.I18nLocales()
	for i := range translations {
		if translations[i].Locale == "tr" {
			translations[i].Messages[string(core.CodeInsufficientFunds)] = "Bakiyeniz yetersiz."
		}
	}
	translations = append(translations, plugins.I18nTranslation{
		Locale:   "es",
		Messages: plugins.I18nDictionary{string(core.CodeInsufficientFunds): "Fondos insuficientes."},
	})

	i18n, err := plugins.I18n(plugins.I18nOptions{
		Translations:  translations,
		DefaultLocale: "tr",
		Detection: []plugins.I18nDetectionStrategy{
			plugins.I18nDetectCookie,
			plugins.I18nDetectHeader,
			plugins.I18nDetectCallback,
		},
		LocaleCookie: "lang",
		GetLocale: func(request *core.HandlerRequest) (string, error) {
			parsed, err := url.Parse(request.URL)
			if err != nil {
				return "", err
			}
			return parsed.Query().Get("lang"), nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	payment, err := pos.New(pos.Config{
		Providers: map[string]core.ProviderEntry{
			"iyzico": pos.IyzicoConfig{APIKey: "key", SecretKey: "secret"},
		},
		Plugins: []core.Plugin{i18n},
	})
	if err != nil {
		log.Fatal(err)
	}

	// The plugin adds one member, "errors".
	member, ok := payment.Method("errors")
	if !ok {
		log.Fatal("i18n eklentisi 'errors' üyesini eklemedi")
	}
	errorsAPI := member.(*plugins.I18nMethods)

	fmt.Println("EXPIRED_CARD (en):", errorsAPI.Message(string(core.CodeExpiredCard), "en"))
	fmt.Println("INSUFFICIENT_FUNDS (tr):", errorsAPI.Message(string(core.CodeInsufficientFunds), "tr"))
	fmt.Println("INSUFFICIENT_FUNDS (es):", errorsAPI.Message(string(core.CodeInsufficientFunds), "es"))
	fmt.Println("diller:", errorsAPI.Locales())

	language, err := errorsAPI.DetectLocale(&core.HandlerRequest{
		Method:  "GET",
		URL:     "/",
		Headers: map[string]string{"accept-language": "de-DE"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("algılanan dil:", language)

	// A failed result is translated in place, code by code.
	translated := errorsAPI.Translate(&core.PaymentResponse{
		Status:       core.StatusFailure,
		Code:         core.CodeInsufficientFunds,
		ErrorMessage: "Insufficient funds.",
	}, "es")
	fmt.Println("çevrilmiş yanıt:", translated.(*core.PaymentResponse).ErrorMessage)
}

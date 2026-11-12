package plugins

// I18nDictionary holds the customer-facing message of every error code in one
// language: the normalized codes and the codes declared by plugins.
type I18nDictionary map[string]string

// ErrorMessageDictionary is the former name of I18nDictionary.
//
// Deprecated: use I18nDictionary.
type ErrorMessageDictionary = I18nDictionary

// I18nLocaleEN is the built-in English message of every normalized error code.
var I18nLocaleEN = I18nDictionary{
	"INSUFFICIENT_FUNDS":    "Your card has insufficient funds. Try another card.",
	"CARD_DECLINED":         "Your bank declined the payment. Contact your bank or try another card.",
	"INVALID_CARD":          "Check your card number.",
	"EXPIRED_CARD":          "Check your card's expiry date.",
	"INVALID_CVC":           "Check the security code on the back of your card.",
	"THREEDS_FAILED":        "Verification failed. Try again and complete the SMS step.",
	"FRAUD_SUSPECTED":       "We couldn't complete this payment. Contact your bank.",
	"LIMIT_EXCEEDED":        "Your card's limit was exceeded. Try another card.",
	"DUPLICATE_ORDER":       "This order was already submitted.",
	"CANCELLED_BY_CUSTOMER": "Payment was cancelled.",
	"INVALID_REQUEST":       "Something went wrong. Please try again.",
	"NETWORK_ERROR":         "We're checking your payment. Don't pay again.",
	"INVALID_HASH":          "Payment could not be verified.",
	"PROVIDER_ERROR":        "Payment is temporarily unavailable. Please try again later.",
	"UNKNOWN":               "Payment failed. Please try again or use another card.",
}

// I18nLocaleTR is the built-in Turkish message of every normalized error code.
var I18nLocaleTR = I18nDictionary{
	"INSUFFICIENT_FUNDS":    "Kartınızın bakiyesi veya limiti yetersiz. Başka bir kart deneyin.",
	"CARD_DECLINED":         "Bankanız ödemeyi onaylamadı. Bankanızla görüşün veya başka bir kart deneyin.",
	"INVALID_CARD":          "Kart numaranızı kontrol edin.",
	"EXPIRED_CARD":          "Kartınızın son kullanma tarihini kontrol edin.",
	"INVALID_CVC":           "Kartınızın arkasındaki güvenlik kodunu kontrol edin.",
	"THREEDS_FAILED":        "Doğrulama başarısız oldu. Tekrar deneyin ve SMS adımını tamamlayın.",
	"FRAUD_SUSPECTED":       "Bu ödeme tamamlanamadı. Bankanızla görüşün.",
	"LIMIT_EXCEEDED":        "Kartınızın işlem limiti aşıldı. Başka bir kart deneyin.",
	"DUPLICATE_ORDER":       "Bu sipariş zaten gönderildi.",
	"CANCELLED_BY_CUSTOMER": "Ödeme iptal edildi.",
	"INVALID_REQUEST":       "Bir sorun oluştu. Lütfen tekrar deneyin.",
	"NETWORK_ERROR":         "Ödemeniz kontrol ediliyor. Lütfen tekrar ödeme yapmayın.",
	"INVALID_HASH":          "Ödeme doğrulanamadı.",
	"PROVIDER_ERROR":        "Ödeme geçici olarak yapılamıyor. Lütfen daha sonra tekrar deneyin.",
	"UNKNOWN":               "Ödeme başarısız oldu. Tekrar deneyin veya başka bir kart kullanın.",
}

// I18nLocaleDE is the built-in German message of every normalized error code.
var I18nLocaleDE = I18nDictionary{
	"INSUFFICIENT_FUNDS":    "Ihre Karte ist nicht ausreichend gedeckt.",
	"CARD_DECLINED":         "Ihre Bank hat die Zahlung abgelehnt. Wenden Sie sich an Ihre Bank oder versuchen Sie eine andere Karte.",
	"INVALID_CARD":          "Überprüfen Sie Ihre Kartennummer.",
	"EXPIRED_CARD":          "Überprüfen Sie das Ablaufdatum Ihrer Karte.",
	"INVALID_CVC":           "Überprüfen Sie den Sicherheitscode auf der Rückseite Ihrer Karte.",
	"THREEDS_FAILED":        "Verifizierung fehlgeschlagen. Versuchen Sie es erneut und schließen Sie den SMS-Schritt ab.",
	"FRAUD_SUSPECTED":       "Diese Zahlung konnte nicht abgeschlossen werden. Wenden Sie sich an Ihre Bank.",
	"LIMIT_EXCEEDED":        "Das Limit Ihrer Karte wurde überschritten. Versuchen Sie eine andere Karte.",
	"DUPLICATE_ORDER":       "Diese Bestellung wurde bereits aufgegeben.",
	"CANCELLED_BY_CUSTOMER": "Die Zahlung wurde abgebrochen.",
	"INVALID_REQUEST":       "Etwas ist schiefgelaufen. Bitte versuchen Sie es erneut.",
	"NETWORK_ERROR":         "Wir überprüfen Ihre Zahlung. Bitte zahlen Sie nicht erneut.",
	"INVALID_HASH":          "Die Zahlung konnte nicht verifiziert werden.",
	"PROVIDER_ERROR":        "Die Zahlung ist vorübergehend nicht verfügbar. Bitte versuchen Sie es später erneut.",
	"UNKNOWN":               "Zahlung fehlgeschlagen. Bitte versuchen Sie es erneut oder verwenden Sie eine andere Karte.",
}

// I18nLocaleRU is the built-in Russian message of every normalized error code.
var I18nLocaleRU = I18nDictionary{
	"INSUFFICIENT_FUNDS":    "На вашей карте недостаточно средств. Попробуйте другую карту.",
	"CARD_DECLINED":         "Ваш банк отклонил платеж. Свяжитесь с банком или попробуйте другую карту.",
	"INVALID_CARD":          "Проверьте номер карты.",
	"EXPIRED_CARD":          "Проверьте срок действия карты.",
	"INVALID_CVC":           "Проверьте код безопасности на обратной стороне карты.",
	"THREEDS_FAILED":        "Не удалось подтвердить операцию. Попробуйте снова и введите код из SMS.",
	"FRAUD_SUSPECTED":       "Не удалось завершить этот платеж. Свяжитесь со своим банком.",
	"LIMIT_EXCEEDED":        "Превышен лимит по вашей карте. Попробуйте другую карту.",
	"DUPLICATE_ORDER":       "Этот заказ уже был оформлен.",
	"CANCELLED_BY_CUSTOMER": "Платеж был отменен.",
	"INVALID_REQUEST":       "Что-то пошло не так. Пожалуйста, попробуйте еще раз.",
	"NETWORK_ERROR":         "Мы проверяем ваш платеж. Пожалуйста, не оплачивайте повторно.",
	"INVALID_HASH":          "Не удалось проверить подлинность платежа.",
	"PROVIDER_ERROR":        "Оплата временно недоступна. Пожалуйста, повторите попытку позже.",
	"UNKNOWN":               "Не удалось совершить платеж. Пожалуйста, попробуйте снова или используйте другую карту.",
}

// I18nLocaleAR is the built-in Arabic (Modern Standard Arabic) message of every
// normalized error code.
var I18nLocaleAR = I18nDictionary{
	"INSUFFICIENT_FUNDS":    "لا يوجد رصيد كافٍ في بطاقتك. جرّب بطاقة أخرى.",
	"CARD_DECLINED":         "رفض البنك عملية الدفع. تواصل مع البنك أو جرّب بطاقة أخرى.",
	"INVALID_CARD":          "تحقّق من رقم بطاقتك.",
	"EXPIRED_CARD":          "تحقّق من تاريخ انتهاء صلاحية بطاقتك.",
	"INVALID_CVC":           "تحقّق من رمز الأمان الموجود على ظهر بطاقتك.",
	"THREEDS_FAILED":        "فشل التحقق. حاول مرة أخرى وأكمل خطوة الرسالة النصية.",
	"FRAUD_SUSPECTED":       "تعذّر إتمام عملية الدفع هذه. تواصل مع البنك.",
	"LIMIT_EXCEEDED":        "تم تجاوز حد بطاقتك. جرّب بطاقة أخرى.",
	"DUPLICATE_ORDER":       "تم إرسال هذا الطلب مسبقًا.",
	"CANCELLED_BY_CUSTOMER": "تم إلغاء عملية الدفع.",
	"INVALID_REQUEST":       "حدث خطأ ما. يُرجى المحاولة مرة أخرى.",
	"NETWORK_ERROR":         "نتحقق من حالة الدفع. لا تدفع مرة أخرى.",
	"INVALID_HASH":          "تعذّر التحقق من عملية الدفع.",
	"PROVIDER_ERROR":        "الدفع غير متاح مؤقتًا. يُرجى المحاولة لاحقًا.",
	"UNKNOWN":               "فشلت عملية الدفع. حاول مرة أخرى أو استخدم بطاقة أخرى.",
}

// I18nLocales returns the built-in translations in their built-in order:
// en, tr, de, ru, ar. The dictionaries are copies, so editing them leaves the
// built-in messages untouched.
func I18nLocales() []I18nTranslation {
	builtIn := []struct {
		locale   string
		messages I18nDictionary
	}{
		{"en", I18nLocaleEN},
		{"tr", I18nLocaleTR},
		{"de", I18nLocaleDE},
		{"ru", I18nLocaleRU},
		{"ar", I18nLocaleAR},
	}
	translations := make([]I18nTranslation, 0, len(builtIn))
	for _, locale := range builtIn {
		translations = append(translations, I18nTranslation{
			Locale:   locale.locale,
			Messages: i18nCopyDictionary(locale.messages),
		})
	}
	return translations
}

// ErrorMessages returns the built-in translations.
//
// Deprecated: use I18nLocales.
func ErrorMessages() []I18nTranslation { return I18nLocales() }

func i18nCopyDictionary(messages I18nDictionary) I18nDictionary {
	copied := make(I18nDictionary, len(messages))
	for code, text := range messages {
		copied[code] = text
	}
	return copied
}

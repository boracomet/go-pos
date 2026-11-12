package sipay

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/boracomet/go-pos/core"
)

// Endpoints, relative to the provider base URL.
const (
	EndpointToken          = "/ccpayment/api/token"
	EndpointPay2D          = "/ccpayment/api/paySmart2D"
	EndpointPay3D          = "/ccpayment/api/paySmart3D"
	EndpointCheckStatus    = "/ccpayment/api/checkstatus"
	EndpointRefund         = "/ccpayment/api/refund"
	EndpointConfirmPayment = "/ccpayment/api/confirmPayment"
	EndpointGetPos         = "/ccpayment/api/getpos"
)

// SuccessCode is the application status code of a successful request.
const SuccessCode = 100

// confirmPayment status: capture or release a pre-authorization.
const (
	ConfirmStatusCapture = 1
	ConfirmStatusVoid    = 2
)

var (
	urlEncodedRe = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)
	binRe        = regexp.MustCompile(`^\d{6,8}$`)
	twoDigitsRe  = regexp.MustCompile(`^\d{2}$`)
	fourDigitsRe = regexp.MustCompile(`^\d{4}$`)
	inputRe      = regexp.MustCompile(`(?i)<input\b[^>]*>`)
	inputNameRe  = regexp.MustCompile(`(?i)\bname\s*=\s*["']([^"']*)["']`)
	inputValueRe = regexp.MustCompile(`(?i)\bvalue\s*=\s*["']([^"']*)["']`)
	entityRe     = regexp.MustCompile(`(?i)&(#x[0-9a-f]+|#\d+|[a-z]+);`)
)

var htmlEntities = map[string]string{
	"quot": "\"",
	"apos": "'",
	"lt":   "<",
	"gt":   ">",
	"amp":  "&",
}

func sha1Hex(value string) (string, error) {
	sum, err := core.Digest(core.SHA1, core.UTF8(value))
	if err != nil {
		return "", err
	}
	return core.ToHex(sum), nil
}

func sha256Hex(value string) (string, error) {
	sum, err := core.Digest(core.SHA256, core.UTF8(value))
	if err != nil {
		return "", err
	}
	return core.ToHex(sum), nil
}

// hashKeyOf derives the AES-256 key: the first 32 hex characters of
// SHA-256(SHA-1(appSecret) + salt). The characters are used as raw bytes, so the
// hex string itself is the 32-byte key.
func hashKeyOf(appSecret, salt string) (string, error) {
	inner, err := sha1Hex(appSecret)
	if err != nil {
		return "", err
	}
	outer, err := sha256Hex(inner + salt)
	if err != nil {
		return "", err
	}
	return outer[:32], nil
}

// CreateHashKey encrypts the "|"-joined fields with AES-256-CBC and returns
// "iv:salt:ciphertext" with "/" written as "__". A random IV and salt make every
// hash key different.
func CreateHashKey(fields []string, appSecret string) (string, error) {
	iv, err := core.RandomHex(8)
	if err != nil {
		return "", err
	}
	salt, err := core.RandomHex(2)
	if err != nil {
		return "", err
	}
	key, err := hashKeyOf(appSecret, salt)
	if err != nil {
		return "", err
	}
	encrypted, err := core.AESCBCEncrypt(core.UTF8(key), core.UTF8(iv), core.UTF8(strings.Join(fields, "|")))
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(iv+":"+salt+":"+core.ToBase64(encrypted), "/", "__"), nil
}

// DecryptHashKey returns the "|"-separated fields of a hash key, or nil when it
// was not made with this app secret.
func DecryptHashKey(hashKey, appSecret string) []string {
	if hashKey == "" {
		return nil
	}
	value := strings.TrimSpace(hashKey)
	// Accept a value that is still URL-encoded, or whose "+" a form parser
	// turned into " ".
	if urlEncodedRe.MatchString(value) {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return nil
		}
		value = decoded
	}
	value = strings.ReplaceAll(value, " ", "+")
	value = strings.ReplaceAll(value, "__", "/")
	parts := strings.Split(value, ":")
	if len(parts) < 3 {
		return nil
	}
	iv, salt, encrypted := parts[0], parts[1], parts[2]
	if iv == "" || salt == "" || encrypted == "" || len(iv) != 16 {
		return nil
	}
	key, err := hashKeyOf(appSecret, salt)
	if err != nil {
		return nil
	}
	ciphertext, err := core.Base64ToBytes(encrypted)
	if err != nil {
		return nil
	}
	plain, err := core.AESCBCDecrypt(core.UTF8(key), core.UTF8(iv), ciphertext)
	if err != nil || !utf8.Valid(plain) {
		return nil
	}
	return strings.Split(string(plain), "|")
}

// VerifyPaymentHash verifies the hash key of a payment result (3D Secure
// callback, paySmart2D response): status|total|invoice_id|order_id|currency_code.
// It returns nil when the hash key is missing, invalid or too short.
func VerifyPaymentHash(hashKey, appSecret string) *VerifiedHash {
	fields := DecryptHashKey(hashKey, appSecret)
	if len(fields) < 5 {
		return nil
	}
	return &VerifiedHash{
		Status:    fields[0],
		Total:     fields[1],
		InvoiceID: fields[2],
		OrderID:   fields[3],
		Currency:  fields[4],
	}
}

// CreatePaymentHash signs a payment (paySmart2D, paySmart3D):
// total|installment|currency_code|merchant_key|invoice_id.
func CreatePaymentHash(total string, installment int, currency, merchantKey, invoiceID, appSecret string) (string, error) {
	return CreateHashKey([]string{total, strconv.Itoa(installment), currency, merchantKey, invoiceID}, appSecret)
}

// CreateStatusHash signs a checkstatus request: invoice_id|merchant_key.
func CreateStatusHash(invoiceID, merchantKey, appSecret string) (string, error) {
	return CreateHashKey([]string{invoiceID, merchantKey}, appSecret)
}

// CreateRefundHash signs a refund: amount|invoice_id|merchant_key.
func CreateRefundHash(amount, invoiceID, merchantKey, appSecret string) (string, error) {
	return CreateHashKey([]string{amount, invoiceID, merchantKey}, appSecret)
}

// CreateConfirmHash signs a confirmPayment request: merchant_key|invoice_id|status.
func CreateConfirmHash(merchantKey, invoiceID string, status int, appSecret string) (string, error) {
	return CreateHashKey([]string{merchantKey, invoiceID, strconv.Itoa(status)}, appSecret)
}

// FormatAmount renders an amount with two decimals, as sent and signed:
// 10 -> "10.00".
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

// GetCurrency returns the currency code: TRY, USD, EUR or GBP.
func GetCurrency(currency string) (string, error) {
	code := strings.ToUpper(currency)
	if code == "" {
		code = "TRY"
	}
	if code == "TL" {
		code = "TRY"
	}
	switch code {
	case "TRY", "USD", "EUR", "GBP":
		return code, nil
	}
	return "", core.NewValidationError("Currency "+currency+" is not supported by Sipay", "", nil)
}

// FormatInstallment returns the installment count, 1 to 12 (1 for a single payment).
func FormatInstallment(installment int) (int, error) {
	count := max(1, installment)
	if count > 12 {
		return 0, core.NewValidationError("Sipay supports 1 to 12 installments, got "+strconv.Itoa(installment), "", nil)
	}
	return count, nil
}

// FormatExpiry returns the card expiry as a two-digit month and four-digit year.
func FormatExpiry(month, year string) (string, string, error) {
	mm := month
	if len(mm) < 2 {
		mm = strings.Repeat("0", 2-len(mm)) + mm
	}
	yyyy := year
	if len(year) == 2 {
		yyyy = "20" + year
	}
	monthNumber, _ := strconv.Atoi(mm)
	if !twoDigitsRe.MatchString(mm) || monthNumber < 1 || monthNumber > 12 || !fourDigitsRe.MatchString(yyyy) {
		return "", "", core.NewValidationError("Invalid card expiry: "+month+"/"+year, "", nil)
	}
	return mm, yyyy, nil
}

// BuildItems builds the items of a payment. Sipay rejects items whose prices do
// not add up to total; when the basket does not (paidPrice includes interest or
// a discount was applied), a single item for the whole order is sent instead.
func BuildItems(basketItems []core.BasketItem, total, description string) ([]Item, error) {
	items := make([]Item, 0, len(basketItems))
	var sum int64
	for _, item := range basketItems {
		price, err := FormatAmount(item.Price)
		if err != nil {
			return nil, err
		}
		minor, err := core.ToMinorUnits(price, "amount")
		if err != nil {
			return nil, err
		}
		sum += minor
		items = append(items, Item{
			Name:        item.Name,
			Price:       price,
			Quantity:    1,
			Description: firstNonEmpty(item.Category1, item.Name),
		})
	}
	totalMinor, err := core.ToMinorUnits(total, "amount")
	if err != nil {
		return nil, err
	}
	if len(items) > 0 && sum == totalMinor {
		return items, nil
	}
	return []Item{{Name: description, Price: total, Quantity: 1, Description: description}}, nil
}

// GetLanguage returns the language of Sipay's messages: "tr" or "en".
func GetLanguage(locale string) string {
	if locale == "" {
		locale = "tr"
	}
	if strings.HasPrefix(strings.ToLower(locale), "en") {
		return "en"
	}
	return "tr"
}

// decodeHTML decodes HTML entities in one pass, so decoded text is never decoded
// again.
func decodeHTML(value string) string {
	return entityRe.ReplaceAllStringFunc(value, func(entity string) string {
		code := entity[1 : len(entity)-1]
		if code[0] != '#' {
			if decoded, ok := htmlEntities[strings.ToLower(code)]; ok {
				return decoded
			}
			return entity
		}
		var point int64
		var err error
		if code[1] == 'x' || code[1] == 'X' {
			point, err = strconv.ParseInt(code[2:], 16, 64)
		} else {
			point, err = strconv.ParseInt(code[1:], 10, 64)
		}
		if err != nil || point > 0x10ffff {
			return entity
		}
		return string(rune(point))
	})
}

// ParseForm returns the hidden inputs of an HTML form. paySmart3D answers a
// rejected request with a form that posts the error to cancel_url.
func ParseForm(html string) map[string]string {
	fields := map[string]string{}
	for _, tag := range inputRe.FindAllString(html, -1) {
		name := inputNameRe.FindStringSubmatch(tag)
		if name == nil || name[1] == "" {
			continue
		}
		value := ""
		if match := inputValueRe.FindStringSubmatch(tag); match != nil {
			value = match[1]
		}
		fields[decodeHTML(name[1])] = decodeHTML(value)
	}
	return fields
}

// MapTransactionStatus maps checkstatus transaction_status to the unified
// status. A sale that was partly refunded is still a success; a fully refunded
// or voided one is cancelled. "Pre-Authorization" is an approved
// pre-authorization (the amount is blocked).
func MapTransactionStatus(transactionStatus string) core.PaymentStatus {
	switch strings.ToLower(strings.TrimSpace(transactionStatus)) {
	case "completed", "partial refund", "partially_refund", "pre-authorization":
		return core.StatusSuccess
	case "refunded", "refund", "cancelled", "canceled", "void":
		return core.StatusCancelled
	case "failed", "fail":
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

// stripSpaces removes every whitespace character, not only ASCII spaces.
func stripSpaces(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

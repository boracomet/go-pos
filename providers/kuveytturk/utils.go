package kuveytturk

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// KT Pay Gate endpoints, relative to the provider base URL.
const (
	EndpointPayment         = "/KTPay/Payment"
	EndpointProvision       = "/KTPay/Provision"
	EndpointSaleReversal    = "/KTPay/SaleReversal"
	EndpointGetTransaction  = "/KTPay/GetTransaction"
	EndpointGetTransactions = "/KTPay/GetTransactions"
)

// SaleReversalType selects what SaleReversal does with a sale.
type SaleReversalType string

const (
	// SaleReversalCancel voids a sale the same day, before end of day.
	SaleReversalCancel SaleReversalType = "Cancel"
	// SaleReversalDrawback refunds the full amount after end of day.
	SaleReversalDrawback SaleReversalType = "Drawback"
	// SaleReversalPartialDrawback refunds part of the amount after end of day.
	SaleReversalPartialDrawback SaleReversalType = "PartialDrawback"
)

// HashPassword returns base64(SHA1(password)), the key every request is signed with.
func HashPassword(password string) (string, error) {
	sum, err := core.Digest(core.SHA1, []byte(password))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// ComputeHash returns base64(HMAC-SHA512(data, key)), key being the hashed password.
func ComputeHash(data, key string) (string, error) {
	mac, err := core.HMAC(core.SHA512, []byte(key), []byte(data))
	if err != nil {
		return "", err
	}
	return core.ToBase64(mac), nil
}

func sign(parts []string, password string) (string, error) {
	hashPassword, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	return ComputeHash(strings.Join(parts, "")+hashPassword, hashPassword)
}

// CreatePaymentHash signs the Payment (card verification) request:
// MerchantId + MerchantOrderId + Amount + SuccessUrl + FailUrl + UserName + hashPassword.
func CreatePaymentHash(credentials HashCredentials, merchantOrderID, amount, successURL, failURL string) (string, error) {
	return sign([]string{
		credentials.MerchantID,
		merchantOrderID,
		amount,
		successURL,
		failURL,
		credentials.Username,
	}, credentials.Password)
}

// CreateProvisionHash signs Provision:
// MerchantId + MerchantOrderId + Amount + UserName + hashPassword.
func CreateProvisionHash(credentials HashCredentials, merchantOrderID, amount string) (string, error) {
	return sign([]string{credentials.MerchantID, merchantOrderID, amount, credentials.Username}, credentials.Password)
}

// CreateSaleReversalHash signs a sale reversal:
// MerchantId + MerchantOrderId + Amount + UserName + hashPassword.
// Amount is signed as "0" for Cancel and Drawback; only PartialDrawback signs the amount.
func CreateSaleReversalHash(credentials HashCredentials, merchantOrderID string, reversalType SaleReversalType, amount string) (string, error) {
	if reversalType != SaleReversalPartialDrawback || amount == "" {
		amount = "0"
	}
	return sign([]string{credentials.MerchantID, merchantOrderID, amount, credentials.Username}, credentials.Password)
}

// CreateGetTransactionHash signs GetTransaction: MerchantId + UserName + hashPassword.
func CreateGetTransactionHash(credentials HashCredentials) (string, error) {
	return sign([]string{credentials.MerchantID, credentials.Username}, credentials.Password)
}

// CreateGetTransactionsHash signs GetTransactions:
// MerchantId + MerchantOrderId + UserName + hashPassword.
func CreateGetTransactionsHash(credentials HashCredentials, merchantOrderID string) (string, error) {
	return sign([]string{credentials.MerchantID, merchantOrderID, credentials.Username}, credentials.Password)
}

// FormatAmount renders an amount in minor units without separators:
// 102.65 TL -> "10265", 1 TL -> "100".
func FormatAmount(amount string) (string, error) {
	minor, err := core.ToMinorUnits(amount, "")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(minor, 10), nil
}

// CurrencyCode returns the four-digit currency code: TRY "0949", USD "0840", EUR "0978".
// An empty currency means TRY; any other unsupported currency is rejected.
func CurrencyCode(currency string) (string, error) {
	switch strings.ToUpper(orDefault(currency, "TRY")) {
	case "TRY", "TL":
		return "0949", nil
	case "USD":
		return "0840", nil
	case "EUR":
		return "0978", nil
	}
	return "", core.NewValidationError(
		fmt.Sprintf("Currency %s is not supported by Kuveyt Türk", currency), providerName, nil)
}

// FormatExpiry returns the card expiry as two-digit month and year ("01", "25").
func FormatExpiry(month, year string) (Expiry, error) {
	mm := padStart(month, 2)
	yy := padStart(year, 2)
	if len(year) == 4 {
		yy = year[len(year)-2:]
	}
	invalid := core.NewValidationError(fmt.Sprintf("Invalid card expiry: %s/%s", month, year), providerName, nil)
	if !twoDigitRe.MatchString(mm) || !twoDigitRe.MatchString(yy) {
		return Expiry{}, invalid
	}
	m, err := strconv.Atoi(mm)
	if err != nil || m < 1 || m > 12 {
		return Expiry{}, invalid
	}
	return Expiry{Month: mm, Year: yy}, nil
}

// FormatInstallment returns the installment count, 1 to 12 (1 for a single payment).
// A count below 1 means a single payment.
func FormatInstallment(installment int) (int, error) {
	count := installment
	if count < 1 {
		count = 1
	}
	if count > 12 {
		return 0, core.NewValidationError(
			fmt.Sprintf("Kuveyt Türk supports 1 to 12 installments, got %d", installment), providerName, nil)
	}
	return count, nil
}

// Language returns the language code of a locale: TR 1, EN 2.
func Language(locale string) int {
	if strings.HasPrefix(strings.ToLower(orDefault(locale, "tr")), "en") {
		return 2
	}
	return 1
}

// ParsePhone splits a Turkish GSM number into country code and subscriber number:
// "+905350000000", "05350000000" or "5350000000" -> {"90", "5350000000"}.
// Other countries' numbers cannot be split reliably and are rejected.
func ParsePhone(gsmNumber string) (Phone, error) {
	digits := nonDigitRe.ReplaceAllString(gsmNumber, "")
	switch {
	case len(digits) == 10:
		return Phone{CC: "90", Subscriber: digits}, nil
	case len(digits) == 11 && strings.HasPrefix(digits, "0"):
		return Phone{CC: "90", Subscriber: digits[1:]}, nil
	case len(digits) == 12 && strings.HasPrefix(digits, "90"):
		return Phone{CC: "90", Subscriber: digits[2:]}, nil
	}
	return Phone{}, core.NewValidationError(fmt.Sprintf(
		"Kuveyt Türk needs a Turkish GSM number (+90 5xx xxx xx xx), got: %s", gsmNumber), providerName, nil)
}

// ParseForm returns the hidden inputs of the auto-submitting form the Payment
// endpoint answers with, HTML entities decoded.
func ParseForm(html string) map[string]string {
	fields := make(map[string]string)
	for _, tag := range inputTagRe.FindAllString(html, -1) {
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

// StatusCode returns the number in a status label such as "Başarısız (2)".
func StatusCode(label string) (int, bool) {
	match := statusCodeRe.FindStringSubmatch(label)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

var (
	twoDigitRe   = regexp.MustCompile(`^\d{2}$`)
	nonDigitRe   = regexp.MustCompile(`\D`)
	whitespaceRe = regexp.MustCompile(`\s`)
	inputTagRe   = regexp.MustCompile(`(?i)<input\b[^>]*>`)
	inputNameRe  = regexp.MustCompile(`(?i)\bname\s*=\s*"([^"]*)"`)
	inputValueRe = regexp.MustCompile(`(?i)\bvalue\s*=\s*"([^"]*)"`)
	statusCodeRe = regexp.MustCompile(`\((\d+)\)\s*$`)
	hexEntityRe  = regexp.MustCompile(`(?i)&#x([0-9a-f]+);`)
	decEntityRe  = regexp.MustCompile(`&#(\d+);`)
)

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func padStart(value string, length int) string {
	for len(value) < length {
		value = "0" + value
	}
	return value
}

// decodeHTML decodes numeric entities before the named ones, so an escaped
// entity such as "&amp;#39;" is not decoded twice.
func decodeHTML(value string) string {
	value = hexEntityRe.ReplaceAllStringFunc(value, func(entity string) string {
		code, err := strconv.ParseInt(entity[3:len(entity)-1], 16, 32)
		if err != nil {
			return entity
		}
		return string(rune(code))
	})
	value = decEntityRe.ReplaceAllStringFunc(value, func(entity string) string {
		code, err := strconv.ParseInt(entity[2:len(entity)-1], 10, 32)
		if err != nil {
			return entity
		}
		return string(rune(code))
	})
	value = strings.ReplaceAll(value, "&quot;", `"`)
	value = strings.ReplaceAll(value, "&#39;", "'")
	value = strings.ReplaceAll(value, "&apos;", "'")
	value = strings.ReplaceAll(value, "&lt;", "<")
	value = strings.ReplaceAll(value, "&gt;", ">")
	return strings.ReplaceAll(value, "&amp;", "&")
}

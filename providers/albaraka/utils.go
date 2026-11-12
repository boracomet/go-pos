package albaraka

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// e-POS endpoints, relative to the provider base URL.
const (
	// EndpointAPI is the JSON API: EndpointAPI + "/Sale", "/Reverse", "/Return", ...
	EndpointAPI = "/ALBMerchantService/MerchantJSONAPI.svc"
	// EndpointGate is the 3D Secure form target, opened in the customer's browser.
	EndpointGate = "/ALBSecurePaymentUI/SecureProcess/SecureVerification.aspx"
)

// CreateMac returns base64(SHA256(values joined without a separator + encKey)).
// MACParams names the values, e.g. MerchantNo:TerminalNo:CardNo:Cvc2:ExpireDate:Amount.
func CreateMac(values []string, encKey string) (string, error) {
	sum, err := core.Digest(core.SHA256, []byte(strings.Join(values, "")+encKey))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// FormatAmount returns the amount in kuruş ("1.20" is 120).
func FormatAmount(amount string) (int64, error) {
	return core.ToMinorUnits(amount, "")
}

// CurrencyCode returns the Posnet currency code: TRY "TL", USD "US", EUR "EU", GBP "GB".
// An empty currency means TRY; any other unsupported currency is rejected.
func CurrencyCode(currency string) (string, error) {
	switch strings.ToUpper(orDefault(currency, "TRY")) {
	case "TRY", "TL":
		return "TL", nil
	case "USD":
		return "US", nil
	case "EUR":
		return "EU", nil
	case "GBP":
		return "GB", nil
	}
	return "", core.NewValidationError(
		fmt.Sprintf("Currency %s is not supported by Albaraka", currency), providerName, nil)
}

// FormatInstallment returns the InstallmentCount / InstallmentType pair, 1 to 99
// installments. A single payment is sent as 0 / "N".
func FormatInstallment(count int) (Installment, error) {
	if count < 1 || count > 99 {
		return Installment{}, invalidInstallment(strconv.Itoa(count))
	}
	return installmentOf(count), nil
}

// ParseInstallment reads the installment count of a completion request, where it
// arrives as a form field. An empty value means a single payment.
func ParseInstallment(value string) (Installment, error) {
	if value == "" {
		return installmentOf(1), nil
	}
	count, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(count) || math.IsInf(count, 0) || count != math.Trunc(count) {
		return Installment{}, invalidInstallment(value)
	}
	return FormatInstallment(int(count))
}

func installmentOf(count int) Installment {
	if count > 1 {
		return Installment{Count: count, Type: "Y"}
	}
	return Installment{Count: 0, Type: "N"}
}

func invalidInstallment(value string) error {
	return core.NewValidationError("Invalid installment count: "+value, providerName, nil)
}

// FormatExpiry returns the card expiry as YYMM ("12", "2030" -> "3012").
func FormatExpiry(month, year string) (string, error) {
	mm := padStart(month, 2)
	yy := padStart(year, 2)
	if len(year) == 4 {
		yy = year[len(year)-2:]
	}
	invalid := core.NewValidationError(fmt.Sprintf("Invalid card expiry: %s/%s", month, year), providerName, nil)
	if !twoDigitRe.MatchString(mm) || !twoDigitRe.MatchString(yy) {
		return "", invalid
	}
	m, err := strconv.Atoi(mm)
	if err != nil || m < 1 || m > 12 {
		return "", invalid
	}
	return yy + mm, nil
}

// FormatOrderID returns the order id the bank expects: up to 20 letters or
// digits, left-padded with zeros to 20 characters.
func FormatOrderID(orderID string) (string, error) {
	if !orderIDRe.MatchString(orderID) {
		return "", core.NewValidationError(
			fmt.Sprintf("Albaraka order ids must be 1 to 20 letters or digits, got: %s", orderID), providerName, nil)
	}
	return padStart(orderID, 20), nil
}

// IsApproved reports whether the host answered with ResponseCode "00".
func IsApproved(response APIResponse) bool {
	return response.ServiceResponseData != nil && response.ServiceResponseData.ResponseCode == "00"
}

// GenerateOrderID returns 20 uppercase hex characters: 80 random bits fit the
// bank's 20-character alphanumeric order id limit.
func GenerateOrderID() (string, error) {
	value, err := core.RandomHex(10)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(value), nil
}

// OrderStatus maps the TransactionData of a TransactionInquiry answer the way
// the Posnet agreement answer is read: a successful (TxnStatus 1) sale is a
// success, a successful reverse or return makes it cancelled. Keys are matched
// case-insensitively. Transactions without those fields leave the status
// pending, because the answer format is not confirmed yet.
func OrderStatus(data any) core.PaymentStatus {
	items := make([]map[string]any, 0)
	switch value := data.(type) {
	case []any:
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				items = append(items, object)
			}
		}
	case map[string]any:
		items = append(items, value)
	case nil:
	default:
		items = append(items, map[string]any{})
	}

	known := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if _, hasStatus := field(item, "txnstatus"); !hasStatus {
			continue
		}
		if _, hasState := field(item, "state"); !hasState {
			continue
		}
		known = append(known, item)
	}
	if len(known) == 0 {
		return core.StatusPending
	}

	sale, reversed := false, false
	for _, item := range known {
		status, _ := field(item, "txnstatus")
		if status != "1" {
			continue
		}
		state, _ := field(item, "state")
		state = strings.ToLower(state)
		switch state {
		case "sale", "vft_sale", "capture", "authorization":
			sale = true
		}
		if strings.Contains(state, "_reverse") || state == "return" || state == "vft_return" {
			reversed = true
		}
	}
	if reversed {
		return core.StatusCancelled
	}
	if sale {
		return core.StatusSuccess
	}
	return core.StatusFailure
}

// field reads a key case-insensitively, as the bank's answer is not consistent
// about casing. The second result reports whether the key is present at all.
func field(item map[string]any, name string) (string, bool) {
	for key, value := range item {
		if strings.ToLower(key) == name {
			return jsString(value), true
		}
	}
	return "", false
}

// jsString renders a decoded JSON value the way JavaScript's String() does, so
// a numeric TxnStatus 1 reads as "1".
func jsString(value any) string {
	switch item := value.(type) {
	case nil:
		return ""
	case string:
		return item
	case bool:
		return strconv.FormatBool(item)
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case json.Number:
		return item.String()
	case []any:
		parts := make([]string, 0, len(item))
		for _, element := range item {
			parts = append(parts, jsString(element))
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	default:
		return fmt.Sprint(item)
	}
}

var (
	twoDigitRe   = regexp.MustCompile(`^\d{2}$`)
	orderIDRe    = regexp.MustCompile(`^[A-Za-z0-9]{1,20}$`)
	whitespaceRe = regexp.MustCompile(`\s`)
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

func escapeHTML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return strings.ReplaceAll(value, "'", "&#39;")
}

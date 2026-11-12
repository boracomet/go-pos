package akbank

import (
	"crypto/hmac"
	"crypto/sha512"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// Sign returns base64(HMAC-SHA512(data, secretKey)), the auth-hash of a request
// body and the hash of a 3D form or callback.
//
// crypto/hmac is used directly: core.HMAC hands one shared hash instance to
// hmac.New, which panics on the second call.
func Sign(data, secretKey string) (string, error) {
	mac := hmac.New(sha512.New, []byte(secretKey))
	if _, err := mac.Write([]byte(data)); err != nil {
		return "", err
	}
	return core.ToBase64(mac.Sum(nil)), nil
}

// ThreeDHashFieldOrder lists the fields signed for the 3D form, in this exact
// order. Fields the form does not carry are signed as empty strings.
var ThreeDHashFieldOrder = []string{
	"paymentModel",
	"txnCode",
	"merchantSafeId",
	"terminalSafeId",
	"orderId",
	"lang",
	"amount",
	"ccbRewardAmount",
	"pcbRewardAmount",
	"xcbRewardAmount",
	"currencyCode",
	"installCount",
	"okUrl",
	"failUrl",
	"emailAddress",
	"subMerchantId",
	"creditCard",
	"expiredDate",
	"cvv",
	"randomNumber",
	"requestDateTime",
	"b2bIdentityNumber",
}

// Create3DFormHash signs the 3D form fields in ThreeDHashFieldOrder.
func Create3DFormHash(fields map[string]string, secretKey string) (string, error) {
	var data strings.Builder
	for _, key := range ThreeDHashFieldOrder {
		data.WriteString(fields[key])
	}
	return Sign(data.String(), secretKey)
}

// requiredCallbackHashFields must be covered by the callback signature for the
// result to be trusted.
var requiredCallbackHashFields = []string{"responseCode", "orderId", "merchantSafeId", "terminalSafeId"}

// VerifyCallback verifies the okUrl/failUrl POST: hash is
// base64(HMAC-SHA512(values of hashParams, secretKey)). The values are joined
// without a separator, so a valid signature does not pin each value to its field.
func VerifyCallback(data map[string]string, secretKey string) bool {
	if data["hash"] == "" || data["hashParams"] == "" {
		return false
	}
	var params []string
	for _, name := range strings.Split(data["hashParams"], "+") {
		if name != "" {
			params = append(params, name)
		}
	}
	for _, field := range requiredCallbackHashFields {
		if !contains(params, field) {
			return false
		}
	}
	var value strings.Builder
	for _, name := range params {
		value.WriteString(data[name])
	}
	expected, err := Sign(value.String(), secretKey)
	if err != nil {
		return false
	}
	return core.SafeEqual(expected, data["hash"])
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// GenerateRandomNumber returns 128 uppercase hex characters.
func GenerateRandomNumber() (string, error) {
	value, err := core.RandomHex(64)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(value), nil
}

// FormatDateTime renders requestDateTime in Europe/Istanbul local time
// ("YYYY-MM-DDTHH:mm:ss.000"). Turkey is UTC+3 all year.
func FormatDateTime(date time.Time) string {
	return date.UTC().Add(3*time.Hour).Format("2006-01-02T15:04:05") + ".000"
}

// FormatAmount renders an amount with two decimals ("1.10").
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

var twoDigitsRe = regexp.MustCompile(`^\d{2}$`)

func padStart(value string, length int) string {
	for len(value) < length {
		value = "0" + value
	}
	return value
}

// FormatExpiry renders the card expiry as MMYY.
func FormatExpiry(month, year string) (string, error) {
	mm := padStart(month, 2)
	yy := year
	if len(year) == 4 {
		yy = year[len(year)-2:]
	} else {
		yy = padStart(year, 2)
	}
	invalid := core.NewValidationError(fmt.Sprintf("Invalid card expiry: %s/%s", month, year), "", nil)
	if !twoDigitsRe.MatchString(mm) || !twoDigitsRe.MatchString(yy) {
		return "", invalid
	}
	if n, err := strconv.Atoi(mm); err != nil || n < 1 || n > 12 {
		return "", invalid
	}
	return mm + yy, nil
}

var currencyCodes = map[string]int{
	"TRY": 949,
	"TL":  949,
	"USD": 840,
	"EUR": 978,
	"GBP": 826,
	"JPY": 392,
	"RUB": 643,
}

// CurrencyCode returns the ISO 4217 numeric code of a currency, defaulting to
// TRY. An unsupported currency is a validation error, never a fallback to TRY.
func CurrencyCode(currency string) (int, error) {
	if currency == "" {
		currency = "TRY"
	}
	if code, ok := currencyCodes[strings.ToUpper(currency)]; ok {
		return code, nil
	}
	return 0, core.NewValidationError(fmt.Sprintf("Currency %s is not supported by Akbank", currency), "", nil)
}

func escapeHTML(value string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(value)
}

// formFields is an ordered list of 3D form fields.
type formFields []FormField

func (f *formFields) set(name, value string) {
	*f = append(*f, FormField{Name: name, Value: value})
}

func (f formFields) get(name string) string {
	for _, field := range f {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

func (f formFields) values() map[string]string {
	out := make(map[string]string, len(f))
	for _, field := range f {
		out[field.Name] = field.Value
	}
	return out
}

func (f formFields) inputs() string {
	var b strings.Builder
	for _, field := range f {
		b.WriteString(`<input type="hidden" name="` + escapeHTML(field.Name) +
			`" value="` + escapeHTML(field.Value) + `">`)
	}
	return b.String()
}

// callbackFields converts the bank's POST body into the flat record the signature
// is computed over. An unsupported shape yields no fields, so verification fails.
func callbackFields(data any) map[string]string {
	switch v := data.(type) {
	case nil:
		return map[string]string{}
	case map[string]string:
		return v
	case map[string]any:
		out := make(map[string]string, len(v))
		for key, value := range v {
			switch value := value.(type) {
			case nil:
				out[key] = ""
			case string:
				out[key] = value
			default:
				out[key] = fmt.Sprint(value)
			}
		}
		return out
	case map[string][]string:
		out := make(map[string]string, len(v))
		for key, values := range v {
			if len(values) > 0 {
				out[key] = values[0]
			} else {
				out[key] = ""
			}
		}
		return out
	default:
		return map[string]string{}
	}
}

// jsonField is one member of a request body, in the order it is serialized. The
// body is signed as it is sent, so the order is part of the request.
type jsonField struct {
	Name  string
	Value any
}

// buildJSON serializes fields in order. A []jsonField value is a nested object.
func buildJSON(fields []jsonField) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(quoteJSON(field.Name))
		b.WriteByte(':')
		switch value := field.Value.(type) {
		case string:
			b.WriteString(quoteJSON(value))
		case int:
			b.WriteString(strconv.Itoa(value))
		case []jsonField:
			b.WriteString(buildJSON(value))
		}
	}
	b.WriteByte('}')
	return b.String()
}

// quoteJSON escapes a string the way JSON.stringify does: no HTML escaping, so
// the signed bytes match the bank's own serialization.
func quoteJSON(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if char < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, char))
			} else {
				b.WriteRune(char)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// stringOf reads a string member; a JSON number is rendered as it arrived.
func stringOf(data map[string]any, key string) string {
	switch value := data[key].(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func objectOf(data map[string]any, key string) map[string]any {
	if value, ok := data[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// detailsOf reads txnDetailList, keeping only the object entries.
func detailsOf(data map[string]any) []TxnDetail {
	list, ok := data["txnDetailList"].([]any)
	if !ok {
		return nil
	}
	out := make([]TxnDetail, 0, len(list))
	for _, entry := range list {
		if detail, ok := entry.(map[string]any); ok {
			out = append(out, TxnDetail(detail))
		}
	}
	return out
}

// installCount mirrors Math.max(1, installment): zero means unset.
func installCount(installment int) int {
	if installment < 1 {
		return 1
	}
	return installment
}

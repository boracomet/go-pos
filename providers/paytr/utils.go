package paytr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// Sign returns base64(HMAC-SHA256(merchantKey, data)). Every PayTR token is a
// signature over a fixed concatenation of fields with merchantSalt appended.
func Sign(data, merchantKey string) (string, error) {
	mac, err := core.HMAC(core.SHA256, []byte(merchantKey), []byte(data))
	if err != nil {
		return "", err
	}
	return core.ToBase64(mac), nil
}

// IframeTokenParams are the fields signed for the iFrame API token.
type IframeTokenParams struct {
	MerchantID     string
	UserIP         string
	MerchantOid    string
	Email          string
	PaymentAmount  string
	UserBasket     string
	NoInstallment  string
	MaxInstallment string
	Currency       string
	TestMode       string
}

// GenerateIframeToken signs the iFrame API token (paytr_token for /odeme/api/get-token).
func GenerateIframeToken(params IframeTokenParams, merchantSalt, merchantKey string) (string, error) {
	data := params.MerchantID +
		params.UserIP +
		params.MerchantOid +
		params.Email +
		params.PaymentAmount +
		params.UserBasket +
		params.NoInstallment +
		params.MaxInstallment +
		params.Currency +
		params.TestMode
	return Sign(data+merchantSalt, merchantKey)
}

// DirectTokenParams are the fields signed for the Direct API token.
type DirectTokenParams struct {
	MerchantID       string
	UserIP           string
	MerchantOid      string
	Email            string
	PaymentAmount    string
	PaymentType      string
	InstallmentCount string
	Currency         string
	TestMode         string
	Non3D            string
}

// GenerateDirectToken signs the Direct API token (paytr_token for /odeme).
func GenerateDirectToken(params DirectTokenParams, merchantSalt, merchantKey string) (string, error) {
	data := params.MerchantID +
		params.UserIP +
		params.MerchantOid +
		params.Email +
		params.PaymentAmount +
		params.PaymentType +
		params.InstallmentCount +
		params.Currency +
		params.TestMode +
		params.Non3D
	return Sign(data+merchantSalt, merchantKey)
}

// GenerateRefundToken signs merchant_id + merchant_oid + return_amount + merchant_salt.
func GenerateRefundToken(merchantID, merchantOid, returnAmount, merchantSalt, merchantKey string) (string, error) {
	return Sign(merchantID+merchantOid+returnAmount+merchantSalt, merchantKey)
}

// GenerateStatusToken signs merchant_id + merchant_oid + merchant_salt.
func GenerateStatusToken(merchantID, merchantOid, merchantSalt, merchantKey string) (string, error) {
	return Sign(merchantID+merchantOid+merchantSalt, merchantKey)
}

// GenerateBinToken signs bin_number + merchant_id + merchant_salt.
func GenerateBinToken(binNumber, merchantID, merchantSalt, merchantKey string) (string, error) {
	return Sign(binNumber+merchantID+merchantSalt, merchantKey)
}

// GenerateInstallmentRatesToken signs merchant_id + request_id + merchant_salt.
func GenerateInstallmentRatesToken(merchantID, requestID, merchantSalt, merchantKey string) (string, error) {
	return Sign(merchantID+requestID+merchantSalt, merchantKey)
}

// GenerateCardListToken signs utoken + merchant_salt (Kart Saklama, capi/list).
func GenerateCardListToken(utoken, merchantSalt, merchantKey string) (string, error) {
	return Sign(utoken+merchantSalt, merchantKey)
}

// GenerateCardDeleteToken signs ctoken + utoken + merchant_salt (capi/delete).
func GenerateCardDeleteToken(ctoken, utoken, merchantSalt, merchantKey string) (string, error) {
	return Sign(ctoken+utoken+merchantSalt, merchantKey)
}

// VerifyCallback checks the hash PayTR sends to the notification (Bildirim) URL:
// base64(HMAC-SHA256(merchant_oid + merchant_salt + status + total_amount, merchant_key)).
// A notification missing any signed field never verifies.
func VerifyCallback(data CallbackData, merchantSalt, merchantKey string) (bool, error) {
	if data.MerchantOid == "" || data.Status == "" || data.Hash == "" {
		return false, nil
	}
	expected, err := Sign(data.MerchantOid+merchantSalt+data.Status+data.TotalAmount, merchantKey)
	if err != nil {
		return false, err
	}
	return core.SafeEqual(expected, data.Hash), nil
}

// FormatBasket encodes the iFrame API user_basket: base64(JSON([[name, "unit price in TL", quantity]])).
func FormatBasket(items []BasketItem) (string, error) {
	encoded, err := FormatDirectBasket(items)
	if err != nil {
		return "", err
	}
	return core.ToBase64([]byte(encoded)), nil
}

// FormatDirectBasket encodes the Direct API user_basket as plain JSON. Prices are
// rendered with 2 decimals; the transport handles URL and HTML escaping.
func FormatDirectBasket(items []BasketItem) (string, error) {
	basket := make([][]any, 0, len(items))
	for _, item := range items {
		price, err := FormatAmount(item.Price)
		if err != nil {
			return "", err
		}
		basket = append(basket, []any{item.Name, price, item.Quantity})
	}
	return marshalCompact(basket)
}

// marshalCompact serializes without HTML escaping, so Turkish characters and
// literal entities survive both the signature and the browser form.
func marshalCompact(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// ConvertToKurus renders an amount in minor units (kuruş), as payment_amount of
// the iFrame API expects it.
func ConvertToKurus(amount string) (string, error) {
	minor, err := core.ToMinorUnits(amount, "amount")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(minor, 10), nil
}

// FormatAmount renders an amount with 2 decimals and a dot separator ("12.34"),
// as the Direct API, refunds and basket prices expect it.
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

// ConvertFromKurus renders minor units (kuruş) as TL ("1001" -> "10.01").
func ConvertFromKurus(kurus string) string {
	value, ok := leadingInt(kurus)
	if !ok {
		return "NaN"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	out := fmt.Sprintf("%d.%02d", value/100, value%100)
	if negative {
		out = "-" + out
	}
	return out
}

// leadingInt parses the leading integer of a string, like parseInt(value, 10).
func leadingInt(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	end := 0
	if end < len(value) && (value[end] == '+' || value[end] == '-') {
		end++
	}
	start := end
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value[:end], 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

var currencyCodes = map[string]string{
	"TRY": "TL",
	"TL":  "TL",
	"USD": "USD",
	"EUR": "EUR",
	"GBP": "GBP",
	"RUB": "RUB",
}

// MapCurrency maps an ISO currency code to the code PayTR accepts. An empty
// currency defaults to TRY; anything outside the supported set is rejected.
func MapCurrency(currency string) (string, error) {
	value := currency
	if value == "" {
		value = "TRY"
	}
	mapped, ok := currencyCodes[strings.ToUpper(value)]
	if !ok {
		return "", core.NewValidationError(
			fmt.Sprintf("Currency %s is not supported by PayTR", currency), "paytr", nil)
	}
	return mapped, nil
}

var merchantOidRe = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// AssertMerchantOid rejects a merchant_oid PayTR would reject: only letters and
// digits, at most 64 characters.
func AssertMerchantOid(merchantOid string) error {
	if !merchantOidRe.MatchString(merchantOid) {
		return core.NewValidationError(fmt.Sprintf(
			"Invalid PayTR merchant_oid %q: only letters and digits are allowed (max 64 characters)",
			merchantOid), "paytr", []core.ValidationIssue{
			{Path: "conversationId", Message: "must be alphanumeric (max 64 characters)"},
		})
	}
	return nil
}

// FormField is one field of an urlencoded body or browser form.
type FormField struct {
	Name  string
	Value string
}

// Form is an ordered set of fields. PayTR signs and posts fields in a fixed
// order, so a map (which Go iterates randomly) cannot be used.
type Form struct {
	fields []FormField
}

// Set adds a field, or replaces the value of an existing one in place.
func (f *Form) Set(name, value string) *Form {
	for i := range f.fields {
		if f.fields[i].Name == name {
			f.fields[i].Value = value
			return f
		}
	}
	f.fields = append(f.fields, FormField{Name: name, Value: value})
	return f
}

// Get returns a field's value, or "" when it is absent.
func (f *Form) Get(name string) string {
	for _, field := range f.fields {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

// Has reports whether a field was set.
func (f *Form) Has(name string) bool {
	for _, field := range f.fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

// Fields returns the fields in insertion order.
func (f *Form) Fields() []FormField { return f.fields }

// Encode renders the form as an application/x-www-form-urlencoded body.
func (f *Form) Encode() string { return CreateFormData(f.fields) }

// CreateFormData encodes fields as application/x-www-form-urlencoded, in order.
func CreateFormData(fields []FormField) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, encodeURIComponent(field.Name)+"="+encodeURIComponent(field.Value))
	}
	return strings.Join(parts, "&")
}

// encodeURIComponent matches JavaScript encodeURIComponent: a space becomes
// %20 (not +) and -_.!~*'() stay literal.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

func escapeHTML(value string) string { return htmlEscaper.Replace(value) }

// BuildAutoSubmitForm renders the HTML form the customer's browser posts to the
// Direct API. Every attribute is escaped once, so a product name containing a
// literal entity is not decoded twice.
func BuildAutoSubmitForm(action string, fields []FormField) string {
	var inputs strings.Builder
	for _, field := range fields {
		inputs.WriteString(`<input type="hidden" name="` + escapeHTML(field.Name) +
			`" value="` + escapeHTML(field.Value) + `">`)
	}
	return `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>` +
		`<form id="paytr-form" method="POST" action="` + escapeHTML(action) + `">` + inputs.String() + `</form>` +
		`<script>document.getElementById("paytr-form").submit();</script>` +
		`</body></html>`
}

// BuildIframeHTML wraps PayTR's payment page in a full-viewport iFrame.
func BuildIframeHTML(iframeURL string) string {
	return `<!DOCTYPE html><html><head><meta charset="UTF-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1.0">` +
		`<title>PayTR</title>` +
		`<style>body{margin:0;padding:0;overflow:hidden}iframe{width:100%;height:100vh;border:none}</style>` +
		`</head><body>` +
		`<iframe src="` + escapeHTML(iframeURL) + `" frameborder="0" scrolling="yes"></iframe>` +
		`</body></html>`
}

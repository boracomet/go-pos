package est

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// IsHostedStoreType reports whether the bank collects the card on its own page.
func IsHostedStoreType(storeType StoreType) bool {
	return storeType == Store3DPayHosting || storeType == Store3DHost
}

// IsMerchantChargedStoreType reports whether the bank only authenticates the
// card and the merchant charges it with XML Auth (using md). With 3d_pay and
// 3d_pay_hosting the bank charges the card itself.
func IsMerchantChargedStoreType(storeType StoreType) bool {
	return storeType == Store3D || storeType == Store3DHost
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// CompareKeys orders field names case-insensitively, comparing digit runs by
// value ("a2" before "a10"). It returns a negative number, zero or a positive
// number, and puts "_" before letters like the bank does.
func CompareKeys(a, b string) int {
	x, y := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		if isDigit(x[i]) && isDigit(y[j]) {
			endI, endJ := i, j
			for endI < len(x) && isDigit(x[endI]) {
				endI++
			}
			for endJ < len(y) && isDigit(y[endJ]) {
				endJ++
			}
			if diff := compareDigitRuns(x[i:endI], y[j:endJ]); diff != 0 {
				return diff
			}
			i, j = endI, endJ
			continue
		}
		if x[i] != y[j] {
			if x[i] < y[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return (len(x) - i) - (len(y) - j)
}

// compareDigitRuns compares by value without overflowing on long runs.
func compareDigitRuns(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// unhashedFields are never covered by the hash.
var unhashedFields = map[string]bool{
	"hash":         true,
	"encoding":     true,
	"countdown":    true,
	"nationalidno": true,
}

func escapeHashValue(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "|", `\|`)
}

// CreateHash builds hash version 3: base64(SHA-512(values sorted by field name,
// then the store key, each with "\" and "|" escaped, joined with "|")).
func CreateHash(fields map[string]string, storeKey string) (string, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !unhashedFields[strings.ToLower(key)] {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return CompareKeys(keys[i], keys[j]) < 0 })

	values := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		values = append(values, escapeHashValue(fields[key]))
	}
	values = append(values, escapeHashValue(storeKey))

	sum, err := core.Digest(core.SHA512, core.UTF8(strings.Join(values, "|")))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// VerifyCallback verifies the HASH of a 3D callback over all returned fields.
func VerifyCallback(data map[string]string, storeKey string) bool {
	if data["HASH"] == "" {
		return false
	}
	expected, err := CreateHash(data, storeKey)
	if err != nil {
		return false
	}
	return core.SafeEqual(expected, data["HASH"])
}

// GenerateRandom returns the random value for the form's `rnd` field.
func GenerateRandom() (string, error) { return core.RandomHex(16) }

// FormatAmount renders an amount with two decimals ("1.10").
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

var currencyCodes = map[string]string{
	"TRY": "949",
	"TL":  "949",
	"USD": "840",
	"EUR": "978",
	"GBP": "826",
	// Currencies of the Girogate payment methods
	"AUD": "036",
	"BRL": "986",
	"CAD": "124",
	"CHF": "756",
	"CNY": "156",
	"CZK": "203",
	"DKK": "208",
	"NOK": "578",
	"NZD": "554",
	"PHP": "608",
	"PLN": "985",
	"SEK": "752",
	"SGD": "702",
	"THB": "764",
	// Macedonian denar, for Halkbank Macedonia
	"MKD": "807",
}

// CurrencyCode returns the ISO 4217 numeric code of a currency, defaulting to
// TRY. An unsupported currency is a validation error, never a fallback to TRY.
func CurrencyCode(currency string) (string, error) {
	if currency == "" {
		currency = "TRY"
	}
	if code, ok := currencyCodes[strings.ToUpper(currency)]; ok {
		return code, nil
	}
	return "", core.NewValidationError(fmt.Sprintf("Currency %s is not supported by EST", currency), "", nil)
}

// girogateCustomerFields are the customer fields each method needs, besides the
// country, in the order they are sent.
var girogateCustomerFields = map[string][]string{
	"P24":                {"email"},
	"ZIMPLER":            {"email"},
	"ENTERCASH":          {"consumerId"},
	"TRUSTLY":            {"consumerId"},
	"ASTROPAYCARD":       {"consumerId"},
	"ASTROPAYDIRECT":     {"consumerId"},
	"GIROPAY":            {"mobile"},
	"DRAGONPAY":          {"email", "phone"},
	"ENETS":              {"email", "phone"},
	"SINGPOST":           {"email", "phone"},
	"KRUNGTHAIBANK":      {"email", "phone"},
	"KRUNGSIBANK":        {"email", "phone"},
	"SIAMCOMMERCIALBANK": {"email", "phone"},
	"BANGKOKBANK":        {"email", "phone"},
}

var girogateFieldNames = map[string]string{
	"email":      "girogateemail",
	"phone":      "girogatephone",
	"mobile":     "girogatemobile",
	"consumerId": "girogateconsumerid",
}

var countryCodeRe = regexp.MustCompile(`^[A-Z]{2}$`)

// BuildGirogateFields returns the form fields of a Girogate payment:
// paymentType, ccode and the customer fields the method needs. They are covered
// by the hash like every other field.
func BuildGirogateFields(options GirogateOptions) ([]FormField, error) {
	method := strings.TrimPrefix(strings.ToUpper(options.Method), "GIROGATE_")
	known := false
	for _, m := range GirogateMethods {
		if string(m) == method {
			known = true
			break
		}
	}
	if !known {
		return nil, core.NewValidationError(fmt.Sprintf("Unknown Girogate method: %s", options.Method), "", nil)
	}
	country := strings.ToUpper(options.Country)
	if !countryCodeRe.MatchString(country) {
		return nil, core.NewValidationError("Girogate country must be a two-letter ISO 3166 code", "", nil)
	}

	fields := []FormField{
		{Name: "paymentType", Value: "GIROGATE_" + method},
		{Name: "ccode", Value: country},
	}
	values := map[string]string{
		"email":      options.Email,
		"phone":      options.Phone,
		"mobile":     options.Phone,
		"consumerId": options.ConsumerID,
	}
	for _, key := range girogateCustomerFields[method] {
		value := values[key]
		if value == "" {
			label := key
			if label == "mobile" {
				label = "phone"
			}
			return nil, core.NewValidationError(
				fmt.Sprintf("Girogate %s needs the customer's %s", method, label), "", nil)
		}
		fields = append(fields, FormField{Name: girogateFieldNames[key], Value: value})
	}
	return fields, nil
}

// FormatInstallment renders `taksit`: empty for a single payment. Zero means unset.
func FormatInstallment(installment int) (string, error) {
	count := installment
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return "", core.NewValidationError(fmt.Sprintf("Invalid installment count: %d", installment), "", nil)
	}
	if count > 1 {
		return strconv.Itoa(count), nil
	}
	return "", nil
}

var twoDigitsRe = regexp.MustCompile(`^\d{2}$`)

func padStart(value string, length int) string {
	for len(value) < length {
		value = "0" + value
	}
	return value
}

// FormatExpiry renders the expiry as two-digit month and year.
func FormatExpiry(month, year string) (Expiry, error) {
	mm := padStart(month, 2)
	yy := year
	if len(year) == 4 {
		yy = year[len(year)-2:]
	} else {
		yy = padStart(year, 2)
	}
	invalid := core.NewValidationError(fmt.Sprintf("Invalid card expiry: %s/%s", month, year), "", nil)
	if !twoDigitsRe.MatchString(mm) || !twoDigitsRe.MatchString(yy) {
		return Expiry{}, invalid
	}
	if n, err := strconv.Atoi(mm); err != nil || n < 1 || n > 12 {
		return Expiry{}, invalid
	}
	return Expiry{Month: mm, Year: yy}, nil
}

var whitespaceRe = regexp.MustCompile(`\s`)

// compactCardNumber removes the whitespace a customer typed into the card number.
func compactCardNumber(cardNumber string) string {
	return whitespaceRe.ReplaceAllString(cardNumber, "")
}

// CardType returns the `cardType` of the 3D form, from the card number.
func CardType(cardNumber string) string {
	if strings.HasPrefix(cardNumber, "34") || strings.HasPrefix(cardNumber, "37") {
		return "amex"
	}
	if len(cardNumber) >= 2 {
		if (cardNumber[0] == '5' && cardNumber[1] >= '1' && cardNumber[1] <= '5') ||
			(cardNumber[0] == '2' && cardNumber[1] >= '2' && cardNumber[1] <= '7') {
			return "mastercard"
		}
	}
	return "visa"
}

func escapeXML(text string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(text)
}

var (
	hexEntityRe = regexp.MustCompile(`&#x([0-9a-fA-F]+);`)
	decEntityRe = regexp.MustCompile(`&#(\d+);`)
)

func unescapeXML(text string) string {
	text = hexEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.ParseUint(hexEntityRe.FindStringSubmatch(m)[1], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
	text = decEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.ParseUint(decEntityRe.FindStringSubmatch(m)[1], 10, 32)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(text)
}

func buildXMLFields(fields []XMLField) string {
	var b strings.Builder
	for _, field := range fields {
		b.WriteString("<" + field.Name + ">")
		if field.Children != nil {
			b.WriteString(buildXMLFields(field.Children))
		} else {
			b.WriteString(escapeXML(field.Value))
		}
		b.WriteString("</" + field.Name + ">")
	}
	return b.String()
}

var nonASCIIRe = regexp.MustCompile(`[^\x20-\x7e]`)

// BuildXML serializes a CC5Request. Characters outside ASCII are sent as
// character references, so the body is valid whatever the declared encoding.
func BuildXML(fields []XMLField) string {
	body := nonASCIIRe.ReplaceAllStringFunc(buildXMLFields(fields), func(m string) string {
		return fmt.Sprintf("&#%d;", []rune(m)[0])
	})
	return `<?xml version="1.0" encoding="ISO-8859-9"?><CC5Request>` + body + `</CC5Request>`
}

var (
	leafRe        = regexp.MustCompile(`<([A-Za-z_][\w.-]*)(?:\s[^>]*)?>([^<]*)</([A-Za-z_][\w.-]*)>`)
	selfClosingRe = regexp.MustCompile(`<([A-Za-z_][\w.-]*)\s*/>`)
)

// leafFields reads the leaf elements of an XML fragment; the first occurrence of
// a tag wins.
func leafFields(xml string) map[string]string {
	result := map[string]string{}
	for _, m := range leafRe.FindAllStringSubmatch(xml, -1) {
		if m[1] != m[3] {
			continue
		}
		if _, ok := result[m[1]]; !ok {
			result[m[1]] = strings.TrimSpace(unescapeXML(m[2]))
		}
	}
	for _, m := range selfClosingRe.FindAllStringSubmatch(xml, -1) {
		if _, ok := result[m[1]]; !ok {
			result[m[1]] = ""
		}
	}
	return result
}

// ParseXMLResponse parses a CC5Response. It fails when the body is not one.
func ParseXMLResponse(xml string) (XMLResponse, error) {
	start := strings.Index(xml, "<CC5Response")
	end := strings.LastIndex(xml, "</CC5Response>")
	if start == -1 || end == -1 {
		return XMLResponse{}, core.NewError("EST returned an unexpected response", "PROVIDER_ERROR")
	}
	content := xml[start:end]
	extra := map[string]string{}
	extraStart := strings.Index(content, "<Extra>")
	extraEnd := strings.Index(content, "</Extra>")
	if extraStart != -1 && extraEnd > extraStart {
		extra = leafFields(content[extraStart+len("<Extra>") : extraEnd])
		content = content[:extraStart] + content[extraEnd+len("</Extra>"):]
	}
	return XMLResponse{Fields: leafFields(content), Extra: extra}, nil
}

// IsApproved reports ProcReturnCode 00 with Response "Approved".
func IsApproved(fields map[string]string) bool {
	return fields["ProcReturnCode"] == "00" && fields["Response"] == "Approved"
}

// numberAboveZero mirrors the bank's numeric comparison: an unparsable or empty
// value is not above zero.
func numberAboveZero(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return false
	}
	n, err := strconv.ParseFloat(text, 64)
	return err == nil && n > 0
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

// callbackFields converts the bank's POST body into the flat record the hash is
// computed over. An unsupported shape yields no fields, so verification fails.
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

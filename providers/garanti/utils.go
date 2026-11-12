package garanti

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

var turkishBytes = map[rune]byte{
	'Ğ': 0xd0,
	'İ': 0xdd,
	'Ş': 0xde,
	'ğ': 0xf0,
	'ı': 0xfd,
	'ş': 0xfe,
}

// encodeISO88599 encodes a string as ISO-8859-9, the charset Garanti hashes over.
// Characters outside it become '?'.
func encodeISO88599(value string) []byte {
	out := make([]byte, 0, len(value))
	for _, char := range value {
		if b, ok := turkishBytes[char]; ok {
			out = append(out, b)
			continue
		}
		if char < 0x100 {
			out = append(out, byte(char))
			continue
		}
		out = append(out, '?')
	}
	return out
}

// upperHash is the uppercase hex digest over ISO-8859-9 bytes, as Garanti computes it.
func upperHash(algorithm core.DigestAlgorithm, data string) (string, error) {
	sum, err := core.Digest(algorithm, encodeISO88599(data))
	if err != nil {
		return "", err
	}
	return strings.ToUpper(core.ToHex(sum)), nil
}

// CreateSecurityData returns SHA1(password + terminal id left-padded to 9 digits).
func CreateSecurityData(password, terminalID string) (string, error) {
	return upperHash(core.SHA1, password+padStart(terminalID, 9))
}

// CreateHashData returns the HashData of an XML API request: SHA512(OrderID +
// TerminalID + card number (empty without a card) + Amount + CurrencyCode + SecurityData).
func CreateHashData(fields HashDataFields) (string, error) {
	return upperHash(core.SHA512, fields.OrderID+fields.TerminalID+fields.CardNumber+
		fields.Amount+fields.CurrencyCode+fields.SecurityData)
}

// Create3DHash returns the secure3dhash of the 3D form: SHA512(terminalid +
// orderid + txnamount + txncurrencycode + successurl + errorurl + txntype +
// txninstallmentcount + store key + SecurityData).
func Create3DHash(fields ThreeDHashFields) (string, error) {
	return upperHash(core.SHA512, fields.TerminalID+fields.OrderID+fields.Amount+fields.CurrencyCode+
		fields.SuccessURL+fields.ErrorURL+fields.Type+fields.Installment+fields.StoreKey+fields.SecurityData)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// VerifyCallback verifies the 3D callback hash. The value is rebuilt from the
// fields named in hashparams (":"-separated), never taken from hashparamsval,
// then hashed with the store key. hashparams must cover the order id and
// mdstatus. SHA-512 (API version 512) and SHA-1 (older terminals) are accepted.
func VerifyCallback(data map[string]string, storeKey string) bool {
	if data["hash"] == "" || data["hashparams"] == "" {
		return false
	}
	var names []string
	for _, name := range strings.Split(data["hashparams"], ":") {
		if name != "" {
			names = append(names, name)
		}
	}
	if !(contains(names, "oid") || contains(names, "orderid")) || !contains(names, "mdstatus") {
		return false
	}

	var value strings.Builder
	for _, name := range names {
		value.WriteString(data[name])
	}
	if echoed, ok := data["hashparamsval"]; ok && echoed != value.String() {
		return false
	}

	hash := strings.ToUpper(data["hash"])
	for _, algorithm := range []core.DigestAlgorithm{core.SHA512, core.SHA1} {
		expected, err := upperHash(algorithm, value.String()+storeKey)
		if err != nil {
			return false
		}
		if core.SafeEqual(expected, hash) {
			return true
		}
	}
	return false
}

// FormatAmount renders an amount in minor units ("120" for 1.20).
func FormatAmount(amount string) (string, error) {
	minor, err := core.ToMinorUnits(amount, "amount")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(minor, 10), nil
}

var currencyCodes = map[string]string{
	"TRY": "949",
	"TL":  "949",
	"USD": "840",
	"EUR": "978",
	"GBP": "826",
	"JPY": "392",
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
	return "", core.NewValidationError(fmt.Sprintf("Currency %s is not supported by Garanti BBVA", currency), "", nil)
}

// FormatInstallment renders the installment count: empty for a single payment.
// Zero means unset.
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

func compactCardNumber(cardNumber string) string {
	return whitespaceRe.ReplaceAllString(cardNumber, "")
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

// BuildXML serializes a GVPSRequest.
func BuildXML(fields []XMLField) string {
	return `<?xml version="1.0" encoding="UTF-8"?><GVPSRequest>` + buildXMLFields(fields) + `</GVPSRequest>`
}

// block returns the content of the first <tag>...</tag>, or "".
func block(xml, tag string) string {
	start := strings.Index(xml, "<"+tag+">")
	if start == -1 {
		return ""
	}
	end := strings.Index(xml[start:], "</"+tag+">")
	if end == -1 {
		return ""
	}
	return xml[start+len(tag)+2 : start+end]
}

var leafRe = regexp.MustCompile(`<([A-Za-z_][\w.-]*)(?:\s[^>]*)?>([^<]*)</([A-Za-z_][\w.-]*)>`)

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
	return result
}

// ParseXMLResponse parses a GVPSResponse. It fails when the body is not one.
func ParseXMLResponse(xml string) (XMLResponse, error) {
	if !strings.Contains(xml, "<GVPSResponse") {
		return XMLResponse{}, core.NewError("Garanti BBVA returned an unexpected response", "PROVIDER_ERROR")
	}
	transactionXML := block(xml, "Transaction")
	responseXML := block(transactionXML, "Response")
	return XMLResponse{
		Response:     leafFields(responseXML),
		Transaction:  leafFields(strings.Replace(transactionXML, "<Response>"+responseXML+"</Response>", "", 1)),
		OrderInquiry: leafFields(block(block(xml, "Order"), "OrderInqResult")),
	}, nil
}

// IsApproved reports response Code 00.
func IsApproved(response XMLResponse) bool {
	return response.Response["Code"] == "00"
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

func (f formFields) get(name string) string {
	for _, field := range f {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
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

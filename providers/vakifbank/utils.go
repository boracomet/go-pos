package vakifbank

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/boracomet/go-pos/core"
)

const (
	// EndpointVPOS is the VPOS 7/24 POX request, relative to the base URL.
	EndpointVPOS = "/VposService/v3/Vposreq.aspx"

	// MPITestURL is the test MPI enrollment (3D Secure) service.
	MPITestURL = "https://3dsecuretest.vakifbank.com.tr:4443/MPIAPI/MPI_Enrollment.aspx"
	// MPIProductionURL is the live MPI enrollment service.
	MPIProductionURL = "https://3dsecure.vakifbank.com.tr:4443/MPIAPI/MPI_Enrollment.aspx"

	// SandboxBaseURL is the test VPOS host.
	SandboxBaseURL = "https://onlineodemetest.vakifbank.com.tr:4443"
	// ProductionBaseURL is the live VPOS host.
	ProductionBaseURL = "https://onlineodeme.vakifbank.com.tr:4443"

	// SuccessCode is the ResultCode of an approved transaction.
	SuccessCode = "0000"
)

// Field is one XML element or form parameter of a request.
type Field struct {
	Key   string
	Value string
}

// Fields is an ordered list of elements; the order is preserved on the wire.
type Fields []Field

var (
	xmlEscaper      = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	xmlEntityReader = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	hexEntityRe     = regexp.MustCompile(`&#x([0-9a-fA-F]+);`)
	decEntityRe     = regexp.MustCompile(`&#(\d+);`)
	// xmlLeafRe has no backreference (RE2 has none), so the closing tag is
	// captured and compared in code instead.
	xmlLeafRe    = regexp.MustCompile(`<([A-Za-z_][\w.-]*)(?:\s[^>]*)?>([^<]*)</([A-Za-z_][\w.-]*)>`)
	orderIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	twoDigitRe   = regexp.MustCompile(`^\d{2}$`)
	fourDigitRe  = regexp.MustCompile(`^\d{4}$`)
	ipRe         = regexp.MustCompile(`^[0-9A-Fa-f:.]{3,45}$`)
	amexRe       = regexp.MustCompile(`^3[47]`)
	troyRe       = regexp.MustCompile(`^(9792|65)`)
	mastercardRe = regexp.MustCompile(`^(5[1-5]|2[2-7])`)
	whitespaceRe = regexp.MustCompile(`\s`)
)

// FormatAmount renders an amount with 2 decimals ("1.20").
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

// CurrencyCode returns the ISO 4217 numeric currency code.
func CurrencyCode(currency string) (string, error) {
	if currency == "" {
		currency = "TRY"
	}
	codes := map[string]string{
		"TRY": "949", "TL": "949", "USD": "840", "EUR": "978", "GBP": "826", "JPY": "392",
	}
	code, ok := codes[strings.ToUpper(currency)]
	if !ok {
		return "", core.NewValidationError("Currency "+currency+" is not supported by VakıfBank", "vakifbank", nil)
	}
	return code, nil
}

// FormatInstallment returns the installment count, or 0 when it must not be
// sent: VPOS rejects an empty NumberOfInstallments.
func FormatInstallment(installment int) (int, error) {
	count := installment
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return 0, core.NewValidationError("Invalid installment count: "+strconv.Itoa(installment), "vakifbank", nil)
	}
	if count > 1 {
		return count, nil
	}
	return 0, nil
}

func expiryParts(month, year string) (string, string, error) {
	mm := padStart(month, 2)
	yyyy := year
	if len(year) == 2 {
		yyyy = "20" + year
	}
	n, _ := strconv.Atoi(mm)
	if !twoDigitRe.MatchString(mm) || n < 1 || n > 12 || !fourDigitRe.MatchString(yyyy) {
		return "", "", core.NewValidationError("Invalid card expiry: "+month+"/"+year, "vakifbank", nil)
	}
	return mm, yyyy, nil
}

// FormatMPIExpiry renders the expiry for the MPI enrollment as YYMM.
func FormatMPIExpiry(month, year string) (string, error) {
	mm, yyyy, err := expiryParts(month, year)
	if err != nil {
		return "", err
	}
	return yyyy[len(yyyy)-2:] + mm, nil
}

// FormatVPOSExpiry renders the expiry for VPOS as YYYYMM.
func FormatVPOSExpiry(month, year string) (string, error) {
	mm, yyyy, err := expiryParts(month, year)
	if err != nil {
		return "", err
	}
	return yyyy + mm, nil
}

// BrandName returns 100 Visa, 200 Mastercard, 300 Troy, 400 Amex.
func BrandName(cardNumber string) string {
	switch {
	case amexRe.MatchString(cardNumber):
		return "400"
	case troyRe.MatchString(cardNumber):
		return "300"
	case mastercardRe.MatchString(cardNumber):
		return "200"
	default:
		return "100"
	}
}

// ValidateOrderID accepts at most 40 letters, digits, "-" or "_": the order id
// doubles as the MPI VerifyEnrollmentRequestId and the VPOS TransactionId.
func ValidateOrderID(orderID string) (string, error) {
	if !orderIDRe.MatchString(orderID) {
		return "", core.NewValidationError(
			"VakıfBank order ids must be 1 to 40 letters, digits, \"-\" or \"_\", got: "+orderID, "vakifbank", nil)
	}
	return orderID, nil
}

// BuildRequestBody wraps ordered fields in a root element and form-encodes the
// document as prmstr.
func BuildRequestBody(root string, fields Fields) string {
	xml := `<?xml version="1.0" encoding="utf-8"?><` + root + `>` + buildXMLFields(fields) + `</` + root + `>`
	return "prmstr=" + encodeURIComponent(xml)
}

func buildXMLFields(fields Fields) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString("<" + f.Key + ">" + xmlEscaper.Replace(f.Value) + "</" + f.Key + ">")
	}
	return b.String()
}

// ParseXML reads the fields of an XML answer (VposResponse, IPaySecure,
// SearchResponse). It fails when the body is not the expected root element.
func ParseXML(xml, root string) (map[string]string, error) {
	if !strings.Contains(xml, "<"+root) {
		return nil, errors.New("VakıfBank returned an unexpected response")
	}
	return leafFields(xml), nil
}

func leafFields(xml string) map[string]string {
	result := make(map[string]string)
	for _, m := range xmlLeafRe.FindAllStringSubmatch(xml, -1) {
		if m[1] != m[3] {
			continue
		}
		if _, seen := result[m[1]]; !seen {
			result[m[1]] = strings.TrimSpace(unescapeXML(m[2]))
		}
	}
	return result
}

func unescapeXML(text string) string {
	text = hexEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		return codePoint(hexEntityRe.FindStringSubmatch(m)[1], 16, m)
	})
	text = decEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		return codePoint(decEntityRe.FindStringSubmatch(m)[1], 10, m)
	})
	return xmlEntityReader.Replace(text)
}

func codePoint(digits string, base int, original string) string {
	n, err := strconv.ParseInt(digits, base, 32)
	if err != nil || !utf8.ValidRune(rune(n)) {
		return original
	}
	return string(rune(n))
}

// IsApproved reports ResultCode 0000.
func IsApproved(fields map[string]string) bool {
	return fields["ResultCode"] == SuccessCode
}

// IsIP reports a value VPOS accepts as a client IP.
func IsIP(value string) bool {
	return value != "" && ipRe.MatchString(value)
}

// encodeURIComponent matches JavaScript's encodeURIComponent, which url.QueryEscape
// does not: it keeps "!'()*" and encodes a space as %20, not "+".
func encodeURIComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isUnreservedJS(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

func isUnreservedJS(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-_.!~*'()", c) >= 0
}

// encodeForm serializes ordered pairs as application/x-www-form-urlencoded.
func encodeForm(fields Fields) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(formComponent(f.Key))
		b.WriteByte('=')
		b.WriteString(formComponent(f.Value))
	}
	return b.String()
}

func formComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

func padStart(value string, length int) string {
	for len(value) < length {
		value = "0" + value
	}
	return value
}

func stripWhitespace(value string) string {
	return whitespaceRe.ReplaceAllString(value, "")
}

func errorCodeTable() map[string]core.PaymentErrorCode {
	return map[string]core.PaymentErrorCode{
		"0001": core.CodeCardDeclined,
		"0005": core.CodeCardDeclined,
		"0051": core.CodeInsufficientFunds,
		"0054": core.CodeExpiredCard,
		"0057": core.CodeCardDeclined,
		"0062": core.CodeCardDeclined,
		"9093": core.CodeProviderError,
	}
}

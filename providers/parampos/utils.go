package parampos

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// MaxInstallments is the highest installment count Param reports rates for (MO_01..MO_12).
const MaxInstallments = 12

var turkishChars = map[rune]byte{
	'Ğ': 0xd0,
	'İ': 0xdd,
	'Ş': 0xde,
	'ğ': 0xf0,
	'ı': 0xfd,
	'ş': 0xfe,
}

// EncodeISO88599 encodes a string as ISO-8859-9 (Turkish), the encoding Param
// uses for hash input. Characters outside the charset are replaced with '?'.
func EncodeISO88599(value string) []byte {
	out := make([]byte, 0, len(value))
	for _, r := range value {
		if b, ok := turkishChars[r]; ok {
			out = append(out, b)
			continue
		}
		if r < 0x100 {
			out = append(out, byte(r))
			continue
		}
		out = append(out, 0x3f)
	}
	return out
}

// GeneratePaymentHash builds Islem_Hash for TP_WMD_UCD (TRY payments):
// base64(sha1(CLIENT_CODE + GUID + Taksit + Islem_Tutar + Toplam_Tutar + Siparis_ID)).
// The amounts must be in the same comma-decimal format sent in the request.
func GeneratePaymentHash(clientCode, guid string, installment int, transactionAmount, totalAmount, orderID string) (string, error) {
	hashString := clientCode + guid + strconv.Itoa(installment) + transactionAmount + totalAmount + orderID
	sum, err := core.Digest(core.SHA1, EncodeISO88599(hashString))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// GeneratePreAuthHash builds Islem_Hash of a pre-authorization
// (TP_Islem_Odeme_OnProv_WMD): base64(sha1(ISO-8859-9(CLIENT_CODE + GUID +
// Islem_Tutar + Toplam_Tutar + Siparis_ID + Hata_URL + Basarili_URL))). Unlike a
// sale the installment is not part of it; the URLs are empty for non-3D
// pre-authorizations.
func GeneratePreAuthHash(clientCode, guid, transactionAmount, totalAmount, orderID, failURL, successURL string) (string, error) {
	hashString := clientCode + guid + transactionAmount + totalAmount + orderID + failURL + successURL
	sum, err := core.Digest(core.SHA1, EncodeISO88599(hashString))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// Generate3DSVerificationHash builds the expected islemHash of a 3D callback:
// base64(sha1(islemGUID + md + mdStatus + orderId + GUID)). This formula is
// hashed as UTF-8, not as ISO-8859-9.
func Generate3DSVerificationHash(islemGUID, md, mdStatus, orderID, guid string) (string, error) {
	hashString := islemGUID + md + mdStatus + orderID + guid
	sum, err := core.Digest(core.SHA1, core.UTF8(hashString))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// Verify3DSCallback verifies the 3D callback hash. guid MUST come from the
// merchant configuration, never from the callback payload: the GUID is the only
// secret in the formula.
func Verify3DSCallback(data any, guid string) bool {
	cb, ok := normalizeCallback(data)
	if !ok {
		return false
	}
	if cb.data.IslemGUID == "" || cb.data.MD == "" || !cb.mdStatusPresent || cb.data.OrderID == "" || cb.data.IslemHash == "" {
		return false
	}
	expected, err := Generate3DSVerificationHash(cb.data.IslemGUID, cb.data.MD, cb.data.MDStatus, cb.data.OrderID, guid)
	if err != nil {
		return false
	}
	return core.SafeEqual(expected, cb.data.IslemHash)
}

// FormatAmount renders a payment amount with 2 decimals and a comma separator ("1000,50").
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, ',', "amount")
}

// FormatRefundAmount renders a refund/cancel amount (Tutar) with 2 decimals and
// a dot separator ("1000.50").
func FormatRefundAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

// ParseAmount converts a Param amount ("1000,50" or "1000.50") to a number.
// The second result is false when the value is absent or not a finite number.
func ParseAmount(amount string) (float64, bool) {
	if amount == "" {
		return 0, false
	}
	return numberOf(strings.Replace(amount, ",", ".", 1))
}

// FormatExpiryMonth renders the card expiry month as MM.
func FormatExpiryMonth(month string) (string, error) {
	padded := month
	for len(padded) < 2 {
		padded = "0" + padded
	}
	n, err := strconv.Atoi(padded)
	if !twoDigits.MatchString(padded) || err != nil || n < 1 || n > 12 {
		return "", core.NewValidationError("Invalid month: "+month, "", nil)
	}
	return padded, nil
}

// FormatExpiryYear renders the card expiry year as YYYY. Two-digit years are
// interpreted as 20YY.
func FormatExpiryYear(year string) (string, error) {
	if twoDigits.MatchString(year) {
		return "20" + year, nil
	}
	if fourDigits.MatchString(year) {
		return year, nil
	}
	return "", core.NewValidationError("Invalid year format: "+year, "", nil)
}

// FormatGSM renders KK_Sahibi_GSM: 10 digits, without country code or leading
// zero (5321234567).
func FormatGSM(gsm string) string {
	if gsm == "" {
		return ""
	}
	digits := nonDigits.ReplaceAllString(gsm, "")
	if len(digits) >= 10 {
		return digits[len(digits)-10:]
	}
	return digits
}

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// EscapeXML escapes the XML special characters. Param's service is sent this
// exact escaping, so element text is escaped here rather than by encoding/xml
// (which writes numeric references for quotes).
func EscapeXML(text string) string {
	return xmlEscaper.Replace(text)
}

var (
	hexEntityRe = regexp.MustCompile(`&#x([0-9a-fA-F]+);`)
	decEntityRe = regexp.MustCompile(`&#(\d+);`)
)

// UnescapeXML decodes XML entities, named and numeric. The passes run in order,
// so a numeric reference that decodes to a named one is decoded again.
func UnescapeXML(text string) string {
	text = hexEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		return codePoint(hexEntityRe.FindStringSubmatch(m)[1], 16, m)
	})
	text = decEntityRe.ReplaceAllStringFunc(text, func(m string) string {
		return codePoint(decEntityRe.FindStringSubmatch(m)[1], 10, m)
	})
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", `"`)
	text = strings.ReplaceAll(text, "&apos;", "'")
	return strings.ReplaceAll(text, "&amp;", "&")
}

func codePoint(digits string, base int, original string) string {
	n, err := strconv.ParseInt(digits, base, 64)
	if err != nil || n < 0 || n > 0x10ffff {
		return original
	}
	return string(rune(n))
}

// Field is one element of a SOAP request body. A field with Children is a
// nested element; otherwise Value is its escaped text.
type Field struct {
	Name     string
	Value    string
	Children Fields
}

// Fields is an ordered list of SOAP elements. Param expects the elements in the
// documented order, so requests are built as a slice, never as a map.
type Fields []Field

// NewField builds a leaf element.
func NewField(name, value string) Field { return Field{Name: name, Value: value} }

// NewGroup builds an element holding child elements.
func NewGroup(name string, children Fields) Field {
	return Field{Name: name, Children: children}
}

// BuildXMLFields serializes ordered fields into XML elements, escaping values.
func BuildXMLFields(fields Fields) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString("<" + f.Name + ">")
		if f.Children != nil {
			b.WriteString(BuildXMLFields(f.Children))
		} else {
			b.WriteString(EscapeXML(f.Value))
		}
		b.WriteString("</" + f.Name + ">")
	}
	return b.String()
}

// BuildSOAPEnvelope builds the SOAP envelope of a Param API request.
func BuildSOAPEnvelope(soapAction string, fields Fields) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" ` +
		`xmlns:xsd="http://www.w3.org/2001/XMLSchema" ` +
		`xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">` +
		`<soap:Body>` +
		`<` + soapAction + ` xmlns="` + Namespace + `">` + BuildXMLFields(fields) + `</` + soapAction + `>` +
		`</soap:Body>` +
		`</soap:Envelope>`
}

var (
	faultRe = regexp.MustCompile(`(?i)<faultstring[^>]*>([\s\S]*?)</faultstring>`)
	// leafRe has no backreference: the opening and closing tags are compared
	// after the match, which is what the service's flat result elements need.
	leafRe  = regexp.MustCompile(`<([\w:.-]+)(?:\s[^>]*)?>([^<]*)</([\w:.-]+)>`)
	emptyRe = regexp.MustCompile(`<([\w:.-]+)(?:\s[^>]*)?/>`)
)

// ParseSOAPResponse extracts the resultTagName element from a SOAP response and
// flattens all leaf elements (including nested ones such as DT_Bilgi) into a
// record. The first occurrence of a tag wins; values are XML-unescaped. It
// returns an error on SOAP faults or when the result element is missing.
func ParseSOAPResponse(soapXML, resultTagName string) (Result, error) {
	if fault := faultRe.FindStringSubmatch(soapXML); fault != nil {
		return nil, newError("Parampos SOAP fault: " + strings.TrimSpace(UnescapeXML(fault[1])))
	}

	tag := regexp.QuoteMeta(resultTagName)
	resultRe := regexp.MustCompile(`(?i)<` + tag + `(?:\s[^>]*)?>([\s\S]*?)</` + tag + `>`)
	match := resultRe.FindStringSubmatch(soapXML)
	if match == nil {
		selfClosing := regexp.MustCompile(`(?i)<` + tag + `(?:\s[^>]*)?/>`)
		if selfClosing.MatchString(soapXML) {
			return Result{}, nil
		}
		return nil, newError("Could not find " + resultTagName + " in SOAP response")
	}

	result := Result{}
	content := match[1]
	for _, m := range leafRe.FindAllStringSubmatch(content, -1) {
		if m[1] != m[3] {
			continue
		}
		name := localName(m[1])
		if _, exists := result[name]; !exists {
			result[name] = UnescapeXML(m[2])
		}
	}
	for _, m := range emptyRe.FindAllStringSubmatch(content, -1) {
		name := localName(m[1])
		if _, exists := result[name]; !exists {
			result[name] = ""
		}
	}
	return result, nil
}

// ParseDataSetRows returns the rows of a .NET DataSet in a SOAP response
// (<RowTag diffgr:id=...>...</RowTag>), each as a flat record of its child
// elements with trimmed values.
func ParseDataSetRows(soapXML, rowTag string) []Result {
	tag := regexp.QuoteMeta(rowTag)
	rowRe := regexp.MustCompile(`(?i)<` + tag + `(?:\s[^>]*)?>([\s\S]*?)</` + tag + `>`)
	rows := []Result{}
	for _, rowMatch := range rowRe.FindAllStringSubmatch(soapXML, -1) {
		row := Result{}
		for _, leaf := range leafRe.FindAllStringSubmatch(rowMatch[1], -1) {
			if leaf[1] != leaf[3] {
				continue
			}
			row[localName(leaf[1])] = strings.TrimSpace(UnescapeXML(leaf[2]))
		}
		rows = append(rows, row)
	}
	return rows
}

func localName(tag string) string {
	if i := strings.LastIndex(tag, ":"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

// IsSuccess reports whether a Param result code means success: numeric codes
// greater than zero.
func IsSuccess(code string) bool {
	return positive(code)
}

// ValidateTurkishIdentityNumber reports whether a TC Kimlik No is valid.
func ValidateTurkishIdentityNumber(identityNumber string) bool {
	if !elevenDigits.MatchString(identityNumber) {
		return false
	}
	digits := make([]int, 11)
	for i := 0; i < 11; i++ {
		digits[i] = int(identityNumber[i] - '0')
	}
	if digits[0] == 0 {
		return false
	}
	sum1 := (digits[0] + digits[2] + digits[4] + digits[6] + digits[8]) * 7
	sum2 := digits[1] + digits[3] + digits[5] + digits[7]
	tenth := (((sum1 - sum2) % 10) + 10) % 10
	if tenth != digits[9] {
		return false
	}
	sum3 := 0
	for _, d := range digits[:10] {
		sum3 += d
	}
	return sum3%10 == digits[10]
}

// MaskCardNumber masks a card number for display, keeping the first and last four digits.
func MaskCardNumber(cardNumber string) string {
	if len(cardNumber) < 10 {
		return cardNumber
	}
	return cardNumber[:4] + strings.Repeat("*", len(cardNumber)-8) + cardNumber[len(cardNumber)-4:]
}

// RatesOf returns the commission rates of a Param rate row by installment count.
// Param marks an unavailable installment with a negative rate (-1 / -2); those
// are omitted.
func RatesOf(row Result) map[int]float64 {
	rates := map[int]float64{}
	for i := 1; i <= MaxInstallments; i++ {
		raw, ok := row["MO_"+pad2(i)]
		if !ok || raw == "" {
			continue
		}
		rate, valid := numberOf(strings.Replace(raw, ",", ".", 1))
		if valid && rate >= 0 {
			rates[i] = rate
		}
	}
	return rates
}

// CalculateTotalMinor computes Param's total: Toplam_Tutar = Islem_Tutar +
// Islem_Tutar x rate / 100, rounded half up to kurus. It returns the total in
// minor units, computed in integers so large amounts do not drift.
func CalculateTotalMinor(priceMinor int64, ratePercent float64) int64 {
	rateScaled := big.NewInt(int64(math.Floor(ratePercent*10000 + 0.5)))
	numerator := new(big.Int).Mul(big.NewInt(priceMinor), new(big.Int).Add(big.NewInt(1000000), rateScaled))
	numerator.Add(numerator, big.NewInt(500000))
	return numerator.Quo(numerator, big.NewInt(1000000)).Int64()
}

// MapOrderStatus maps a Param Durum value to the unified payment status.
func MapOrderStatus(durum string) core.PaymentStatus {
	switch durum {
	case "SUCCESS", "PARTIAL_REFUND":
		return core.StatusSuccess
	case "CANCEL", "REFUND":
		return core.StatusCancelled
	case "FAIL", "BANK_FAIL":
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

var (
	twoDigits    = regexp.MustCompile(`^\d{2}$`)
	fourDigits   = regexp.MustCompile(`^\d{4}$`)
	elevenDigits = regexp.MustCompile(`^\d{11}$`)
	nonDigits    = regexp.MustCompile(`\D`)
	bankCodeRe   = regexp.MustCompile(`^\d{1,2}$`)
)

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// numberOf mirrors the service's numeric strings: blank is 0, anything that is
// not a finite number is reported as absent.
func numberOf(value string) (float64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, true
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

// fixed2 renders a float with exactly two decimals, rounding halves up on the
// exact binary value, as the service's amounts are rendered.
func fixed2(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "NaN"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	r := new(big.Rat).SetFloat64(value)
	if r == nil {
		return sign + "0.00"
	}
	r.Mul(r, big.NewRat(100, 1))
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	return sign + strconv.FormatInt(new(big.Int).Quo(n, big.NewInt(100)).Int64(), 10) + "." +
		pad2(int(new(big.Int).Rem(n, big.NewInt(100)).Int64()))
}

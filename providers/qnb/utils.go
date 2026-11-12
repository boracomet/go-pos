package qnb

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/boracomet/go-pos/core"
)

const (
	// EndpointGate is the 3D Secure form target, posted by the customer's browser.
	EndpointGate = "/Gateway/Default.aspx"
	// EndpointAPI is the XML API (PayforRequest).
	EndpointAPI = "/Gateway/XmlGate.aspx"

	// MbrID is QNB's institution code on the PayFor platform.
	MbrID = "5"

	// SandboxBaseURL is the test host.
	SandboxBaseURL = "https://vpostest.qnb.com.tr"
	// ProductionBaseURL is the live host.
	ProductionBaseURL = "https://vpos.qnb.com.tr"
)

// SecureTypes lists the supported 3D models: "3DModel" lets the bank verify the
// card and charges it later with 3DModelPayment; "3DPay" verifies and charges it.
var SecureTypes = []string{"3DModel", "3DPay"}

// Field is one XML element of a request.
type Field struct {
	Key   string
	Value string
}

// Fields is an ordered list of XML elements; the order is preserved on the wire.
type Fields []Field

var (
	xmlEscaper      = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	xmlEntityReader = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	hexEntityRe     = regexp.MustCompile(`&#x([0-9a-fA-F]+);`)
	decEntityRe     = regexp.MustCompile(`&#(\d+);`)
	// xmlLeafRe has no backreference (RE2 has none), so the closing tag is
	// captured and compared in code instead.
	xmlLeafRe    = regexp.MustCompile(`<([A-Za-z_][\w.-]*)(?:\s[^>]*)?>([^<]*)</([A-Za-z_][\w.-]*)>`)
	orderIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	twoDigitRe   = regexp.MustCompile(`^\d{2}$`)
	whitespaceRe = regexp.MustCompile(`\s`)
)

// sha1Base64 hashes with SHA-1. core.Digest only fails for an unknown algorithm,
// so its error is unreachable here.
func sha1Base64(data string) string {
	sum, _ := core.Digest(core.SHA1, core.UTF8(data))
	return core.ToBase64(sum)
}

// Create3DHash builds the 3D form hash:
// base64(SHA1(MbrId + OrderId + PurchAmount + OkUrl + FailUrl + TxnType + InstallmentCount + Rnd + MerchantPass)).
func Create3DHash(fields HashFields) string {
	return sha1Base64(MbrID +
		fields.OrderID +
		fields.Amount +
		fields.OkURL +
		fields.FailURL +
		fields.TxnType +
		fields.Installment +
		fields.Rnd +
		fields.MerchantPass)
}

// VerifyCallback checks the ResponseHash of a 3D callback:
// base64(SHA1(MerchantID + MerchantPass + OrderId + AuthCode + ProcReturnCode + 3DStatus + ResponseRnd + UserCode)),
// with the merchant id and user code taken from the configuration, never the callback.
func VerifyCallback(data map[string]string, credentials Credentials) bool {
	if data["ResponseHash"] == "" || data["ResponseRnd"] == "" || data["OrderId"] == "" {
		return false
	}
	expected := sha1Base64(credentials.MerchantID +
		credentials.MerchantPass +
		data["OrderId"] +
		data["AuthCode"] +
		data["ProcReturnCode"] +
		data["3DStatus"] +
		data["ResponseRnd"] +
		credentials.UserCode)
	return core.SafeEqual(expected, data["ResponseHash"])
}

// FormatAmount renders an amount with 2 decimals ("1.20").
func FormatAmount(amount string) (string, error) {
	return core.FormatDecimal(amount, '.', "amount")
}

// CurrencyCode returns the ISO 4217 numeric currency code.
func CurrencyCode(currency string) (string, error) {
	if currency == "" {
		currency = "TRY"
	}
	codes := map[string]string{"TRY": "949", "TL": "949", "USD": "840", "EUR": "978"}
	code, ok := codes[strings.ToUpper(currency)]
	if !ok {
		return "", core.NewValidationError("Currency "+currency+" is not supported by QNB", "qnb", nil)
	}
	return code, nil
}

// FormatInstallment renders InstallmentCount: "0" for a single payment.
func FormatInstallment(installment int) (string, error) {
	count := installment
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return "", core.NewValidationError("Invalid installment count: "+strconv.Itoa(installment), "qnb", nil)
	}
	if count > 1 {
		return strconv.Itoa(count), nil
	}
	return "0", nil
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
	n, _ := strconv.Atoi(mm)
	if !twoDigitRe.MatchString(mm) || n < 1 || n > 12 || !twoDigitRe.MatchString(yy) {
		return "", core.NewValidationError("Invalid card expiry: "+month+"/"+year, "qnb", nil)
	}
	return mm + yy, nil
}

// ValidateOrderID accepts 1 to 64 letters, digits, "-" or "_"; the bank rejects
// Turkish characters.
func ValidateOrderID(orderID string) (string, error) {
	if !orderIDRe.MatchString(orderID) {
		return "", core.NewValidationError(
			"QNB order ids must be 1 to 64 letters, digits, \"-\" or \"_\", got: "+orderID, "qnb", nil)
	}
	return orderID, nil
}

// BuildXML wraps ordered fields in a PayforRequest document.
func BuildXML(fields Fields) string {
	return `<?xml version="1.0" encoding="utf-8"?><PayforRequest>` + buildXMLFields(fields) + `</PayforRequest>`
}

func buildXMLFields(fields Fields) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString("<" + f.Key + ">" + xmlEscaper.Replace(f.Value) + "</" + f.Key + ">")
	}
	return b.String()
}

// ParseXMLResponse reads the fields of a PayforResponse; empty elements become
// "". It fails when the body is not one.
func ParseXMLResponse(xml string) (map[string]string, error) {
	if !strings.Contains(xml, "<PayforResponse") {
		return nil, errors.New("QNB returned an unexpected response")
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

// IsApproved reports ProcReturnCode 00.
func IsApproved(fields map[string]string) bool {
	return fields["ProcReturnCode"] == "00"
}

// MapOrderStatus reads an OrderInquiry answer: TxnStatus Y (approved), V
// (voided); IsVoided / IsRefunded mark cancelled or refunded sales.
func MapOrderStatus(inquiry map[string]string) core.PaymentStatus {
	if inquiry["IsVoided"] == "true" || inquiry["IsRefunded"] == "true" || inquiry["TxnStatus"] == "V" {
		return core.StatusCancelled
	}
	if inquiry["TxnStatus"] == "Y" && inquiry["TxnResult"] == "Success" {
		return core.StatusSuccess
	}
	if inquiry["TxnResult"] == "Failed" {
		return core.StatusFailure
	}
	return core.StatusPending
}

// IsCapturedSale reports an approved, not cancelled sale in an OrderInquiry answer.
func IsCapturedSale(inquiry map[string]string) bool {
	return IsApproved(inquiry) && MapOrderStatus(inquiry) == core.StatusSuccess
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
	table := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes)+4)
	for code, normalized := range core.ISO8583ErrorCodes {
		table[code] = normalized
	}
	table["M030"] = core.CodeProviderError
	table["V013"] = core.CodeInvalidRequest
	table["V014"] = core.CodeInvalidRequest
	table["V034"] = core.CodeThreeDSFailed
	return table
}

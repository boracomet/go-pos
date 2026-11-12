package yapikredi

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// Posnet endpoints, relative to the provider base URL.
const (
	// EndpointAPI is the XML API (posnetRequest), posted as the xmldata form field.
	EndpointAPI = "/PosnetWebService/XML"
	// EndpointGate is the 3D Secure form target, opened in the customer's browser.
	EndpointGate = "/3DSWebService/YKBPaymentService"
)

// FormatAmount returns the amount in minor units ("120" for 1.20).
func FormatAmount(amount string) (string, error) {
	minor, err := core.ToMinorUnits(amount, "")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(minor, 10), nil
}

// CurrencyCode returns the Posnet currency code: TRY "TL", USD "US", EUR "EU".
// An empty currency means TRY; any other unsupported currency is rejected.
func CurrencyCode(currency string) (string, error) {
	switch strings.ToUpper(orDefault(currency, "TRY")) {
	case "TRY", "TL":
		return "TL", nil
	case "USD":
		return "US", nil
	case "EUR":
		return "EU", nil
	}
	return "", core.NewValidationError(
		fmt.Sprintf("Currency %s is not supported by Yapı Kredi", currency), providerName, nil)
}

// FormatInstallment returns the installment count as two digits, "00" for a
// single payment. The count must be 1 to 99; 0 means a single payment.
func FormatInstallment(count int) (string, error) {
	if count == 0 {
		count = 1
	}
	if count < 1 || count > 99 {
		return "", core.NewValidationError(
			"Invalid installment count: "+strconv.Itoa(count), providerName, nil)
	}
	if count > 1 {
		return padStart(strconv.Itoa(count), 2), nil
	}
	return "00", nil
}

// FormatExpiry returns the card expiry as YYMM ("12", "2030" -> "3012").
func FormatExpiry(month, year string) (string, error) {
	mm := padStart(month, 2)
	yy := padStart(year, 2)
	if len(year) == 4 {
		yy = year[len(year)-2:]
	}
	invalid := core.NewValidationError(
		fmt.Sprintf("Invalid card expiry: %s/%s", month, year), providerName, nil)
	if !twoDigitRe.MatchString(mm) || !twoDigitRe.MatchString(yy) {
		return "", invalid
	}
	m, err := strconv.Atoi(mm)
	if err != nil || m < 1 || m > 12 {
		return "", invalid
	}
	return yy + mm, nil
}

// ToXID returns the order id as the 20-character XID of a 3D Secure payment,
// left-padded with zeros.
func ToXID(orderID string) (string, error) {
	return padOrderID(orderID, 20, xidRe)
}

// ToOrderID returns the order id of a non-3D sale: 24 characters, left-padded
// with zeros.
func ToOrderID(orderID string) (string, error) {
	return padOrderID(orderID, 24, orderIDRe)
}

// ToThreeDSOrderID returns the order id the order inquiry knows a 3D Secure
// payment by: "TDS_" + XID.
func ToThreeDSOrderID(orderID string) (string, error) {
	xid, err := ToXID(orderID)
	if err != nil {
		return "", err
	}
	return "TDS_" + xid, nil
}

func padOrderID(orderID string, length int, pattern *regexp.Regexp) (string, error) {
	if !pattern.MatchString(orderID) {
		return "", core.NewValidationError(fmt.Sprintf(
			"Yapı Kredi order ids must be 1 to %d letters, digits or \"_\", got: %s", length, orderID),
			providerName, nil)
	}
	return padStart(orderID, length), nil
}

// MACFields are the order values a Posnet MAC covers, with the credentials that
// sign them. Amount is in minor units, as sent with the request.
type MACFields struct {
	XID        string
	Amount     string
	Currency   string
	MerchantID string
	TerminalID string
	EncKey     string
}

// CreateMac returns the MAC of the 3D resolve and transaction requests:
// base64(SHA256(XID;amount;currency;merchantId;base64(SHA256(encKey;terminalId)))).
func CreateMac(fields MACFields) (string, error) {
	firstHash, err := sha256Base64(fields.EncKey + ";" + fields.TerminalID)
	if err != nil {
		return "", err
	}
	return sha256Base64(strings.Join([]string{
		fields.XID, fields.Amount, fields.Currency, fields.MerchantID, firstHash,
	}, ";"))
}

// CreateResponseMac returns the bank's MAC on its answers, which the 3D guide
// recommends checking:
// base64(SHA256(first;xid;amount;currency;merchantId;base64(SHA256(encKey;terminalId)))),
// where first is mdStatus (resolve answer) or hostlogkey (charge answer).
func CreateResponseMac(first string, fields MACFields) (string, error) {
	firstHash, err := sha256Base64(fields.EncKey + ";" + fields.TerminalID)
	if err != nil {
		return "", err
	}
	return sha256Base64(strings.Join([]string{
		first, fields.XID, fields.Amount, fields.Currency, fields.MerchantID, firstHash,
	}, ";"))
}

func sha256Base64(data string) (string, error) {
	sum, err := core.Digest(core.SHA256, []byte(data))
	if err != nil {
		return "", err
	}
	return core.ToBase64(sum), nil
}

// XMLField is one posnetRequest element, in the order the bank reads them.
// Children holds a nested block and takes precedence over Value; an empty Value
// renders an empty element.
type XMLField struct {
	Name     string
	Value    string
	Children []XMLField
}

// BuildXMLFields serializes ordered fields into XML elements. Values are escaped.
func BuildXMLFields(fields []XMLField) string {
	var out strings.Builder
	for _, field := range fields {
		out.WriteString("<" + field.Name + ">")
		if len(field.Children) > 0 {
			out.WriteString(BuildXMLFields(field.Children))
		} else {
			out.WriteString(escapeXML(field.Value))
		}
		out.WriteString("</" + field.Name + ">")
	}
	return out.String()
}

// BuildRequestBody returns the posnetRequest XML, sent form-encoded as xmldata.
// Every character outside printable ASCII is sent as a numeric character
// reference, so the body stays ASCII whatever the card holder name holds.
func BuildRequestBody(fields []XMLField) string {
	xml := `<?xml version="1.0" encoding="ISO-8859-9"?><posnetRequest>` +
		asciiReferences(BuildXMLFields(fields)) + `</posnetRequest>`
	return "xmldata=" + encodeURIComponent(xml)
}

// XMLResponse is a parsed posnetResponse.
type XMLResponse struct {
	// Fields holds the top-level fields: approved, respCode, respText,
	// hostlogkey, authCode, mac, ...
	Fields map[string]string `json:"fields"`
	// Transactions holds the agreement (order inquiry) transactions.
	Transactions []map[string]string `json:"transactions"`
	// OOSRequestData holds oosRequestDataResponse: data1, data2, sign.
	OOSRequestData map[string]string `json:"oosRequestData"`
	// OOSResolveMerchantData holds oosResolveMerchantDataResponse: xid, amount,
	// currency, installment, mdStatus, ...
	OOSResolveMerchantData map[string]string `json:"oosResolveMerchantData"`
}

// ParseXMLResponse parses a posnetResponse, keeping the top-level fields and the
// nested blocks apart. It returns an error when the body is not one.
func ParseXMLResponse(body string) (XMLResponse, error) {
	// The order inquiry declares utf-16; read as text, its ASCII comes with NULs
	xml := strings.ReplaceAll(body, "\x00", "")
	if !strings.Contains(xml, "<posnetResponse") {
		return XMLResponse{}, errors.New("Yapı Kredi returned an unexpected response")
	}
	request := block(xml, "oosRequestDataResponse")
	resolve := block(xml, "oosResolveMerchantDataResponse")
	list := block(xml, "transactions")
	top := xml
	top = strings.Replace(top, "<oosRequestDataResponse>"+request+"</oosRequestDataResponse>", "", 1)
	top = strings.Replace(top, "<oosResolveMerchantDataResponse>"+resolve+"</oosResolveMerchantDataResponse>", "", 1)
	top = strings.Replace(top, "<transactions>"+list+"</transactions>", "", 1)

	fields, err := leafFields(top)
	if err != nil {
		return XMLResponse{}, err
	}
	transactions := make([]map[string]string, 0)
	for _, match := range transactionRe.FindAllStringSubmatch(list, -1) {
		transaction, err := leafFields(match[1])
		if err != nil {
			return XMLResponse{}, err
		}
		transactions = append(transactions, transaction)
	}
	requestData, err := leafFields(request)
	if err != nil {
		return XMLResponse{}, err
	}
	resolveData, err := leafFields(resolve)
	if err != nil {
		return XMLResponse{}, err
	}
	return XMLResponse{
		Fields:                 fields,
		Transactions:           transactions,
		OOSRequestData:         requestData,
		OOSResolveMerchantData: resolveData,
	}, nil
}

// IsApproved reports whether the host answered with approved "1".
func IsApproved(response XMLResponse) bool {
	return response.Fields["approved"] == "1"
}

// leafFields flattens the leaf elements of a block. The first occurrence of a
// tag wins; values are XML-unescaped and trimmed.
func leafFields(xml string) (map[string]string, error) {
	result := make(map[string]string)
	for _, match := range leafRe.FindAllStringSubmatch(xml, -1) {
		tag, value, closing := match[1], match[2], match[3]
		// The pattern cannot use a backreference, so the closing tag is compared here
		if tag != closing {
			continue
		}
		if _, exists := result[tag]; exists {
			continue
		}
		text, err := unescapeXML(value)
		if err != nil {
			return nil, err
		}
		result[tag] = strings.TrimSpace(text)
	}
	return result, nil
}

// block returns the content of the first <tag> element, or "" when it is absent.
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

func escapeXML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	text = strings.ReplaceAll(text, `"`, "&quot;")
	return strings.ReplaceAll(text, "'", "&apos;")
}

func unescapeXML(text string) (string, error) {
	var invalid error
	decode := func(pattern *regexp.Regexp, base int) func(string) string {
		return func(match string) string {
			if invalid != nil {
				return match
			}
			code, err := strconv.ParseInt(pattern.FindStringSubmatch(match)[1], base, 32)
			if err != nil || code > 0x10FFFF || (code >= 0xD800 && code <= 0xDFFF) {
				invalid = fmt.Errorf("invalid XML character reference: %s", match)
				return match
			}
			return string(rune(code))
		}
	}
	out := hexEntityRe.ReplaceAllStringFunc(text, decode(hexEntityRe, 16))
	out = decEntityRe.ReplaceAllStringFunc(out, decode(decEntityRe, 10))
	if invalid != nil {
		return "", invalid
	}
	out = strings.ReplaceAll(out, "&lt;", "<")
	out = strings.ReplaceAll(out, "&gt;", ">")
	out = strings.ReplaceAll(out, "&quot;", `"`)
	out = strings.ReplaceAll(out, "&apos;", "'")
	return strings.ReplaceAll(out, "&amp;", "&"), nil
}

func asciiReferences(text string) string {
	var out strings.Builder
	for _, char := range text {
		if char < 0x20 || char > 0x7e {
			out.WriteString("&#" + strconv.FormatInt(int64(char), 10) + ";")
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}

const upperHex = "0123456789ABCDEF"

// encodeURIComponent encodes a URI component the way the bank's form expects:
// A-Za-z0-9 and -_.!~*'() stay as they are, everything else becomes %XX over the
// UTF-8 bytes, and a space becomes %20 rather than '+'.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			strings.IndexByte("-_.!~*'()", char) >= 0 {
			out.WriteByte(char)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(upperHex[char>>4])
		out.WriteByte(upperHex[char&0x0f])
	}
	return out.String()
}

var (
	twoDigitRe    = regexp.MustCompile(`^\d{2}$`)
	xidRe         = regexp.MustCompile(`^[A-Za-z0-9_]{1,20}$`)
	orderIDRe     = regexp.MustCompile(`^[A-Za-z0-9_]{1,24}$`)
	whitespaceRe  = regexp.MustCompile(`\s`)
	leafRe        = regexp.MustCompile(`<([A-Za-z_][\w.-]*)(?:\s[^>]*)?>([^<]*)</([A-Za-z_][\w.-]*)>`)
	transactionRe = regexp.MustCompile(`(?s)<transaction>(.*?)</transaction>`)
	hexEntityRe   = regexp.MustCompile(`&#x([0-9a-fA-F]+);`)
	decEntityRe   = regexp.MustCompile(`&#(\d+);`)
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

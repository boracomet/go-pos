package denizbank

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/boracomet/go-pos/core"
)

const (
	// Endpoint is both the 3D form target and the form POST API, relative to the
	// base URL.
	Endpoint = "/mpi/Default.aspx"

	// SandboxBaseURL is the test host.
	SandboxBaseURL = "https://test.inter-vpos.com.tr"
	// ProductionBaseURL is the live host.
	ProductionBaseURL = "https://inter-vpos.com.tr"
)

// Field is one form parameter of a request.
type Field struct {
	Key   string
	Value string
}

// Fields is an ordered list of parameters; the order is preserved on the wire.
type Fields []Field

var (
	twoDigitRe   = regexp.MustCompile(`^\d{2}$`)
	amexRe       = regexp.MustCompile(`^3[47]`)
	mastercardRe = regexp.MustCompile(`^(5[1-5]|2[2-7])`)
	whitespaceRe = regexp.MustCompile(`\s`)
)

// sha1Base64 hashes with SHA-1. core.Digest only fails for an unknown algorithm,
// so its error is unreachable here.
func sha1Base64(data string) string {
	sum, _ := core.Digest(core.SHA1, core.UTF8(data))
	return core.ToBase64(sum)
}

// Create3DHash builds the 3D form hash:
// base64(SHA1(ShopCode + OrderId + PurchAmount + OkUrl + FailUrl + TxnType + InstallmentCount + Rnd + MerchantPass)).
func Create3DHash(fields HashFields) string {
	return sha1Base64(fields.ShopCode +
		fields.OrderID +
		fields.Amount +
		fields.OkURL +
		fields.FailURL +
		fields.TxnType +
		fields.Installment +
		fields.Rnd +
		fields.MerchantPass)
}

// VerifyCallback checks the 3D callback HASH. The value is rebuilt from the
// fields named in HASHPARAMS (":"-separated), never taken from HASHPARAMSVAL,
// and must cover OrderId: HASH = base64(SHA1(values + MerchantPass)).
func VerifyCallback(data map[string]string, merchantPass string) bool {
	if data["HASH"] == "" || data["HASHPARAMS"] == "" {
		return false
	}
	var names []string
	for _, name := range strings.Split(data["HASHPARAMS"], ":") {
		if name != "" {
			names = append(names, name)
		}
	}
	if !slices.Contains(names, "OrderId") {
		return false
	}
	var joined strings.Builder
	for _, name := range names {
		joined.WriteString(data[name])
	}
	value := joined.String()
	if given, sent := data["HASHPARAMSVAL"]; sent && given != value {
		return false
	}
	return core.SafeEqual(sha1Base64(value+merchantPass), data["HASH"])
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
	codes := map[string]string{"TRY": "949", "TL": "949", "USD": "840", "EUR": "978", "GBP": "826"}
	code, ok := codes[strings.ToUpper(currency)]
	if !ok {
		return "", core.NewValidationError("Currency "+currency+" is not supported by Denizbank", "denizbank", nil)
	}
	return code, nil
}

// FormatInstallment renders InstallmentCount: empty for a single payment.
func FormatInstallment(installment int) (string, error) {
	count := installment
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return "", core.NewValidationError("Invalid installment count: "+strconv.Itoa(installment), "denizbank", nil)
	}
	if count > 1 {
		return strconv.Itoa(count), nil
	}
	return "", nil
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
		return "", core.NewValidationError("Invalid card expiry: "+month+"/"+year, "denizbank", nil)
	}
	return mm + yy, nil
}

// CardType returns the CardType of the 3D form: "visa", "mastercard" or "amex",
// defaulting to "visa" when the BIN is unknown.
func CardType(cardNumber string) string {
	switch {
	case amexRe.MatchString(cardNumber):
		return "amex"
	case mastercardRe.MatchString(cardNumber):
		return "mastercard"
	default:
		return "visa"
	}
}

// ParseResponse reads an InterVPos answer: "Key=Value;;Key=Value;;".
func ParseResponse(text string) (map[string]string, error) {
	if !strings.Contains(text, "ProcReturnCode=") {
		return nil, errors.New("Denizbank returned an unexpected response")
	}
	result := make(map[string]string)
	for _, pair := range strings.Split(text, ";;") {
		index := strings.Index(pair, "=")
		if index <= 0 {
			continue
		}
		key := strings.TrimSpace(strings.TrimLeft(pair[:index], ";"))
		if key == "" {
			continue
		}
		if _, seen := result[key]; seen {
			continue
		}
		result[key] = strings.TrimSpace(pair[index+1:])
	}
	return result, nil
}

// IsApproved reports ProcReturnCode 00.
func IsApproved(fields map[string]string) bool {
	return fields["ProcReturnCode"] == "00"
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
	table := make(map[string]core.PaymentErrorCode, len(core.ISO8583ErrorCodes))
	for code, normalized := range core.ISO8583ErrorCodes {
		table[code] = normalized
	}
	return table
}

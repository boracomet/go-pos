package core

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PaymentValidationRules selects the checks a provider needs. The rules are
// deliberately conservative: they reject what every provider rejects, and
// nothing that some provider accepts.
type PaymentValidationRules struct {
	// Card validates PaymentCard (the provider receives card data).
	Card bool
	// StoredCard means the provider accepts StoredCard instead of PaymentCard.
	StoredCard bool
	// SaveCard means the provider can save the card during the payment.
	SaveCard bool
	// BasketMatchesPrice requires basket item prices to add up to Price (iyzico).
	BasketMatchesPrice bool
	// BasketRequired requires at least one basket item.
	BasketRequired bool
	// Required lists dotted paths that must be present and non-empty, e.g. "buyer.email".
	Required []string
}

var (
	decimalRe = regexp.MustCompile(`^\d+(\.\d{1,2})?$`)
	// Domain labels exclude '.', so the pattern matches in linear time.
	emailRe    = regexp.MustCompile(`^[^\s@]+@[^\s@.]+(\.[^\s@.]+)+$`)
	ipv4Re     = regexp.MustCompile(`^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$`)
	ipv6Re     = regexp.MustCompile(`^[0-9a-fA-F:]+(%\w+)?$`)
	cardRe     = regexp.MustCompile(`^\d{12,19}$`)
	yearRe     = regexp.MustCompile(`^\d{2}(\d{2})?$`)
	twoDigitRe = regexp.MustCompile(`^\d{2}$`)
	cvcRe      = regexp.MustCompile(`^\d{3,4}$`)
	gsmRe      = regexp.MustCompile(`^\d{10,15}$`)
	spaceDash  = regexp.MustCompile(`[\s-]`)
	gsmStrip   = regexp.MustCompile(`[\s()+-]`)
)

// Luhn reports whether a digit string passes the Luhn checksum.
func Luhn(digits string) bool {
	sum := 0
	for i := 0; i < len(digits); i++ {
		d := int(digits[len(digits)-1-i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

func isBlank(value string) bool { return strings.TrimSpace(value) == "" }

// toCents converts a 2-decimal amount to minor units, reporting false when the
// value is not a valid decimal amount.
func toCents(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if !decimalRe.MatchString(text) {
		return 0, false
	}
	whole, fraction := text, ""
	if i := strings.Index(text, "."); i >= 0 {
		whole, fraction = text[:i], text[i+1:]
	}
	for len(fraction) < 2 {
		fraction += "0"
	}
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(fraction, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return w*100 + f, true
}

// RequestValidator collects validation issues and reports them together.
type RequestValidator struct {
	Issues []ValidationIssue
}

// Add records an issue for a dotted field path.
func (v *RequestValidator) Add(path, message string) *RequestValidator {
	v.Issues = append(v.Issues, ValidationIssue{Path: path, Message: message})
	return v
}

// Amount validates a decimal amount, returning its minor units. A blank value
// is an issue only when required.
func (v *RequestValidator) Amount(value, path string, required bool) (int64, bool) {
	if isBlank(value) {
		if required {
			v.Add(path, "is required")
		}
		return 0, false
	}
	cents, ok := toCents(value)
	if !ok {
		v.Add(path, `must be a decimal amount with at most 2 decimals, e.g. "10.50"`)
		return 0, false
	}
	if cents <= 0 {
		v.Add(path, "must be greater than 0")
	}
	return cents, true
}

// Card validates card number, holder, expiry and CVC. A card is valid through
// the last day of its expiry month.
func (v *RequestValidator) Card(card *PaymentCard, path string, now time.Time) *RequestValidator {
	if path == "" {
		path = "paymentCard"
	}
	if card == nil {
		return v.Add(path, "is required")
	}

	number := spaceDash.ReplaceAllString(card.CardNumber, "")
	if !cardRe.MatchString(number) {
		v.Add(path+".cardNumber", "must be 12-19 digits")
	} else if !Luhn(number) {
		v.Add(path+".cardNumber", "is not a valid card number (Luhn check failed)")
	}

	if isBlank(card.CardHolderName) {
		v.Add(path+".cardHolderName", "is required")
	}

	month, monthErr := strconv.Atoi(strings.TrimSpace(card.ExpireMonth))
	rawYear := strings.TrimSpace(card.ExpireYear)
	year, yearErr := strconv.Atoi(rawYear)
	if twoDigitRe.MatchString(rawYear) {
		year = 2000 + year
	}
	if monthErr != nil || month < 1 || month > 12 {
		v.Add(path+".expireMonth", "must be 1-12")
	} else if !yearRe.MatchString(rawYear) || yearErr != nil {
		v.Add(path+".expireYear", "must be YY or YYYY")
	} else {
		current := now.UTC().Year()*12 + int(now.UTC().Month()) - 1
		if year*12+(month-1) < current {
			v.Add(path+".expireYear", "card is expired")
		}
	}

	if !cvcRe.MatchString(card.CVC) {
		v.Add(path+".cvc", "must be 3 or 4 digits")
	}
	return v
}

// Required checks that every dotted path is present and non-empty.
func (v *RequestValidator) Required(source any, paths []string) *RequestValidator {
	for _, path := range paths {
		if isBlank(valueAt(source, path)) {
			v.Add(path, "is required")
		}
	}
	return v
}

// BuyerFormats validates email, IP and GSM number when they are present.
func (v *RequestValidator) BuyerFormats(buyer Buyer) *RequestValidator {
	if !isBlank(buyer.Email) && !emailRe.MatchString(buyer.Email) {
		v.Add("buyer.email", "is not a valid email address")
	}
	if !isBlank(buyer.IP) && !ipv4Re.MatchString(buyer.IP) && !ipv6Re.MatchString(buyer.IP) {
		v.Add("buyer.ip", "is not a valid IPv4 or IPv6 address")
	}
	if !isBlank(buyer.GSMNumber) {
		digits := gsmStrip.ReplaceAllString(buyer.GSMNumber, "")
		if !gsmRe.MatchString(digits) {
			v.Add("buyer.gsmNumber", "must have 10-15 digits")
		}
	}
	return v
}

// Assert returns a ValidationError listing every issue, or nil when there are none.
func (v *RequestValidator) Assert(provider string) error {
	if len(v.Issues) == 0 {
		return nil
	}
	parts := make([]string, 0, len(v.Issues))
	for _, i := range v.Issues {
		parts = append(parts, i.Path+" "+i.Message)
	}
	return NewValidationError("Invalid request: "+strings.Join(parts, "; "), provider, v.Issues)
}

// valueAt resolves a dotted path against a struct by its json tags.
func valueAt(source any, path string) string {
	current := reflect.ValueOf(source)
	for _, key := range strings.Split(path, ".") {
		for current.Kind() == reflect.Ptr || current.Kind() == reflect.Interface {
			if current.IsNil() {
				return ""
			}
			current = current.Elem()
		}
		switch current.Kind() {
		case reflect.Struct:
			current = fieldByJSONTag(current, key)
		case reflect.Map:
			current = current.MapIndex(reflect.ValueOf(key))
		default:
			return ""
		}
		if !current.IsValid() {
			return ""
		}
	}
	for current.Kind() == reflect.Ptr || current.Kind() == reflect.Interface {
		if current.IsNil() {
			return ""
		}
		current = current.Elem()
	}
	if current.Kind() == reflect.String {
		return current.String()
	}
	if current.CanInterface() {
		return fmt.Sprint(current.Interface())
	}
	return ""
}

func fieldByJSONTag(v reflect.Value, key string) reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag == key {
			return v.Field(i)
		}
		if t.Field(i).Anonymous {
			if found := fieldByJSONTag(v.Field(i), key); found.IsValid() {
				return found
			}
		}
	}
	return reflect.Value{}
}

// ValidatePaymentRequest validates amounts, basket, buyer formats, and
// optionally the card and provider-specific required fields.
func ValidatePaymentRequest(request *PaymentRequest, rules PaymentValidationRules, provider string) error {
	v := &RequestValidator{}
	if request == nil {
		v.Add("request", "is required")
		return v.Assert(provider)
	}

	price, priceOK := v.Amount(request.Price, "price", true)
	v.Amount(request.PaidPrice, "paidPrice", false)

	items := request.BasketItems
	if rules.BasketRequired && len(items) == 0 {
		v.Add("basketItems", "must not be empty")
	}

	var basketTotal int64
	basketValid := true
	if rules.BasketMatchesPrice {
		for i, item := range items {
			cents, ok := v.Amount(item.Price, fmt.Sprintf("basketItems[%d].price", i), true)
			if !ok || cents <= 0 {
				basketValid = false
			} else {
				basketTotal += cents
			}
		}
	}
	if rules.BasketMatchesPrice && len(items) > 0 && basketValid && priceOK && price > 0 && basketTotal != price {
		v.Add("basketItems", fmt.Sprintf("prices add up to %.2f but price is %.2f",
			float64(basketTotal)/100, float64(price)/100))
	}

	if request.StoredCard != nil {
		if !rules.StoredCard {
			v.Add("storedCard", "is not supported by this provider")
		} else {
			if isBlank(request.StoredCard.CustomerToken) {
				v.Add("storedCard.customerToken", "is required")
			}
			if isBlank(request.StoredCard.CardToken) {
				v.Add("storedCard.cardToken", "is required")
			}
		}
	} else if rules.Card {
		v.Card(request.PaymentCard, "paymentCard", time.Now())
	}
	if request.SaveCard != nil && request.SaveCard.Enabled && !rules.SaveCard {
		v.Add("saveCard", "is not supported by this provider for this payment type")
	}
	if len(rules.Required) > 0 {
		v.Required(request, rules.Required)
	}
	v.BuyerFormats(request.Buyer)

	return v.Assert(provider)
}

// ValidateRefundAmount validates the amount of a refund.
func ValidateRefundAmount(price, provider string) error {
	v := &RequestValidator{}
	v.Amount(price, "price", true)
	return v.Assert(provider)
}

// ValidateCaptureRequest validates a capture: a payment id and a positive amount.
func ValidateCaptureRequest(request *CaptureRequest, provider string) error {
	v := &RequestValidator{}
	if request == nil {
		v.Add("request", "is required")
		return v.Assert(provider)
	}
	if isBlank(request.PaymentID) {
		v.Add("paymentId", "is required")
	}
	v.Amount(request.Amount, "amount", true)
	return v.Assert(provider)
}

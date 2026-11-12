package testing

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/boracomet/go-pos/core"
)

type scenarioKind string

const (
	scenarioSuccess         scenarioKind = "success"
	scenarioDecline         scenarioKind = "decline"
	scenarioThreeDSRequired scenarioKind = "threeds-required"
	scenarioThreeDSFailed   scenarioKind = "threeds-failed"
	scenarioNetworkError    scenarioKind = "network-error"
	scenarioRefundFails     scenarioKind = "refund-fails"
)

// scenario is the outcome the mock bank applies to an operation.
type scenario struct {
	kind    scenarioKind
	code    core.PaymentErrorCode
	message string
}

func successScenario() scenario { return scenario{kind: scenarioSuccess} }

func declineScenario(code core.PaymentErrorCode, message string) scenario {
	return scenario{kind: scenarioDecline, code: code, message: message}
}

var declineMessages = map[core.PaymentErrorCode]string{
	core.CodeCardDeclined:      "The card was declined",
	core.CodeInsufficientFunds: "Insufficient funds",
	core.CodeExpiredCard:       "The card has expired",
	core.CodeInvalidCVC:        "The CVC is incorrect",
	core.CodeFraudSuspected:    "The payment was blocked as suspected fraud",
	core.CodeLimitExceeded:     "The card limit was exceeded",
	core.CodeProviderError:     "The mock bank could not process the request",
	core.CodeThreeDSFailed:     "3D Secure authentication failed",
}

const (
	declinedFallbackMessage = "Declined by the mock bank"
	lostResponseMessage     = "No response from the mock bank (ETIMEDOUT). The transaction may have been processed; verify it with getPayment() before retrying."
	invalidSignatureMessage = "Invalid 3D Secure callback signature"
)

var cardScenarios = map[string]scenario{
	CardDeclined:          declineScenario(core.CodeCardDeclined, ""),
	CardInsufficientFunds: declineScenario(core.CodeInsufficientFunds, ""),
	CardExpired:           declineScenario(core.CodeExpiredCard, ""),
	CardInvalidCVC:        declineScenario(core.CodeInvalidCVC, ""),
	CardFraudSuspected:    declineScenario(core.CodeFraudSuspected, ""),
	CardLimitExceeded:     declineScenario(core.CodeLimitExceeded, ""),
	CardProviderError:     declineScenario(core.CodeProviderError, ""),
	CardThreeDSRequired:   {kind: scenarioThreeDSRequired},
	CardThreeDSFailed:     {kind: scenarioThreeDSFailed},
	CardNetworkError:      {kind: scenarioNetworkError},
	CardRefundFails:       {kind: scenarioRefundFails},
}

var mockRules = core.PaymentValidationRules{Card: true, StoredCard: true, SaveCard: true}

var (
	mastercardRe = regexp.MustCompile(`^(5[1-5]|2[2-7])`)
	amexRe       = regexp.MustCompile(`^3[47]`)
)

// errorCodeTable maps every normalized code to itself, so a raw code the mock
// bank produces keeps its meaning. THREEDS_REQUIRED is not a normalized code:
// it declines as CARD_DECLINED.
func errorCodeTable() map[string]core.PaymentErrorCode {
	codes := []core.PaymentErrorCode{
		core.CodeInsufficientFunds,
		core.CodeCardDeclined,
		core.CodeInvalidCard,
		core.CodeExpiredCard,
		core.CodeInvalidCVC,
		core.CodeThreeDSFailed,
		core.CodeFraudSuspected,
		core.CodeLimitExceeded,
		core.CodeDuplicateOrder,
		core.CodeCancelledByCustomer,
		core.CodeInvalidRequest,
		core.CodeNetworkError,
		core.CodeInvalidHash,
		core.CodeProviderError,
		core.CodeUnknown,
	}
	table := make(map[string]core.PaymentErrorCode, len(codes)+1)
	for _, code := range codes {
		table[string(code)] = code
	}
	table["THREEDS_REQUIRED"] = core.CodeCardDeclined
	return table
}

// toMinor converts a decimal amount ("12.50") to minor units, rejecting invalid
// input with a ValidationError.
func toMinor(amount string) (int64, error) {
	return core.ToMinorUnits(amount, "")
}

func associationOf(number string) string {
	switch {
	case strings.HasPrefix(number, "4"):
		return "VISA"
	case mastercardRe.MatchString(number):
		return "MASTER_CARD"
	case strings.HasPrefix(number, "9792"), strings.HasPrefix(number, "65"):
		return "TROY"
	case amexRe.MatchString(number):
		return "AMERICAN_EXPRESS"
	default:
		return "UNKNOWN"
	}
}

func stripSpaces(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func escapeHTML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return strings.ReplaceAll(value, `"`, "&quot;")
}

// formField is one hidden input of the 3D Secure page. The fields are ordered so
// the generated HTML is deterministic.
type formField struct {
	name  string
	value string
}

// autoSubmitForm builds the mock bank's 3D Secure page: it posts the signed
// result to action as soon as it is rendered.
func autoSubmitForm(action string, fields []formField) string {
	var inputs strings.Builder
	for _, field := range fields {
		inputs.WriteString(`<input type="hidden" name="`)
		inputs.WriteString(escapeHTML(field.name))
		inputs.WriteString(`" value="`)
		inputs.WriteString(escapeHTML(field.value))
		inputs.WriteString(`">`)
	}
	return `<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Mock 3D Secure</title></head><body>` +
		`<form id="mock-3ds" method="POST" action="` + escapeHTML(action) + `">` + inputs.String() + `</form>` +
		`<script>document.getElementById("mock-3ds").submit();</script>` +
		`</body></html>`
}

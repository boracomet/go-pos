package plugins

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/boracomet/go-pos/core"
)

// CardMaskingStyle is how a card number (PAN) is masked.
type CardMaskingStyle string

const (
	// CardMaskFirst6Last4 keeps the BIN and the last four digits: 545454******5454.
	CardMaskFirst6Last4 CardMaskingStyle = "first6last4"
	// CardMaskLast4 keeps only the last four digits: ************5454.
	CardMaskLast4 CardMaskingStyle = "last4"
	// CardMaskNone replaces the whole number: <redacted>.
	CardMaskNone CardMaskingStyle = "none"
)

// RedactOptions configures the redaction plugin. Every option adds to the
// built-in rules; none of them can unmask a default field.
type RedactOptions struct {
	// Fields are more field names to mask when encountered as object keys. They
	// are added to the defaults (card details, CVV/CVC, expiration dates,
	// cardholder name, identity number, phone and secrets), which always stay
	// masked. Matching is case-insensitive.
	Fields []string
	// Card is the PAN masking style: a CardMaskingStyle (or its string value), or
	// a func(pan string) string receiving the digits only. Default: first6last4.
	Card any
	// Custom are more patterns to match and mask anywhere in string values,
	// added to the built-in Turkish identity number (TCKN) pattern. Every match
	// is replaced, in slice order.
	Custom []*regexp.Regexp
	// MaskString is the placeholder for generic redacted values.
	// Default: "<redacted>".
	MaskString string
	// RedactLogger masks logged messages and metadata. Default: true. The
	// application installs the wrapper with RedactLogger, because a plugin
	// cannot replace the configured logger.
	RedactLogger *bool
	// RedactEvents masks event payloads before listeners and other plugins see
	// them. Default: true.
	RedactEvents *bool
}

// redactDefaultFields always stay masked: listing an extra field must not unmask
// the CVC.
var redactDefaultFields = []string{
	"cardNumber",
	"cardHolderName",
	"cvc",
	"cvv",
	"securityCode",
	"expireMonth",
	"expireYear",
	"identityNumber",
	"gsmNumber",
	"phoneNumber",
	"phone",
	"secretKey",
	"apiKey",
	"merchantKey",
	"merchantSalt",
	"password",
	"clientPassword",
	"storeKey",
	"hashKey",
	"apiSecret",
	"clientSecret",
	"terminalProvPassword",
}

// redactResultPassthrough are result fields kept as the provider sent them,
// because the masked result is also what the caller gets back: identifiers, the
// 3D Secure and checkout form pages (their markup can hold long digit runs that
// look like card numbers) and the stored card tokens. Events see them unmasked too.
var redactResultPassthrough = []string{
	"paymentId",
	"conversationId",
	"threeDSHtmlContent",
	"redirectUrl",
	"checkoutFormContent",
	"paymentPageUrl",
	"token",
	"storedCard",
}

// redactTCKNPattern matches 11-digit Turkish identity numbers.
var redactTCKNPattern = regexp.MustCompile(`\b\d{11}\b`)

// redactRef identifies a value on the current masking path, so a reference cycle
// is replaced with the mask instead of recursing forever.
type redactRef struct {
	kind   reflect.Kind
	ptr    uintptr
	length int
}

func redactIdentity(value reflect.Value) (redactRef, bool) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Map:
		if value.IsNil() {
			return redactRef{}, false
		}
		return redactRef{kind: value.Kind(), ptr: value.Pointer()}, true
	case reflect.Slice:
		if value.IsNil() {
			return redactRef{}, false
		}
		return redactRef{kind: value.Kind(), ptr: value.Pointer(), length: value.Len()}, true
	default:
		return redactRef{}, false
	}
}

// RedactedError is the masked copy of an error: its message and stack are
// redacted and its type name is kept for diagnostics.
type RedactedError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

func (e *RedactedError) Error() string { return e.Message }

// Redactor masks cardholder and personal data in values, strings and card
// numbers. It never mutates its input.
type Redactor struct {
	fields   map[string]struct{}
	card     any
	patterns []*regexp.Regexp
	maskStr  string
}

// CreateRedactor builds a redaction transformer from the redaction options.
func CreateRedactor(options RedactOptions) *Redactor {
	fields := make(map[string]struct{}, len(redactDefaultFields)+len(options.Fields))
	for _, name := range redactDefaultFields {
		fields[strings.ToLower(name)] = struct{}{}
	}
	for _, name := range options.Fields {
		fields[strings.ToLower(name)] = struct{}{}
	}
	card := any(CardMaskFirst6Last4)
	if options.Card != nil {
		card = options.Card
	}
	patterns := make([]*regexp.Regexp, 0, len(options.Custom)+1)
	patterns = append(patterns, redactTCKNPattern)
	patterns = append(patterns, options.Custom...)
	maskStr := options.MaskString
	if maskStr == "" {
		maskStr = "<redacted>"
	}
	return &Redactor{fields: fields, card: card, patterns: patterns, maskStr: maskStr}
}

// MaskString masks card numbers and the custom patterns found anywhere in text.
func (r *Redactor) MaskString(text string) string {
	masked := redactMaskPAN(text, func(digits string) string {
		return r.formatPan(digits)
	})
	for _, pattern := range r.patterns {
		masked = pattern.ReplaceAllLiteralString(masked, r.maskStr)
	}
	return masked
}

// MaskCard masks a card number according to the configured style.
func (r *Redactor) MaskCard(pan string) string { return r.formatPan(pan) }

// Mask masks an object, array or string according to the configured rules,
// preserving the input's concrete type so callers can keep using it.
func (r *Redactor) Mask(value any) any {
	return r.maskValue(value, "", map[redactRef]bool{})
}

func (r *Redactor) formatPan(pan string) string {
	digits := redactDigitsOnly(pan)
	if len(digits) < 10 {
		return "<redacted>"
	}
	switch style := r.card.(type) {
	case func(string) string:
		return style(digits)
	case func(string) (string, error):
		masked, err := style(digits)
		if err != nil {
			return "<redacted>"
		}
		return masked
	}
	switch redactStyleName(r.card) {
	case string(CardMaskLast4):
		return strings.Repeat("*", len(digits)-4) + digits[len(digits)-4:]
	case string(CardMaskNone):
		return "<redacted>"
	default:
		middle := len(digits) - 10
		if middle < 6 {
			middle = 6
		}
		return digits[:6] + strings.Repeat("*", middle) + digits[len(digits)-4:]
	}
}

func redactStyleName(style any) string {
	switch typed := style.(type) {
	case CardMaskingStyle:
		return string(typed)
	case string:
		return typed
	default:
		return ""
	}
}

func redactDigitsOnly(text string) string {
	var builder strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			builder.WriteByte(text[i])
		}
	}
	return builder.String()
}

func redactIsDigit(char byte) bool { return char >= '0' && char <= '9' }

func redactIsSeparator(char byte) bool { return char == ' ' || char == '-' }

// redactMaskPAN replaces card numbers of 13 to 19 digits, including numbers
// written with spaces or dashes and numbers glued to a word character. It
// reproduces a lookbehind/lookahead scanner by hand: a match starts at a digit
// no digit precedes, takes as many digits as possible up to 19, and must not be
// followed by another digit. A run of 20 or more unbroken digits is therefore
// left alone, exactly as the pattern leaves it.
func redactMaskPAN(text string, format func(digits string) string) string {
	var builder strings.Builder
	index := 0
	for index < len(text) {
		if !redactIsDigit(text[index]) || (index > 0 && redactIsDigit(text[index-1])) {
			builder.WriteByte(text[index])
			index++
			continue
		}
		digits := redactDigitRun(text, index)
		match, ok := redactPANMatch(text, digits)
		if !ok {
			builder.WriteByte(text[index])
			index++
			continue
		}
		builder.WriteString(text[index:match.start])
		builder.WriteString(format(text[match.start:match.end]))
		index = match.end
	}
	return builder.String()
}

// redactDigitRun collects the positions of the digits of the maximal run that
// starts at index: digits separated only by spaces or dashes.
func redactDigitRun(text string, index int) []int {
	positions := make([]int, 0, 24)
	for cursor := index; cursor < len(text); cursor++ {
		if redactIsDigit(text[cursor]) {
			positions = append(positions, cursor)
			continue
		}
		if !redactIsSeparator(text[cursor]) {
			break
		}
	}
	return positions
}

type redactPANSpan struct {
	start int
	end   int
}

// redactPANMatch finds the first match of the run, starting at any digit a
// separator (or the run start) precedes, and taking the longest digit count that
// is not followed by another digit.
func redactPANMatch(text string, positions []int) (redactPANSpan, bool) {
	for start := 0; start < len(positions); start++ {
		if start > 0 && positions[start] == positions[start-1]+1 {
			continue
		}
		available := len(positions) - start
		if available < 13 {
			return redactPANSpan{}, false
		}
		count := available
		if count > 19 {
			count = 19
		}
		for count >= 13 {
			last := positions[start+count-1]
			if last+1 >= len(text) || !redactIsDigit(text[last+1]) {
				return redactPANSpan{start: positions[start], end: last + 1}, true
			}
			count--
		}
	}
	return redactPANSpan{}, false
}

func (r *Redactor) maskValue(value any, key string, seen map[redactRef]bool) any {
	if value == nil {
		return nil
	}
	current := reflect.ValueOf(value)
	for current.Kind() == reflect.Interface {
		if current.IsNil() {
			return value
		}
		current = current.Elem()
	}

	if key != "" {
		lower := strings.ToLower(key)
		if _, listed := r.fields[lower]; listed {
			switch lower {
			case "cardnumber":
				if current.Kind() == reflect.String {
					return redactLike(current.Type(), r.formatPan(current.String()))
				}
				return r.maskStr
			case "cvc", "cvv":
				return "***"
			default:
				return r.maskStr
			}
		}
	}

	if current.Kind() == reflect.String {
		return redactLike(current.Type(), r.MaskString(current.String()))
	}

	switch current.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
	default:
		return value
	}

	if ref, ok := redactIdentity(current); ok {
		if seen[ref] {
			return r.maskStr
		}
		seen[ref] = true
		defer delete(seen, ref)
	}

	if current.Kind() == reflect.Slice || current.Kind() == reflect.Array {
		return r.maskSequence(current, key, seen)
	}
	if asError, ok := value.(error); ok {
		return r.maskError(asError)
	}
	switch current.Kind() {
	case reflect.Pointer:
		if current.IsNil() {
			return value
		}
		out := reflect.New(current.Type().Elem())
		redactAssign(out.Elem(), r.maskValue(current.Elem().Interface(), key, seen))
		return out.Interface()
	case reflect.Map:
		return r.maskMap(current, seen)
	default:
		return r.maskStruct(current, seen)
	}
}

func (r *Redactor) maskSequence(current reflect.Value, key string, seen map[redactRef]bool) any {
	var out reflect.Value
	if current.Kind() == reflect.Array {
		out = reflect.New(reflect.ArrayOf(current.Len(), current.Type().Elem())).Elem()
	} else {
		out = reflect.MakeSlice(reflect.SliceOf(current.Type().Elem()), current.Len(), current.Len())
	}
	for i := 0; i < current.Len(); i++ {
		redactAssign(out.Index(i), r.maskValue(current.Index(i).Interface(), key, seen))
	}
	return out.Interface()
}

func (r *Redactor) maskMap(current reflect.Value, seen map[redactRef]bool) any {
	out := reflect.MakeMapWithSize(current.Type(), current.Len())
	iterator := current.MapRange()
	for iterator.Next() {
		key := iterator.Key()
		name := ""
		if key.Kind() == reflect.String {
			name = key.String()
		} else {
			name = fmt.Sprint(key.Interface())
		}
		masked := reflect.ValueOf(r.maskValue(iterator.Value().Interface(), name, seen))
		if !masked.IsValid() {
			masked = reflect.Zero(current.Type().Elem())
		}
		if masked.Type().AssignableTo(current.Type().Elem()) {
			out.SetMapIndex(key, masked)
			continue
		}
		if masked.Type().ConvertibleTo(current.Type().Elem()) && masked.Kind() == current.Type().Elem().Kind() {
			out.SetMapIndex(key, masked.Convert(current.Type().Elem()))
			continue
		}
		out.SetMapIndex(key, reflect.Zero(current.Type().Elem()))
	}
	return out.Interface()
}

func (r *Redactor) maskStruct(current reflect.Value, seen map[redactRef]bool) any {
	out := reflect.New(current.Type()).Elem()
	out.Set(current)
	structType := current.Type()
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			name = field.Name
		}
		redactAssign(out.Field(i), r.maskValue(current.Field(i).Interface(), name, seen))
	}
	return out.Interface()
}

func (r *Redactor) maskError(err error) any {
	masked := &RedactedError{Name: fmt.Sprintf("%T", err), Message: r.MaskString(err.Error())}
	if stacked, ok := err.(interface{ Stack() string }); ok {
		masked.Stack = r.MaskString(stacked.Stack())
	}
	return masked
}

// redactLike keeps a named string type, so a masked status is still a status.
func redactLike(target reflect.Type, text string) any {
	value := reflect.ValueOf(text)
	if target.Kind() == reflect.String && target != value.Type() {
		return value.Convert(target).Interface()
	}
	return text
}

// redactAssign writes a masked value back into a copy of the original field. A
// value whose type no longer fits (an object replaced by the mask string) becomes
// the zero value rather than leaking the original.
func redactAssign(target reflect.Value, masked any) {
	if !target.CanSet() {
		return
	}
	if masked == nil {
		target.Set(reflect.Zero(target.Type()))
		return
	}
	value := reflect.ValueOf(masked)
	if value.Type().AssignableTo(target.Type()) {
		target.Set(value)
		return
	}
	if value.Kind() == target.Kind() && value.Type().ConvertibleTo(target.Type()) {
		target.Set(value.Convert(target.Type()))
		return
	}
	target.Set(reflect.Zero(target.Type()))
}

// redactKeepPassthrough puts the fields the caller needs back as they were.
func redactKeepPassthrough(masked, original any) any {
	if masked == nil || original == nil {
		return masked
	}
	target := pluginDeref(reflect.ValueOf(masked))
	source := pluginDeref(reflect.ValueOf(original))
	if !target.IsValid() || !source.IsValid() {
		return masked
	}
	switch {
	case target.Kind() == reflect.Map && source.Kind() == reflect.Map:
		if target.Type().Key().Kind() != reflect.String || !source.Type().AssignableTo(target.Type()) {
			return masked
		}
		for _, name := range redactResultPassthrough {
			key := reflect.ValueOf(name).Convert(target.Type().Key())
			value := source.MapIndex(key)
			if value.IsValid() {
				target.SetMapIndex(key, value)
			}
		}
		return masked
	case target.Kind() == reflect.Struct && source.Kind() == reflect.Struct:
		out := reflect.New(target.Type()).Elem()
		out.Set(target)
		for _, name := range redactResultPassthrough {
			field, _ := pluginStructFieldTagged(source, name)
			if !field.IsValid() || !field.CanInterface() {
				continue
			}
			destination := pluginStructField(out, name)
			if destination.IsValid() {
				redactAssign(destination, field.Interface())
			}
		}
		return out.Interface()
	default:
		return masked
	}
}

// RedactingLogger masks messages, metadata and errors before forwarding them to
// the logger it wraps.
type RedactingLogger struct {
	inner    core.Logger
	redactor *Redactor
}

// NewRedactingLogger wraps a logger with a redactor. A nil logger stays nil.
func NewRedactingLogger(inner core.Logger, redactor *Redactor) *RedactingLogger {
	if inner == nil || redactor == nil {
		return nil
	}
	return &RedactingLogger{inner: inner, redactor: redactor}
}

func (l *RedactingLogger) meta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	if masked, ok := l.redactor.Mask(meta).(map[string]any); ok {
		return masked
	}
	return nil
}

// Debug logs a masked message and masked metadata.
func (l *RedactingLogger) Debug(message string, meta map[string]any) {
	l.inner.Debug(l.redactor.MaskString(message), l.meta(meta))
}

// Info logs a masked message and masked metadata.
func (l *RedactingLogger) Info(message string, meta map[string]any) {
	l.inner.Info(l.redactor.MaskString(message), l.meta(meta))
}

// Error logs a masked message, a masked error and masked metadata.
func (l *RedactingLogger) Error(message string, err error, meta map[string]any) {
	var masked error
	if err != nil {
		if asError, ok := l.redactor.Mask(err).(error); ok {
			masked = asError
		} else {
			masked = &RedactedError{Message: l.redactor.MaskString(err.Error())}
		}
	}
	l.inner.Error(l.redactor.MaskString(message), masked, l.meta(meta))
}

// RedactLogger wraps a logger so every logged message, error and metadata value
// is masked. Wrapping an already wrapped logger returns it unchanged, so calling
// it twice is safe. A nil logger is returned as nil.
func RedactLogger(logger core.Logger, options RedactOptions) core.Logger {
	if logger == nil {
		return nil
	}
	if wrapped, ok := logger.(*RedactingLogger); ok {
		return wrapped
	}
	return NewRedactingLogger(logger, CreateRedactor(options))
}

// RedactMethods is the "redact" member the plugin adds to the facade.
type RedactMethods struct {
	redactor *Redactor
}

// Mask masks an object, array or string according to the configured rules.
func (m *RedactMethods) Mask(value any) any { return m.redactor.Mask(value) }

// MaskCard masks a card number according to the configured card masking style.
func (m *RedactMethods) MaskCard(pan string) string { return m.redactor.MaskCard(pan) }

// Redactor returns the transformer the plugin uses.
func (m *RedactMethods) Redactor() *Redactor { return m.redactor }

// Redact masks cardholder and personal data from logs, events and audit trails
// for PCI DSS and KVKK compliance. It never alters the request payload sent to
// the provider: only the copies passed to events and listeners are masked.
//
// The configured logger cannot be replaced from inside a plugin, so install
// RedactLogger as the application's logger to mask log output as well.
func Redact(options RedactOptions) core.Plugin {
	redactor := CreateRedactor(options)
	redactEvents := options.RedactEvents == nil || *options.RedactEvents

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDRedact,
		Options: options,
		Hooks: core.PluginHooks{
			After: []core.AfterHook{
				{
					Matcher: func(core.OperationResultContext) bool { return redactEvents },
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						return &core.AfterHookResult{
							Request: redactor.Mask(ctx.Request),
							Result:  redactKeepPassthrough(redactor.Mask(ctx.Result), ctx.Result),
						}, nil
					},
				},
			},
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"redact": &RedactMethods{redactor: redactor}}, nil
		},
	})
}

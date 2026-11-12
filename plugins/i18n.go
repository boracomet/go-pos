package plugins

import (
	"context"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/boracomet/go-pos/core"
)

// I18nDetectionStrategy is where the language of an HTTP handler response comes
// from: the Accept-Language header, a cookie, or the GetLocale callback.
type I18nDetectionStrategy string

// The locale detection strategies, in the order they are tried.
const (
	I18nDetectHeader   I18nDetectionStrategy = "header"
	I18nDetectCookie   I18nDetectionStrategy = "cookie"
	I18nDetectCallback I18nDetectionStrategy = "callback"
)

// I18nTranslation is one language's messages. Translations are merged in the
// order they are listed, so a later entry overrides an earlier one.
type I18nTranslation struct {
	Locale   string
	Messages I18nDictionary
}

// I18nOptions configures the i18n plugin.
type I18nOptions struct {
	// Translations are the languages to support, from I18nLocales or your own.
	// English is built in; a code missing in a language keeps the English message.
	Translations []I18nTranslation
	// DefaultLocale is the language of direct calls and of responses whose
	// language was not detected. Empty means "en".
	DefaultLocale string
	// Detection lists the strategies tried for HTTP handler responses, in
	// priority order; the first supported language wins. Nil means header only,
	// an empty slice disables detection.
	Detection []I18nDetectionStrategy
	// LocaleCookie is the cookie the cookie strategy reads. Empty means "locale".
	LocaleCookie string
	// GetLocale returns the language of a request for the callback strategy. An
	// empty string means the request carries no language.
	GetLocale func(request *core.HandlerRequest) (string, error)
}

// LocalizedErrorsOptions configures the deprecated LocalizedErrors constructor.
type LocalizedErrorsOptions struct {
	// Locale is the default language. Empty means "en".
	Locale string
	// FallbackLocale is used for codes missing in a language. Empty means "en".
	FallbackLocale string
	// Messages adds languages or overrides the built-in messages.
	Messages []I18nTranslation
	// DetectLocale is true (the default) to use the Accept-Language header, false
	// to always use Locale, or a func(*core.HandlerRequest) (string, error) or
	// func(*core.HandlerRequest) string returning the language of a request.
	DetectLocale any
}

// ParseAcceptLanguage returns the languages of an Accept-Language header, most
// preferred first, lowercased and without the ones excluded by a zero quality.
func ParseAcceptLanguage(header string) []string {
	if header == "" {
		return []string{}
	}
	type i18nLanguage struct {
		tag   string
		q     float64
		index int
	}
	languages := make([]i18nLanguage, 0)
	for index, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(param, "q=") {
				number, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64)
				if err != nil {
					number = 0
				}
				q = number
				break
			}
		}
		if tag == "" || tag == "*" || !(q > 0) {
			continue
		}
		languages = append(languages, i18nLanguage{tag: tag, q: q, index: index})
	}
	sort.SliceStable(languages, func(i, j int) bool {
		if languages[i].q != languages[j].q {
			return languages[i].q > languages[j].q
		}
		return languages[i].index < languages[j].index
	})
	tags := make([]string, 0, len(languages))
	for _, language := range languages {
		tags = append(tags, language.tag)
	}
	return tags
}

// i18nLocalizable reports whether a result carries a normalized error code, the
// only results whose message the plugin replaces.
func i18nLocalizable(value any) bool {
	current := pluginDeref(reflect.ValueOf(value))
	if !current.IsValid() {
		return false
	}
	switch current.Kind() {
	case reflect.Map, reflect.Struct:
		_, ok := pluginStringField(value, "code")
		return ok
	default:
		return false
	}
}

// i18nCookie reads one cookie of a Cookie header, decoded, or "" when absent.
func i18nCookie(cookies, name string) string {
	for _, part := range strings.Split(cookies, ";") {
		index := strings.Index(part, "=")
		if index > 0 && strings.TrimSpace(part[:index]) == name {
			return i18nDecodeURIComponent(strings.TrimSpace(part[index+1:]))
		}
	}
	return ""
}

// i18nDecodeURIComponent decodes a percent-encoded value, keeping it as it is
// when it is not valid UTF-8 once decoded. A plus sign stays a plus sign.
func i18nDecodeURIComponent(value string) string {
	decoded, err := url.QueryUnescape(strings.ReplaceAll(value, "+", "%2B"))
	if err != nil || !utf8.ValidString(decoded) {
		return value
	}
	return decoded
}

func i18nSetString(field reflect.Value, text string) {
	if !field.IsValid() || !field.CanSet() {
		return
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(text)
	case reflect.Pointer:
		if field.Type().Elem().Kind() == reflect.String {
			pointer := reflect.New(field.Type().Elem())
			pointer.Elem().SetString(text)
			field.Set(pointer)
		}
	}
}

func i18nMapValue(text string, target reflect.Type) (reflect.Value, bool) {
	switch target.Kind() {
	case reflect.String:
		return reflect.ValueOf(text).Convert(target), true
	case reflect.Interface:
		if target.NumMethod() == 0 {
			return reflect.ValueOf(text), true
		}
	}
	return reflect.Value{}, false
}

// i18nWithMessages copies a result with its errorMessage replaced, keeping the
// provider's own message in providerMessage. The copy has the result's own type.
func i18nWithMessages(result any, text, original string, keepOriginal bool) any {
	value := pluginDeref(reflect.ValueOf(result))
	if !value.IsValid() {
		return result
	}
	switch value.Kind() {
	case reflect.Struct:
		copied := reflect.New(value.Type()).Elem()
		copied.Set(value)
		i18nSetString(pluginStructField(copied, "errorMessage"), text)
		if keepOriginal {
			i18nSetString(pluginStructField(copied, "providerMessage"), original)
		}
		if outer := reflect.ValueOf(result); outer.Kind() == reflect.Pointer {
			pointer := reflect.New(value.Type())
			pointer.Elem().Set(copied)
			return pointer.Interface()
		}
		return copied.Interface()
	case reflect.Map:
		messageValue, ok := i18nMapValue(text, value.Type().Elem())
		if !ok {
			return result
		}
		copied := reflect.MakeMapWithSize(value.Type(), value.Len()+1)
		iterator := value.MapRange()
		for iterator.Next() {
			copied.SetMapIndex(iterator.Key(), iterator.Value())
		}
		keyType := value.Type().Key()
		if keyType.Kind() != reflect.String {
			return result
		}
		copied.SetMapIndex(reflect.ValueOf("errorMessage").Convert(keyType), messageValue)
		if keepOriginal {
			if originalValue, ok := i18nMapValue(original, value.Type().Elem()); ok {
				copied.SetMapIndex(reflect.ValueOf("providerMessage").Convert(keyType), originalValue)
			}
		}
		return copied.Interface()
	default:
		return result
	}
}

type i18nRuntime struct {
	dictionaries   map[string]I18nDictionary
	locales        []string
	defaultLocale  string
	fallbackLocale string
	detection      []I18nDetectionStrategy
	localeCookie   string
	getLocale      func(*core.HandlerRequest) (string, error)

	mu             sync.RWMutex
	pluginMessages map[string]string
}

// supported returns the supported language of a language tag (tr-TR → tr), or "".
func (r *i18nRuntime) supported(tag string) string {
	if tag == "" {
		return ""
	}
	lower := strings.ToLower(tag)
	if _, ok := r.dictionaries[lower]; ok {
		return lower
	}
	base, _, _ := strings.Cut(lower, "-")
	if _, ok := r.dictionaries[base]; ok {
		return base
	}
	return ""
}

// lookup resolves a code in a language, then in the fallback language, then in
// the codes declared by plugins. The second result is false when no message
// exists, which is not the same as an empty message.
func (r *i18nRuntime) lookup(code, language string) (string, bool) {
	lang := r.defaultLocale
	if supported := r.supported(language); supported != "" {
		lang = supported
	}
	if text, ok := r.dictionaries[lang][code]; ok {
		return text, true
	}
	if text, ok := r.dictionaries[r.fallbackLocale][code]; ok {
		return text, true
	}
	r.mu.RLock()
	text, ok := r.pluginMessages[code]
	r.mu.RUnlock()
	return text, ok
}

func (r *i18nRuntime) languageOf(language []string) string {
	if len(language) > 0 {
		return language[0]
	}
	return r.defaultLocale
}

func (r *i18nRuntime) message(code string, language ...string) string {
	text, _ := r.lookup(code, r.languageOf(language))
	return text
}

func (r *i18nRuntime) translate(result any, language ...string) any {
	if !i18nLocalizable(result) {
		return result
	}
	lang := r.languageOf(language)
	code, _ := pluginStringField(result, "code")
	text, found := "", false
	if errorCode, ok := pluginStringField(result, "errorCode"); ok {
		text, found = r.lookup(errorCode, lang)
	}
	if !found {
		text, found = r.lookup(code, lang)
	}
	if !found || text == "" {
		return result
	}
	original, keepOriginal := pluginStringField(result, "providerMessage")
	if !keepOriginal {
		original, keepOriginal = pluginStringField(result, "errorMessage")
	}
	return i18nWithMessages(result, text, original, keepOriginal)
}

func (r *i18nRuntime) detectLocale(request *core.HandlerRequest) (string, error) {
	for _, strategy := range r.detection {
		locale := ""
		switch strategy {
		case I18nDetectHeader:
			for _, tag := range ParseAcceptLanguage(pluginHeader(request, "accept-language")) {
				if supported := r.supported(tag); supported != "" {
					locale = supported
					break
				}
			}
		case I18nDetectCookie:
			locale = r.supported(i18nCookie(pluginHeader(request, "cookie"), r.localeCookie))
		case I18nDetectCallback:
			if r.getLocale != nil {
				tag, err := r.getLocale(request)
				if err != nil {
					return "", err
				}
				locale = r.supported(tag)
			}
		}
		if locale != "" {
			return locale, nil
		}
	}
	return r.defaultLocale, nil
}

// I18nMethods are the members the plugin adds under "errors".
type I18nMethods struct {
	runtime *i18nRuntime
}

// Message returns the message of an error code in a language, or "" when there
// is none. Omitting the language uses the default one.
func (m *I18nMethods) Message(code string, language ...string) string {
	return m.runtime.message(code, language...)
}

// Translate returns a result with its errorMessage translated, for results of
// provider-specific methods that do not pass through the hooks.
func (m *I18nMethods) Translate(result any, language ...string) any {
	return m.runtime.translate(result, language...)
}

// DetectLocale returns the language of an HTTP request by the configured
// detection strategies, or the default language.
func (m *I18nMethods) DetectLocale(request *core.HandlerRequest) (string, error) {
	return m.runtime.detectLocale(request)
}

// Locales returns the languages that have translations, in the order they were
// added, English first.
func (m *I18nMethods) Locales() []string {
	return append([]string{}, m.runtime.locales...)
}

// I18n translates the errorMessage of failed results into the customer's
// language, chosen by the result's normalized code or by a plugin's error code
// in errorCode. The provider's own message stays in providerMessage. Direct
// calls answer in DefaultLocale; HTTP handler responses in the detected language.
func I18n(options I18nOptions) (core.Plugin, error) {
	return i18nBuild(options, "en")
}

// LocalizedErrors builds the i18n plugin with every built-in language enabled.
//
// Deprecated: use I18n with I18nLocales.
func LocalizedErrors(options LocalizedErrorsOptions) (core.Plugin, error) {
	translations := append(I18nLocales(), options.Messages...)
	detection := []I18nDetectionStrategy{I18nDetectHeader}
	var getLocale func(*core.HandlerRequest) (string, error)
	switch detect := options.DetectLocale.(type) {
	case nil:
	case bool:
		if !detect {
			detection = []I18nDetectionStrategy{}
		}
	case func(*core.HandlerRequest) (string, error):
		detection = []I18nDetectionStrategy{I18nDetectCallback}
		getLocale = detect
	case func(*core.HandlerRequest) string:
		detection = []I18nDetectionStrategy{I18nDetectCallback}
		getLocale = func(request *core.HandlerRequest) (string, error) { return detect(request), nil }
	}
	return i18nBuild(I18nOptions{
		Translations:  translations,
		DefaultLocale: options.Locale,
		Detection:     detection,
		GetLocale:     getLocale,
	}, options.FallbackLocale)
}

func i18nBuild(options I18nOptions, fallback string) (core.Plugin, error) {
	dictionaries := map[string]I18nDictionary{"en": i18nCopyDictionary(I18nLocaleEN)}
	locales := []string{"en"}
	for _, translation := range options.Translations {
		key := strings.ToLower(translation.Locale)
		merged := i18nCopyDictionary(dictionaries[key])
		for code, text := range translation.Messages {
			merged[code] = text
		}
		if _, known := dictionaries[key]; !known {
			locales = append(locales, key)
		}
		dictionaries[key] = merged
	}
	defaultLocale := strings.ToLower(options.DefaultLocale)
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	fallbackLocale := strings.ToLower(fallback)
	if fallbackLocale == "" {
		fallbackLocale = "en"
	}
	for _, required := range []string{defaultLocale, fallbackLocale} {
		if _, known := dictionaries[required]; !known {
			return core.Plugin{}, core.NewConfigurationError(
				"i18n: no translations for '"+required+"'. Pass them in translations."+required+".", "")
		}
	}
	detection := options.Detection
	if detection == nil {
		detection = []I18nDetectionStrategy{I18nDetectHeader}
	}
	localeCookie := options.LocaleCookie
	if localeCookie == "" {
		localeCookie = "locale"
	}
	runtime := &i18nRuntime{
		dictionaries:   dictionaries,
		locales:        locales,
		defaultLocale:  defaultLocale,
		fallbackLocale: fallbackLocale,
		detection:      detection,
		localeCookie:   localeCookie,
		getLocale:      options.GetLocale,
	}
	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDI18n,
		Options: options,
		Hooks: core.PluginHooks{
			After: []core.AfterHook{
				{
					Matcher: func(ctx core.OperationResultContext) bool {
						return i18nLocalizable(ctx.Result)
					},
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						return &core.AfterHookResult{Result: runtime.translate(ctx.Result)}, nil
					},
				},
			},
		},
		OnResponse: func(_ context.Context, response *core.HandlerResponse, rctx core.ResponseContext) (*core.HandlerResponse, error) {
			if response == nil || !i18nLocalizable(response.Body) {
				return nil, nil
			}
			language, err := runtime.detectLocale(rctx.Request)
			if err != nil {
				return nil, err
			}
			translated := *response
			translated.Body = runtime.translate(response.Body, language)
			return &translated, nil
		},
		Methods: func(_ context.Context, pctx core.PluginContext) (map[string]any, error) {
			// The live map of plugin error codes: English defaults for their codes.
			runtime.mu.Lock()
			runtime.pluginMessages = pctx.ErrorCodes()
			runtime.mu.Unlock()
			return map[string]any{"errors": &I18nMethods{runtime: runtime}}, nil
		},
	}), nil
}

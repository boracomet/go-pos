// Package plugins holds the plugins that extend a payment facade: operation
// hooks, event listeners, HTTP endpoints and request/response interception.
//
// Every plugin is built by a constructor that returns a core.Plugin. The
// constructors are also reachable by id through BuildPlugin, so a plugin list
// can be driven by configuration.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// The ids of the plugins in this package, as they appear in Plugin.ID.
const (
	PluginIDRateLimit          = "rate-limit"
	PluginIDPendingResolver    = "pending-resolver"
	PluginIDRedact             = "redact"
	PluginIDNotifications      = "notifications"
	PluginIDWebhookForwarding  = "webhook-forwarding"
	PluginIDThreeDSTimeout     = "three-ds-timeout"
	PluginIDTelemetry          = "telemetry"
	PluginIDI18n               = "i18n"
	PluginIDCaptcha            = "captcha"
	PluginIDChaos              = "chaos"
	PluginIDFraudRules         = "fraud-rules"
	PluginIDMCP                = "mcp"
	PluginIDOpenAPI            = "openapi"
	PluginIDCurrencyConversion = "currency-conversion"
	PluginIDRefundApproval     = "refund-approval"
	PluginIDFailover           = "failover"
)

// Factory builds a plugin from its options: nil selects the plugin's defaults,
// otherwise options must be the plugin's own options struct (or a pointer to it).
type Factory func(options any) (core.Plugin, error)

var (
	indexFactoryMu sync.RWMutex
	indexFactories = map[string]Factory{}
)

func init() {
	RegisterFactory(PluginIDRateLimit, indexFactory(PluginIDRateLimit, RateLimit))
	RegisterFactory(PluginIDPendingResolver, indexFactory(PluginIDPendingResolver, PendingResolver))
	RegisterFactory(PluginIDRedact, indexFactory(PluginIDRedact, func(options RedactOptions) (core.Plugin, error) {
		return Redact(options), nil
	}))
	RegisterFactory(PluginIDNotifications, indexFactory(PluginIDNotifications, Notifications))
	RegisterFactory(PluginIDWebhookForwarding, indexFactory(PluginIDWebhookForwarding, WebhookForwarding))
	RegisterFactory(PluginIDThreeDSTimeout, indexFactory(PluginIDThreeDSTimeout, ThreeDSTimeout))
	RegisterFactory(PluginIDTelemetry, indexFactory(PluginIDTelemetry, Telemetry))
	RegisterFactory(PluginIDI18n, indexFactory(PluginIDI18n, I18n))
	RegisterFactory(PluginIDCaptcha, indexFactory(PluginIDCaptcha, Captcha))
	RegisterFactory(PluginIDChaos, indexFactory(PluginIDChaos, Chaos))
	RegisterFactory(PluginIDFraudRules, indexFactory(PluginIDFraudRules, FraudRules))
	RegisterFactory(PluginIDMCP, indexFactory(PluginIDMCP, MCP))
	RegisterFactory(PluginIDOpenAPI, indexFactory(PluginIDOpenAPI, OpenAPI))
	RegisterFactory(PluginIDCurrencyConversion, indexFactory(PluginIDCurrencyConversion, CurrencyConversion))
	RegisterFactory(PluginIDRefundApproval, indexFactory(PluginIDRefundApproval, RefundApproval))
	RegisterFactory(PluginIDFailover, indexFactory(PluginIDFailover, Failover))
}

// RegisterFactory registers a plugin factory under id, replacing any factory
// already registered for it. Plugins outside this package use it to become
// reachable through BuildPlugin.
func RegisterFactory(id string, factory Factory) {
	indexFactoryMu.Lock()
	defer indexFactoryMu.Unlock()
	indexFactories[id] = factory
}

// BuildPlugin builds the plugin registered under id. It reports a configuration
// error when the id is unknown or the options do not belong to that plugin.
func BuildPlugin(id string, options any) (core.Plugin, error) {
	indexFactoryMu.RLock()
	factory, ok := indexFactories[id]
	indexFactoryMu.RUnlock()
	if !ok {
		return core.Plugin{}, core.NewConfigurationError(fmt.Sprintf("plugins: unknown plugin '%s'", id), "")
	}
	return factory(options)
}

// PluginIDs returns the registered plugin ids, sorted.
func PluginIDs() []string {
	indexFactoryMu.RLock()
	ids := make([]string, 0, len(indexFactories))
	for id := range indexFactories {
		ids = append(ids, id)
	}
	indexFactoryMu.RUnlock()
	sort.Strings(ids)
	return ids
}

// indexFactory adapts a typed constructor to the options-erased Factory shape.
func indexFactory[T any](id string, build func(T) (core.Plugin, error)) Factory {
	return func(options any) (core.Plugin, error) {
		var typed T
		switch value := options.(type) {
		case nil:
		case T:
			typed = value
		case *T:
			if value != nil {
				typed = *value
			}
		default:
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("%s: unexpected options type %T", id, options), "")
		}
		return build(typed)
	}
}

// pluginNowMs is the epoch millisecond clock every plugin timestamp uses.
func pluginNowMs() int64 { return time.Now().UnixMilli() }

// pluginHeader reads a request header by case-insensitive name.
func pluginHeader(request *core.HandlerRequest, name string) string {
	if request == nil {
		return ""
	}
	for key, value := range request.Headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// pluginField reads a field of a request or result by its JSON name. It accepts
// the typed core structs, maps, and pointers to either, and returns nil when the
// field is absent.
func pluginField(value any, name string) any {
	current := reflect.ValueOf(value)
	for current.IsValid() && (current.Kind() == reflect.Pointer || current.Kind() == reflect.Interface) {
		if current.IsNil() {
			return nil
		}
		current = current.Elem()
	}
	if !current.IsValid() {
		return nil
	}
	switch current.Kind() {
	case reflect.Map:
		keyType := current.Type().Key()
		if keyType.Kind() != reflect.String {
			return nil
		}
		found := current.MapIndex(reflect.ValueOf(name).Convert(keyType))
		if !found.IsValid() {
			return nil
		}
		return found.Interface()
	case reflect.Struct:
		field := pluginStructField(current, name)
		if !field.IsValid() || !field.CanInterface() {
			return nil
		}
		return field.Interface()
	default:
		return nil
	}
}

// pluginStructField finds a struct field by its JSON tag, then by its name.
func pluginStructField(structValue reflect.Value, name string) reflect.Value {
	field, _ := pluginStructFieldTagged(structValue, name)
	return field
}

// pluginStructFieldTagged also returns the field's JSON tag, so a reader can
// tell an empty value that JSON omits from one it keeps.
func pluginStructFieldTagged(structValue reflect.Value, name string) (reflect.Value, string) {
	structType := structValue.Type()
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		if strings.Split(tag, ",")[0] == name {
			return structValue.Field(i), tag
		}
	}
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.PkgPath == "" && strings.EqualFold(field.Name, name) {
			return structValue.Field(i), field.Tag.Get("json")
		}
	}
	return reflect.Value{}, ""
}

func pluginJSONOmitsEmpty(tag string) bool {
	for _, option := range strings.Split(tag, ",")[1:] {
		if option == "omitempty" {
			return true
		}
	}
	return false
}

// pluginDeref unwraps pointers and interfaces, yielding an invalid value for nil.
func pluginDeref(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

func pluginScalarString(field any) (string, bool) {
	current := pluginDeref(reflect.ValueOf(field))
	if !current.IsValid() || current.Kind() != reflect.String {
		return "", false
	}
	return current.String(), true
}

// pluginStringField reads a field only when it holds a string, so a provider
// sending a number where a string belongs is ignored. A struct field JSON omits
// when empty reads as absent, the way an omitted property does.
func pluginStringField(value any, name string) (string, bool) {
	current := pluginDeref(reflect.ValueOf(value))
	if !current.IsValid() {
		return "", false
	}
	switch current.Kind() {
	case reflect.Map:
		return pluginScalarString(pluginField(value, name))
	case reflect.Struct:
		field, tag := pluginStructFieldTagged(current, name)
		if !field.IsValid() || !field.CanInterface() {
			return "", false
		}
		text, ok := pluginScalarString(field.Interface())
		if !ok {
			return "", false
		}
		if text == "" && field.Kind() == reflect.String && pluginJSONOmitsEmpty(tag) {
			return "", false
		}
		return text, true
	default:
		return "", false
	}
}

// pluginString reads a field as a string, or "" when it is absent or not one.
func pluginString(value any, name string) string {
	text, _ := pluginStringField(value, name)
	return text
}

// pluginNumber reads a numeric field as a float64. The second result is false
// when the field is absent, not a number, or a zero JSON omits.
func pluginNumber(value any, name string) (float64, bool) {
	current := pluginDeref(reflect.ValueOf(value))
	if !current.IsValid() {
		return 0, false
	}
	var field reflect.Value
	var tag string
	switch current.Kind() {
	case reflect.Map:
		raw := pluginField(value, name)
		if raw == nil {
			return 0, false
		}
		field = pluginDeref(reflect.ValueOf(raw))
	case reflect.Struct:
		found, foundTag := pluginStructFieldTagged(current, name)
		if !found.IsValid() || !found.CanInterface() {
			return 0, false
		}
		field, tag = pluginDeref(reflect.ValueOf(found.Interface())), foundTag
	default:
		return 0, false
	}
	if !field.IsValid() {
		return 0, false
	}
	var number float64
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number = float64(field.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number = float64(field.Uint())
	case reflect.Float32, reflect.Float64:
		number = field.Float()
	default:
		return 0, false
	}
	if number == 0 && tag != "" && pluginJSONOmitsEmpty(tag) {
		return 0, false
	}
	return number, true
}

// pluginGeneric turns a value into the JSON tree of maps, slices and scalars, so
// a payload can be walked without knowing its concrete type.
func pluginGeneric(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return value
	}
	return out
}

// pluginCall runs a callback, turning a panic into an error: a plugin callback
// must never take the payment down with it.
func pluginCall(task func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if asError, ok := recovered.(error); ok {
				err = asError
			} else {
				err = fmt.Errorf("%v", recovered)
			}
		}
	}()
	return task()
}

// pluginSleep waits for a duration, or until the context is cancelled.
func pluginSleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// pluginTaskGroup tracks background work so flush can wait for the tasks started
// so far, without waiting for tasks started later.
type pluginTaskGroup struct {
	mu    sync.Mutex
	tasks map[chan struct{}]struct{}
}

func (g *pluginTaskGroup) start(task func()) <-chan struct{} {
	done := make(chan struct{})
	g.mu.Lock()
	if g.tasks == nil {
		g.tasks = map[chan struct{}]struct{}{}
	}
	g.tasks[done] = struct{}{}
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.tasks, done)
			g.mu.Unlock()
			close(done)
		}()
		task()
	}()
	return done
}

func (g *pluginTaskGroup) flush(ctx context.Context) error {
	g.mu.Lock()
	pending := make([]chan struct{}, 0, len(g.tasks))
	for done := range g.tasks {
		pending = append(pending, done)
	}
	g.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// pluginDoer returns the transport a plugin posts with, defaulting to the shared
// HTTP client. Every call carries its own deadline on the context.
func pluginDoer(doer core.Doer) core.Doer {
	if doer == nil {
		return http.DefaultClient
	}
	return doer
}

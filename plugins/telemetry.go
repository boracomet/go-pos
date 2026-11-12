package plugins

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"

	"github.com/boracomet/go-pos/core"
)

// TelemetryAttributeValue is an attribute value a span or metric accepts: a
// string, a number or a boolean.
type TelemetryAttributeValue any

// TelemetryAttributes are the attributes as a telemetry backend sees them.
type TelemetryAttributes map[string]TelemetryAttributeValue

// The span status codes, matching the OpenTelemetry SpanStatusCode.
const (
	TelemetryStatusUnset = 0
	TelemetryStatusOK    = 1
	TelemetryStatusError = 2
)

// TelemetrySpanStatus is the outcome reported on a span.
type TelemetrySpanStatus struct {
	Code    int
	Message string
}

// TelemetrySpan is the span surface the plugin calls. It is a subset of the
// OpenTelemetry Span, so an adapter around any tracer fits.
type TelemetrySpan interface {
	SetAttribute(key string, value TelemetryAttributeValue)
	SetStatus(status TelemetrySpanStatus)
	// End finishes the span at an epoch millisecond timestamp; omitting it ends
	// the span now.
	End(endTime ...int64)
}

// TelemetrySpanOptions are the start options the plugin passes to a tracer.
type TelemetrySpanOptions struct {
	Attributes TelemetryAttributes
	// StartTime is an epoch millisecond timestamp; 0 means now.
	StartTime int64
}

// TelemetryTracer starts spans. It is a subset of the OpenTelemetry Tracer.
type TelemetryTracer interface {
	StartSpan(name string, options TelemetrySpanOptions) TelemetrySpan
}

// TelemetryCounter counts calls. It is a subset of the OpenTelemetry Counter.
type TelemetryCounter interface {
	Add(value float64, attributes TelemetryAttributes)
}

// TelemetryHistogram records durations. It is a subset of the OpenTelemetry
// Histogram.
type TelemetryHistogram interface {
	Record(value float64, attributes TelemetryAttributes)
}

// TelemetryMeter creates the instruments. It is a subset of the OpenTelemetry
// Meter.
type TelemetryMeter interface {
	CreateCounter(name string) TelemetryCounter
	CreateHistogram(name string) TelemetryHistogram
}

// TelemetryOptions configures the telemetry plugin. Nothing is reported unless a
// tracer or a meter is given.
type TelemetryOptions struct {
	// Enabled turns reporting on. Default: true. With false no hook does any work.
	Enabled *bool
	// Tracer receives one span per provider call.
	Tracer TelemetryTracer
	// Meter receives the call counter and the duration histogram.
	Meter TelemetryMeter
	// Operations are the operations to report. Nil reports all of them.
	Operations []core.PaymentOperation
	// PendingTimeoutMs drops the start time of a call whose after hooks never ran
	// (a provider that threw), so an aborted call cannot leak. Default: 300000.
	PendingTimeoutMs int
	// MaxPending caps the concurrently open calls per operation. Default: 512.
	MaxPending int
}

// telemetryAllowedAttributes is the whole attribute set this plugin can produce,
// by construction. Values never come from the request or the provider response
// body: only from the operation name, the provider id, the normalized result
// fields and the transport metadata. The order is the order attributes are set in.
var telemetryAllowedAttributes = []string{
	"payment.provider",
	"payment.operation",
	"payment.result",
	"payment.code",
	"payment.http.status",
	"payment.retry.count",
	"payment.duration_ms",
}

// telemetryMaxTextLength is the longest string attribute value; provider codes
// are far shorter.
const telemetryMaxTextLength = 64

// telemetryPrimitive keeps an attribute value inside the primitive types a
// backend accepts, and reports whether it is one.
func telemetryPrimitive(value any) (TelemetryAttributeValue, bool) {
	if value == nil {
		return nil, false
	}
	current := reflect.ValueOf(value)
	for current.Kind() == reflect.Pointer || current.Kind() == reflect.Interface {
		if current.IsNil() {
			return nil, false
		}
		current = current.Elem()
	}
	switch current.Kind() {
	case reflect.String:
		return current.String(), true
	case reflect.Bool:
		return current.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(current.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(current.Uint()), true
	case reflect.Float32, reflect.Float64:
		return current.Float(), true
	default:
		return nil, false
	}
}

// telemetryPickAttributes keeps the attribute set inside the allow-list and
// drops anything non-primitive, non-finite, empty or overlong.
func telemetryPickAttributes(candidates map[string]any) TelemetryAttributes {
	out := TelemetryAttributes{}
	for _, name := range telemetryAllowedAttributes {
		value, ok := telemetryPrimitive(candidates[name])
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				out[name] = typed
			}
		case string:
			if len(typed) > 0 && len(typed) <= telemetryMaxTextLength {
				out[name] = typed
			}
		default:
			out[name] = typed
		}
	}
	return out
}

type telemetryRuntime struct {
	mu               sync.Mutex
	enabled          bool
	tracer           TelemetryTracer
	meter            TelemetryMeter
	operations       []core.PaymentOperation
	pendingTimeoutMs int64
	maxPending       int
	started          map[core.PaymentOperation][]int64
	counter          TelemetryCounter
	duration         TelemetryHistogram
	logger           core.Logger
}

// reports is true when there is anything to report to, for this operation.
func (r *telemetryRuntime) reports(operation core.PaymentOperation) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabledLocked(operation)
}

func (r *telemetryRuntime) enabledLocked(operation core.PaymentOperation) bool {
	if !r.enabled || (r.tracer == nil && r.meter == nil) {
		return false
	}
	if r.operations == nil {
		return true
	}
	for _, candidate := range r.operations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func (r *telemetryRuntime) report(err error) {
	// Telemetry must never change the outcome of a payment, and a broken logger
	// must not break it either.
	_ = pluginCall(func() error {
		r.mu.Lock()
		logger := r.logger
		r.mu.Unlock()
		if logger != nil {
			logger.Error("telemetry: reporting failed", err, nil)
		}
		return nil
	})
}

func (r *telemetryRuntime) safely(task func()) {
	if err := pluginCall(func() error { task(); return nil }); err != nil {
		r.report(err)
	}
}

func (r *telemetryRuntime) startCall(operation core.PaymentOperation) {
	now := pluginNowMs()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started == nil {
		r.started = map[core.PaymentOperation][]int64{}
	}
	opens := r.started[operation]
	// A call whose after hooks never ran must not hold its slot forever, and the
	// queue must stay bounded.
	for len(opens) > 0 && now-opens[0] > r.pendingTimeoutMs {
		opens = opens[1:]
	}
	if len(opens) >= r.maxPending && len(opens) > 0 {
		opens = opens[1:]
	}
	r.started[operation] = append(opens, now)
}

func (r *telemetryRuntime) takeStart(operation core.PaymentOperation) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	opens := r.started[operation]
	if len(opens) == 0 {
		return 0, false
	}
	start := opens[0]
	if len(opens) == 1 {
		delete(r.started, operation)
	} else {
		r.started[operation] = opens[1:]
	}
	return start, true
}

func (r *telemetryRuntime) finishCall(ctx core.OperationResultContext) {
	if !r.reports(ctx.Operation) {
		return
	}
	// A before hook of another plugin may have returned the result before this
	// plugin's before hook ran: report the call without a measured duration.
	start, hasStart := r.takeStart(ctx.Operation)
	now := pluginNowMs()
	status, hasStatus := pluginStringField(ctx.Result, "status")
	httpStatus, hasHTTPStatus := pluginNumber(ctx.Result, "httpStatus")
	attempts, hasAttempts := pluginNumber(ctx.Result, "attempts")

	candidates := map[string]any{
		"payment.provider":  ctx.Provider,
		"payment.operation": string(ctx.Operation),
		"payment.code":      pluginField(ctx.Result, "code"),
	}
	if hasStatus {
		candidates["payment.result"] = status
	}
	if hasHTTPStatus {
		candidates["payment.http.status"] = httpStatus
	}
	if hasAttempts {
		candidates["payment.retry.count"] = attempts
	}
	if hasStart {
		candidates["payment.duration_ms"] = float64(now - start)
	}
	attributes := telemetryPickAttributes(candidates)

	r.safely(func() {
		r.mu.Lock()
		tracer := r.tracer
		r.mu.Unlock()
		if tracer == nil {
			return
		}
		startTime := now
		if hasStart {
			startTime = start
		}
		span := tracer.StartSpan("payment."+string(ctx.Operation), TelemetrySpanOptions{StartTime: startTime})
		if span == nil {
			return
		}
		for _, name := range telemetryAllowedAttributes {
			if value, ok := attributes[name]; ok {
				span.SetAttribute(name, value)
			}
		}
		if hasStatus {
			switch core.PaymentStatus(status) {
			case core.StatusFailure:
				span.SetStatus(TelemetrySpanStatus{Code: TelemetryStatusError})
			case core.StatusSuccess:
				span.SetStatus(TelemetrySpanStatus{Code: TelemetryStatusOK})
			}
		}
		span.End(now)
	})

	r.safely(func() {
		r.mu.Lock()
		meter := r.meter
		if meter != nil {
			if r.counter == nil {
				r.counter = meter.CreateCounter("payment.calls")
			}
			if r.duration == nil {
				r.duration = meter.CreateHistogram("payment.duration")
			}
		}
		counter, duration := r.counter, r.duration
		r.mu.Unlock()
		if meter == nil {
			return
		}
		metricAttributes := TelemetryAttributes{}
		for name, value := range attributes {
			if name != "payment.duration_ms" {
				metricAttributes[name] = value
			}
		}
		if counter != nil {
			counter.Add(1, metricAttributes)
		}
		if hasStart && duration != nil {
			duration.Record(float64(now-start), metricAttributes)
		}
	})
}

// TelemetryMethods is the "telemetry" member the plugin adds to the facade.
type TelemetryMethods struct {
	runtime *telemetryRuntime
}

// Enable starts reporting calls again.
func (m *TelemetryMethods) Enable() {
	m.runtime.mu.Lock()
	defer m.runtime.mu.Unlock()
	m.runtime.enabled = true
}

// Disable stops reporting: the hooks do no work at all.
func (m *TelemetryMethods) Disable() {
	m.runtime.mu.Lock()
	defer m.runtime.mu.Unlock()
	m.runtime.enabled = false
	m.runtime.started = nil
}

// Enabled reports whether calls are being reported.
func (m *TelemetryMethods) Enabled() bool {
	m.runtime.mu.Lock()
	defer m.runtime.mu.Unlock()
	return m.runtime.enabled && (m.runtime.tracer != nil || m.runtime.meter != nil)
}

// Telemetry reports every provider call to a tracer and a meter.
//
// One span per call, named payment.<operation>, carrying only these attributes:
// payment.provider, payment.operation, payment.result, payment.code,
// payment.http.status, payment.retry.count and payment.duration_ms. No card data,
// credentials, tokens, conversation ids or request and response bodies are ever
// read, so nothing sensitive can reach an attribute.
//
// Metrics: the counter payment.calls (one per call, with the same attributes
// minus the duration) and the histogram payment.duration (milliseconds).
func Telemetry(options TelemetryOptions) (core.Plugin, error) {
	if options.Operations != nil {
		for _, operation := range options.Operations {
			known := false
			for _, candidate := range core.AllPaymentOperations {
				if candidate == operation {
					known = true
					break
				}
			}
			if !known {
				names := make([]string, 0, len(core.AllPaymentOperations))
				for _, candidate := range core.AllPaymentOperations {
					names = append(names, string(candidate))
				}
				return core.Plugin{}, core.NewConfigurationError(
					fmt.Sprintf("telemetry: unknown operation '%s'. Use one of: %s.", operation, strings.Join(names, ", ")), "")
			}
		}
	}
	pendingTimeoutMs := options.PendingTimeoutMs
	if pendingTimeoutMs == 0 {
		pendingTimeoutMs = 300000
	}
	maxPending := options.MaxPending
	if maxPending == 0 {
		maxPending = 512
	}
	runtime := &telemetryRuntime{
		enabled:          options.Enabled == nil || *options.Enabled,
		tracer:           options.Tracer,
		meter:            options.Meter,
		operations:       options.Operations,
		pendingTimeoutMs: int64(pendingTimeoutMs),
		maxPending:       maxPending,
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDTelemetry,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			runtime.mu.Lock()
			runtime.logger = pctx.Logger()
			runtime.mu.Unlock()
			return nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{
				{
					Matcher: func(ctx core.OperationContext) bool { return runtime.reports(ctx.Operation) },
					Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
						runtime.startCall(ctx.Operation)
						return nil, nil
					},
				},
			},
			After: []core.AfterHook{
				{
					Matcher: func(ctx core.OperationResultContext) bool { return runtime.reports(ctx.Operation) },
					Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
						runtime.finishCall(ctx)
						return nil, nil
					},
				},
			},
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"telemetry": &TelemetryMethods{runtime: runtime}}, nil
		},
	}), nil
}

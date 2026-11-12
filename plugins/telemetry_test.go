package plugins

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// telemetryTestSpan is a span as the fake tracer records it.
type telemetryTestSpan struct {
	name       string
	attributes TelemetryAttributes
	status     *TelemetrySpanStatus
	startTime  int64
	endTime    int64
	ended      bool
}

func (s *telemetryTestSpan) SetAttribute(key string, value TelemetryAttributeValue) {
	s.attributes[key] = value
}

func (s *telemetryTestSpan) SetStatus(status TelemetrySpanStatus) { s.status = &status }

func (s *telemetryTestSpan) End(endTime ...int64) {
	if len(endTime) > 0 {
		s.endTime = endTime[0]
	}
	s.ended = true
}

type telemetryTestTracer struct {
	mu    sync.Mutex
	spans []*telemetryTestSpan
	start func(name string, options TelemetrySpanOptions) TelemetrySpan
}

func (t *telemetryTestTracer) StartSpan(name string, options TelemetrySpanOptions) TelemetrySpan {
	if t.start != nil {
		return t.start(name, options)
	}
	span := &telemetryTestSpan{name: name, attributes: TelemetryAttributes{}, startTime: options.StartTime}
	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()
	return span
}

func (t *telemetryTestTracer) recorded() []*telemetryTestSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*telemetryTestSpan{}, t.spans...)
}

func (t *telemetryTestTracer) names() []string {
	names := []string{}
	for _, span := range t.recorded() {
		names = append(names, span.name)
	}
	return names
}

// telemetryTestBrokenSpan fails on every call after it was started.
type telemetryTestBrokenSpan struct{}

func (telemetryTestBrokenSpan) SetAttribute(string, TelemetryAttributeValue) {
	panic("tracer exploded")
}
func (telemetryTestBrokenSpan) SetStatus(TelemetrySpanStatus) { panic("tracer exploded") }
func (telemetryTestBrokenSpan) End(...int64)                  { panic("tracer exploded") }

type telemetryTestMetric struct {
	value      float64
	attributes TelemetryAttributes
}

type telemetryTestCounter struct {
	mu    sync.Mutex
	calls []telemetryTestMetric
	add   func(float64, TelemetryAttributes)
}

func (c *telemetryTestCounter) Add(value float64, attributes TelemetryAttributes) {
	if c.add != nil {
		c.add(value, attributes)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, telemetryTestMetric{value: value, attributes: attributes})
}

func (c *telemetryTestCounter) recorded() []telemetryTestMetric {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telemetryTestMetric{}, c.calls...)
}

type telemetryTestHistogram struct {
	mu     sync.Mutex
	calls  []telemetryTestMetric
	record func(float64, TelemetryAttributes)
}

func (h *telemetryTestHistogram) Record(value float64, attributes TelemetryAttributes) {
	if h.record != nil {
		h.record(value, attributes)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, telemetryTestMetric{value: value, attributes: attributes})
}

func (h *telemetryTestHistogram) recorded() []telemetryTestMetric {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]telemetryTestMetric{}, h.calls...)
}

type telemetryTestMeter struct {
	mu                sync.Mutex
	counters          map[string]*telemetryTestCounter
	histograms        map[string]*telemetryTestHistogram
	createdCounters   []string
	createdHistograms []string
	counter           TelemetryCounter
	histogram         TelemetryHistogram
	createCounter     func(string) TelemetryCounter
	createHistogram   func(string) TelemetryHistogram
}

func newTelemetryTestMeter() *telemetryTestMeter {
	return &telemetryTestMeter{
		counters:   map[string]*telemetryTestCounter{},
		histograms: map[string]*telemetryTestHistogram{},
	}
}

func (m *telemetryTestMeter) CreateCounter(name string) TelemetryCounter {
	if m.createCounter != nil {
		return m.createCounter(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdCounters = append(m.createdCounters, name)
	if m.counter != nil {
		return m.counter
	}
	counter := &telemetryTestCounter{}
	m.counters[name] = counter
	return counter
}

func (m *telemetryTestMeter) CreateHistogram(name string) TelemetryHistogram {
	if m.createHistogram != nil {
		return m.createHistogram(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdHistograms = append(m.createdHistograms, name)
	if m.histogram != nil {
		return m.histogram
	}
	histogram := &telemetryTestHistogram{}
	m.histograms[name] = histogram
	return histogram
}

func (m *telemetryTestMeter) counterCalls(name string) []telemetryTestMetric {
	m.mu.Lock()
	counter := m.counters[name]
	m.mu.Unlock()
	if counter == nil {
		return nil
	}
	return counter.recorded()
}

func (m *telemetryTestMeter) histogramSamples(name string) []telemetryTestMetric {
	m.mu.Lock()
	histogram := m.histograms[name]
	m.mu.Unlock()
	if histogram == nil {
		return nil
	}
	return histogram.recorded()
}

func (m *telemetryTestMeter) created() ([]string, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.createdCounters...), append([]string{}, m.createdHistograms...)
}

func telemetryTestSetup(t *testing.T, options TelemetryOptions) (core.Plugin, *TelemetryMethods, *pluginTestContext) {
	t.Helper()
	plugin, err := Telemetry(options)
	if err != nil {
		t.Fatalf("telemetry() failed: %v", err)
	}
	pctx := newPluginTestContext()
	pctx.logger = &pluginTestLogger{}
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	methods, ok := pluginTestMethods(t, plugin, pctx)["telemetry"].(*TelemetryMethods)
	if !ok {
		t.Fatal("the telemetry member is missing")
	}
	return plugin, methods, pctx
}

func telemetryTestBefore(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, provider string, request any) {
	t.Helper()
	pluginTestRunBefore(t, plugin, pluginTestOperation(operation, provider, request))
}

// telemetryTestCall runs the before hooks and the after hooks around a canned
// provider result, the way the facade does.
func telemetryTestCall(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, provider string, request, result any) any {
	t.Helper()
	telemetryTestBefore(t, plugin, operation, provider, request)
	return pluginTestRunAfter(t, plugin, pluginTestOutcome(operation, provider, request, result))
}

func telemetryTestSuccess() *core.PaymentResponse {
	return &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "mock-payment"}
}

// telemetryTestHTTPFailure is a provider result whose error body carries card
// data on purpose: spans must never pick it up.
func telemetryTestHTTPFailure(request *core.PaymentRequest) *core.PaymentResponse {
	return &core.PaymentResponse{
		Status:    core.StatusFailure,
		ErrorCode: "GATEWAY_DOWN",
		ErrorMessage: fmt.Sprintf("Card %s cvc %s email %s ip %s",
			request.PaymentCard.CardNumber, request.PaymentCard.CVC, request.Buyer.Email, request.Buyer.IP),
		HTTPStatus: 502,
		Attempts:   3,
		RawResponse: map[string]any{
			"status": 502,
			"data": map[string]any{
				"errorCode":    "GATEWAY_DOWN",
				"errorMessage": "Bad gateway",
				"token":        "tok_5528790000000008",
			},
		},
	}
}

func TestTelemetryEmitsOneSpanPerCall(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	request := pluginTestPaymentRequest()

	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", request, telemetryTestSuccess())
	telemetryTestCall(t, plugin, core.OperationGetPayment, "mock", "mock-payment", telemetryTestSuccess())

	spans := tracer.recorded()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}
	if got := tracer.names(); got[0] != "payment.createPayment" || got[1] != "payment.getPayment" {
		t.Errorf("span names = %v", got)
	}
	first := spans[0]
	if first.attributes["payment.provider"] != "mock" ||
		first.attributes["payment.operation"] != "createPayment" ||
		first.attributes["payment.result"] != string(core.StatusSuccess) {
		t.Errorf("attributes = %v", first.attributes)
	}
	if first.status == nil || first.status.Code != TelemetryStatusOK {
		t.Errorf("status = %+v, want OK", first.status)
	}
	if !first.ended || first.endTime <= 0 {
		t.Errorf("the span was not ended: %+v", first)
	}
	duration, ok := first.attributes["payment.duration_ms"].(float64)
	if !ok || duration < 0 {
		t.Errorf("payment.duration_ms = %v, want a non-negative number", first.attributes["payment.duration_ms"])
	}
	if spans[1].attributes["payment.operation"] != "getPayment" {
		t.Errorf("second span attributes = %v", spans[1].attributes)
	}
}

func TestTelemetryMarksFailuresWithTheNormalizedCode(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	failure := &core.PaymentResponse{
		Status:       core.StatusFailure,
		Code:         core.CodeInsufficientFunds,
		ErrorCode:    string(core.CodeInsufficientFunds),
		ErrorMessage: "Insufficient funds",
	}
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), failure)

	spans := tracer.recorded()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].attributes["payment.result"] != string(core.StatusFailure) {
		t.Errorf("payment.result = %v", spans[0].attributes["payment.result"])
	}
	if spans[0].attributes["payment.code"] != string(core.CodeInsufficientFunds) {
		t.Errorf("payment.code = %v", spans[0].attributes["payment.code"])
	}
	if spans[0].status == nil || spans[0].status.Code != TelemetryStatusError {
		t.Errorf("status = %+v, want ERROR", spans[0].status)
	}
}

func TestTelemetryReportsLostResponseAsPendingWithoutStatus(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	pending := &core.PaymentResponse{
		Status:    core.StatusPending,
		Code:      core.CodeNetworkError,
		ErrorCode: string(core.CodeNetworkError),
	}
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), pending)

	spans := tracer.recorded()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].attributes["payment.result"] != string(core.StatusPending) {
		t.Errorf("payment.result = %v", spans[0].attributes["payment.result"])
	}
	if spans[0].attributes["payment.code"] != string(core.CodeNetworkError) {
		t.Errorf("payment.code = %v", spans[0].attributes["payment.code"])
	}
	if spans[0].status != nil {
		t.Errorf("status = %+v, want none for a pending call", spans[0].status)
	}
}

func TestTelemetryCarriesHTTPStatusAndRetryCount(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	request := pluginTestPaymentRequest()
	result := telemetryTestHTTPFailure(request)
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "httpfail", request, result)

	spans := tracer.recorded()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].attributes["payment.http.status"] != float64(502) {
		t.Errorf("payment.http.status = %v, want 502", spans[0].attributes["payment.http.status"])
	}
	if spans[0].attributes["payment.retry.count"] != float64(3) {
		t.Errorf("payment.retry.count = %v, want 3", spans[0].attributes["payment.retry.count"])
	}
	if _, present := spans[0].attributes["payment.code"]; present {
		t.Errorf("payment.code = %v, want absent without a normalized code", spans[0].attributes["payment.code"])
	}
}

func TestTelemetryNeverLeaksCardDataOrBodies(t *testing.T) {
	tracer := &telemetryTestTracer{}
	meter := newTelemetryTestMeter()
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer, Meter: meter})
	request := pluginTestPaymentRequest()
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "httpfail", request, telemetryTestHTTPFailure(request))

	spans := tracer.recorded()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	allowed := map[string]bool{}
	for _, name := range telemetryAllowedAttributes {
		allowed[name] = true
	}
	sensitive := []string{
		"5528790000000008", "john.doe@example.com", "85.34.78.112", "tok_5528790000000008",
		"GATEWAY_DOWN", "Bad gateway", "John Doe", "B67832", "74300864791",
	}
	seen := []TelemetryAttributes{spans[0].attributes}
	for _, metric := range meter.counterCalls("payment.calls") {
		seen = append(seen, metric.attributes)
	}
	for _, attributes := range seen {
		for key, value := range attributes {
			if !allowed[key] {
				t.Errorf("unexpected attribute %q", key)
			}
			text := fmt.Sprint(value)
			for _, needle := range sensitive {
				if strings.Contains(text, needle) {
					t.Errorf("attribute %q carries %q", key, needle)
				}
			}
		}
	}
}

func TestTelemetryBrokenBackendsNeverChangeTheOutcome(t *testing.T) {
	bomb := func(string, TelemetrySpanOptions) TelemetrySpan { panic("tracer exploded") }
	tracer := &telemetryTestTracer{start: bomb}
	meter := newTelemetryTestMeter()
	meter.createCounter = func(string) TelemetryCounter { panic("tracer exploded") }
	meter.createHistogram = func(string) TelemetryHistogram { panic("tracer exploded") }
	plugin, _, pctx := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer, Meter: meter})

	result := telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	payment, ok := result.(*core.PaymentResponse)
	if !ok || payment.Status != core.StatusSuccess || payment.Code != "" {
		t.Fatalf("result = %+v, want an untouched success", result)
	}
	if len(pctx.logger.(*pluginTestLogger).all()) == 0 {
		t.Error("the reporting failure was not logged")
	}

	halfBroken := &telemetryTestTracer{start: func(string, TelemetrySpanOptions) TelemetrySpan {
		return telemetryTestBrokenSpan{}
	}}
	halfPlugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: halfBroken})
	if status := telemetryTestCall(t, halfPlugin, core.OperationCreatePayment, "mock",
		pluginTestPaymentRequest(), telemetryTestSuccess()).(*core.PaymentResponse).Status; status != core.StatusSuccess {
		t.Errorf("half broken tracer status = %q, want success", status)
	}

	brokenMeter := newTelemetryTestMeter()
	brokenMeter.counter = &telemetryTestCounter{add: func(float64, TelemetryAttributes) { panic("tracer exploded") }}
	brokenMeter.histogram = &telemetryTestHistogram{record: func(float64, TelemetryAttributes) { panic("tracer exploded") }}
	meterPlugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Meter: brokenMeter})
	if status := telemetryTestCall(t, meterPlugin, core.OperationCreatePayment, "mock",
		pluginTestPaymentRequest(), telemetryTestSuccess()).(*core.PaymentResponse).Status; status != core.StatusSuccess {
		t.Errorf("broken meter status = %q, want success", status)
	}
}

func TestTelemetryReportsMetricsPerCall(t *testing.T) {
	tracer := &telemetryTestTracer{}
	meter := newTelemetryTestMeter()
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer, Meter: meter})
	for i := 0; i < 2; i++ {
		telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	}

	counters, histograms := meter.created()
	if len(counters) != 1 || counters[0] != "payment.calls" {
		t.Errorf("created counters = %v", counters)
	}
	if len(histograms) != 1 || histograms[0] != "payment.duration" {
		t.Errorf("created histograms = %v", histograms)
	}
	calls := meter.counterCalls("payment.calls")
	samples := meter.histogramSamples("payment.duration")
	if len(calls) != 2 || len(samples) != 2 {
		t.Fatalf("recorded %d counter calls and %d samples, want 2 each", len(calls), len(samples))
	}
	if calls[0].value != 1 {
		t.Errorf("counter value = %v, want 1", calls[0].value)
	}
	if calls[0].attributes["payment.provider"] != "mock" || calls[0].attributes["payment.result"] != string(core.StatusSuccess) {
		t.Errorf("counter attributes = %v", calls[0].attributes)
	}
	if _, present := calls[0].attributes["payment.duration_ms"]; present {
		t.Error("the counter carries the duration attribute")
	}
	if samples[0].value < 0 {
		t.Errorf("duration sample = %v, want a non-negative number", samples[0].value)
	}
	if len(tracer.recorded()) != 2 {
		t.Errorf("recorded %d spans, want 2", len(tracer.recorded()))
	}

	metricsOnly := newTelemetryTestMeter()
	meterPlugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Meter: metricsOnly})
	telemetryTestCall(t, meterPlugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if calls := metricsOnly.counterCalls("payment.calls"); len(calls) != 1 {
		t.Errorf("metrics-only mode recorded %d counter calls, want 1", len(calls))
	}
}

func TestTelemetryFiltersOperationsAndRefusesUnknownOnes(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{
		Tracer:     tracer,
		Operations: []core.PaymentOperation{core.OperationCreatePayment},
	})
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	telemetryTestCall(t, plugin, core.OperationBinCheck, "mock", "552879", map[string]any{"status": "success"})

	if names := tracer.names(); len(names) != 1 || names[0] != "payment.createPayment" {
		t.Errorf("span names = %v, want only payment.createPayment", names)
	}
	if _, err := Telemetry(TelemetryOptions{Operations: []core.PaymentOperation{core.PaymentOperation("nope")}}); err == nil {
		t.Error("telemetry() accepted an unknown operation")
	} else if _, ok := err.(*core.ConfigurationError); !ok {
		t.Errorf("error is %T, want *core.ConfigurationError", err)
	}
}

func TestTelemetryCanBeDisabledAtRuntime(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, methods, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	if !methods.Enabled() {
		t.Fatal("the plugin reports itself disabled, want enabled")
	}
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if len(tracer.recorded()) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(tracer.recorded()))
	}

	methods.Disable()
	if methods.Enabled() {
		t.Error("the plugin still reports itself enabled")
	}
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if len(tracer.recorded()) != 1 {
		t.Errorf("recorded %d spans while disabled, want 1", len(tracer.recorded()))
	}

	methods.Enable()
	telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if len(tracer.recorded()) != 2 {
		t.Errorf("recorded %d spans after re-enabling, want 2", len(tracer.recorded()))
	}
}

func TestTelemetrySurvivesACallWhoseAfterHooksNeverRan(t *testing.T) {
	tracer := &telemetryTestTracer{}
	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer})
	request := pluginTestPaymentRequest()

	// The provider threw: the before hook ran, the after hook never did.
	telemetryTestBefore(t, plugin, core.OperationCreatePayment, "bad", request)
	if len(tracer.recorded()) != 0 {
		t.Fatalf("recorded %d spans for the aborted call, want 0", len(tracer.recorded()))
	}

	result := telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", request, telemetryTestSuccess())
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.(*core.PaymentResponse).Status)
	}
	spans := tracer.recorded()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if _, ok := spans[0].attributes["payment.duration_ms"].(float64); !ok {
		t.Errorf("payment.duration_ms = %v, want a number", spans[0].attributes["payment.duration_ms"])
	}
}

// The OpenTelemetry-shaped types below carry extra members and still satisfy the
// plugin's interfaces: no telemetry dependency is needed.
type telemetryTestOtelSpan struct {
	attributes TelemetryAttributes
	status     *TelemetrySpanStatus
	ended      int64
}

func (s *telemetryTestOtelSpan) SetAttribute(key string, value TelemetryAttributeValue) {
	s.attributes[key] = value
}
func (s *telemetryTestOtelSpan) SetStatus(status TelemetrySpanStatus) { s.status = &status }
func (s *telemetryTestOtelSpan) End(endTime ...int64) {
	if len(endTime) > 0 {
		s.ended = endTime[0]
	}
}
func (s *telemetryTestOtelSpan) AddEvent(string)   {}
func (s *telemetryTestOtelSpan) IsRecording() bool { return true }

type telemetryTestOtelTracer struct {
	spans []*telemetryTestOtelSpan
}

func (t *telemetryTestOtelTracer) StartSpan(name string, options TelemetrySpanOptions) TelemetrySpan {
	span := &telemetryTestOtelSpan{attributes: TelemetryAttributes{}}
	for key, value := range options.Attributes {
		span.attributes[key] = value
	}
	t.spans = append(t.spans, span)
	return span
}

type telemetryTestOtelCounter struct {
	calls []float64
}

func (c *telemetryTestOtelCounter) Add(value float64, _ TelemetryAttributes) {
	c.calls = append(c.calls, value)
}

type telemetryTestOtelMeter struct {
	counter *telemetryTestOtelCounter
}

func (m *telemetryTestOtelMeter) CreateCounter(string) TelemetryCounter { return m.counter }
func (m *telemetryTestOtelMeter) CreateHistogram(string) TelemetryHistogram {
	return &telemetryTestHistogram{}
}

func TestTelemetryAcceptsOpenTelemetryShapedBackends(t *testing.T) {
	tracer := &telemetryTestOtelTracer{}
	meter := &telemetryTestOtelMeter{counter: &telemetryTestOtelCounter{}}
	var _ TelemetryTracer = tracer
	var _ TelemetryMeter = meter

	plugin, _, _ := telemetryTestSetup(t, TelemetryOptions{Tracer: tracer, Meter: meter})
	result := telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.(*core.PaymentResponse).Status)
	}
	if len(tracer.spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(tracer.spans))
	}
	if tracer.spans[0].attributes["payment.operation"] != "createPayment" {
		t.Errorf("attributes = %v", tracer.spans[0].attributes)
	}
	if tracer.spans[0].status == nil || tracer.spans[0].status.Code != TelemetryStatusOK {
		t.Errorf("status = %+v, want OK", tracer.spans[0].status)
	}
	if len(meter.counter.calls) != 1 || meter.counter.calls[0] != 1 {
		t.Errorf("counter calls = %v, want [1]", meter.counter.calls)
	}
}

func TestTelemetryDoesNothingWithoutBackends(t *testing.T) {
	plugin, methods, _ := telemetryTestSetup(t, TelemetryOptions{})
	if methods.Enabled() {
		t.Error("the plugin reports itself enabled without a tracer or a meter")
	}
	result := telemetryTestCall(t, plugin, core.OperationCreatePayment, "mock", pluginTestPaymentRequest(), telemetryTestSuccess())
	if result.(*core.PaymentResponse).Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.(*core.PaymentResponse).Status)
	}
}

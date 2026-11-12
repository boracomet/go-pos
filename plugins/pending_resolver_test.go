package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func pendingResolverTestSetup(t *testing.T, options PendingResolverOptions) (core.Plugin, *PendingResolverMethods, *pluginTestContext, *pluginTestProvider) {
	t.Helper()
	plugin, err := PendingResolver(options)
	if err != nil {
		t.Fatalf("pendingResolver() failed: %v", err)
	}
	provider := newPluginTestProvider("mock")
	pctx := newPluginTestContext()
	pctx.providers["mock"] = provider
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	methods, ok := pluginTestMethods(t, plugin, pctx)["pending"].(*PendingResolverMethods)
	if !ok {
		t.Fatal("the pending member is missing")
	}
	return plugin, methods, pctx, provider
}

// pendingResolverTestLostResponse runs a createPayment whose response was lost:
// the provider returns a pending NETWORK_ERROR result and the after hook stores it.
func pendingResolverTestLostResponse(t *testing.T, plugin core.Plugin, provider *pluginTestProvider, conversationID string) *core.PaymentResponse {
	t.Helper()
	provider.payment = &core.PaymentResponse{
		Status:       core.StatusPending,
		PaymentID:    "mock-payment",
		Code:         core.CodeNetworkError,
		ErrorCode:    string(core.CodeNetworkError),
		ErrorMessage: "The response was lost",
	}
	request := pluginTestPaymentRequest()
	request.ConversationID = conversationID
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	pluginTestRunAfter(t, plugin, pluginTestOutcome(core.OperationCreatePayment, "mock", request, result))
	return result
}

// pendingResolverTestDue moves a stored payment into the past, the way advancing
// the clock does: age is how long ago it was captured.
func pendingResolverTestDue(t *testing.T, store PendingStore, id string, age time.Duration) {
	t.Helper()
	item, err := store.Get(id)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if item == nil {
		t.Fatalf("%q is not in the store", id)
	}
	now := pluginNowMs()
	item.NextAttemptAt = now - 1
	if age > 0 {
		item.CreatedAt = now - age.Milliseconds()
	}
	if err := store.Save(*item); err != nil {
		t.Fatalf("save failed: %v", err)
	}
}

func pendingResolverTestResolve(t *testing.T, methods *PendingResolverMethods) ResolveSummary {
	t.Helper()
	summary, err := methods.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	return summary
}

func pendingResolverTestList(t *testing.T, methods *PendingResolverMethods) []PendingPayment {
	t.Helper()
	items, err := methods.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	return items
}

func pendingResolverTestEvents(pctx *pluginTestContext, eventType core.PaymentEventType) []*core.PaymentEvent {
	events := []*core.PaymentEvent{}
	for _, event := range pctx.emitted() {
		if event.Type == eventType {
			events = append(events, event)
		}
	}
	return events
}

// pendingResolverTestPlainStore is a store without Claim, so only the plugin's
// own guard keeps two resolvers apart.
type pendingResolverTestPlainStore struct {
	mu    sync.Mutex
	items map[string]PendingPayment
}

func newPendingResolverTestPlainStore() *pendingResolverTestPlainStore {
	return &pendingResolverTestPlainStore{items: map[string]PendingPayment{}}
}

func (s *pendingResolverTestPlainStore) Save(payment PendingPayment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[payment.ID] = payment
	return nil
}

func (s *pendingResolverTestPlainStore) Get(id string) (*PendingPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.items[id]
	if !exists {
		return nil, nil
	}
	return &item, nil
}

func (s *pendingResolverTestPlainStore) List() ([]PendingPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingPayment, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	return out, nil
}

func (s *pendingResolverTestPlainStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

func (s *pendingResolverTestPlainStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// pendingResolverTestGateProvider holds the status query open so a second
// resolver can run while the first one owns the payment.
type pendingResolverTestGateProvider struct {
	*pluginTestProvider
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (p *pendingResolverTestGateProvider) GetPayment(ctx context.Context, id string) (*core.PaymentResponse, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return p.pluginTestProvider.GetPayment(ctx, id)
}

// pendingResolverTestConcurrent runs two resolvers, the second one starting while
// the first is inside its status query.
func pendingResolverTestConcurrent(t *testing.T, methods *PendingResolverMethods, gate *pendingResolverTestGateProvider) (ResolveSummary, ResolveSummary) {
	t.Helper()
	var waiting sync.WaitGroup
	var first, second ResolveSummary
	var firstErr, secondErr error
	waiting.Add(1)
	go func() {
		defer waiting.Done()
		first, firstErr = methods.Resolve(context.Background())
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first resolver never queried the provider")
	}
	waiting.Add(1)
	go func() {
		defer waiting.Done()
		second, secondErr = methods.Resolve(context.Background())
	}()
	close(gate.release)
	waiting.Wait()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("resolve failed: %v / %v", firstErr, secondErr)
	}
	return first, second
}

func TestPendingResolverRecordsNetworkErrorAndResolvesToSucceeded(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s", "1m"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-101")
	if initial.Status != core.StatusPending || initial.Code != core.CodeNetworkError {
		t.Fatalf("initial result = %q/%q, want pending NETWORK_ERROR", initial.Status, initial.Code)
	}

	before := pendingResolverTestList(t, methods)
	if len(before) != 1 {
		t.Fatalf("stored %d payments, want 1", len(before))
	}
	if before[0].ID != initial.PaymentID || before[0].ConversationID != "ORDER-101" {
		t.Errorf("stored payment = %+v", before[0])
	}
	if early := pendingResolverTestResolve(t, methods); early.Checked != 0 {
		t.Errorf("early resolve checked %d, want 0", early.Checked)
	}

	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	provider.queue = []*core.PaymentResponse{{Status: core.StatusSuccess, PaymentID: initial.PaymentID}}
	summary := pendingResolverTestResolve(t, methods)
	if summary.Checked != 1 || summary.Succeeded != 1 {
		t.Errorf("summary = %+v, want 1 checked and 1 succeeded", summary)
	}
	if remaining := pendingResolverTestList(t, methods); len(remaining) != 0 {
		t.Errorf("%d payments remain, want 0", len(remaining))
	}

	events := pendingResolverTestEvents(pctx, core.EventPaymentSucceeded)
	if len(events) != 1 {
		t.Fatalf("emitted %d success events, want 1", len(events))
	}
	if events[0].ResolvedFrom != core.ResolvedFromPending {
		t.Errorf("resolvedFrom = %q, want pending", events[0].ResolvedFrom)
	}
	if events[0].PaymentID != initial.PaymentID {
		t.Errorf("paymentId = %q, want %q", events[0].PaymentID, initial.PaymentID)
	}
	if provider.count("createPayment") != 1 {
		t.Errorf("createPayment ran %d times, want 1", provider.count("createPayment"))
	}
	if provider.count("getPayment") != 1 {
		t.Errorf("getPayment ran %d times, want 1", provider.count("getPayment"))
	}
}

func TestPendingResolverResolvesToFailed(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-102")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	provider.queue = []*core.PaymentResponse{{
		Status:         core.StatusFailure,
		Code:           core.CodeCardDeclined,
		ErrorMessage:   "Declined by bank",
		PaymentID:      initial.PaymentID,
		ConversationID: "ORDER-102",
	}}

	summary := pendingResolverTestResolve(t, methods)
	if summary.Failed != 1 {
		t.Errorf("summary = %+v, want 1 failed", summary)
	}
	events := pendingResolverTestEvents(pctx, core.EventPaymentFailed)
	if len(events) != 1 {
		t.Fatalf("emitted %d failure events, want 1", len(events))
	}
	if events[0].ResolvedFrom != core.ResolvedFromPending || events[0].Code != core.CodeCardDeclined {
		t.Errorf("event = %+v", events[0])
	}
	if remaining := pendingResolverTestList(t, methods); len(remaining) != 0 {
		t.Errorf("%d payments remain, want 0", len(remaining))
	}
}

func TestPendingResolverGivesUpAndEmitsUnresolved(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"1m"},
		GiveUpAfter: "30m",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-103")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 31*time.Minute)

	summary := pendingResolverTestResolve(t, methods)
	if summary.Unresolved != 1 {
		t.Errorf("summary = %+v, want 1 unresolved", summary)
	}
	events := pendingResolverTestEvents(pctx, core.EventPaymentUnresolved)
	if len(events) != 1 {
		t.Fatalf("emitted %d unresolved events, want 1", len(events))
	}
	if events[0].PaymentID != initial.PaymentID {
		t.Errorf("paymentId = %q, want %q", events[0].PaymentID, initial.PaymentID)
	}
	if remaining := pendingResolverTestList(t, methods); len(remaining) != 0 {
		t.Errorf("%d payments remain, want 0", len(remaining))
	}
	if provider.count("getPayment") != 0 {
		t.Errorf("an abandoned payment was queried %d times, want 0", provider.count("getPayment"))
	}
}

func TestPendingResolverSupportsManualInspectionAndDeletion(t *testing.T) {
	plugin, methods, _, provider := pendingResolverTestSetup(t, PendingResolverOptions{Schedule: []any{"10s"}})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "123456789")

	item, err := methods.Get(initial.PaymentID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if item == nil || item.ID != initial.PaymentID {
		t.Fatalf("get returned %+v", item)
	}
	if err := methods.Delete(initial.PaymentID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	deleted, err := methods.Get(initial.PaymentID)
	if err != nil {
		t.Fatalf("get after delete failed: %v", err)
	}
	if deleted != nil {
		t.Errorf("the payment is still stored: %+v", deleted)
	}
}

func TestPendingResolverResolvesExactlyOnceWithTwoResolvers(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-201")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	provider.queue = []*core.PaymentResponse{{Status: core.StatusSuccess, PaymentID: initial.PaymentID}}
	gate := &pendingResolverTestGateProvider{
		pluginTestProvider: provider,
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	pctx.providers["mock"] = gate

	first, second := pendingResolverTestConcurrent(t, methods, gate)
	if first.Checked+second.Checked != 1 {
		t.Errorf("checked %d + %d, want 1 in total", first.Checked, second.Checked)
	}
	if first.Succeeded+second.Succeeded != 1 {
		t.Errorf("succeeded %d + %d, want 1 in total", first.Succeeded, second.Succeeded)
	}
	if events := pendingResolverTestEvents(pctx, core.EventPaymentSucceeded); len(events) != 1 {
		t.Errorf("emitted %d success events, want 1", len(events))
	}
	if remaining := pendingResolverTestList(t, methods); len(remaining) != 0 {
		t.Errorf("%d payments remain, want 0", len(remaining))
	}
}

func TestPendingResolverGuardsConcurrentResolversWithoutClaim(t *testing.T) {
	store := newPendingResolverTestPlainStore()
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Store:       store,
		Schedule:    []any{"10s"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-202")
	pendingResolverTestDue(t, store, initial.PaymentID, 11*time.Second)
	provider.queue = []*core.PaymentResponse{{Status: core.StatusSuccess, PaymentID: initial.PaymentID}}
	gate := &pendingResolverTestGateProvider{
		pluginTestProvider: provider,
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	pctx.providers["mock"] = gate

	first, second := pendingResolverTestConcurrent(t, methods, gate)
	if first.Succeeded+second.Succeeded != 1 {
		t.Errorf("succeeded %d + %d, want 1 in total", first.Succeeded, second.Succeeded)
	}
	if events := pendingResolverTestEvents(pctx, core.EventPaymentSucceeded); len(events) != 1 {
		t.Errorf("emitted %d success events, want 1", len(events))
	}
	if store.size() != 0 {
		t.Errorf("%d payments remain, want 0", store.size())
	}
}

func TestPendingResolverKeepsPaymentPendingWhenTheQueryTimesOut(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s", "1m"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-203")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	provider.getError = errors.New("request timed out")

	summary := pendingResolverTestResolve(t, methods)
	if summary.Succeeded != 0 || summary.Failed != 0 || summary.StillPending != 1 {
		t.Errorf("summary = %+v, want 1 still pending", summary)
	}
	if events := pendingResolverTestEvents(pctx, core.EventPaymentSucceeded); len(events) != 0 {
		t.Errorf("emitted %d success events, want 0", len(events))
	}
	if events := pendingResolverTestEvents(pctx, core.EventPaymentFailed); len(events) != 0 {
		t.Errorf("emitted %d failure events, want 0", len(events))
	}
	remaining := pendingResolverTestList(t, methods)
	if len(remaining) != 1 {
		t.Fatalf("%d payments remain, want 1", len(remaining))
	}
	if remaining[0].Attempts != 1 || remaining[0].LastError != "request timed out" {
		t.Errorf("stored payment = %+v", remaining[0])
	}

	provider.getError = nil
	provider.payment = &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: initial.PaymentID}
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 71*time.Second)
	retry := pendingResolverTestResolve(t, methods)
	if retry.Succeeded != 1 {
		t.Errorf("retry summary = %+v, want 1 succeeded", retry)
	}
	if provider.count("getPayment") != 2 {
		t.Errorf("getPayment ran %d times, want 2", provider.count("getPayment"))
	}
	if provider.count("createPayment") != 1 {
		t.Errorf("createPayment ran %d times, want only the original", provider.count("createPayment"))
	}
}

func TestPendingResolverKeepsAmountsAsStrings(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s"},
		GiveUpAfter: "1h",
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-204")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	// The typed result carries no amount: the strings stored at capture time stay
	// authoritative, and a numeric paidPrice can never replace them.
	provider.queue = []*core.PaymentResponse{{Status: core.StatusSuccess, PaymentID: initial.PaymentID}}

	summary := pendingResolverTestResolve(t, methods)
	if summary.Succeeded != 1 {
		t.Fatalf("summary = %+v, want 1 succeeded", summary)
	}
	events := pendingResolverTestEvents(pctx, core.EventPaymentSucceeded)
	if len(events) != 1 {
		t.Fatalf("emitted %d success events, want 1", len(events))
	}
	if events[0].Amount != "1.2" {
		t.Errorf("amount = %q, want the string '1.2'", events[0].Amount)
	}
	if events[0].Currency != string(core.CurrencyTRY) {
		t.Errorf("currency = %q, want TRY", events[0].Currency)
	}
}

func TestPendingResolverValidatesDurations(t *testing.T) {
	cases := []struct {
		name    string
		options PendingResolverOptions
	}{
		{"schedule", PendingResolverOptions{Schedule: []any{"soon"}}},
		{"empty schedule", PendingResolverOptions{Schedule: []any{}}},
		{"zero schedule", PendingResolverOptions{Schedule: []any{0}}},
		{"giveUpAfter", PendingResolverOptions{GiveUpAfter: "forever"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := PendingResolver(testCase.options); err == nil {
				t.Fatal("pendingResolver() accepted invalid durations")
			}
		})
	}
}

func TestPendingResolverResolveEndpoint(t *testing.T) {
	plugin, methods, pctx, provider := pendingResolverTestSetup(t, PendingResolverOptions{
		Schedule:    []any{"10s"},
		GiveUpAfter: "1h",
		Endpoint:    true,
	})
	initial := pendingResolverTestLostResponse(t, plugin, provider, "ORDER-301")
	pendingResolverTestDue(t, methods.Store(), initial.PaymentID, 11*time.Second)
	provider.queue = []*core.PaymentResponse{{Status: core.StatusSuccess, PaymentID: initial.PaymentID}}

	endpoint, ok := plugin.Endpoints["resolve"]
	if !ok {
		t.Fatal("the resolve endpoint is missing")
	}
	if endpoint.Method != "POST" || endpoint.Path != "/pending/resolve" || !endpoint.Privileged {
		t.Errorf("endpoint = %+v", endpoint)
	}
	ectx := &pluginTestEndpointContext{pluginTestContext: pctx, request: pluginTestPost("/pending/resolve", nil, nil)}
	body, err := endpoint.Handler(context.Background(), ectx)
	if err != nil {
		t.Fatalf("the endpoint failed: %v", err)
	}
	summary, ok := body.(ResolveSummary)
	if !ok {
		t.Fatalf("the endpoint returned %T, want ResolveSummary", body)
	}
	if summary.Succeeded != 1 {
		t.Errorf("summary = %+v, want 1 succeeded", summary)
	}
}

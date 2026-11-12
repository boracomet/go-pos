package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/boracomet/go-pos/core"
)

func threeDSTimeoutTestRequest(t *testing.T, conversationID string) map[string]any {
	t.Helper()
	request := pluginTestJSON(t, pluginTestPaymentRequest())
	request["callbackUrl"] = "https://example.com/callback"
	if conversationID != "" {
		request["conversationId"] = conversationID
	}
	return request
}

func threeDSTimeoutTestPlugin(t *testing.T, options ThreeDSTimeoutOptions) (core.Plugin, *ThreeDSTimeoutMethods, *pluginTestContext, *pluginTestProvider) {
	t.Helper()
	plugin, err := ThreeDSTimeout(options)
	if err != nil {
		t.Fatalf("threeDSTimeout() failed: %v", err)
	}
	pctx := newPluginTestContext()
	provider := newPluginTestProvider("mock")
	pctx.providers["mock"] = provider
	methods, ok := pluginTestMethods(t, plugin, pctx)["threeDS"].(*ThreeDSTimeoutMethods)
	if !ok {
		t.Fatal("the threeDS member is missing")
	}
	return plugin, methods, pctx, provider
}

func threeDSTimeoutTestTrack(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, request, result any) {
	t.Helper()
	pluginTestRunAfter(t, plugin, pluginTestOutcome(operation, "mock", request, result))
}

func threeDSTimeoutTestList(t *testing.T, store ThreeDSFlowStore) []StartedThreeDSFlow {
	t.Helper()
	flows, err := store.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	return flows
}

// threeDSTimeoutTestStore is a custom store counting what the plugin asks of it.
type threeDSTimeoutTestStore struct {
	mu      sync.Mutex
	flows   map[string]StartedThreeDSFlow
	saves   int
	gets    int
	deletes int
	lists   int
}

func newThreeDSTimeoutTestStore() *threeDSTimeoutTestStore {
	return &threeDSTimeoutTestStore{flows: map[string]StartedThreeDSFlow{}}
}

func (s *threeDSTimeoutTestStore) Save(flow StartedThreeDSFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.flows[flow.PaymentID] = flow
	return nil
}

func (s *threeDSTimeoutTestStore) Get(paymentID string) (*StartedThreeDSFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	flow, ok := s.flows[paymentID]
	if !ok {
		return nil, nil
	}
	return &flow, nil
}

func (s *threeDSTimeoutTestStore) Delete(paymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	delete(s.flows, paymentID)
	return nil
}

func (s *threeDSTimeoutTestStore) List() ([]StartedThreeDSFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	flows := make([]StartedThreeDSFlow, 0, len(s.flows))
	for _, flow := range s.flows {
		flows = append(flows, flow)
	}
	return flows, nil
}

func TestThreeDSTimeoutParsesDurations(t *testing.T) {
	if _, err := ThreeDSTimeout(ThreeDSTimeoutOptions{After: -5}); err == nil ||
		!strings.Contains(err.Error(), "greater than 0") {
		t.Errorf("after -5: error = %v, want 'greater than 0'", err)
	}
	if _, err := ThreeDSTimeout(ThreeDSTimeoutOptions{After: "invalid"}); err == nil ||
		!strings.Contains(err.Error(), "invalid duration 'invalid'") {
		t.Errorf("after 'invalid': error = %v, want \"invalid duration 'invalid'\"", err)
	}
	for _, after := range []any{5000, "15m"} {
		plugin, err := ThreeDSTimeout(ThreeDSTimeoutOptions{After: after})
		if err != nil {
			t.Fatalf("after %v: %v", after, err)
		}
		if plugin.ID != PluginIDThreeDSTimeout {
			t.Errorf("after %v: id = %q, want three-ds-timeout", after, plugin.ID)
		}
	}
}

func TestThreeDSTimeoutTracksStartedFlows(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	plugin, _, _, _ := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{Store: store, After: "10m"})

	request := threeDSTimeoutTestRequest(t, "123456789")
	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, request,
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-INIT"})

	tracked := threeDSTimeoutTestList(t, store)
	if len(tracked) != 1 {
		t.Fatalf("tracked %d flows, want 1", len(tracked))
	}
	flow := tracked[0]
	if flow.PaymentID != "PMT-INIT" {
		t.Errorf("paymentId = %q, want PMT-INIT", flow.PaymentID)
	}
	if flow.ConversationID != "123456789" {
		t.Errorf("conversationId = %q, want 123456789", flow.ConversationID)
	}
	if flow.Operation != core.OperationInitThreeDSPayment {
		t.Errorf("operation = %q, want initThreeDSPayment", flow.Operation)
	}
	if flow.Provider != "mock" {
		t.Errorf("provider = %q, want mock", flow.Provider)
	}
	if flow.StartedAt <= 0 {
		t.Errorf("startedAt = %d, want a positive timestamp", flow.StartedAt)
	}

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSAuthorize,
		threeDSTimeoutTestRequest(t, "conv-auth-123"),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-AUTH"})

	tracked = threeDSTimeoutTestList(t, store)
	if len(tracked) != 2 {
		t.Fatalf("tracked %d flows, want 2", len(tracked))
	}
	found := false
	for _, candidate := range tracked {
		if candidate.ConversationID == "conv-auth-123" {
			found = true
		}
	}
	if !found {
		t.Error("the pre-authorization flow was not tracked")
	}
}

func TestThreeDSTimeoutDoesNotTrackFailedInit(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	plugin, _, _, _ := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{Store: store})

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, ""),
		&core.ThreeDSInitResponse{Status: core.StatusFailure, ErrorMessage: "Invalid card number"})

	if flows := threeDSTimeoutTestList(t, store); len(flows) != 0 {
		t.Errorf("tracked %d flows, want 0", len(flows))
	}
}

func TestThreeDSTimeoutRemovesFlowOnCompletion(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	plugin, _, _, _ := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{Store: store})

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, ""),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-COMPLETE"})
	if flows := threeDSTimeoutTestList(t, store); len(flows) != 1 {
		t.Fatalf("tracked %d flows, want 1", len(flows))
	}

	callback := map[string]any{"paymentId": "PMT-COMPLETE", "status": "success"}
	threeDSTimeoutTestTrack(t, plugin, core.OperationCompleteThreeDSPayment, callback,
		&core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-COMPLETE"})

	if flows := threeDSTimeoutTestList(t, store); len(flows) != 0 {
		t.Errorf("tracked %d flows after completion, want 0", len(flows))
	}
}

func TestThreeDSTimeoutKeepsFreshFlows(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	expired := 0
	plugin, methods, _, _ := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{
		Store:    store,
		After:    "15m",
		OnExpire: func(StartedThreeDSFlow) error { expired++; return nil },
	})
	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, ""),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-FRESH"})

	result, err := methods.ExpireStale(context.Background())
	if err != nil {
		t.Fatalf("expireStale failed: %v", err)
	}
	if result.Checked != 0 {
		t.Errorf("checked = %d, want 0", result.Checked)
	}
	if len(result.Expired) != 0 {
		t.Errorf("expired = %d flows, want 0", len(result.Expired))
	}
	if expired != 0 {
		t.Errorf("onExpire ran %d times, want 0", expired)
	}
	if flows := threeDSTimeoutTestList(t, store); len(flows) != 1 {
		t.Errorf("tracked %d flows, want 1", len(flows))
	}
}

func TestThreeDSTimeoutExpiresAbandonedFlows(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	var expiredFlows []StartedThreeDSFlow
	plugin, methods, _, provider := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{
		Store:    store,
		After:    "15m",
		OnExpire: func(flow StartedThreeDSFlow) error { expiredFlows = append(expiredFlows, flow); return nil },
	})
	provider.payment = &core.PaymentResponse{Status: core.StatusPending, PaymentID: "PMT-STALE"}

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, "ORDER-999"),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-STALE"})
	flow := threeDSTimeoutTestList(t, store)[0]

	result, err := methods.ExpireStale(context.Background(), flow.StartedAt+16*60*1000)
	if err != nil {
		t.Fatalf("expireStale failed: %v", err)
	}
	if result.Checked != 1 {
		t.Errorf("checked = %d, want 1", result.Checked)
	}
	if len(result.Expired) != 1 {
		t.Fatalf("expired = %d flows, want 1", len(result.Expired))
	}
	if result.Expired[0].ConversationID != "ORDER-999" {
		t.Errorf("expired conversationId = %q, want ORDER-999", result.Expired[0].ConversationID)
	}
	if len(expiredFlows) != 1 || expiredFlows[0].ConversationID != "ORDER-999" {
		t.Errorf("onExpire flows = %v, want one ORDER-999 flow", expiredFlows)
	}
	if flows := threeDSTimeoutTestList(t, store); len(flows) != 0 {
		t.Errorf("tracked %d flows after expiry, want 0", len(flows))
	}
}

func TestThreeDSTimeoutKeepsLateSuccess(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	expired := 0
	plugin, methods, _, provider := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{
		Store:         store,
		After:         "15m",
		OnExpire:      func(StartedThreeDSFlow) error { expired++; return nil },
		CheckProvider: pluginTestPtr(true),
	})
	provider.payment = &core.PaymentResponse{Status: core.StatusSuccess, PaymentID: "PMT-RESOLVED"}

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, "ORDER-LATE-OK"),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-RESOLVED"})
	flow := threeDSTimeoutTestList(t, store)[0]

	result, err := methods.ExpireStale(context.Background(), flow.StartedAt+20*60*1000)
	if err != nil {
		t.Fatalf("expireStale failed: %v", err)
	}
	if result.Checked != 1 || result.Resolved != 1 {
		t.Errorf("checked = %d, resolved = %d; want 1 and 1", result.Checked, result.Resolved)
	}
	if len(result.Expired) != 0 {
		t.Errorf("expired = %d flows, want 0", len(result.Expired))
	}
	if expired != 0 {
		t.Errorf("onExpire ran %d times, want 0", expired)
	}
	if flows := threeDSTimeoutTestList(t, store); len(flows) != 0 {
		t.Errorf("tracked %d flows, want 0", len(flows))
	}
}

func TestThreeDSTimeoutClearsProviderFailure(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	expired := 0
	plugin, methods, _, provider := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{
		Store:         store,
		After:         "15m",
		OnExpire:      func(StartedThreeDSFlow) error { expired++; return nil },
		CheckProvider: pluginTestPtr(true),
	})
	provider.payment = &core.PaymentResponse{Status: core.StatusFailure, PaymentID: "PMT-FAILED"}

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, "ORDER-FAILED"),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-FAILED"})
	flow := threeDSTimeoutTestList(t, store)[0]

	result, err := methods.ExpireStale(context.Background(), flow.StartedAt+20*60*1000)
	if err != nil {
		t.Fatalf("expireStale failed: %v", err)
	}
	if result.Checked != 1 {
		t.Errorf("checked = %d, want 1", result.Checked)
	}
	if len(result.Expired) != 0 {
		t.Errorf("expired = %d flows, want 0", len(result.Expired))
	}
	if expired != 0 {
		t.Errorf("onExpire ran %d times, want 0", expired)
	}
	if flows := threeDSTimeoutTestList(t, store); len(flows) != 0 {
		t.Errorf("tracked %d flows, want 0", len(flows))
	}
}

func TestThreeDSTimeoutSupportsCustomStore(t *testing.T) {
	store := newThreeDSTimeoutTestStore()
	plugin, _, _, _ := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{Store: store})

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, ""),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-CUSTOM"})

	if store.saves != 1 {
		t.Errorf("save was called %d times, want 1", store.saves)
	}
	if len(store.flows) != 1 {
		t.Errorf("the custom store holds %d flows, want 1", len(store.flows))
	}
}

func TestThreeDSTimeoutExpiresWhenProviderLookupFails(t *testing.T) {
	store := NewMemoryThreeDSFlowStore()
	expired := 0
	plugin, methods, _, provider := threeDSTimeoutTestPlugin(t, ThreeDSTimeoutOptions{
		Store:    store,
		After:    "10m",
		OnExpire: func(StartedThreeDSFlow) error { expired++; return nil },
	})
	provider.getError = errors.New("Network timeout to bank")

	threeDSTimeoutTestTrack(t, plugin, core.OperationInitThreeDSPayment, threeDSTimeoutTestRequest(t, ""),
		&core.ThreeDSInitResponse{Status: core.StatusPending, PaymentID: "PMT-UNREACHABLE"})
	flow := threeDSTimeoutTestList(t, store)[0]

	result, err := methods.ExpireStale(context.Background(), flow.StartedAt+15*60*1000)
	if err != nil {
		t.Fatalf("expireStale failed: %v", err)
	}
	if result.Checked != 1 {
		t.Errorf("checked = %d, want 1", result.Checked)
	}
	if len(result.Expired) != 1 {
		t.Errorf("expired = %d flows, want 1", len(result.Expired))
	}
	if expired != 1 {
		t.Errorf("onExpire ran %d times, want 1", expired)
	}
}

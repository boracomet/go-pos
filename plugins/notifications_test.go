package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

const (
	notificationsTestSlackURL   = "https://hooks.slack.com/services/T/B/X"
	notificationsTestDiscordURL = "https://discord.com/api/webhooks/1/abc"
)

// notificationsTestSend records what a custom channel receives.
type notificationsTestSend struct {
	mu       sync.Mutex
	messages []string
	events   []NotificationEvent
	err      error
	release  chan struct{}
	started  chan struct{}
}

func (s *notificationsTestSend) send(message string, event NotificationEvent) error {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	s.mu.Lock()
	s.messages = append(s.messages, message)
	s.events = append(s.events, event)
	failure := s.err
	s.mu.Unlock()
	if s.release != nil {
		<-s.release
	}
	return failure
}

func (s *notificationsTestSend) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *notificationsTestSend) messageAt(index int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messages[index]
}

func (s *notificationsTestSend) eventAt(index int) NotificationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[index]
}

func notificationsTestSetup(t *testing.T, options NotificationsOptions) (core.Plugin, *NotificationsMethods, *pluginTestContext) {
	t.Helper()
	plugin, err := Notifications(options)
	if err != nil {
		t.Fatalf("notifications() failed: %v", err)
	}
	pctx := newPluginTestContext()
	pctx.logger = &pluginTestLogger{}
	if err := pluginTestInit(plugin, pctx); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	methods, ok := pluginTestMethods(t, plugin, pctx)["notifications"].(*NotificationsMethods)
	if !ok {
		t.Fatal("the notifications member is missing")
	}
	return plugin, methods, pctx
}

// notificationsTestEmit hands the event an operation result triggers to the
// plugin's listener, the way the facade does.
func notificationsTestEmit(t *testing.T, plugin core.Plugin, operation core.PaymentOperation, request, result any) {
	t.Helper()
	event := core.EventFor(operation, "mock", request, result)
	if event == nil {
		t.Fatalf("%s produced no event", operation)
	}
	listener, ok := plugin.Events[core.EventAll]
	if !ok {
		t.Fatal("the notifications plugin listens to no events")
	}
	if err := listener(context.Background(), event); err != nil {
		t.Fatalf("the event listener failed: %v", err)
	}
}

func notificationsTestCreate(t *testing.T, plugin core.Plugin, provider *pluginTestProvider, request *core.PaymentRequest) *core.PaymentResponse {
	t.Helper()
	result, err := provider.CreatePayment(context.Background(), request)
	if err != nil {
		t.Fatalf("createPayment failed: %v", err)
	}
	notificationsTestEmit(t, plugin, core.OperationCreatePayment, request, result)
	return result
}

func notificationsTestFlush(t *testing.T, methods *NotificationsMethods) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := methods.Flush(ctx); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
}

// notificationsTestBodies maps each posted URL to its decoded body. Channels are
// delivered concurrently, so the order of the posts is not fixed.
func notificationsTestBodies(t *testing.T, doer *pluginTestDoer) map[string]map[string]any {
	t.Helper()
	bodies := map[string]map[string]any{}
	for i := 0; i < doer.count(); i++ {
		var body map[string]any
		if err := json.Unmarshal([]byte(doer.bodyAt(i)), &body); err != nil {
			t.Fatalf("body %d is not JSON: %v", i, err)
		}
		bodies[doer.requestAt(i).URL.String()] = body
	}
	return bodies
}

func notificationsTestDeclinedProvider() *pluginTestProvider {
	provider := newPluginTestProvider("mock")
	provider.payment = &core.PaymentResponse{
		Status:       core.StatusFailure,
		Code:         core.CodeInsufficientFunds,
		ErrorCode:    string(core.CodeInsufficientFunds),
		ErrorMessage: "Insufficient funds",
	}
	return provider
}

func notificationsTestRefundRequest(paymentID string) *core.RefundRequest {
	return &core.RefundRequest{
		PaymentID:      paymentID,
		Price:          "0.5",
		Currency:       string(core.CurrencyTRY),
		IP:             "85.34.78.112",
		ConversationID: "123456789",
	}
}

func TestNotificationsSendsReadableMessagesToSlackDiscordAndTelegram(t *testing.T) {
	doer := &pluginTestDoer{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Doer: doer,
		Channels: []NotificationChannel{
			{Type: NotificationChannelSlack, WebhookURL: notificationsTestSlackURL},
			{Type: NotificationChannelDiscord, WebhookURL: notificationsTestDiscordURL},
			{Type: NotificationChannelTelegram, BotToken: "123:abc", ChatID: -100},
		},
	})
	provider := newPluginTestProvider("mock")

	result := notificationsTestCreate(t, plugin, provider, pluginTestPaymentRequest())
	notificationsTestFlush(t, methods)

	text := "✅ Payment received: 1.2 TRY (mock)\nPayment: " + result.PaymentID + " · Order: 123456789"
	bodies := notificationsTestBodies(t, doer)
	if len(bodies) != 3 {
		t.Fatalf("posted to %d urls, want 3", len(bodies))
	}
	if got := bodies[notificationsTestSlackURL]["text"]; got != text {
		t.Errorf("slack text = %v, want %q", got, text)
	}
	discord := bodies[notificationsTestDiscordURL]
	if got := discord["content"]; got != text {
		t.Errorf("discord content = %v, want %q", got, text)
	}
	mentions, ok := discord["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatalf("discord allowed_mentions = %v, want an object", discord["allowed_mentions"])
	}
	parse, ok := mentions["parse"].([]any)
	if !ok || len(parse) != 0 {
		t.Errorf("discord allowed_mentions.parse = %v, want an empty list", mentions["parse"])
	}
	telegram := bodies["https://api.telegram.org/bot123:abc/sendMessage"]
	if telegram == nil {
		t.Fatal("nothing was posted to telegram")
	}
	if got := telegram["text"]; got != text {
		t.Errorf("telegram text = %v, want %q", got, text)
	}
	if got := telegram["chat_id"]; got != float64(-100) {
		t.Errorf("telegram chat_id = %v, want -100", got)
	}
}

func TestNotificationsNeverPutsCardDataInMessagesOrEvents(t *testing.T) {
	doer := &pluginTestDoer{}
	send := &notificationsTestSend{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Doer: doer,
		Channels: []NotificationChannel{
			{Type: NotificationChannelSlack, WebhookURL: notificationsTestSlackURL},
			{Type: NotificationChannelCustom, Send: send.send},
		},
	})
	request := pluginTestPaymentRequest()
	notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), request)
	notificationsTestFlush(t, methods)

	card := request.PaymentCard.CardNumber
	for i := 0; i < doer.count(); i++ {
		if strings.Contains(doer.bodyAt(i), card) {
			t.Errorf("post %d carries the card number", i)
		}
	}
	if send.count() != 1 {
		t.Fatalf("the custom channel received %d messages, want 1", send.count())
	}
	if strings.Contains(send.messageAt(0), card) {
		t.Error("the message carries the card number")
	}
	event := send.eventAt(0)
	fields := pluginTestJSON(t, event)
	if _, ok := fields["request"]; ok {
		t.Error("the event carries the request")
	}
	if _, ok := fields["result"]; ok {
		t.Error("the event carries the result")
	}
	if event.Type != core.EventPaymentSucceeded || event.Provider != "mock" || event.Amount != "1.2" {
		t.Errorf("event = %+v, want payment.succeeded from mock for 1.2", event)
	}
}

func TestNotificationsSendsEachChannelOnlyItsEvents(t *testing.T) {
	sales, failures, refunds := &notificationsTestSend{}, &notificationsTestSend{}, &notificationsTestSend{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Channels: []NotificationChannel{
			{Type: NotificationChannelCustom, Send: sales.send, Events: []core.PaymentEventName{core.EventPaymentSucceeded}},
			{Type: NotificationChannelCustom, Send: failures.send, Events: []core.PaymentEventName{core.EventPaymentFailed, core.EventRefundFailed}},
			{Type: NotificationChannelCustom, Send: refunds.send, Events: []core.PaymentEventName{core.EventRefundSucceeded}},
		},
	})

	paid := notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	declined := pluginTestPaymentRequest()
	declined.PaymentCard.CardNumber = "4000000000009995"
	notificationsTestCreate(t, plugin, notificationsTestDeclinedProvider(), declined)

	refundRequest := notificationsTestRefundRequest(paid.PaymentID)
	refund, err := newPluginTestProvider("mock").Refund(context.Background(), refundRequest)
	if err != nil {
		t.Fatalf("refund failed: %v", err)
	}
	notificationsTestEmit(t, plugin, core.OperationRefund, refundRequest, refund)
	notificationsTestFlush(t, methods)

	if sales.count() != 1 {
		t.Errorf("sales received %d messages, want 1", sales.count())
	}
	if failures.count() != 1 {
		t.Fatalf("failures received %d messages, want 1", failures.count())
	}
	if !strings.Contains(failures.messageAt(0), "Error: INSUFFICIENT_FUNDS") {
		t.Errorf("failure message = %q, want it to carry the error code", failures.messageAt(0))
	}
	if refunds.count() != 1 {
		t.Fatalf("refunds received %d messages, want 1", refunds.count())
	}
	if !regexp.MustCompile(`^↩️ Refund completed: 0\.5 TRY \(mock\)`).MatchString(refunds.messageAt(0)) {
		t.Errorf("refund message = %q", refunds.messageAt(0))
	}
}

func TestNotificationsSkipsSmallAmountsAndFilteredEvents(t *testing.T) {
	send := &notificationsTestSend{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		MinAmount: pluginTestPtr(1.0),
		Filter:    func(event NotificationEvent) bool { return event.ConversationID != "skip" },
		Channels:  []NotificationChannel{{Type: NotificationChannelCustom, Send: send.send}},
	})
	provider := newPluginTestProvider("mock")

	notificationsTestCreate(t, plugin, provider, pluginTestPaymentRequest())
	small := pluginTestPaymentRequest()
	small.Price, small.PaidPrice = "0.5", "0.5"
	notificationsTestCreate(t, plugin, provider, small)
	skipped := pluginTestPaymentRequest()
	skipped.ConversationID = "skip"
	notificationsTestCreate(t, plugin, provider, skipped)
	notificationsTestFlush(t, methods)

	if send.count() != 1 {
		t.Errorf("the channel received %d messages, want 1", send.count())
	}
}

func TestNotificationsNeverFailsThePaymentWhenAChannelFails(t *testing.T) {
	doer := &pluginTestDoer{status: 500}
	reached := &notificationsTestSend{}
	var mu sync.Mutex
	reported := [][2]string{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Doer: doer,
		OnError: func(err error, channel string, _ NotificationEvent) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, [2]string{channel, err.Error()})
		},
		Channels: []NotificationChannel{
			{Type: NotificationChannelSlack, WebhookURL: notificationsTestSlackURL},
			{Type: NotificationChannelCustom, Name: "email", Send: func(string, NotificationEvent) error {
				return errors.New("SMTP down")
			}},
			{Type: NotificationChannelCustom, Send: reached.send},
		},
	})

	result := notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	notificationsTestFlush(t, methods)

	if result.Status != core.StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if doer.count() != 1 {
		t.Errorf("posted %d times, want 1", doer.count())
	}
	if reached.count() != 1 {
		t.Errorf("the reachable channel got %d messages, want 1", reached.count())
	}
	mu.Lock()
	pairs := append([][2]string{}, reported...)
	mu.Unlock()
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	want := [][2]string{{"email", "SMTP down"}, {"slack", "HTTP 500"}}
	if len(pairs) != len(want) {
		t.Fatalf("onError reported %v, want %v", pairs, want)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Errorf("onError %d = %v, want %v", i, pairs[i], want[i])
		}
	}

	broken, brokenMethods, _ := notificationsTestSetup(t, NotificationsOptions{
		Format:  func(NotificationEvent) string { panic("bad format") },
		OnError: func(error, string, NotificationEvent) { panic("bad onError") },
		Channels: []NotificationChannel{
			{Type: NotificationChannelCustom, Send: reached.send},
		},
	})
	if status := notificationsTestCreate(t, broken, newPluginTestProvider("mock"), pluginTestPaymentRequest()).Status; status != core.StatusSuccess {
		t.Errorf("broken status = %q, want success", status)
	}
	notificationsTestFlush(t, brokenMethods)
}

func TestNotificationsLogsFailuresWithoutOnError(t *testing.T) {
	doer := &pluginTestDoer{status: 404}
	plugin, methods, pctx := notificationsTestSetup(t, NotificationsOptions{
		Doer:     doer,
		Channels: []NotificationChannel{{Type: NotificationChannelDiscord, WebhookURL: notificationsTestDiscordURL}},
	})
	notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	notificationsTestFlush(t, methods)

	logger := pctx.logger.(*pluginTestLogger)
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Level != "error" || entry.Message != "notifications: the discord channel failed" {
		t.Errorf("log entry = %+v", entry)
	}
	if entry.Err == nil || entry.Err.Error() != "HTTP 404" {
		t.Errorf("logged error = %v, want HTTP 404", entry.Err)
	}
	if entry.Meta["event"] != core.EventPaymentSucceeded {
		t.Errorf("logged meta = %v, want the event type", entry.Meta)
	}
}

func TestNotificationsSendsInBackgroundAndHandsWorkToWaitUntil(t *testing.T) {
	send := &notificationsTestSend{release: make(chan struct{}), started: make(chan struct{}, 1)}
	var waits []<-chan struct{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		WaitUntil: func(done <-chan struct{}) { waits = append(waits, done) },
		Channels:  []NotificationChannel{{Type: NotificationChannelCustom, Send: send.send}},
	})

	notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	select {
	case <-send.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the message was never handed to the channel")
	}
	if send.count() != 1 {
		t.Errorf("the channel received %d messages, want 1", send.count())
	}
	if len(waits) != 1 || waits[0] == nil {
		t.Fatalf("waitUntil received %v, want one pending task", waits)
	}

	flushed := make(chan struct{})
	go func() {
		_ = methods.Flush(context.Background())
		close(flushed)
	}()
	select {
	case <-flushed:
		t.Fatal("flush returned while the message was still being sent")
	case <-time.After(20 * time.Millisecond):
	}
	close(send.release)
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not return once the message was sent")
	}
}

func TestNotificationsFormatsInEveryLanguageEscapedForSlack(t *testing.T) {
	doer := &pluginTestDoer{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Doer:     doer,
		Locale:   NotificationLocaleTR,
		Channels: []NotificationChannel{{Type: NotificationChannelSlack, WebhookURL: notificationsTestSlackURL}},
	})
	request := pluginTestPaymentRequest()
	request.ConversationID = "<@here> & co"
	notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), request)
	notificationsTestFlush(t, methods)

	text, _ := notificationsTestBodies(t, doer)[notificationsTestSlackURL]["text"].(string)
	pattern := regexp.MustCompile(`^✅ Ödeme alındı: 1\.2 TRY \(mock\)\nÖdeme: .+ · Sipariş: &lt;@here&gt; &amp; co$`)
	if !pattern.MatchString(text) {
		t.Errorf("slack text = %q", text)
	}

	event := NotificationEvent{
		Type:      core.EventPaymentFailed,
		Provider:  "iyzico",
		Operation: core.OperationCreatePayment,
	}
	cases := []struct {
		locale NotificationLocale
		want   string
	}{
		{"", "❌ Payment failed (iyzico)"},
		{NotificationLocaleDE, "❌ Zahlung fehlgeschlagen (iyzico)"},
		{NotificationLocaleRU, "❌ Платёж не прошёл (iyzico)"},
		{NotificationLocaleAR, "❌ فشلت الدفعة (iyzico)"},
	}
	for _, testCase := range cases {
		var got string
		if testCase.locale == "" {
			got = FormatNotification(event)
		} else {
			got = FormatNotification(event, testCase.locale)
		}
		if got != testCase.want {
			t.Errorf("formatNotification(%q) = %q, want %q", testCase.locale, got, testCase.want)
		}
	}
}

func TestNotificationsUsesACustomFormat(t *testing.T) {
	send := &notificationsTestSend{}
	plugin, methods, _ := notificationsTestSetup(t, NotificationsOptions{
		Format: func(event NotificationEvent) string {
			return string(event.Type) + ": " + event.Amount + " " + event.Currency + " via " + event.Provider
		},
		Channels: []NotificationChannel{{Type: NotificationChannelCustom, Send: send.send}},
	})
	notificationsTestCreate(t, plugin, newPluginTestProvider("mock"), pluginTestPaymentRequest())
	notificationsTestFlush(t, methods)

	if send.count() != 1 {
		t.Fatalf("the channel received %d messages, want 1", send.count())
	}
	if got := send.messageAt(0); got != "payment.succeeded: 1.2 TRY via mock" {
		t.Errorf("message = %q", got)
	}
}

func TestNotificationsRejectsIncompleteChannelsAndUnknownLanguages(t *testing.T) {
	if _, err := Notifications(NotificationsOptions{
		Channels: []NotificationChannel{{Type: NotificationChannelSlack, WebhookURL: ""}},
	}); err != nil {
		if _, ok := err.(*core.ConfigurationError); !ok {
			t.Errorf("error is %T, want *core.ConfigurationError", err)
		}
	} else {
		t.Error("an empty slack webhook url was accepted")
	}

	_, err := Notifications(NotificationsOptions{
		Channels: []NotificationChannel{{Type: NotificationChannelTelegram, BotToken: "x", ChatID: ""}},
	})
	if err == nil || !strings.Contains(err.Error(), "the telegram channel is incomplete") {
		t.Errorf("telegram error = %v", err)
	}

	_, err = Notifications(NotificationsOptions{
		Locale:   NotificationLocale("fr"),
		Channels: []NotificationChannel{{Type: NotificationChannelCustom, Send: func(string, NotificationEvent) error { return nil }}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown locale 'fr'") {
		t.Errorf("locale error = %v", err)
	}
}

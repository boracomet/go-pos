package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/boracomet/go-pos/core"
)

// NotificationEvent is a payment event as notifications see it: without the
// request and the result, so card data can never end up in a message.
type NotificationEvent struct {
	Type           core.PaymentEventType `json:"type"`
	Provider       string                `json:"provider"`
	Operation      core.PaymentOperation `json:"operation"`
	PaymentID      string                `json:"paymentId,omitempty"`
	ConversationID string                `json:"conversationId,omitempty"`
	Amount         string                `json:"amount,omitempty"`
	Currency       string                `json:"currency,omitempty"`
	Code           core.PaymentErrorCode `json:"code,omitempty"`
	ResolvedFrom   string                `json:"resolvedFrom,omitempty"`
}

// The notification channel types.
const (
	NotificationChannelSlack    = "slack"
	NotificationChannelDiscord  = "discord"
	NotificationChannelTelegram = "telegram"
	NotificationChannelCustom   = "custom"
)

// NotificationChannel delivers messages to one destination. Type selects which
// of the remaining fields are used.
type NotificationChannel struct {
	// Type is "slack", "discord", "telegram" or "custom".
	Type string
	// Events are the events sent to this channel. Default: all.
	Events []core.PaymentEventName
	// WebhookURL is the incoming webhook URL of a Slack or Discord channel.
	WebhookURL string
	// BotToken is the Telegram bot token.
	BotToken string
	// ChatID is the Telegram chat, group or channel id, as a string or a number.
	ChatID any
	// Name is the custom channel's name for logs and OnError. Default: "custom".
	Name string
	// Send delivers the message anywhere: email, SMS, a queue.
	Send func(message string, event NotificationEvent) error
}

// NotificationLocale is a language of the default messages.
type NotificationLocale string

// The languages the default messages are translated into.
const (
	NotificationLocaleEN NotificationLocale = "en"
	NotificationLocaleTR NotificationLocale = "tr"
	NotificationLocaleDE NotificationLocale = "de"
	NotificationLocaleRU NotificationLocale = "ru"
	NotificationLocaleAR NotificationLocale = "ar"
)

// NotificationsOptions configures the notifications plugin.
type NotificationsOptions struct {
	// Channels are the destinations messages are sent to.
	Channels []NotificationChannel
	// Locale is the language of the default messages. Default: "en".
	Locale NotificationLocale
	// Format builds the message text instead of the default one.
	Format func(event NotificationEvent) string
	// MinAmount skips events with an amount below this (compared as a number, in
	// any currency). Events without an amount are always sent.
	MinAmount *float64
	// Filter returns false to skip an event.
	Filter func(event NotificationEvent) bool
	// WaitUntil keeps the runtime alive until the messages are sent, on
	// serverless platforms. It receives the channel closed when the work is done.
	WaitUntil func(done <-chan struct{})
	// OnError is called when a channel fails. Default: the payment logger's Error.
	OnError func(err error, channel string, event NotificationEvent)
	// TimeoutMs gives up on a channel after this many milliseconds. Default: 10000.
	TimeoutMs int
	// Doer is the transport used for Slack, Discord and Telegram.
	// Default: http.DefaultClient.
	Doer core.Doer
}

// notificationsLabels holds, per language: succeeded, authorized, pending,
// failed, cancelled, refunded, refundFailed, payment, order, error.
var notificationsLabels = map[NotificationLocale][10]string{
	NotificationLocaleEN: {
		"Payment received",
		"Payment authorized",
		"Payment pending",
		"Payment failed",
		"Payment cancelled",
		"Refund completed",
		"Refund failed",
		"Payment",
		"Order",
		"Error",
	},
	NotificationLocaleTR: {
		"Ödeme alındı",
		"Ödeme provizyonu alındı",
		"Ödeme beklemede",
		"Ödeme başarısız",
		"Ödeme iptal edildi",
		"İade tamamlandı",
		"İade başarısız",
		"Ödeme",
		"Sipariş",
		"Hata",
	},
	NotificationLocaleDE: {
		"Zahlung eingegangen",
		"Zahlung autorisiert",
		"Zahlung ausstehend",
		"Zahlung fehlgeschlagen",
		"Zahlung storniert",
		"Erstattung abgeschlossen",
		"Erstattung fehlgeschlagen",
		"Zahlung",
		"Bestellung",
		"Fehler",
	},
	NotificationLocaleRU: {
		"Платёж получен",
		"Платёж авторизован",
		"Платёж в ожидании",
		"Платёж не прошёл",
		"Платёж отменён",
		"Возврат выполнен",
		"Возврат не выполнен",
		"Платёж",
		"Заказ",
		"Ошибка",
	},
	NotificationLocaleAR: {
		"تم استلام الدفعة",
		"تم تفويض الدفعة",
		"الدفعة قيد الانتظار",
		"فشلت الدفعة",
		"تم إلغاء الدفعة",
		"تم الاسترداد",
		"فشل الاسترداد",
		"الدفعة",
		"الطلب",
		"الخطأ",
	},
}

var notificationsLocaleOrder = []NotificationLocale{
	NotificationLocaleEN,
	NotificationLocaleTR,
	NotificationLocaleDE,
	NotificationLocaleRU,
	NotificationLocaleAR,
}

var notificationsTypes = []core.PaymentEventType{
	core.EventPaymentSucceeded,
	core.EventPaymentAuthorized,
	core.EventPaymentPending,
	core.EventPaymentFailed,
	core.EventPaymentCancelled,
	core.EventRefundSucceeded,
	core.EventRefundFailed,
}

var notificationsIcons = []string{"✅", "🔒", "⏳", "❌", "🚫", "↩️", "⚠️"}

// FormatNotification builds the default message:
// "✅ Payment received: 150.00 TRY (iyzico)" followed by the ids. An event type
// outside the known outcomes has no icon and no label.
func FormatNotification(event NotificationEvent, locale ...NotificationLocale) string {
	language := NotificationLocaleEN
	if len(locale) > 0 && locale[0] != "" {
		language = locale[0]
	}
	labels, known := notificationsLabels[language]
	if !known {
		labels = notificationsLabels[NotificationLocaleEN]
	}
	index := -1
	for i, eventType := range notificationsTypes {
		if eventType == event.Type {
			index = i
			break
		}
	}
	icon, label := "", ""
	if index >= 0 {
		icon, label = notificationsIcons[index], labels[index]
	}
	amount := ""
	if event.Amount != "" {
		amount = strings.TrimRightFunc(fmt.Sprintf(": %s %s", event.Amount, event.Currency), unicode.IsSpace)
	}
	details := make([]string, 0, 3)
	if event.PaymentID != "" {
		details = append(details, fmt.Sprintf("%s: %s", labels[7], event.PaymentID))
	}
	if event.ConversationID != "" {
		details = append(details, fmt.Sprintf("%s: %s", labels[8], event.ConversationID))
	}
	if event.Code != "" {
		details = append(details, fmt.Sprintf("%s: %s", labels[9], event.Code))
	}
	head := fmt.Sprintf("%s %s%s (%s)", icon, label, amount, event.Provider)
	if len(details) == 0 {
		return head
	}
	return head + "\n" + strings.Join(details, " · ")
}

// notificationsSlackEscape escapes the characters Slack reads as markup.
func notificationsSlackEscape(text string) string {
	replaced := strings.ReplaceAll(text, "&", "&amp;")
	replaced = strings.ReplaceAll(replaced, "<", "&lt;")
	return strings.ReplaceAll(replaced, ">", "&gt;")
}

func notificationsAmount(text string) (float64, bool) {
	amount, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, false
	}
	return amount, true
}

func notificationsEventOf(event *core.PaymentEvent) NotificationEvent {
	if event == nil {
		return NotificationEvent{}
	}
	return NotificationEvent{
		Type:           event.Type,
		Provider:       event.Provider,
		Operation:      event.Operation,
		PaymentID:      event.PaymentID,
		ConversationID: event.ConversationID,
		Amount:         event.Amount,
		Currency:       event.Currency,
		Code:           event.Code,
		ResolvedFrom:   event.ResolvedFrom,
	}
}

type notificationsRuntime struct {
	options NotificationsOptions
	locale  NotificationLocale
	timeout time.Duration
	doer    core.Doer
	group   pluginTaskGroup

	mu     sync.Mutex
	logger core.Logger
}

func (r *notificationsRuntime) setLogger(logger core.Logger) {
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
}

func (r *notificationsRuntime) log() core.Logger {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logger
}

// post carries its own deadline, so a message outlives the request that triggered it.
func (r *notificationsRuntime) post(url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	response, err := r.doer.Do(request)
	if err != nil {
		return err
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func (r *notificationsRuntime) report(err error, name string, event NotificationEvent) {
	// A failing OnError must not fail the payment either.
	_ = pluginCall(func() error {
		if r.options.OnError != nil {
			r.options.OnError(err, name, event)
			return nil
		}
		if logger := r.log(); logger != nil {
			logger.Error(
				fmt.Sprintf("notifications: the %s channel failed", name),
				err,
				map[string]any{"event": string(event.Type), "paymentId": event.PaymentID},
			)
		}
		return nil
	})
}

func (r *notificationsRuntime) deliver(channel NotificationChannel, message string, event NotificationEvent) error {
	switch channel.Type {
	case NotificationChannelSlack:
		return r.post(channel.WebhookURL, map[string]any{"text": notificationsSlackEscape(message)})
	case NotificationChannelDiscord:
		// No @everyone or role pings from order ids.
		return r.post(channel.WebhookURL, map[string]any{
			"content":          message,
			"allowed_mentions": map[string]any{"parse": []string{}},
		})
	case NotificationChannelTelegram:
		return r.post("https://api.telegram.org/bot"+channel.BotToken+"/sendMessage", map[string]any{
			"chat_id": channel.ChatID,
			"text":    message,
		})
	case NotificationChannelCustom:
		return channel.Send(message, event)
	default:
		return nil
	}
}

func (r *notificationsRuntime) notify(event NotificationEvent) error {
	if r.options.MinAmount != nil && event.Amount != "" {
		if amount, ok := notificationsAmount(event.Amount); ok && amount < *r.options.MinAmount {
			return nil
		}
	}
	if r.options.Filter != nil && !r.options.Filter(event) {
		return nil
	}
	channels := make([]NotificationChannel, 0, len(r.options.Channels))
	for _, channel := range r.options.Channels {
		events := channel.Events
		if len(events) == 0 {
			events = []core.PaymentEventName{core.EventAll}
		}
		for _, name := range events {
			if name == core.EventAll || string(name) == string(event.Type) {
				channels = append(channels, channel)
				break
			}
		}
	}
	if len(channels) == 0 {
		return nil
	}

	message := FormatNotification(event, r.locale)
	if r.options.Format != nil {
		message = r.options.Format(event)
	}

	var waiting sync.WaitGroup
	for _, channel := range channels {
		waiting.Add(1)
		go func(channel NotificationChannel) {
			defer waiting.Done()
			name := channel.Type
			if channel.Type == NotificationChannelCustom {
				name = channel.Name
				if name == "" {
					name = NotificationChannelCustom
				}
			}
			if err := pluginCall(func() error { return r.deliver(channel, message, event) }); err != nil {
				r.report(err, name, event)
			}
		}(channel)
	}
	waiting.Wait()
	return nil
}

// NotificationsMethods is the "notifications" member the plugin adds to the facade.
type NotificationsMethods struct {
	runtime *notificationsRuntime
}

// Flush waits until every message started so far is sent or failed.
func (m *NotificationsMethods) Flush(ctx context.Context) error {
	return m.runtime.group.flush(ctx)
}

// Notifications sends payment events as readable messages to Slack, Discord,
// Telegram or a custom channel (email, SMS...). Messages are sent in the
// background: a failing channel is reported to OnError and never affects the
// payment.
func Notifications(options NotificationsOptions) (core.Plugin, error) {
	locale := options.Locale
	if locale == "" {
		locale = NotificationLocaleEN
	}
	if _, known := notificationsLabels[locale]; !known {
		names := make([]string, 0, len(notificationsLocaleOrder))
		for _, name := range notificationsLocaleOrder {
			names = append(names, string(name))
		}
		return core.Plugin{}, core.NewConfigurationError(
			fmt.Sprintf("notifications: unknown locale '%s'. Use %s or format.", locale, strings.Join(names, ", ")), "")
	}
	for _, channel := range options.Channels {
		missing := false
		switch channel.Type {
		case NotificationChannelTelegram:
			missing = channel.BotToken == "" || channel.ChatID == nil || channel.ChatID == ""
		case NotificationChannelCustom:
			missing = channel.Send == nil
		default:
			missing = channel.WebhookURL == ""
		}
		if missing {
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("notifications: the %s channel is incomplete", channel.Type), "")
		}
	}
	timeoutMs := options.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 10000
	}
	runtime := &notificationsRuntime{
		options: options,
		locale:  locale,
		timeout: time.Duration(timeoutMs) * time.Millisecond,
		doer:    pluginDoer(options.Doer),
	}

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDNotifications,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			runtime.setLogger(pctx.Logger())
			return nil
		},
		Events: core.PaymentEventListeners{
			core.EventAll: func(_ context.Context, event *core.PaymentEvent) error {
				notification := notificationsEventOf(event)
				done := runtime.group.start(func() {
					if err := pluginCall(func() error { return runtime.notify(notification) }); err != nil {
						runtime.report(err, "format", notification)
					}
				})
				if options.WaitUntil != nil {
					options.WaitUntil(done)
				}
				return nil
			},
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"notifications": &NotificationsMethods{runtime: runtime}}, nil
		},
	}), nil
}

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// FraudVerdict is what a trigger does with the payment.
type FraudVerdict string

const (
	// FraudVerdictBlock refuses the payment before the provider is called.
	FraudVerdictBlock FraudVerdict = "block"
	// FraudVerdictThreeDS asks for 3D Secure verification.
	FraudVerdictThreeDS FraudVerdict = "threeDS"
	// FraudVerdictFlag lets the payment through and reports it.
	FraudVerdictFlag FraudVerdict = "flag"
)

// FraudOutcome is how a trigger ended: refused, allowed and marked, or cleared.
type FraudOutcome string

const (
	// FraudOutcomeBlocked means the payment was refused.
	FraudOutcomeBlocked FraudOutcome = "blocked"
	// FraudOutcomeFlagged means the payment was allowed and marked.
	FraudOutcomeFlagged FraudOutcome = "flagged"
	// FraudOutcomeAllowed means the challenge cleared it, or it already was 3D Secure.
	FraudOutcomeAllowed FraudOutcome = "allowed"
)

// FraudKeyKind is how attempts are grouped. A value is always hashed with the
// plugin salt before it reaches the store, so a card number never becomes a key.
type FraudKeyKind string

const (
	// FraudKeyIP groups by the visitor address.
	FraudKeyIP FraudKeyKind = "ip"
	// FraudKeyEmail groups by the buyer email.
	FraudKeyEmail FraudKeyKind = "email"
	// FraudKeyBuyer groups by the buyer id.
	FraudKeyBuyer FraudKeyKind = "buyer"
	// FraudKeyBin groups by the first 6 digits of the card.
	FraudKeyBin FraudKeyKind = "bin"
	// FraudKeyPrefix groups by the first 8 digits of the card.
	FraudKeyPrefix FraudKeyKind = "prefix"
	// FraudKeyCard groups by the whole card number, hashed.
	FraudKeyCard FraudKeyKind = "card"
)

// FraudMemberKind is a grouping key kind, or FraudMemberAmount, for counting
// distinct values inside a window.
type FraudMemberKind = FraudKeyKind

// FraudMemberAmount counts distinct amounts instead of distinct identities.
const FraudMemberAmount FraudMemberKind = "amount"

// FraudVelocityEvent selects what a velocity rule counts.
type FraudVelocityEvent string

const (
	// FraudVelocityAttempt counts every attempt as it is made, blocked or not.
	FraudVelocityAttempt FraudVelocityEvent = "attempt"
	// FraudVelocityDeclined counts only attempts the provider declined.
	FraudVelocityDeclined FraudVelocityEvent = "declined"
)

// FraudContext is one derived attempt: only data that is safe to log, never a
// PAN, CVC or expiry.
type FraudContext struct {
	Operation core.PaymentOperation
	Provider  string
	// Request is the operation request. Read it in rules; never store or log it.
	Request any
	// Bin is the first 6 digits of the card, when the request carries one.
	Bin string
	// Prefix is the first 8 digits of the card.
	Prefix string
	// Last4 is the last 4 digits of the card.
	Last4 string
	// MaskedCard is the BIN plus the last four, e.g. "552879******0008": the
	// widest form a card number may take.
	MaskedCard string
	Email      string
	IP         string
	BuyerID    string
	// Amount is paidPrice, or price when there is none. Nil when neither parses.
	Amount   *float64
	Currency string
	// Installment is nil when the request carries none.
	Installment    *int
	ConversationID string
}

// FraudRuleHit is what a custom rule reports. An empty Code means FRAUD_BLOCKED,
// an empty Verdict means the plugin verdict, an empty Message means the default
// message of the code.
type FraudRuleHit struct {
	Code    string
	Verdict FraudVerdict
	Message string
}

// FraudRule is a rule of your own: return a hit to trigger it, nil to let the
// payment through. An error fails the operation.
type FraudRule func(ctx FraudContext) (*FraudRuleHit, error)

// FraudKeyFunc derives the value to hash for a grouping key. An empty string
// means the attempt has no value for it, so nothing is counted.
type FraudKeyFunc func(ctx FraudContext) (string, error)

// FraudVelocityRule counts attempts per grouping key inside a window.
type FraudVelocityRule struct {
	// Key is the built-in kind to group by. Ignored when KeyFunc is set.
	Key FraudKeyKind
	// KeyFunc groups by a value of your own, hashed like every other key.
	KeyFunc FraudKeyFunc
	// Max is how many hits to allow: an attempt rule triggers on its Max+1-th
	// attempt (the current one counts), a declined rule triggers once the window
	// already holds Max declines. Must be at least 1.
	Max int
	// Window is milliseconds or a duration string ("30s", "10m", "1h", "1d").
	// Nil keeps the plugin window.
	Window any
	// On selects what is counted. Default: FraudVelocityAttempt.
	On FraudVelocityEvent
	// Distinct counts different values of this kind in the group instead of every
	// attempt: FraudKeyCard on Key FraudKeyBuyer is one buyer trying several cards.
	Distinct FraudMemberKind
	// When applies the rule only while it returns true.
	When func(ctx FraudContext) bool
	// Verdict of this rule. Empty keeps the plugin verdict.
	Verdict FraudVerdict
	// Code of the refusal. Default: RATE_LIMITED.
	Code string
}

// FraudWindow is the state of one counter window.
type FraudWindow struct {
	Count   int   `json:"count"`
	ResetAt int64 `json:"resetAt"`
}

// FraudStore is the counter backend. Keys are derived and hashed by the plugin,
// so a store never sees card data.
type FraudStore interface {
	// Hit registers a hit and returns the count of the live window.
	Hit(key string, windowMs int64) (FraudWindow, error)
	// Peek returns the count of the live window without registering a hit, and 0
	// once it expired.
	Peek(key string) (int, error)
}

// FraudDistinctStore is the optional distinct-member capability of a FraudStore.
// A rule with Distinct set needs it.
type FraudDistinctStore interface {
	Distinct(group, member string, windowMs int64) (FraudWindow, error)
}

// FraudResetStore is the optional reset capability of a FraudStore. An empty key
// drops every counter.
type FraudResetStore interface {
	Reset(key string) error
}

type fraudRulesGroup struct {
	members map[string]bool
	resetAt int64
}

// MemoryFraudStore is an in-memory fixed-window store for one process, tests and
// development. It evicts the oldest keys once it holds more than maxEntries
// windows and groups together, so rotating identities cannot grow it without
// bound.
type MemoryFraudStore struct {
	mu          sync.Mutex
	maxEntries  int
	windows     map[string]FraudWindow
	windowOrder []string
	groups      map[string]*fraudRulesGroup
	groupOrder  []string
}

// NewMemoryFraudStore builds an in-memory store holding at most maxEntries
// windows and groups (10000 when omitted).
func NewMemoryFraudStore(maxEntries ...int) *MemoryFraudStore {
	limit := 10000
	if len(maxEntries) > 0 {
		limit = maxEntries[0]
	}
	return &MemoryFraudStore{
		maxEntries: limit,
		windows:    map[string]FraudWindow{},
		groups:     map[string]*fraudRulesGroup{},
	}
}

// Hit counts a hit, starting a new window when the previous one expired.
func (s *MemoryFraudStore) Hit(key string, windowMs int64) (FraudWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	if live, ok := s.windows[key]; ok && live.ResetAt > now {
		live.Count++
		s.windows[key] = live
		return live, nil
	}
	fresh := FraudWindow{Count: 1, ResetAt: now + windowMs}
	if _, exists := s.windows[key]; !exists {
		s.windowOrder = append(s.windowOrder, key)
	}
	s.windows[key] = fresh
	s.trim()
	return fresh, nil
}

// Peek returns the live count of a key, or 0 once its window expired.
func (s *MemoryFraudStore) Peek(key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live, ok := s.windows[key]; ok && live.ResetAt > time.Now().UnixMilli() {
		return live.Count, nil
	}
	return 0, nil
}

// Distinct registers a member of a group and returns how many different members
// the live window holds.
func (s *MemoryFraudStore) Distinct(group, member string, windowMs int64) (FraudWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	live, exists := s.groups[group]
	keep := exists && live.resetAt > now
	entry := live
	if !keep {
		entry = &fraudRulesGroup{members: map[string]bool{}, resetAt: now + windowMs}
	}
	entry.members[member] = true
	if !exists {
		s.groupOrder = append(s.groupOrder, group)
	}
	s.groups[group] = entry
	if !keep {
		s.trim()
	}
	return FraudWindow{Count: len(entry.members), ResetAt: entry.resetAt}, nil
}

// Reset drops one key, or every counter when key is empty.
func (s *MemoryFraudStore) Reset(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		s.windows = map[string]FraudWindow{}
		s.groups = map[string]*fraudRulesGroup{}
		s.windowOrder = nil
		s.groupOrder = nil
		return nil
	}
	delete(s.windows, key)
	delete(s.groups, key)
	s.windowOrder = fraudRulesWithoutKey(s.windowOrder, key)
	s.groupOrder = fraudRulesWithoutKey(s.groupOrder, key)
	return nil
}

// trim evicts the oldest window key first, then the oldest group key, from both
// maps: a key can name a window and a group at once.
func (s *MemoryFraudStore) trim() {
	for len(s.windows)+len(s.groups) > s.maxEntries {
		oldest := ""
		switch {
		case len(s.windowOrder) > 0:
			oldest = s.windowOrder[0]
		case len(s.groupOrder) > 0:
			oldest = s.groupOrder[0]
		default:
			return
		}
		delete(s.windows, oldest)
		delete(s.groups, oldest)
		s.windowOrder = fraudRulesWithoutKey(s.windowOrder, oldest)
		s.groupOrder = fraudRulesWithoutKey(s.groupOrder, oldest)
	}
}

func fraudRulesWithoutKey(order []string, key string) []string {
	for i, candidate := range order {
		if candidate == key {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

// FraudDecision is a trigger as the plugin and its callbacks see it.
type FraudDecision struct {
	Code    string       `json:"code"`
	Verdict FraudVerdict `json:"verdict"`
	Outcome FraudOutcome `json:"outcome"`
	// Challenged is true when the challenge hook cleared the trigger.
	Challenged bool `json:"challenged"`
	// Key is the derived, hashed counter key of the rule that triggered, empty
	// when the rule counts nothing.
	Key       string                `json:"key"`
	Count     int                   `json:"count"`
	Operation core.PaymentOperation `json:"operation"`
	Amount    *float64              `json:"amount,omitempty"`
	Currency  string                `json:"currency,omitempty"`
	// MaskedCard is the BIN plus the last four, never the full number.
	MaskedCard     string `json:"maskedCard,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}

// FraudIpSource is where FraudClientIP may read an address from.
type FraudIpSource struct {
	// Headers are the request headers, in any case.
	Headers map[string]string
	// SocketAddress is the address the connection arrived on, when the framework
	// gives one.
	SocketAddress string
}

// FraudIpOptions is how FraudClientIP decides which address is the visitor's.
type FraudIpOptions struct {
	// Headers names the headers allowed to carry the visitor address, most
	// preferred first. Default: none, so no header is read at all.
	Headers []string
	// TrustedProxies lists addresses or IPv4 CIDRs of the proxies in front of the
	// app. When set, a header is only used for a request that arrived from one of
	// them, and a comma separated chain is read from the right.
	TrustedProxies []string
}

// FraudBinRange blocks one BIN or an inclusive range of BINs, 6 to 8 digits. An
// empty To blocks From alone.
type FraudBinRange struct {
	From string
	To   string
}

// FraudRulesOptions configures the fraud rules plugin.
type FraudRulesOptions struct {
	// Store is the counter backend. Default: an in-memory store.
	Store FraudStore
	// Salt of the derived keys. Default: random per process. Set a stable secret
	// when several instances share a store, otherwise their keys never match.
	Salt string
	// Operations the guard runs for. Default: the four payment and 3DS inits.
	// completeThreeDSPayment can never be guarded.
	Operations []core.PaymentOperation
	// Velocity rules, in order.
	Velocity []FraudVelocityRule
	// Rules of your own, in order, after the blocked BINs and the amount limit.
	Rules []FraudRule
	// BlockBins are the BINs to refuse, exact or as a range.
	BlockBins []FraudBinRange
	// MaxAmount refuses amounts above it, compared as a number in the currency of
	// the request. A number or a numeric string.
	MaxAmount any
	// Verdict of triggers that do not set one. Default: FraudVerdictBlock.
	Verdict FraudVerdict
	// Window is the default window of the velocity rules: milliseconds or a
	// duration string. Default: "10m".
	Window any
	// Challenge can clear a block, e.g. a captcha. An error counts as a failure,
	// so an unreachable service never lets a payment through.
	Challenge func(ctx FraudContext) (bool, error)
	// OnDecision is called for every trigger, blocked, flagged or cleared. Its
	// error is logged and never changes the outcome.
	OnDecision func(decision FraudDecision, ctx FraudContext) error
	// Keys overrides where an identity comes from.
	Keys map[FraudKeyKind]FraudKeyFunc
	// ResolveIP hands over the HTTP request's headers and connection address.
	// Without it the plugin only uses buyer.ip of the request, a value your own
	// code wrote.
	ResolveIP func(ctx core.OperationContext) (*FraudIpSource, error)
	// IP is which headers and proxies ResolveIP may believe.
	IP *FraudIpOptions
}

// FraudErrorMessages are the default messages of the plugin codes.
var FraudErrorMessages = map[string]string{
	"FRAUD_BLOCKED":          "This payment was not accepted. Please contact us or use another payment method.",
	"RATE_LIMITED":           "Too many payment attempts from here. Please try again later.",
	"BLOCKED_BIN":            "This card is not accepted.",
	"AMOUNT_LIMIT":           "This card is not accepted for amounts this high.",
	"THREEDS_REQUIRED":       "This payment needs 3D Secure verification.",
	"FRAUD_CHALLENGE_FAILED": "Verification failed. Please try again.",
}

var fraudRulesDefaultOperations = []core.PaymentOperation{
	core.OperationCreatePayment,
	core.OperationInitThreeDSPayment,
	core.OperationAuthorize,
	core.OperationInitThreeDSAuthorize,
}

var fraudRulesThreeDSOperations = map[core.PaymentOperation]bool{
	core.OperationInitThreeDSPayment:   true,
	core.OperationInitThreeDSAuthorize: true,
}

var fraudRulesUnits = map[string]float64{
	"ms": 1,
	"s":  1000,
	"m":  60000,
	"h":  3600000,
	"d":  86400000,
}

var fraudRulesDurationPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)$`)

func fraudRulesParseDuration(value any, what string) (int64, error) {
	switch typed := value.(type) {
	case int:
		return fraudRulesPositiveDuration(float64(typed), what)
	case int64:
		return fraudRulesPositiveDuration(float64(typed), what)
	case float32:
		return fraudRulesPositiveDuration(float64(typed), what)
	case float64:
		return fraudRulesPositiveDuration(typed, what)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: invalid %s '%s'. Use milliseconds or '30s', '10m', '1h', '1d'.", what, typed.String()), "")
		}
		return fraudRulesPositiveDuration(parsed, what)
	case string:
		match := fraudRulesDurationPattern.FindStringSubmatch(strings.TrimSpace(typed))
		if match == nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: invalid %s '%s'. Use milliseconds or '30s', '10m', '1h', '1d'.", what, typed), "")
		}
		number, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: invalid %s '%s'. Use milliseconds or '30s', '10m', '1h', '1d'.", what, typed), "")
		}
		return int64(math.Round(number * fraudRulesUnits[match[2]])), nil
	default:
		return 0, core.NewConfigurationError(
			fmt.Sprintf("fraudRules: invalid %s '%v'. Use milliseconds or '30s', '10m', '1h', '1d'.", what, value), "")
	}
}

func fraudRulesPositiveDuration(number float64, what string) (int64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
		return 0, core.NewConfigurationError(
			fmt.Sprintf("fraudRules: %s must be positive, got %s", what, fraudRulesNumberText(number)), "")
	}
	return int64(number), nil
}

func fraudRulesNumberText(number float64) string {
	return strconv.FormatFloat(number, 'f', -1, 64)
}

var fraudRulesNonDigits = regexp.MustCompile(`\D`)

// fraudRulesPanDigits holds the digits of a card number in a local variable:
// never a field of a context.
func fraudRulesPanDigits(value string) string {
	digits := fraudRulesNonDigits.ReplaceAllString(value, "")
	if len(digits) >= 13 && len(digits) <= 19 {
		return digits
	}
	return ""
}

func fraudRulesBinDigits(value string) string {
	digits := fraudRulesNonDigits.ReplaceAllString(value, "")
	if len(digits) >= 6 && len(digits) <= 8 {
		return digits
	}
	return ""
}

func fraudRulesText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func fraudRulesNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// fraudRulesAmountOf reads a money value: a number, or a string holding one.
func fraudRulesAmountOf(value any) (float64, bool) {
	if text, ok := value.(string); ok {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	}
	return fraudRulesNumber(value)
}

// fraudRulesView normalizes a request into a JSON tree, so typed structs,
// embedded 3DS requests and plain maps all read the same way.
func fraudRulesView(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

var fraudRulesIPv4 = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

func fraudRulesIPv4Bytes(value string) []int {
	match := fraudRulesIPv4.FindStringSubmatch(value)
	if match == nil {
		return nil
	}
	bytes := make([]int, 0, 4)
	for _, part := range match[1:] {
		number, err := strconv.Atoi(part)
		if err != nil || number > 255 {
			return nil
		}
		bytes = append(bytes, number)
	}
	return bytes
}

var fraudRulesMappedIPv4 = regexp.MustCompile(`^::ffff:(.+)$`)
var fraudRulesIPv6Text = regexp.MustCompile(`^[0-9a-f:.]+$`)

// FraudNormalizeIP returns an address as the counter sees it: no brackets, no
// "::ffff:" prefix, no whitespace. It returns "" for anything that is not an
// IPv4 or IPv6 address.
func FraudNormalizeIP(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if raw == "" {
		return ""
	}
	if match := fraudRulesMappedIPv4.FindStringSubmatch(raw); match != nil {
		raw = match[1]
	}
	if fraudRulesIPv4Bytes(raw) != nil {
		return raw
	}
	if strings.Contains(raw, ":") && fraudRulesIPv6Text.MatchString(raw) {
		return raw
	}
	return ""
}

// fraudRulesProxyMatcher matches one trustedProxies entry: an exact address, or
// an IPv4 range in CIDR notation.
func fraudRulesProxyMatcher(entry string) func(string) bool {
	parts := strings.SplitN(strings.TrimSpace(entry), "/", 2)
	ipBytes := fraudRulesIPv4Bytes(strings.TrimSpace(parts[0]))
	if len(parts) == 2 && ipBytes != nil {
		mask, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err == nil && mask >= 0 && mask <= 32 {
			base := fraudRulesIPv4Number(ipBytes)
			keep := uint32(0)
			if mask != 0 {
				keep = uint32(0xffffffff) << (32 - mask)
			}
			return func(ip string) bool {
				other := fraudRulesIPv4Bytes(ip)
				return other != nil && fraudRulesIPv4Number(other)&keep == base
			}
		}
	}
	exact := FraudNormalizeIP(entry)
	if exact == "" {
		return func(string) bool { return false }
	}
	return func(ip string) bool { return ip == exact }
}

func fraudRulesIPv4Number(parts []int) uint32 {
	return uint32(parts[0])<<24 | uint32(parts[1])<<16 | uint32(parts[2])<<8 | uint32(parts[3])
}

// FraudClientIP returns the visitor address of a request from the sources
// configured. Nothing is trusted by default: a forwarded-for header is a header
// any client can set, so a counter keyed on it is a counter the client chooses.
// Pass Headers when your own proxy writes the address, and TrustedProxies when
// you can tell that connection from a customer: only then is a header read, and
// only after the addresses it lists have been checked against your proxies.
func FraudClientIP(source *FraudIpSource, options *FraudIpOptions) string {
	socket := ""
	var headers map[string]string
	if source != nil {
		socket = FraudNormalizeIP(source.SocketAddress)
		headers = source.Headers
	}
	names := make([]string, 0)
	if options != nil {
		for _, name := range options.Headers {
			if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
				names = append(names, trimmed)
			}
		}
	}
	if len(names) == 0 {
		return socket
	}
	trusted := make([]func(string) bool, 0)
	if options != nil {
		for _, entry := range options.TrustedProxies {
			trusted = append(trusted, fraudRulesProxyMatcher(entry))
		}
	}
	matches := func(ip string) bool {
		for _, match := range trusted {
			if match(ip) {
				return true
			}
		}
		return false
	}
	// A header written by someone else is only readable from a connection you accept.
	if socket != "" && len(trusted) > 0 && !matches(socket) {
		return socket
	}
	for _, name := range names {
		value, found := "", false
		for key, header := range headers {
			if strings.ToLower(key) == name {
				value, found = header, true
				break
			}
		}
		if !found {
			continue
		}
		chain := make([]string, 0)
		for _, part := range strings.Split(value, ",") {
			if ip := FraudNormalizeIP(part); ip != "" {
				chain = append(chain, ip)
			}
		}
		// Read from the closest proxy inward: entries a customer appended sit on
		// the left.
		for index := len(chain) - 1; index >= 0; index-- {
			candidate := chain[index]
			if len(trusted) == 0 || !matches(candidate) {
				return candidate
			}
		}
	}
	return socket
}

// fraudRulesCardDigits reads the PAN of a request and keeps only its digits in a
// local variable: they never become a field of a context.
func fraudRulesCardDigits(request any) string {
	card := fraudRulesView(fraudRulesView(request)["paymentCard"])
	return fraudRulesPanDigits(fraudRulesText(card["cardNumber"]))
}

func fraudRulesDerive(ctx core.OperationContext, options FraudRulesOptions) (FraudContext, error) {
	view := fraudRulesView(ctx.Request)
	buyer := fraudRulesView(view["buyer"])
	digits := fraudRulesCardDigits(ctx.Request)

	// The address of the HTTP request when the app hands one over, buyer.ip
	// otherwise. A header is only ever read through FraudClientIP, which trusts
	// nothing by default.
	ip := ""
	if options.ResolveIP != nil {
		source, err := options.ResolveIP(ctx)
		if err != nil {
			return FraudContext{}, err
		}
		ip = FraudClientIP(source, options.IP)
	}
	if ip == "" {
		ip = FraudNormalizeIP(fraudRulesText(buyer["ip"]))
	}

	attempt := FraudContext{
		Operation:      ctx.Operation,
		Provider:       ctx.Provider,
		Request:        ctx.Request,
		Email:          strings.ToLower(fraudRulesText(buyer["email"])),
		IP:             ip,
		BuyerID:        fraudRulesText(buyer["id"]),
		Currency:       fraudRulesText(view["currency"]),
		ConversationID: fraudRulesText(view["conversationId"]),
	}
	if digits != "" {
		attempt.Bin = digits[:6]
		attempt.Prefix = digits[:8]
		attempt.Last4 = digits[len(digits)-4:]
		attempt.MaskedCard = digits[:6] + strings.Repeat("*", len(digits)-10) + digits[len(digits)-4:]
	}
	raw, present := view["paidPrice"]
	if !present || raw == nil {
		raw = view["price"]
	}
	if amount, ok := fraudRulesAmountOf(raw); ok {
		attempt.Amount = &amount
	}
	if installment, ok := fraudRulesNumber(view["installment"]); ok {
		value := int(installment)
		attempt.Installment = &value
	}
	return attempt, nil
}

type fraudRulesTrigger struct {
	code    string
	verdict FraudVerdict
	message string
	key     string
	count   int
}

type fraudRulesVelocity struct {
	rule     FraudVelocityRule
	windowMs int64
	declined bool
	verdict  FraudVerdict
	id       string
}

type fraudRulesBinBounds struct {
	from float64
	to   float64
}

// FraudMethods is the "fraud" member the plugin adds to the facade.
type FraudMethods struct {
	store FraudStore
}

// Store returns the counter backend the plugin uses.
func (m *FraudMethods) Store() FraudStore { return m.store }

// Reset drops one counter key, or every counter of this plugin when key is
// empty. A store without the reset capability does nothing.
func (m *FraudMethods) Reset(key string) error {
	if resetter, ok := m.store.(FraudResetStore); ok {
		return resetter.Reset(key)
	}
	return nil
}

// FraudRules builds the fraud rules and card-testing guard: it counts attempts
// per IP, BIN, card, email or buyer inside a window, blocks BIN ranges and
// amounts, flags one buyer trying several cards, and refuses a payment before
// the provider is called.
//
// A refusal is an ordinary failure result (status "failure", code FRAUD_SUSPECTED
// plus the rule's errorCode), so it is never reported as success and never
// retried. Provider callbacks and 3D Secure completions are never guarded:
// refusing one would strand a payment the customer already finished at the bank.
func FraudRules(options FraudRulesOptions) (core.Plugin, error) {
	operations := fraudRulesDefaultOperations
	if options.Operations != nil {
		operations = options.Operations
	}
	operationSet := map[core.PaymentOperation]bool{}
	operationList := make([]core.PaymentOperation, 0, len(operations))
	for _, operation := range operations {
		if operationSet[operation] {
			continue
		}
		operationSet[operation] = true
		operationList = append(operationList, operation)
	}
	// A callback carries the outcome of a payment the customer already finished
	// at the bank: refusing it loses money instead of preventing it.
	if operationSet[core.OperationCompleteThreeDSPayment] {
		return core.Plugin{}, core.NewConfigurationError(
			"fraudRules: completeThreeDSPayment is the provider callback and 3DS completion. "+
				"Guarding it would strand paid orders, so it can never be blocked.", "")
	}
	if len(operationList) == 0 {
		return core.Plugin{}, core.NewConfigurationError("fraudRules: operations must not be empty", "")
	}

	window := options.Window
	if window == nil {
		window = "10m"
	}
	defaultWindowMs, err := fraudRulesParseDuration(window, "window")
	if err != nil {
		return core.Plugin{}, err
	}
	verdict := options.Verdict
	if verdict == "" {
		verdict = FraudVerdictBlock
	}
	store := options.Store
	if store == nil {
		store = NewMemoryFraudStore()
	}
	distinctStore, hasDistinct := store.(FraudDistinctStore)
	salt := options.Salt
	if salt == "" {
		salt, err = core.RandomHex(16)
		if err != nil {
			return core.Plugin{}, err
		}
	}

	velocity := make([]fraudRulesVelocity, 0, len(options.Velocity))
	for index, rule := range options.Velocity {
		if rule.Max < 1 {
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: velocity rule %d needs a max of at least 1, got %d", index, rule.Max), "")
		}
		if rule.Distinct != "" && !hasDistinct {
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: velocity rule %d counts distinct values, which the store does ", index)+
					"not support. Implement store.distinct(group, member, windowMs), or drop the rule.", "")
		}
		windowMs := defaultWindowMs
		if rule.Window != nil {
			windowMs, err = fraudRulesParseDuration(rule.Window, "rule window")
			if err != nil {
				return core.Plugin{}, err
			}
		}
		ruleVerdict := rule.Verdict
		if ruleVerdict == "" {
			ruleVerdict = verdict
		}
		kind := string(rule.Key)
		if rule.KeyFunc != nil {
			kind = "custom"
		}
		velocity = append(velocity, fraudRulesVelocity{
			rule:     rule,
			windowMs: windowMs,
			declined: rule.On == FraudVelocityDeclined,
			verdict:  ruleVerdict,
			// Rule id, so that two rules over the same kind never share a counter.
			id: fmt.Sprintf("%d:%s", index, kind),
		})
	}
	hasDeclined := false
	for _, entry := range velocity {
		if entry.declined {
			hasDeclined = true
			break
		}
	}

	blockedBins := make([]fraudRulesBinBounds, 0, len(options.BlockBins))
	for _, entry := range options.BlockBins {
		to := entry.To
		if to == "" {
			to = entry.From
		}
		fromDigits := fraudRulesBinDigits(entry.From)
		toDigits := fraudRulesBinDigits(to)
		if fromDigits == "" || toDigits == "" {
			label := entry.From
			if entry.To != "" {
				label = entry.From + "," + entry.To
			}
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: blocked BIN '%s' must be 6 to 8 digits, or a [from, to] range", label), "")
		}
		from, _ := strconv.ParseFloat(fromDigits, 64)
		end, _ := strconv.ParseFloat(toDigits, 64)
		blockedBins = append(blockedBins, fraudRulesBinBounds{from: from, to: end})
	}

	var maxAmount *float64
	if options.MaxAmount != nil {
		parsed, ok := fraudRulesAmountOf(options.MaxAmount)
		if !ok {
			return core.Plugin{}, core.NewConfigurationError(
				fmt.Sprintf("fraudRules: invalid maxAmount '%v'", options.MaxAmount), "")
		}
		maxAmount = &parsed
	}

	var logger core.Logger

	tag := func(value string) (string, error) {
		mac, err := core.HMAC(core.SHA256, []byte(salt), []byte(value))
		if err != nil {
			return "", err
		}
		return core.ToHex(mac)[:16], nil
	}

	valueOf := func(kind FraudKeyKind, attempt FraudContext, digits string) (string, error) {
		if custom, ok := options.Keys[kind]; ok && custom != nil {
			return custom(attempt)
		}
		switch kind {
		case FraudKeyIP:
			return attempt.IP, nil
		case FraudKeyEmail:
			return attempt.Email, nil
		case FraudKeyBuyer:
			return attempt.BuyerID, nil
		case FraudKeyBin:
			return attempt.Bin, nil
		case FraudKeyPrefix:
			return attempt.Prefix, nil
		default:
			return digits, nil
		}
	}

	groupOf := func(entry fraudRulesVelocity, attempt FraudContext, digits string) (string, error) {
		raw := ""
		if entry.rule.KeyFunc != nil {
			value, err := entry.rule.KeyFunc(attempt)
			if err != nil {
				return "", err
			}
			raw = strings.TrimSpace(value)
		} else {
			value, err := valueOf(entry.rule.Key, attempt, digits)
			if err != nil {
				return "", err
			}
			raw = value
		}
		if raw == "" {
			return "", nil
		}
		digest, err := tag(raw)
		if err != nil {
			return "", err
		}
		return entry.id + ":" + digest, nil
	}

	memberOf := func(kind FraudMemberKind, attempt FraudContext, digits string) (string, error) {
		if kind == FraudMemberAmount {
			if attempt.Amount == nil {
				return "", nil
			}
			return "amount:" + fraudRulesNumberText(*attempt.Amount), nil
		}
		raw, err := valueOf(kind, attempt, digits)
		if err != nil {
			return "", err
		}
		if raw == "" {
			return "", nil
		}
		digest, err := tag(raw)
		if err != nil {
			return "", err
		}
		return string(kind) + ":" + digest, nil
	}

	// countAttempts registers every attempt counter and returns the first window
	// it crossed: a later rule still counts even once an earlier one triggered.
	countAttempts := func(attempt FraudContext, digits string) (*fraudRulesTrigger, error) {
		var first *fraudRulesTrigger
		for _, entry := range velocity {
			if entry.rule.When != nil && !entry.rule.When(attempt) {
				continue
			}
			key, err := groupOf(entry, attempt, digits)
			if err != nil {
				return nil, err
			}
			if key == "" {
				continue
			}
			count := 0
			switch {
			case entry.declined:
				count, err = store.Peek(key)
			case entry.rule.Distinct != "":
				var member string
				member, err = memberOf(entry.rule.Distinct, attempt, digits)
				if err != nil {
					return nil, err
				}
				if member == "" {
					continue
				}
				var window FraudWindow
				window, err = distinctStore.Distinct(key, member, entry.windowMs)
				count = window.Count
			default:
				var window FraudWindow
				window, err = store.Hit(key, entry.windowMs)
				count = window.Count
			}
			if err != nil {
				return nil, err
			}
			crossed := count > entry.rule.Max
			if entry.declined {
				crossed = count >= entry.rule.Max
			}
			if crossed && first == nil {
				code := entry.rule.Code
				if code == "" {
					code = "RATE_LIMITED"
				}
				first = &fraudRulesTrigger{code: code, verdict: entry.verdict, key: key, count: count}
			}
		}
		return first, nil
	}

	hit := func(code string, out *FraudRuleHit) *fraudRulesTrigger {
		trigger := &fraudRulesTrigger{code: code, verdict: verdict}
		if out != nil {
			if out.Verdict != "" {
				trigger.verdict = out.Verdict
			}
			trigger.message = out.Message
		}
		return trigger
	}

	// matchRules checks the blocked BINs, the amount limit, then your own rules.
	matchRules := func(attempt FraudContext) (*fraudRulesTrigger, error) {
		if attempt.Bin != "" {
			bin, _ := strconv.ParseFloat(attempt.Bin, 64)
			prefix := math.NaN()
			if attempt.Prefix != "" {
				prefix, _ = strconv.ParseFloat(attempt.Prefix, 64)
			}
			for _, blocked := range blockedBins {
				if (bin >= blocked.from && bin <= blocked.to) || (prefix >= blocked.from && prefix <= blocked.to) {
					return hit("BLOCKED_BIN", nil), nil
				}
			}
		}
		if maxAmount != nil && attempt.Amount != nil && *attempt.Amount > *maxAmount {
			return hit("AMOUNT_LIMIT", nil), nil
		}
		for _, rule := range options.Rules {
			out, err := rule(attempt)
			if err != nil {
				return nil, err
			}
			if out == nil {
				continue
			}
			code := out.Code
			if code == "" {
				code = "FRAUD_BLOCKED"
			}
			return hit(code, out), nil
		}
		return nil, nil
	}

	report := func(attempt FraudContext, trigger *fraudRulesTrigger, outcome FraudOutcome, challenged bool) {
		decision := FraudDecision{
			Code:           trigger.code,
			Verdict:        trigger.verdict,
			Outcome:        outcome,
			Challenged:     challenged,
			Key:            trigger.key,
			Count:          trigger.count,
			Operation:      attempt.Operation,
			Amount:         attempt.Amount,
			Currency:       attempt.Currency,
			MaskedCard:     attempt.MaskedCard,
			ConversationID: attempt.ConversationID,
		}
		// Derived data only: a fraud log line never carries a request body.
		if logger != nil {
			logger.Info("fraudRules: payment "+string(outcome), map[string]any{
				"code":      decision.Code,
				"outcome":   string(outcome),
				"key":       decision.Key,
				"count":     decision.Count,
				"operation": string(decision.Operation),
			})
		}
		if options.OnDecision != nil {
			if err := options.OnDecision(decision, attempt); err != nil && logger != nil {
				logger.Error("fraudRules: onDecision failed", err, nil)
			}
		}
	}

	refuse := func(trigger *fraudRulesTrigger, code string) *core.BeforeHookResult {
		message := trigger.message
		if message == "" {
			message = FraudErrorMessages[code]
		}
		if message == "" {
			message = FraudErrorMessages["FRAUD_BLOCKED"]
		}
		paymentCode := core.CodeFraudSuspected
		if trigger.verdict == FraudVerdictThreeDS {
			paymentCode = core.CodeThreeDSFailed
		}
		return &core.BeforeHookResult{Result: map[string]any{
			"status":       string(core.StatusFailure),
			"code":         string(paymentCode),
			"errorCode":    code,
			"errorMessage": message,
		}}
	}

	decide := func(attempt FraudContext, trigger *fraudRulesTrigger) (*core.BeforeHookResult, error) {
		if trigger.verdict == FraudVerdictFlag {
			report(attempt, trigger, FraudOutcomeFlagged, false)
			return nil, nil
		}
		if trigger.verdict == FraudVerdictThreeDS && fraudRulesThreeDSOperations[attempt.Operation] {
			report(attempt, trigger, FraudOutcomeAllowed, false)
			return nil, nil
		}
		if trigger.verdict == FraudVerdictBlock && options.Challenge != nil {
			cleared, err := options.Challenge(attempt)
			if err != nil {
				cleared = false
			}
			outcome := FraudOutcomeBlocked
			if cleared {
				outcome = FraudOutcomeAllowed
			}
			report(attempt, trigger, outcome, cleared)
			if cleared {
				return nil, nil
			}
			return refuse(trigger, "FRAUD_CHALLENGE_FAILED"), nil
		}
		report(attempt, trigger, FraudOutcomeBlocked, false)
		code := trigger.code
		if trigger.verdict == FraudVerdictThreeDS {
			code = "THREEDS_REQUIRED"
		}
		return refuse(trigger, code), nil
	}

	introspection := map[string]any{
		"operations": operationList,
		"verdict":    verdict,
	}
	if options.Window != nil {
		introspection["window"] = options.Window
	}
	errorCodes := make(map[string]string, len(FraudErrorMessages))
	for code, message := range FraudErrorMessages {
		errorCodes[code] = message
	}

	return core.DefinePlugin(core.Plugin{
		ID:         PluginIDFraudRules,
		Options:    introspection,
		ErrorCodes: errorCodes,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			logger = pctx.Logger()
			return nil
		},
		Hooks: core.PluginHooks{
			Before: []core.BeforeHook{{
				Matcher: func(ctx core.OperationContext) bool {
					return operationSet[ctx.Operation]
				},
				Handler: func(ctx core.OperationContext) (*core.BeforeHookResult, error) {
					attempt, err := fraudRulesDerive(ctx, options)
					if err != nil {
						return nil, err
					}
					digits := fraudRulesCardDigits(attempt.Request)
					breach, err := countAttempts(attempt, digits)
					if err != nil {
						return nil, err
					}
					trigger := breach
					if trigger == nil {
						trigger, err = matchRules(attempt)
						if err != nil {
							return nil, err
						}
					}
					if trigger == nil {
						return nil, nil
					}
					return decide(attempt, trigger)
				},
			}},
			After: []core.AfterHook{{
				// Declines the provider made, for guarded operations only. A refusal
				// by this plugin carries one of its codes and is not an issuer decline.
				Matcher: func(ctx core.OperationResultContext) bool {
					if !hasDeclined || !operationSet[ctx.Operation] {
						return false
					}
					view := fraudRulesView(ctx.Result)
					if fraudRulesText(view["status"]) != string(core.StatusFailure) {
						return false
					}
					errorCode := fraudRulesText(view["errorCode"])
					_, own := FraudErrorMessages[errorCode]
					return !own
				},
				Handler: func(ctx core.OperationResultContext) (*core.AfterHookResult, error) {
					attempt, err := fraudRulesDerive(ctx.OperationContext, options)
					if err != nil {
						return nil, err
					}
					digits := fraudRulesCardDigits(attempt.Request)
					for _, entry := range velocity {
						if !entry.declined {
							continue
						}
						if entry.rule.When != nil && !entry.rule.When(attempt) {
							continue
						}
						key, err := groupOf(entry, attempt, digits)
						if err != nil {
							return nil, err
						}
						if key == "" {
							continue
						}
						if _, err := store.Hit(key, entry.windowMs); err != nil {
							return nil, err
						}
					}
					return nil, nil
				},
			}},
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"fraud": &FraudMethods{store: store}}, nil
		},
	}), nil
}

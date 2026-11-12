package plugins

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/boracomet/go-pos/core"
)

// RateLimitCounter is the state of one key's window: how many hits it took and
// the epoch millisecond when the window resets.
type RateLimitCounter struct {
	Count   int   `json:"count"`
	ResetAt int64 `json:"resetAt"`
}

// RateLimitStore is the storage backend of the rate limiting counters.
type RateLimitStore interface {
	// Increment adds a hit to key within a window of windowMs milliseconds and
	// returns the updated count with the window's reset timestamp.
	Increment(key string, windowMs int64) (RateLimitCounter, error)
}

// RateLimitResetStore is the optional reset capability of a RateLimitStore. An
// empty key clears every counter.
type RateLimitResetStore interface {
	Reset(key string) error
}

type rateLimitEntry struct {
	count   int
	resetAt int64
}

// MemoryRateLimitStore is an in-memory fixed-window counter store. It evicts the
// oldest inserted key once it holds more than maxEntries keys.
type MemoryRateLimitStore struct {
	mu         sync.Mutex
	maxEntries int
	order      []string
	entries    map[string]rateLimitEntry
}

// NewMemoryRateLimitStore builds an in-memory store holding at most maxEntries
// keys (10000 when omitted).
func NewMemoryRateLimitStore(maxEntries ...int) *MemoryRateLimitStore {
	limit := 10000
	if len(maxEntries) > 0 {
		limit = maxEntries[0]
	}
	return &MemoryRateLimitStore{maxEntries: limit, entries: map[string]rateLimitEntry{}}
}

// Increment counts a hit, starting a new window when the previous one expired.
func (s *MemoryRateLimitStore) Increment(key string, windowMs int64) (RateLimitCounter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := pluginNowMs()
	entry, exists := s.entries[key]
	if !exists || entry.resetAt <= now {
		fresh := rateLimitEntry{count: 1, resetAt: now + windowMs}
		if !exists {
			s.order = append(s.order, key)
		}
		s.entries[key] = fresh
		for len(s.entries) > s.maxEntries && len(s.order) > 0 {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.entries, oldest)
		}
		return RateLimitCounter{Count: fresh.count, ResetAt: fresh.resetAt}, nil
	}
	entry.count++
	s.entries[key] = entry
	return RateLimitCounter{Count: entry.count, ResetAt: entry.resetAt}, nil
}

// Reset clears one key, or every key when key is empty.
func (s *MemoryRateLimitStore) Reset(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		s.entries = map[string]rateLimitEntry{}
		s.order = nil
		return nil
	}
	if _, exists := s.entries[key]; !exists {
		return nil
	}
	delete(s.entries, key)
	for i, candidate := range s.order {
		if candidate == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// RateLimitRule limits one route pattern. Patterns are matched in slice order
// and the first match wins, so the order is part of the configuration.
type RateLimitRule struct {
	// Pattern is the route to match: an exact path, a suffix such as "/payment",
	// or a wildcard such as "/callback/*".
	Pattern string
	// Excluded excludes the route from rate limiting entirely.
	Excluded bool
	// Window is the rule's window: a duration string ("1m", "30s", "1h") or a
	// number of milliseconds. Empty keeps the default window.
	Window any
	// Max is the rule's request limit. Nil keeps the default limit.
	Max *int
}

// RateLimitOptions configures the rate limit plugin.
type RateLimitOptions struct {
	// Window is the default window: a duration string or milliseconds.
	// Default: "1m".
	Window any
	// Max is the default number of requests per window. Default: 60.
	Max *int
	// Rules are the per-route rules and exclusions, checked in order.
	Rules []RateLimitRule
	// Key identifies the client. Required unless TrustProxy is enabled: without
	// one of the two there is no client identity the sender cannot forge.
	Key func(request *core.HandlerRequest) (string, error)
	// TrustProxy trusts proxy-set headers (X-Forwarded-For, X-Real-IP,
	// CF-Connecting-IP) for the default key: true means one trusted hop, a
	// positive integer means that many hops. The right-most X-Forwarded-For
	// entries are used, so values the client prepends are ignored. Disabled by
	// default, because unprotected runtimes let any client set these headers.
	TrustProxy any
	// Store holds the counters. Default: an in-memory store.
	Store RateLimitStore
	// ExcludeCallbacks excludes provider callbacks unless a rule matches them.
	// Default: true.
	ExcludeCallbacks *bool
	// PassOnStoreError lets requests through, unlimited, while the store fails,
	// and logs the error. Default: false, which answers 503 until the store is back.
	PassOnStoreError bool
}

var rateLimitDurationPattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)?$`)

// ParseDuration converts a window duration ("1m", "30s", "1h", "1d", "500ms") or
// a number of milliseconds into milliseconds.
func ParseDuration(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return rateLimitNumberDuration(float64(typed))
	case int64:
		return rateLimitNumberDuration(float64(typed))
	case float64:
		return rateLimitNumberDuration(typed)
	case float32:
		return rateLimitNumberDuration(float64(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, core.NewConfigurationError(fmt.Sprintf("rateLimit: invalid window duration '%s'", typed), "")
		}
		match := rateLimitDurationPattern.FindStringSubmatch(trimmed)
		if match == nil {
			return 0, core.NewConfigurationError(fmt.Sprintf("rateLimit: invalid window duration format '%s'", typed), "")
		}
		return rateLimitScaledDuration(match[1], match[2])
	default:
		return 0, core.NewConfigurationError(fmt.Sprintf("rateLimit: invalid window duration '%v'", value), "")
	}
}

func rateLimitNumberDuration(number float64) (int64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
		return 0, core.NewConfigurationError(
			fmt.Sprintf("rateLimit: window must be a positive number, got %s", rateLimitFormatNumber(number)), "")
	}
	return int64(number), nil
}

func rateLimitScaledDuration(number, unit string) (int64, error) {
	parsed, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, core.NewConfigurationError(fmt.Sprintf("rateLimit: invalid window duration '%s'", number), "")
	}
	switch strings.ToLower(unit) {
	case "d":
		return int64(math.Round(parsed * 86400000)), nil
	case "h":
		return int64(math.Round(parsed * 3600000)), nil
	case "m":
		return int64(math.Round(parsed * 60000)), nil
	case "s":
		return int64(math.Round(parsed * 1000)), nil
	default:
		return int64(math.Round(parsed)), nil
	}
}

func rateLimitFormatNumber(number float64) string {
	return strconv.FormatFloat(number, 'g', -1, 64)
}

// rateLimitWindowSet reports whether a rule window is set: an empty string or a
// zero number keeps the default window.
func rateLimitWindowSet(window any) bool {
	switch typed := window.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return true
	}
}

// rateLimitForwardedKey derives the client identity from the proxy headers,
// counting the X-Forwarded-For chain from the right: with N trusted hops the
// entry at length-N is the identity the closest proxy actually observed.
func rateLimitForwardedKey(hops int) func(*core.HandlerRequest) (string, error) {
	return func(request *core.HandlerRequest) (string, error) {
		forwarded := pluginHeader(request, "x-forwarded-for")
		if forwarded != "" {
			chain := make([]string, 0, 4)
			for _, entry := range strings.Split(forwarded, ",") {
				if trimmed := strings.TrimSpace(entry); trimmed != "" {
					chain = append(chain, trimmed)
				}
			}
			if len(chain) > 0 {
				index := len(chain) - hops
				if index < 0 {
					index = 0
				}
				return chain[index], nil
			}
		}
		if realIP := strings.TrimSpace(pluginHeader(request, "x-real-ip")); realIP != "" {
			return realIP, nil
		}
		if cfIP := strings.TrimSpace(pluginHeader(request, "cf-connecting-ip")); cfIP != "" {
			return cfIP, nil
		}
		return "unknown", nil
	}
}

func rateLimitIsCallbackPath(path string) bool {
	normalized := strings.TrimRight(path, "/")
	return strings.HasSuffix(normalized, "/callback") ||
		strings.Contains(normalized, "/callback/") ||
		strings.HasSuffix(normalized, "/complete-3ds") ||
		normalized == "/callback"
}

func rateLimitMatchesRule(pattern, path string) bool {
	normPattern := strings.TrimRight(pattern, "/")
	normPath := strings.TrimRight(path, "/")
	if normPattern == normPath {
		return true
	}
	if strings.Contains(normPattern, "*") {
		parts := strings.Split(normPattern, "*")
		for i, part := range parts {
			parts[i] = regexp.QuoteMeta(part)
		}
		source := strings.Join(parts, ".*")
		exact := regexp.MustCompile(`(?i)^` + source + `$`)
		prefixed := regexp.MustCompile(`(?i)^/[^/]+` + source + `$`)
		return exact.MatchString(normPath) || prefixed.MatchString(normPath)
	}
	actionPattern := normPattern
	if !strings.HasPrefix(actionPattern, "/") {
		actionPattern = "/" + actionPattern
	}
	return normPath == actionPattern || strings.HasSuffix(normPath, actionPattern)
}

// RateLimitMethods is the "rateLimit" member the plugin adds to the facade.
type RateLimitMethods struct {
	store RateLimitStore
}

// Store returns the counter store the plugin uses.
func (m *RateLimitMethods) Store() RateLimitStore { return m.store }

// Reset clears the counter of one key, or every counter when key is empty. A
// store without the reset capability does nothing.
func (m *RateLimitMethods) Reset(key string) error {
	if resetter, ok := m.store.(RateLimitResetStore); ok {
		return resetter.Reset(key)
	}
	return nil
}

// RateLimit builds the general HTTP request rate limiter for handler endpoints.
// It protects payment, 3D Secure and query endpoints against bot floods and
// brute-force card testing, and excludes provider callbacks by default.
func RateLimit(options RateLimitOptions) (core.Plugin, error) {
	window := options.Window
	if window == nil {
		window = "1m"
	}
	defaultWindowMs, err := ParseDuration(window)
	if err != nil {
		return core.Plugin{}, err
	}
	defaultMax := 60
	if options.Max != nil {
		defaultMax = *options.Max
	}
	if defaultMax <= 0 {
		return core.Plugin{}, core.NewConfigurationError(
			fmt.Sprintf("rateLimit: max must be a positive integer, got %d", defaultMax), "")
	}

	hops, err := rateLimitTrustedHops(options.TrustProxy)
	if err != nil {
		return core.Plugin{}, err
	}
	// Proxy headers are attacker-controlled on any runtime without a trusted
	// proxy in front, so there is no safe default key in that mode.
	if options.Key == nil && hops == 0 {
		return core.Plugin{}, core.NewConfigurationError(
			"rateLimit: no trustworthy client identity. Set \"trustProxy\" when the app runs behind a "+
				"reverse proxy that sets X-Forwarded-For, or pass an explicit \"key\" function.", "")
	}

	store := options.Store
	if store == nil {
		store = NewMemoryRateLimitStore()
	}
	keyFn := options.Key
	if keyFn == nil {
		keyFn = rateLimitForwardedKey(hops)
	}
	excludeCallbacks := options.ExcludeCallbacks == nil || *options.ExcludeCallbacks
	rules := options.Rules
	var logger core.Logger

	return core.DefinePlugin(core.Plugin{
		ID:      PluginIDRateLimit,
		Options: options,
		Init: func(_ context.Context, pctx core.PluginContext) error {
			logger = pctx.Logger()
			return nil
		},
		ErrorCodes: map[string]string{
			"RATE_LIMITED": "Too many requests, please try again later",
		},
		Methods: func(_ context.Context, _ core.PluginContext) (map[string]any, error) {
			return map[string]any{"rateLimit": &RateLimitMethods{store: store}}, nil
		},
		OnRequest: func(_ context.Context, request *core.HandlerRequest, rctx core.RequestContext) (*core.HandlerResponse, error) {
			path := rctx.Path

			var matched *RateLimitRule
			matchedPattern := ""
			for i := range rules {
				if rateLimitMatchesRule(rules[i].Pattern, path) {
					matched = &rules[i]
					matchedPattern = rules[i].Pattern
					break
				}
			}
			if matched != nil && matched.Excluded {
				return nil, nil
			}
			if excludeCallbacks && matched == nil && rateLimitIsCallbackPath(path) {
				return nil, nil
			}

			windowMs := defaultWindowMs
			if matched != nil && rateLimitWindowSet(matched.Window) {
				parsed, err := ParseDuration(matched.Window)
				if err != nil {
					return nil, err
				}
				windowMs = parsed
			}
			limit := defaultMax
			if matched != nil && matched.Max != nil {
				limit = *matched.Max
			}

			clientKey, err := keyFn(request)
			if err != nil {
				return nil, err
			}
			ruleKey := "global"
			if matchedPattern != "" {
				ruleKey = "rule:" + matchedPattern
			}

			counter, err := store.Increment(ruleKey+":"+clientKey, windowMs)
			if err != nil {
				if !options.PassOnStoreError {
					return nil, err
				}
				if logger != nil {
					logger.Error("rateLimit: store failed, request let through (passOnStoreError)", err, nil)
				}
				return nil, nil
			}

			if counter.Count <= limit {
				return nil, nil
			}
			now := pluginNowMs()
			retryAfter := int64(math.Ceil(float64(counter.ResetAt-now) / 1000))
			if retryAfter < 1 {
				retryAfter = 1
			}
			return &core.HandlerResponse{
				Status: 429,
				Headers: map[string]string{
					"content-type":          "application/json",
					"retry-after":           strconv.FormatInt(retryAfter, 10),
					"x-ratelimit-limit":     strconv.Itoa(limit),
					"x-ratelimit-remaining": "0",
					"x-ratelimit-reset":     strconv.FormatInt(int64(math.Ceil(float64(counter.ResetAt)/1000)), 10),
				},
				Body: map[string]any{
					"status":       "error",
					"code":         "RATE_LIMITED",
					"errorCode":    "RATE_LIMITED",
					"errorMessage": "Too many requests, please try again later",
				},
			}, nil
		},
	}), nil
}

func rateLimitTrustedHops(trustProxy any) (int, error) {
	invalid := func() (int, error) {
		return 0, core.NewConfigurationError(
			fmt.Sprintf("rateLimit: trustProxy must be true, false or a positive hop count, got %v", trustProxy), "")
	}
	switch typed := trustProxy.(type) {
	case nil:
		return 0, nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		if typed < 1 {
			return invalid()
		}
		return typed, nil
	case int64:
		if typed < 1 {
			return invalid()
		}
		return int(typed), nil
	case float64:
		if typed != math.Trunc(typed) || typed < 1 {
			return invalid()
		}
		return int(typed), nil
	default:
		return invalid()
	}
}

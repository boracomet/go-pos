package plugins

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boracomet/go-pos/core"
)

func fraudTestSetup(t *testing.T, options FraudRulesOptions) (*failoverTestFacade, *failoverTestProvider) {
	t.Helper()
	plugin, err := FraudRules(options)
	if err != nil {
		t.Fatalf("fraudRules: %v", err)
	}
	return failoverTestSingle(t, plugin, nil)
}

func fraudTestWantText(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

func fraudTestWantStatus(t *testing.T, record map[string]any, want core.PaymentStatus) {
	t.Helper()
	fraudTestWantText(t, "status", failoverTestText(record, "status"), string(want))
}

func fraudTestWantConfigError(t *testing.T, options FraudRulesOptions, contains string) {
	t.Helper()
	_, err := FraudRules(options)
	if err == nil {
		t.Fatalf("expected a configuration error for %+v", options)
	}
	var configuration *core.ConfigurationError
	if !errors.As(err, &configuration) {
		t.Fatalf("expected a ConfigurationError, got %T: %v", err, err)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err.Error(), contains)
	}
}

// fraudTestRecordingStore records every call and counts cumulatively: a hit
// count is the number of hits of that key so far.
type fraudTestRecordingStore struct {
	mu        sync.Mutex
	hits      []fraudTestHit
	peeks     []string
	distincts []fraudTestDistinct
}

type fraudTestHit struct {
	key      string
	windowMs int64
}

type fraudTestDistinct struct {
	group  string
	member string
}

func (s *fraudTestRecordingStore) Hit(key string, windowMs int64) (FraudWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits = append(s.hits, fraudTestHit{key: key, windowMs: windowMs})
	count := 0
	for _, hit := range s.hits {
		if hit.key == key {
			count++
		}
	}
	return FraudWindow{Count: count}, nil
}

func (s *fraudTestRecordingStore) Peek(key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peeks = append(s.peeks, key)
	count := 0
	for _, hit := range s.hits {
		if hit.key == key {
			count++
		}
	}
	return count, nil
}

func (s *fraudTestRecordingStore) Distinct(group, member string, _ int64) (FraudWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.distincts = append(s.distincts, fraudTestDistinct{group: group, member: member})
	seen := map[string]bool{}
	for _, entry := range s.distincts {
		if entry.group == group {
			seen[entry.member] = true
		}
	}
	return FraudWindow{Count: len(seen)}, nil
}

func (s *fraudTestRecordingStore) hitKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.hits))
	for _, hit := range s.hits {
		out = append(out, hit.key)
	}
	return out
}

func (s *fraudTestRecordingStore) hitsWithPrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, hit := range s.hits {
		if strings.HasPrefix(hit.key, prefix) {
			count++
		}
	}
	return count
}

func (s *fraudTestRecordingStore) distinctKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.distincts))
	for _, entry := range s.distincts {
		out = append(out, entry.member)
	}
	return out
}

func (s *fraudTestRecordingStore) peekKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.peeks...)
}

var fraudTestKeyPattern = regexp.MustCompile(`^\d+:[a-z]+:[0-9a-f]{16}$`)
var fraudTestMemberPattern = regexp.MustCompile(`^[a-z]+:[0-9a-f]{16}$`)
var fraudTestDigestSuffix = regexp.MustCompile(`:[0-9a-f]{16}$`)

func fraudTestRuleIDs(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, fraudTestDigestSuffix.ReplaceAllString(key, ""))
	}
	return out
}

func fraudTestMethods(t *testing.T, facade *failoverTestFacade) *FraudMethods {
	t.Helper()
	member, ok := facade.method(t, "fraud").(*FraudMethods)
	if !ok {
		t.Fatalf("fraud member has type %T", facade.method(t, "fraud"))
	}
	return member
}

func TestFraudRulesVelocity(t *testing.T) {
	t.Run("blocks the attempt after the window limit, without calling the provider", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 2, Window: "10m"}},
		})
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)

		blocked := facade.createPayment(t, failoverTestRequest())
		fraudTestWantStatus(t, blocked, core.StatusFailure)
		fraudTestWantText(t, "code", failoverTestText(blocked, "code"), string(core.CodeFraudSuspected))
		fraudTestWantText(t, "errorCode", failoverTestText(blocked, "errorCode"), "RATE_LIMITED")
		if failoverTestText(blocked, "errorMessage") == "" {
			t.Error("errorMessage is empty")
		}
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("provider calls = %d, want 2", got)
		}

		again := facade.createPayment(t, failoverTestRequest())
		fraudTestWantText(t, "errorCode", failoverTestText(again, "errorCode"), "RATE_LIMITED")
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("provider calls = %d, want 2", got)
		}
	})

	t.Run("lets the next attempt through once the window passed", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1, Window: "120ms"}},
		})
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusFailure)
		time.Sleep(200 * time.Millisecond)
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("provider calls = %d, want 2", got)
		}
	})

	t.Run("counts attempts per key, so one buyer cannot exhaust another window", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyCard, Max: 1}},
		})
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess)), core.StatusSuccess)
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess)), core.StatusFailure)
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestCardRequest(failoverTestCardMastercard)), core.StatusSuccess)

		byIP, ipProvider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
		})
		fraudTestWantStatus(t, byIP.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		fraudTestWantStatus(t, byIP.createPayment(t, failoverTestRequest()), core.StatusFailure)
		other := failoverTestRequest()
		other.Buyer.IP = "85.34.78.113"
		fraudTestWantStatus(t, byIP.createPayment(t, other), core.StatusSuccess)

		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("card provider calls = %d, want 2", got)
		}
		if got := ipProvider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("ip provider calls = %d, want 2", got)
		}
	})

	t.Run("keeps two rules over the same kind in separate windows", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Store: store,
			Velocity: []FraudVelocityRule{
				{Key: FraudKeyIP, Max: 5, Code: "IP_LIMIT"},
				{Key: FraudKeyIP, Max: 1, Code: "OTHER_IP_LIMIT"},
			},
		})
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		second := facade.createPayment(t, failoverTestRequest())
		fraudTestWantText(t, "errorCode", failoverTestText(second, "errorCode"), "OTHER_IP_LIMIT")

		want := []string{"0:ip", "1:ip", "0:ip", "1:ip"}
		got := fraudTestRuleIDs(store.hitKeys())
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("rule ids = %v, want %v", got, want)
		}
		for _, key := range store.hitKeys() {
			if !regexp.MustCompile(`^\d:ip:[0-9a-f]{16}$`).MatchString(key) {
				t.Errorf("key %q does not match the rule id and digest shape", key)
			}
		}
	})

	t.Run("counts declines of the provider, and never its own refusals", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Store:    store,
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 2, Window: "1h", On: FraudVelocityDeclined}},
		})
		for i := 0; i < 2; i++ {
			request := failoverTestCardRequest(failoverTestCardDeclined)
			request.PaidPrice = "1"
			request.Price = "1"
			declined := facade.createPayment(t, request)
			fraudTestWantText(t, "code", failoverTestText(declined, "code"), string(core.CodeCardDeclined))
		}
		if got := store.hitsWithPrefix("0:ip:"); got != 2 {
			t.Errorf("decline hits = %d, want 2", got)
		}

		blocked := facade.createPayment(t, failoverTestRequest())
		fraudTestWantStatus(t, blocked, core.StatusFailure)
		fraudTestWantText(t, "errorCode", failoverTestText(blocked, "errorCode"), "RATE_LIMITED")
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("provider calls = %d, want 2", got)
		}
		if got := store.hitsWithPrefix("0:ip:"); got != 2 {
			t.Errorf("decline hits after the refusal = %d, want 2", got)
		}
	})

	t.Run("counts different cards of one buyer, and an amount ladder", func(t *testing.T) {
		cards, _ := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyBuyer, Distinct: FraudKeyCard, Max: 2, Window: "30m"}},
		})
		fraudTestWantStatus(t, cards.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess)), core.StatusSuccess)
		fraudTestWantStatus(t, cards.createPayment(t, failoverTestCardRequest(failoverTestCardMastercard)), core.StatusSuccess)
		fraudTestWantStatus(t, cards.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess)), core.StatusSuccess)
		fourth := cards.createPayment(t, failoverTestCardRequest(failoverTestCardExpired))
		fraudTestWantStatus(t, fourth, core.StatusFailure)
		fraudTestWantText(t, "errorCode", failoverTestText(fourth, "errorCode"), "RATE_LIMITED")

		ladder, ladderProvider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyBuyer, Distinct: FraudMemberAmount, Max: 3, Window: "15m"}},
		})
		for _, paidPrice := range []string{"1.2", "2.5", "3.9"} {
			request := failoverTestRequest()
			request.PaidPrice = paidPrice
			fraudTestWantStatus(t, ladder.createPayment(t, request), core.StatusSuccess)
		}
		if got := ladderProvider.count(core.OperationCreatePayment); got != 3 {
			t.Errorf("provider calls = %d, want 3", got)
		}
		probed := failoverTestRequest()
		probed.PaidPrice = "100.1"
		fraudTestWantStatus(t, ladder.createPayment(t, probed), core.StatusFailure)
		if got := ladderProvider.count(core.OperationCreatePayment); got != 3 {
			t.Errorf("provider calls after the ladder = %d, want 3", got)
		}

		repeat, _ := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyEmail, Distinct: FraudKeyCard, Max: 2}},
		})
		repeat.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess))
		repeat.createPayment(t, failoverTestCardRequest(failoverTestCardMastercard))
		fraudTestWantStatus(t, repeat.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess)), core.StatusSuccess)
	})

	t.Run("applies a rule only when its condition holds", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Store:    store,
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1, When: func(FraudContext) bool { return false }}},
		})
		for i := 0; i < 3; i++ {
			fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		}
		if got := provider.count(core.OperationCreatePayment); got != 3 {
			t.Errorf("provider calls = %d, want 3", got)
		}
		if got := len(store.hitKeys()); got != 0 {
			t.Errorf("hits = %d, want 0", got)
		}
	})

	t.Run("exposes the counter store and resets it", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
		})
		facade.createPayment(t, failoverTestRequest())
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusFailure)
		if err := fraudTestMethods(t, facade).Reset(""); err != nil {
			t.Fatalf("reset: %v", err)
		}
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("provider calls = %d, want 2", got)
		}
		if _, ok := fraudTestMethods(t, facade).Store().(*MemoryFraudStore); !ok {
			t.Errorf("store has type %T, want *MemoryFraudStore", fraudTestMethods(t, facade).Store())
		}
	})
}

func TestFraudRulesBuiltInRules(t *testing.T) {
	t.Run("refuses an exact BIN and a BIN range with BLOCKED_BIN", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			BlockBins: []FraudBinRange{
				{From: "552879"},
				{From: failoverTestCardDeclined[:6], To: "400001"},
			},
		})
		blocked := facade.createPayment(t, failoverTestRequest())
		fraudTestWantStatus(t, blocked, core.StatusFailure)
		fraudTestWantText(t, "code", failoverTestText(blocked, "code"), string(core.CodeFraudSuspected))
		fraudTestWantText(t, "errorCode", failoverTestText(blocked, "errorCode"), "BLOCKED_BIN")
		if !strings.Contains(failoverTestText(blocked, "errorMessage"), "not accepted") {
			t.Errorf("errorMessage = %q", failoverTestText(blocked, "errorMessage"))
		}
		if got := provider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}

		ranged := facade.createPayment(t, failoverTestCardRequest(failoverTestCardDeclined))
		fraudTestWantText(t, "errorCode", failoverTestText(ranged, "errorCode"), "BLOCKED_BIN")
		if got := provider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}

		prefix, prefixProvider := fraudTestSetup(t, FraudRulesOptions{BlockBins: []FraudBinRange{{From: "55287900"}}})
		fraudTestWantText(t, "errorCode", failoverTestText(prefix.createPayment(t, failoverTestRequest()), "errorCode"), "BLOCKED_BIN")
		if got := prefixProvider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}
	})

	t.Run("refuses an amount above the limit and keeps a valid one", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{MaxAmount: "50000"})
		small := failoverTestRequest()
		small.PaidPrice = "49999.99"
		fraudTestWantStatus(t, facade.createPayment(t, small), core.StatusSuccess)
		big := failoverTestRequest()
		big.PaidPrice = "50000.01"
		result := facade.createPayment(t, big)
		fraudTestWantStatus(t, result, core.StatusFailure)
		fraudTestWantText(t, "errorCode", failoverTestText(result, "errorCode"), "AMOUNT_LIMIT")
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Errorf("provider calls = %d, want 1", got)
		}
	})

	t.Run("runs your own rules, with a code and a verdict of their own", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Rules: []FraudRule{
				func(ctx FraudContext) (*FraudRuleHit, error) {
					if ctx.Currency == "USD" {
						return &FraudRuleHit{Code: "USD_NOT_ACCEPTED"}, nil
					}
					return nil, nil
				},
				func(ctx FraudContext) (*FraudRuleHit, error) {
					installment := 0
					if ctx.Installment != nil {
						installment = *ctx.Installment
					}
					if installment > 3 {
						return &FraudRuleHit{Code: "LONG_INSTALLMENT", Verdict: FraudVerdictFlag}, nil
					}
					return nil, nil
				},
			},
		})
		usd := failoverTestRequest()
		usd.Currency = "USD"
		result := facade.createPayment(t, usd)
		fraudTestWantStatus(t, result, core.StatusFailure)
		fraudTestWantText(t, "errorCode", failoverTestText(result, "errorCode"), "USD_NOT_ACCEPTED")
		if failoverTestText(result, "errorMessage") == "" {
			t.Error("errorMessage is empty")
		}
		if got := provider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}

		installments := failoverTestRequest()
		installments.Installment = 2
		fraudTestWantStatus(t, facade.createPayment(t, installments), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Errorf("provider calls = %d, want 1", got)
		}
	})

	t.Run("checks the counters before the rules, so blocked attempts still count", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Store:     store,
			BlockBins: []FraudBinRange{{From: "552879"}},
			Velocity:  []FraudVelocityRule{{Key: FraudKeyIP, Max: 9}},
		})
		fraudTestWantText(t, "errorCode", failoverTestText(facade.createPayment(t, failoverTestRequest()), "errorCode"), "BLOCKED_BIN")
		fraudTestWantText(t, "errorCode", failoverTestText(facade.createPayment(t, failoverTestRequest()), "errorCode"), "BLOCKED_BIN")
		keys := store.hitKeys()
		if len(keys) != 2 {
			t.Fatalf("hits = %d, want 2", len(keys))
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, "0:ip:") {
				t.Errorf("key %q is not the ip counter", key)
			}
		}
	})
}

func TestFraudRulesVerdicts(t *testing.T) {
	t.Run("flags instead of blocking when the verdict is flag", func(t *testing.T) {
		decisions := []FraudDecision{}
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Verdict:   FraudVerdictFlag,
			MaxAmount: 10,
			OnDecision: func(decision FraudDecision, _ FraudContext) error {
				decisions = append(decisions, decision)
				return nil
			},
		})
		request := failoverTestRequest()
		request.PaidPrice = "999"
		fraudTestWantStatus(t, facade.createPayment(t, request), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Errorf("provider calls = %d, want 1", got)
		}
		if len(decisions) != 1 {
			t.Fatalf("decisions = %d, want 1", len(decisions))
		}
		fraudTestWantText(t, "code", decisions[0].Code, "AMOUNT_LIMIT")
		if decisions[0].Verdict != FraudVerdictFlag {
			t.Errorf("verdict = %q, want flag", decisions[0].Verdict)
		}
		if decisions[0].Outcome != FraudOutcomeFlagged {
			t.Errorf("outcome = %q, want flagged", decisions[0].Outcome)
		}
	})

	t.Run("requires 3D Secure on a non-3DS payment and accepts one that already has it", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{Verdict: FraudVerdictThreeDS, MaxAmount: 10})
		request := failoverTestRequest()
		request.PaidPrice = "999"
		refused := facade.createPayment(t, request)
		fraudTestWantStatus(t, refused, core.StatusFailure)
		fraudTestWantText(t, "code", failoverTestText(refused, "code"), string(core.CodeThreeDSFailed))
		fraudTestWantText(t, "errorCode", failoverTestText(refused, "errorCode"), "THREEDS_REQUIRED")
		if got := provider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("createPayment calls = %d, want 0", got)
		}

		threeDS := failoverTestThreeDSRequestWithCallback()
		threeDS.PaidPrice = "999"
		fraudTestWantStatus(t, facade.initThreeDS(t, threeDS), core.StatusPending)
		if got := provider.count(core.OperationInitThreeDSPayment); got != 1 {
			t.Errorf("initThreeDSPayment calls = %d, want 1", got)
		}
	})

	t.Run("clears a block with the challenge hook, and fails closed", func(t *testing.T) {
		decisions := []FraudDecision{}
		accept := false
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			MaxAmount: 10,
			Challenge: func(FraudContext) (bool, error) { return accept, nil },
			OnDecision: func(decision FraudDecision, _ FraudContext) error {
				decisions = append(decisions, decision)
				return nil
			},
		})
		request := failoverTestRequest()
		request.PaidPrice = "999"
		refused := facade.createPayment(t, request)
		fraudTestWantStatus(t, refused, core.StatusFailure)
		fraudTestWantText(t, "errorCode", failoverTestText(refused, "errorCode"), "FRAUD_CHALLENGE_FAILED")
		if got := provider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}

		accept = true
		fraudTestWantStatus(t, facade.createPayment(t, request), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Errorf("provider calls = %d, want 1", got)
		}
		if len(decisions) != 2 {
			t.Fatalf("decisions = %d, want 2", len(decisions))
		}
		if decisions[0].Outcome != FraudOutcomeBlocked || decisions[0].Challenged {
			t.Errorf("first decision = %+v, want blocked and not challenged", decisions[0])
		}
		if decisions[1].Outcome != FraudOutcomeAllowed || !decisions[1].Challenged {
			t.Errorf("second decision = %+v, want allowed and challenged", decisions[1])
		}

		broken, brokenProvider := fraudTestSetup(t, FraudRulesOptions{
			MaxAmount: 10,
			Challenge: func(FraudContext) (bool, error) { return false, errors.New("down") },
		})
		fraudTestWantText(t, "errorCode", failoverTestText(broken.createPayment(t, request), "errorCode"), "FRAUD_CHALLENGE_FAILED")
		if got := brokenProvider.count(core.OperationCreatePayment); got != 0 {
			t.Errorf("provider calls = %d, want 0", got)
		}
	})

	t.Run("reports the rule message when one is given", func(t *testing.T) {
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Rules: []FraudRule{func(ctx FraudContext) (*FraudRuleHit, error) {
				if ctx.IP == "85.34.78.112" {
					return &FraudRuleHit{Code: "TEST_IP", Message: "Nope."}, nil
				}
				return nil, nil
			}},
		})
		result := facade.createPayment(t, failoverTestRequest())
		fraudTestWantText(t, "errorCode", failoverTestText(result, "errorCode"), "TEST_IP")
		fraudTestWantText(t, "errorMessage", failoverTestText(result, "errorMessage"), "Nope.")
	})
}

func TestFraudRulesCallbacksAndCompletion(t *testing.T) {
	t.Run("cannot be pointed at the provider callback", func(t *testing.T) {
		fraudTestWantConfigError(t, FraudRulesOptions{
			Operations: []core.PaymentOperation{core.OperationCompleteThreeDSPayment},
		}, "")
		fraudTestWantConfigError(t, FraudRulesOptions{
			Operations: []core.PaymentOperation{core.OperationCreatePayment, core.OperationCompleteThreeDSPayment},
		}, "strand paid orders")
	})

	t.Run("never blocks a 3DS completion, even over every limit", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity:  []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
			BlockBins: []FraudBinRange{{From: "552879"}},
			MaxAmount: 1,
		})
		facade.initThreeDS(t, failoverTestThreeDSRequestWithCallback())
		fraudTestWantStatus(t, facade.initThreeDS(t, failoverTestThreeDSRequestWithCallback()), core.StatusFailure)

		callback := facade.completeThreeDS(t, map[string]any{
			"paymentId":   "PAY-1",
			"mdStatus":    "1",
			"signature":   "forged",
			"paymentCard": failoverTestCard(),
			"buyer":       failoverTestBuyer(),
			"paidPrice":   "9999",
		})
		if got := provider.count(core.OperationCompleteThreeDSPayment); got != 1 {
			t.Errorf("completeThreeDSPayment calls = %d, want 1", got)
		}
		fraudTestWantText(t, "errorCode", failoverTestText(callback, "errorCode"), "INVALID_HASH")
		if strings.Contains(failoverTestText(callback, "errorMessage"), "fraud") {
			t.Errorf("errorMessage = %q", failoverTestText(callback, "errorMessage"))
		}
	})

	t.Run("guards only the operations it is given", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Operations: []core.PaymentOperation{core.OperationAuthorize},
			BlockBins:  []FraudBinRange{{From: "552879"}},
		})
		authorized := facade.authorize(t, failoverTestRequest())
		fraudTestWantText(t, "errorCode", failoverTestText(authorized, "errorCode"), "BLOCKED_BIN")
		if got := provider.count(core.OperationAuthorize); got != 0 {
			t.Errorf("authorize calls = %d, want 0", got)
		}
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		if got := provider.count(core.OperationCreatePayment); got != 1 {
			t.Errorf("createPayment calls = %d, want 1", got)
		}
	})

	t.Run("does not touch refunds, cancels or queries", func(t *testing.T) {
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Velocity:  []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
			MaxAmount: 5000,
		})
		paid := facade.createPayment(t, failoverTestRequest())
		fraudTestWantStatus(t, paid, core.StatusSuccess)
		fraudTestWantText(t, "errorCode", failoverTestText(facade.createPayment(t, failoverTestRequest()), "errorCode"), "RATE_LIMITED")

		otherRequest := failoverTestRequest()
		otherRequest.Buyer.IP = "85.34.78.119"
		other := facade.createPayment(t, otherRequest)
		fraudTestWantStatus(t, other, core.StatusSuccess)

		refund := failoverTestRefundRequest()
		refund.PaymentID = failoverTestText(paid, "paymentId")
		fraudTestWantStatus(t, facade.refund(t, refund), core.StatusSuccess)
		fraudTestWantStatus(t, facade.getPayment(t, failoverTestText(paid, "paymentId")), core.StatusSuccess)
		cancel := failoverTestCancelRequest()
		cancel.PaymentID = failoverTestText(other, "paymentId")
		fraudTestWantStatus(t, facade.cancel(t, cancel), core.StatusSuccess)

		if got := provider.count(core.OperationRefund); got != 1 {
			t.Errorf("refund calls = %d, want 1", got)
		}
		if got := provider.count(core.OperationGetPayment); got != 1 {
			t.Errorf("getPayment calls = %d, want 1", got)
		}
		if got := provider.count(core.OperationCancel); got != 1 {
			t.Errorf("cancel calls = %d, want 1", got)
		}
		if got := provider.count(core.OperationCreatePayment); got != 2 {
			t.Errorf("createPayment calls = %d, want 2", got)
		}
	})
}

// fraudTestPlainStore has no distinct capability.
type fraudTestPlainStore struct{}

func (fraudTestPlainStore) Hit(string, int64) (FraudWindow, error) { return FraudWindow{}, nil }
func (fraudTestPlainStore) Peek(string) (int, error)               { return 0, nil }

func TestFraudRulesPluggableStore(t *testing.T) {
	t.Run("uses the store it is given instead of memory", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Store: store,
			Velocity: []FraudVelocityRule{
				{Key: FraudKeyIP, Max: 1},
				{Key: FraudKeyBuyer, Distinct: FraudKeyCard, Max: 5},
				{Key: FraudKeyEmail, Max: 1, On: FraudVelocityDeclined},
			},
		})
		facade.createPayment(t, failoverTestRequest())
		if len(store.hitKeys()) == 0 {
			t.Error("the store recorded no hit")
		}
		if got := len(store.distinctKeys()); got != 1 {
			t.Errorf("distinct calls = %d, want 1", got)
		}
		if got := len(store.peekKeys()); got != 1 {
			t.Errorf("peek calls = %d, want 1", got)
		}
		if fraudTestMethods(t, facade).Store() != store {
			t.Error("the plugin does not expose the store it was given")
		}
	})

	t.Run("rejects a distinct rule a store cannot serve", func(t *testing.T) {
		fraudTestWantConfigError(t, FraudRulesOptions{
			Store:    fraudTestPlainStore{},
			Velocity: []FraudVelocityRule{{Key: FraudKeyBuyer, Distinct: FraudKeyCard, Max: 1}},
		}, "does not support")
	})

	t.Run("derives stable keys from a shared salt, so instances share one window", func(t *testing.T) {
		store := NewMemoryFraudStore()
		build := func(salt string) *failoverTestFacade {
			plugin, err := FraudRules(FraudRulesOptions{
				Salt: salt, Store: store,
				Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
			})
			if err != nil {
				t.Fatalf("fraudRules: %v", err)
			}
			facade, _ := failoverTestSingle(t, plugin, nil)
			return facade
		}
		first := build("same-salt")
		second := build("same-salt")
		fraudTestWantStatus(t, first.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		fraudTestWantStatus(t, second.createPayment(t, failoverTestRequest()), core.StatusFailure)
		other := build("other-salt")
		fraudTestWantStatus(t, other.createPayment(t, failoverTestRequest()), core.StatusSuccess)
	})

	t.Run("caps the in-memory store so rotating identities cannot grow it forever", func(t *testing.T) {
		store := NewMemoryFraudStore(4)
		for i := 0; i < 50; i++ {
			if _, err := store.Hit(fmt.Sprintf("0:ip:%d", i), 60000); err != nil {
				t.Fatalf("hit: %v", err)
			}
			if _, err := store.Distinct("1:buyer:x", fmt.Sprintf("card:%d", i), 60000); err != nil {
				t.Fatalf("distinct: %v", err)
			}
		}
		store.mu.Lock()
		size := len(store.windows) + len(store.groups)
		store.mu.Unlock()
		if size > 4 {
			t.Errorf("store holds %d entries, want at most 4", size)
		}
		if count, _ := store.Peek("0:ip:0"); count != 0 {
			t.Errorf("oldest window count = %d, want 0", count)
		}
		if count, _ := store.Peek("0:ip:49"); count != 1 {
			t.Errorf("newest window count = %d, want 1", count)
		}
		if err := store.Reset(""); err != nil {
			t.Fatalf("reset: %v", err)
		}
		store.mu.Lock()
		windows, groups := len(store.windows), len(store.groups)
		store.mu.Unlock()
		if windows != 0 || groups != 0 {
			t.Errorf("after reset: %d windows and %d groups, want 0 and 0", windows, groups)
		}
	})
}

func TestFraudRulesCardDataNeverLeaves(t *testing.T) {
	t.Run("keeps a full PAN out of the store, the log, the decisions and the result", func(t *testing.T) {
		pan := failoverTestCardSuccess
		store := &fraudTestRecordingStore{}
		logger := &failoverTestLogger{}
		decisions := []FraudDecision{}
		plugin, err := FraudRules(FraudRulesOptions{
			Salt:      "test-salt",
			Store:     store,
			BlockBins: []FraudBinRange{{From: "552879"}},
			MaxAmount: 5,
			Velocity: []FraudVelocityRule{
				{Key: FraudKeyIP, Max: 1},
				{Key: FraudKeyCard, Max: 1},
				{Key: FraudKeyBuyer, Distinct: FraudKeyCard, Max: 1},
				{Key: FraudKeyEmail, Max: 1, On: FraudVelocityDeclined},
				{Key: FraudKeyBin, Max: 1},
				{Key: FraudKeyPrefix, Max: 1},
			},
			Rules: []FraudRule{func(ctx FraudContext) (*FraudRuleHit, error) {
				if ctx.MaskedCard == "" {
					return &FraudRuleHit{Code: "NO_CARD"}, nil
				}
				return nil, nil
			}},
			OnDecision: func(decision FraudDecision, _ FraudContext) error {
				decisions = append(decisions, decision)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("fraudRules: %v", err)
		}
		provider := failoverTestNewProvider("mock")
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{provider},
			Plugins:   []core.Plugin{plugin},
			Logger:    logger,
		})

		results := []map[string]any{}
		for i := 0; i < 3; i++ {
			results = append(results, facade.createPayment(t, failoverTestCardRequest(pan)))
		}
		results = append(results, facade.createPayment(t, failoverTestRequest()))
		threeDS := failoverTestThreeDSRequestWithCallback()
		threeDS.PaidPrice = "99"
		results = append(results, facade.initThreeDS(t, threeDS))

		anyFailure := false
		for _, result := range results {
			if failoverTestText(result, "status") == string(core.StatusFailure) {
				anyFailure = true
			}
			if runs := failoverTestDigitRuns(t, result); len(runs) != 0 {
				t.Errorf("result carries digit runs %v", runs)
			}
			if strings.Contains(failoverTestJSON(t, result), pan) {
				t.Error("result carries the full PAN")
			}
		}
		if !anyFailure {
			t.Error("no attempt was refused")
		}

		watched := []any{store.hitKeys(), store.peekKeys(), store.distinctKeys(), logger.all(), decisions}
		if runs := failoverTestDigitRuns(t, watched); len(runs) != 0 {
			t.Errorf("store, log or decisions carry digit runs %v", runs)
		}
		for _, key := range store.hitKeys() {
			if !fraudTestKeyPattern.MatchString(key) {
				t.Errorf("key %q is not a rule id and a digest", key)
			}
		}
		for _, member := range store.distinctKeys() {
			if !fraudTestMemberPattern.MatchString(member) {
				t.Errorf("member %q is not a kind and a digest", member)
			}
		}

		memory := NewMemoryFraudStore()
		guardedPlugin, err := FraudRules(FraudRulesOptions{
			Salt: "test-salt", Store: memory,
			Velocity: []FraudVelocityRule{{Key: FraudKeyCard, Max: 5}},
		})
		if err != nil {
			t.Fatalf("fraudRules: %v", err)
		}
		guarded, _ := failoverTestSingle(t, guardedPlugin, nil)
		guarded.createPayment(t, failoverTestCardRequest(pan))
		memory.mu.Lock()
		windows := make([]string, 0, len(memory.windows))
		for key := range memory.windows {
			windows = append(windows, key)
		}
		members := []string{}
		for _, group := range memory.groups {
			for member := range group.members {
				members = append(members, member)
			}
		}
		memory.mu.Unlock()
		internals := map[string]any{"windows": windows, "members": members}
		if runs := failoverTestDigitRuns(t, internals); len(runs) != 0 {
			t.Errorf("the default store holds digit runs %v", runs)
		}
		if strings.Contains(failoverTestJSON(t, internals), pan) {
			t.Error("the default store holds the full PAN")
		}
		if len(windows) != 1 {
			t.Fatalf("windows = %d, want 1", len(windows))
		}
		if !fraudTestKeyPattern.MatchString(windows[0]) {
			t.Errorf("window key %q is not a rule id and a digest", windows[0])
		}
		if len(members) != 0 {
			t.Errorf("members = %v, want none", members)
		}
	})

	t.Run("only hands rules masked card data", func(t *testing.T) {
		seen := []string{}
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Rules: []FraudRule{func(ctx FraudContext) (*FraudRuleHit, error) {
				seen = append(seen, ctx.MaskedCard, ctx.Bin, ctx.Prefix, ctx.Last4)
				return nil, nil
			}},
		})
		facade.createPayment(t, failoverTestCardRequest(failoverTestCardSuccess))
		want := []string{"424242******4242", "424242", "42424242", "4242"}
		if strings.Join(seen, "|") != strings.Join(want, "|") {
			t.Errorf("masked card data = %v, want %v", seen, want)
		}
	})
}

func TestFraudRulesVisitorAddress(t *testing.T) {
	chain := map[string]string{"x-forwarded-for": "203.0.113.7, 10.0.0.9"}

	t.Run("reads no header until the app names one", func(t *testing.T) {
		fraudTestWantText(t, "socket only", FraudClientIP(&FraudIpSource{Headers: chain, SocketAddress: "10.0.0.9"}, nil), "10.0.0.9")
		fraudTestWantText(t, "no socket", FraudClientIP(&FraudIpSource{Headers: chain}, nil), "")
		fraudTestWantText(t, "proxies alone", FraudClientIP(
			&FraudIpSource{Headers: chain, SocketAddress: "10.0.0.9"},
			&FraudIpOptions{TrustedProxies: []string{"10.0.0.0/8"}},
		), "10.0.0.9")
		fraudTestWantText(t, "bad quad", FraudNormalizeIP("999.1.1.1"), "")
		fraudTestWantText(t, "hostname", FraudNormalizeIP("example.com"), "")
		fraudTestWantText(t, "bracketed ipv6", FraudNormalizeIP("[2001:DB8::1]"), "2001:db8::1")
		fraudTestWantText(t, "mapped ipv4", FraudNormalizeIP("::ffff:127.0.0.1"), "127.0.0.1")
	})

	t.Run("reads the address from a declared header, closest proxy inward", func(t *testing.T) {
		options := &FraudIpOptions{Headers: []string{"x-forwarded-for"}, TrustedProxies: []string{"10.0.0.0/8"}}
		fraudTestWantText(t, "visitor behind our proxy", FraudClientIP(
			&FraudIpSource{Headers: chain, SocketAddress: "10.0.0.9"}, options), "203.0.113.7")
		fraudTestWantText(t, "every hop trusted", FraudClientIP(
			&FraudIpSource{Headers: map[string]string{"x-forwarded-for": "8.8.8.8, 203.0.113.7, 10.0.0.9"}, SocketAddress: "10.0.0.9"},
			&FraudIpOptions{Headers: []string{"x-forwarded-for"}, TrustedProxies: []string{"10.0.0.0/8", "203.0.113.7"}},
		), "8.8.8.8")
		fraudTestWantText(t, "untrusted connection", FraudClientIP(
			&FraudIpSource{Headers: chain, SocketAddress: "198.51.100.4"}, options), "198.51.100.4")
		fraudTestWantText(t, "case insensitive header", FraudClientIP(
			&FraudIpSource{Headers: map[string]string{"X-Forwarded-For": "203.0.113.7"}},
			&FraudIpOptions{Headers: []string{"x-forwarded-for"}},
		), "203.0.113.7")
		fraudTestWantText(t, "junk entries skipped", FraudClientIP(
			&FraudIpSource{Headers: map[string]string{"x-forwarded-for": "unknown, 203.0.113.7"}},
			&FraudIpOptions{Headers: []string{"x-forwarded-for"}},
		), "203.0.113.7")
		fraudTestWantText(t, "only junk", FraudClientIP(
			&FraudIpSource{Headers: map[string]string{"x-forwarded-for": "unknown"}},
			&FraudIpOptions{Headers: []string{"x-forwarded-for"}},
		), "")
		fraudTestWantText(t, "first header that carries an address wins", FraudClientIP(
			&FraudIpSource{Headers: map[string]string{"x-real-ip": "203.0.113.7", "x-forwarded-for": "198.51.100.4"}},
			&FraudIpOptions{Headers: []string{"x-real-ip", "x-forwarded-for"}},
		), "203.0.113.7")
	})

	t.Run("keys the counter on the request address, and hashes it like every other key", func(t *testing.T) {
		source := &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "1.1.1.1"}, SocketAddress: "10.0.0.9"}
		build := func(ip *FraudIpOptions) (*failoverTestFacade, *fraudTestRecordingStore) {
			store := &fraudTestRecordingStore{}
			plugin, err := FraudRules(FraudRulesOptions{
				Salt: "test-salt", Store: store,
				ResolveIP: func(core.OperationContext) (*FraudIpSource, error) { return source, nil },
				IP:        ip,
				Velocity:  []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
			})
			if err != nil {
				t.Fatalf("fraudRules: %v", err)
			}
			facade, _ := failoverTestSingle(t, plugin, nil)
			return facade, store
		}
		uniqueKeys := func(store *fraudTestRecordingStore) int {
			seen := map[string]bool{}
			for _, key := range store.hitKeys() {
				seen[key] = true
			}
			return len(seen)
		}

		trusting, trustingStore := build(nil)
		fraudTestWantStatus(t, trusting.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		source = &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "2.2.2.2"}, SocketAddress: "10.0.0.9"}
		fraudTestWantText(t, "errorCode", failoverTestText(trusting.createPayment(t, failoverTestRequest()), "errorCode"), "RATE_LIMITED")
		if got := uniqueKeys(trustingStore); got != 1 {
			t.Errorf("distinct keys = %d, want 1", got)
		}
		for _, key := range trustingStore.hitKeys() {
			if strings.Contains(key, "1.1.1.1") || strings.Contains(key, "10.0.0.9") {
				t.Errorf("key %q carries a raw address", key)
			}
		}

		source = &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "1.1.1.1"}, SocketAddress: "10.0.0.9"}
		opted, optedStore := build(&FraudIpOptions{Headers: []string{"x-forwarded-for"}, TrustedProxies: []string{"10.0.0.0/8"}})
		fraudTestWantStatus(t, opted.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		source = &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "2.2.2.2"}, SocketAddress: "10.0.0.9"}
		fraudTestWantStatus(t, opted.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		if got := uniqueKeys(optedStore); got != 2 {
			t.Errorf("distinct keys = %d, want 2", got)
		}

		source = &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "8.8.8.8"}, SocketAddress: "198.51.100.4"}
		direct, directStore := build(&FraudIpOptions{Headers: []string{"x-forwarded-for"}, TrustedProxies: []string{"10.0.0.0/8"}})
		fraudTestWantStatus(t, direct.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		source = &FraudIpSource{Headers: map[string]string{"x-forwarded-for": "9.9.9.9"}, SocketAddress: "198.51.100.4"}
		fraudTestWantText(t, "errorCode", failoverTestText(direct.createPayment(t, failoverTestRequest()), "errorCode"), "RATE_LIMITED")
		if got := uniqueKeys(directStore); got != 1 {
			t.Errorf("distinct keys = %d, want 1", got)
		}
	})

	t.Run("derives no ip key from a value that is not an address", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			Store:    store,
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1}},
		})
		for _, ip := range []string{"not-an-ip", "999.1.1.1", "", "example.org"} {
			request := failoverTestRequest()
			request.Buyer.IP = ip
			facade.createPayment(t, request)
		}
		if got := len(store.hitKeys()); got != 0 {
			t.Errorf("hits = %d, want 0", got)
		}
		fraudTestWantStatus(t, facade.createPayment(t, failoverTestRequest()), core.StatusSuccess)
		fraudTestWantText(t, "errorCode", failoverTestText(facade.createPayment(t, failoverTestRequest()), "errorCode"), "RATE_LIMITED")
		keys := store.hitKeys()
		seen := map[string]bool{}
		for _, key := range keys {
			seen[key] = true
			if !regexp.MustCompile(`^\d:ip:[0-9a-f]{16}$`).MatchString(key) {
				t.Errorf("key %q does not match the rule id and digest shape", key)
			}
		}
		if len(seen) != 1 {
			t.Errorf("distinct keys = %d, want 1", len(seen))
		}
	})
}

func TestFraudRulesEventsAndConfiguration(t *testing.T) {
	t.Run("emits payment.failed for a refusal and never a success", func(t *testing.T) {
		facade, _ := fraudTestSetup(t, FraudRulesOptions{BlockBins: []FraudBinRange{{From: "552879"}}})
		failed := 0
		succeeded := 0
		var first *core.PaymentEvent
		facade.on(core.EventPaymentFailed, func(_ context.Context, event *core.PaymentEvent) error {
			failed++
			if first == nil {
				first = event
			}
			return nil
		})
		facade.on(core.EventPaymentSucceeded, func(_ context.Context, _ *core.PaymentEvent) error {
			succeeded++
			return nil
		})
		facade.createPayment(t, failoverTestRequest())
		if failed != 1 {
			t.Errorf("payment.failed = %d, want 1", failed)
		}
		if succeeded != 0 {
			t.Errorf("payment.succeeded = %d, want 0", succeeded)
		}
		if first == nil {
			t.Fatal("no event")
		}
		if first.Code != core.CodeFraudSuspected {
			t.Errorf("event code = %q, want FRAUD_SUSPECTED", first.Code)
		}
		if first.Operation != core.OperationCreatePayment {
			t.Errorf("event operation = %q", first.Operation)
		}
		fraudTestWantText(t, "event provider", first.Provider, "mock")
	})

	t.Run("registers its codes and lets the localization plugin translate them", func(t *testing.T) {
		fraud, err := FraudRules(FraudRulesOptions{BlockBins: []FraudBinRange{{From: "552879"}}})
		if err != nil {
			t.Fatalf("fraudRules: %v", err)
		}
		localized, err := LocalizedErrors(LocalizedErrorsOptions{
			Locale: "tr",
			Messages: []I18nTranslation{{
				Locale:   "tr",
				Messages: I18nDictionary{"RATE_LIMITED": "Çok fazla deneme yaptınız."},
			}},
		})
		if err != nil {
			t.Fatalf("localizedErrors: %v", err)
		}
		facadeWithBoth := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{failoverTestNewProvider("mock")},
			Plugins:   []core.Plugin{fraud, localized},
		})
		codes := facadeWithBoth.ErrorCodes()
		if !strings.Contains(codes["FRAUD_BLOCKED"], "not accepted") {
			t.Errorf("FRAUD_BLOCKED = %q", codes["FRAUD_BLOCKED"])
		}
		if !strings.Contains(codes["RATE_LIMITED"], "Too many payment attempts") {
			t.Errorf("RATE_LIMITED = %q", codes["RATE_LIMITED"])
		}
		messages, ok := facadeWithBoth.method(t, "errors").(*I18nMethods)
		if !ok {
			t.Fatalf("errors member has type %T", facadeWithBoth.method(t, "errors"))
		}
		fraudTestWantText(t, "tr RATE_LIMITED", messages.Message("RATE_LIMITED", "tr"), "Çok fazla deneme yaptınız.")
		fraudTestWantText(t, "tr FRAUD_BLOCKED", messages.Message("FRAUD_BLOCKED", "tr"), codes["FRAUD_BLOCKED"])

		blocked := facadeWithBoth.createPayment(t, failoverTestRequest())
		fraudTestWantText(t, "errorCode", failoverTestText(blocked, "errorCode"), "BLOCKED_BIN")
		fraudTestWantText(t, "errorMessage", failoverTestText(blocked, "errorMessage"), codes["BLOCKED_BIN"])
		fraudTestWantText(t, "providerMessage", failoverTestText(blocked, "providerMessage"), codes["BLOCKED_BIN"])
	})

	t.Run("keeps onDecision failures from changing the outcome", func(t *testing.T) {
		facade, _ := fraudTestSetup(t, FraudRulesOptions{
			MaxAmount:  1,
			OnDecision: func(FraudDecision, FraudContext) error { return errors.New("alerting is down") },
		})
		request := failoverTestRequest()
		request.PaidPrice = "9"
		fraudTestWantStatus(t, facade.createPayment(t, request), core.StatusFailure)
	})

	t.Run("validates its own configuration", func(t *testing.T) {
		fraudTestWantConfigError(t, FraudRulesOptions{Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 0}}}, "")
		fraudTestWantConfigError(t, FraudRulesOptions{Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: -1}}}, "")
		fraudTestWantConfigError(t, FraudRulesOptions{Window: "soon"}, "invalid window")
		fraudTestWantConfigError(t, FraudRulesOptions{
			Velocity: []FraudVelocityRule{{Key: FraudKeyIP, Max: 1, Window: "1x"}},
		}, "invalid rule window")
		fraudTestWantConfigError(t, FraudRulesOptions{BlockBins: []FraudBinRange{{From: "552"}}}, "6 to 8 digits")
		fraudTestWantConfigError(t, FraudRulesOptions{MaxAmount: "lots"}, "invalid maxAmount")
		fraudTestWantConfigError(t, FraudRulesOptions{Operations: []core.PaymentOperation{}}, "must not be empty")
	})

	t.Run("reads a missing card, buyer or amount as no key instead of guessing one", func(t *testing.T) {
		store := &fraudTestRecordingStore{}
		facade, provider := fraudTestSetup(t, FraudRulesOptions{
			Store: store,
			Velocity: []FraudVelocityRule{
				{Key: FraudKeyBuyer, Max: 1},
				{Key: FraudKeyIP, Max: 1},
				{Key: FraudKeyCard, Max: 1},
			},
		})
		anonymous := failoverTestRequest()
		anonymous.Buyer = core.Buyer{}
		fraudTestWantStatus(t, facade.createPayment(t, anonymous), core.StatusSuccess)
		got := fraudTestRuleIDs(store.hitKeys())
		if strings.Join(got, ",") != "2:card" {
			t.Errorf("rule ids = %v, want [2:card]", got)
		}

		missing := failoverTestRequest()
		missing.PaymentCard = nil
		result := facade.createPayment(t, missing)
		fraudTestWantText(t, "errorCode", failoverTestText(result, "errorCode"), "VALIDATION_ERROR")
		if strings.Contains(failoverTestText(result, "errorMessage"), "fraud") {
			t.Errorf("errorMessage = %q", failoverTestText(result, "errorMessage"))
		}
		if calls := provider.count(core.OperationCreatePayment); calls != 2 {
			t.Errorf("provider calls = %d, want 2", calls)
		}
		if got := store.hitsWithPrefix("2:card"); got != 1 {
			t.Errorf("card hits = %d, want 1", got)
		}
	})
}

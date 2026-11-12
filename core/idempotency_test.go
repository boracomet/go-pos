package core

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestMemoryIdempotencyStoreSetAndGet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()

	if got, err := store.Get(ctx, "absent"); err != nil || got != "" {
		t.Errorf("Get(absent) = (%q, %v), want an empty value", got, err)
	}
	if err := store.Set(ctx, "k", "v", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Errorf("Get = %q, want v", got)
	}

	// Set replaces the value.
	if err := store.Set(ctx, "k", "w", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := store.Get(ctx, "k"); got != "w" {
		t.Errorf("Get after a second Set = %q, want w", got)
	}
}

func TestMemoryIdempotencyStoreSetIfAbsent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()

	stored, err := store.SetIfAbsent(ctx, "k", "first", 60)
	if err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	if !stored {
		t.Error("SetIfAbsent on an absent key = false, want true")
	}

	stored, err = store.SetIfAbsent(ctx, "k", "second", 60)
	if err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	if stored {
		t.Error("SetIfAbsent on an existing key = true, want false")
	}
	if got, _ := store.Get(ctx, "k"); got != "first" {
		t.Errorf("Get = %q, want the first value to be kept", got)
	}
}

func TestMemoryIdempotencyStoreExpiresEntries(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()

	// A zero TTL expires at once, which is the state a store reaches after the
	// TTL of a live entry has elapsed.
	if err := store.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := store.Get(ctx, "k"); err != nil || got != "" {
		t.Errorf("Get of an expired entry = (%q, %v), want an empty value", got, err)
	}
	stored, err := store.SetIfAbsent(ctx, "k", "w", 0)
	if err != nil || !stored {
		t.Errorf("SetIfAbsent of an expired entry = (%v, %v), want (true, nil)", stored, err)
	}

	// A live entry is not replaced.
	if err := store.Set(ctx, "live", "v", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if stored, err := store.SetIfAbsent(ctx, "live", "w", 60); err != nil || stored {
		t.Errorf("SetIfAbsent of a live entry = (%v, %v), want (false, nil)", stored, err)
	}
	if got, _ := store.Get(ctx, "live"); got != "v" {
		t.Errorf("Get = %q, want v", got)
	}
}

func TestMemoryIdempotencyStoreDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()
	if err := store.Set(ctx, "k", "v", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := store.Get(ctx, "k"); got != "" {
		t.Errorf("Get after Delete = %q, want an empty value", got)
	}
	// Deleting an absent key is not an error.
	if err := store.Delete(ctx, "absent"); err != nil {
		t.Errorf("Delete(absent) = %v, want nil", err)
	}
	if stored, err := store.SetIfAbsent(ctx, "k", "again", 60); err != nil || !stored {
		t.Errorf("SetIfAbsent after Delete = (%v, %v), want (true, nil)", stored, err)
	}
}

func TestMemoryIdempotencyStoreEvictsTheOldestEntries(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore(2)
	for _, key := range []string{"a", "b", "c"} {
		if err := store.Set(ctx, key, key, 60); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if got, _ := store.Get(ctx, "a"); got != "" {
		t.Errorf("Get(a) = %q, want the oldest entry to be evicted", got)
	}
	for _, key := range []string{"b", "c"} {
		if got, _ := store.Get(ctx, key); got != key {
			t.Errorf("Get(%q) = %q, want %q", key, got, key)
		}
	}
}

func TestMemoryIdempotencyStoreSetRefreshesTheEvictionOrder(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore(2)
	for _, key := range []string{"a", "b"} {
		if err := store.Set(ctx, key, key, 60); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	// Writing "a" again moves it to the end, so "b" is the oldest now.
	if err := store.Set(ctx, "a", "a2", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, "c", "c", 60); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := store.Get(ctx, "b"); got != "" {
		t.Errorf("Get(b) = %q, want b to be evicted", got)
	}
	if got, _ := store.Get(ctx, "a"); got != "a2" {
		t.Errorf("Get(a) = %q, want a2", got)
	}
	if got, _ := store.Get(ctx, "c"); got != "c" {
		t.Errorf("Get(c) = %q, want c", got)
	}
}

func TestMemoryIdempotencyStoreDefaultLimit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()
	const total = 10001
	for i := 0; i < total; i++ {
		if err := store.Set(ctx, idempotencyTestKey(i), "v", 60); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got, _ := store.Get(ctx, idempotencyTestKey(0)); got != "" {
		t.Errorf("the oldest of %d entries is still present, want the default limit of 10000", total)
	}
	if got, _ := store.Get(ctx, idempotencyTestKey(total-1)); got != "v" {
		t.Error("the newest entry is missing")
	}
}

func TestMemoryIdempotencyStoreIsSafeForConcurrentUse(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryIdempotencyStore()
	const writers = 64

	var mu sync.Mutex
	winners := 0
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, err := store.SetIfAbsent(ctx, "shared", "v", 60)
			if err != nil {
				t.Errorf("SetIfAbsent: %v", err)
				return
			}
			if stored {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Errorf("%d writers stored the key, want exactly 1", winners)
	}
	if got, _ := store.Get(ctx, "shared"); got != "v" {
		t.Errorf("Get = %q, want v", got)
	}
}

func TestMemoryIdempotencyStoreSatisfiesTheInterface(t *testing.T) {
	var store IdempotencyStore = NewMemoryIdempotencyStore()
	if store == nil {
		t.Fatal("store is nil")
	}
}

func TestFingerprintIgnoresKeyOrder(t *testing.T) {
	first := map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": 3}}
	second := map[string]any{"b": map[string]any{"d": 3, "c": 2}, "a": 1}

	a, err := Fingerprint(first)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	b, err := Fingerprint(second)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if a != b {
		t.Errorf("fingerprints differ: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("fingerprint length = %d, want 64 hex characters", len(a))
	}
	if a != strings.ToLower(a) {
		t.Errorf("fingerprint = %q, want lowercase hex", a)
	}

	other, err := Fingerprint(map[string]any{"a": 2})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if other == a {
		t.Error("different values produced the same fingerprint")
	}
}

func TestFingerprintMatchesTheCanonicalJSONDigest(t *testing.T) {
	value := map[string]any{"b": 2, "a": map[string]any{"d": 4, "c": 3}}
	got, err := Fingerprint(value)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	sum, err := Digest(SHA256, []byte(`{"a":{"c":3,"d":4},"b":2}`))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if want := ToHex(sum); got != want {
		t.Errorf("Fingerprint = %s, want %s", got, want)
	}
}

func TestFingerprintDoesNotEscapeHTML(t *testing.T) {
	got, err := Fingerprint(map[string]any{"a": "<b>&c"})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	sum, err := Digest(SHA256, []byte(`{"a":"<b>&c"}`))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if want := ToHex(sum); got != want {
		t.Errorf("Fingerprint = %s, want %s (no HTML escaping, no trailing newline)", got, want)
	}
}

func TestFingerprintOfStructsAndMaps(t *testing.T) {
	type body struct {
		OrderID string `json:"merchant_oid"`
		Status  string `json:"status"`
		Amount  string `json:"total_amount"`
	}
	fromStruct, err := Fingerprint(body{OrderID: "O1", Status: "success", Amount: "100"})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	fromMap, err := Fingerprint(map[string]any{"status": "success", "total_amount": "100", "merchant_oid": "O1"})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if fromStruct != fromMap {
		t.Errorf("a struct and the equivalent map differ: %s vs %s", fromStruct, fromMap)
	}

	// A parsed callback body fingerprints the same as the value it came from.
	var parsed any
	if err := json.Unmarshal([]byte(`{"total_amount":"100","merchant_oid":"O1","status":"success"}`), &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	fromJSON, err := Fingerprint(parsed)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if fromJSON != fromStruct {
		t.Errorf("a parsed body differs from the struct: %s vs %s", fromJSON, fromStruct)
	}
}

func TestFingerprintOfNilAndScalars(t *testing.T) {
	cases := []any{nil, "", "a", 1, true, []any{1, 2}, map[string]any{}}
	seen := make(map[string]bool, len(cases))
	for _, value := range cases {
		got, err := Fingerprint(value)
		if err != nil {
			t.Fatalf("Fingerprint(%v): %v", value, err)
		}
		if len(got) != 64 {
			t.Errorf("Fingerprint(%v) = %q, want 64 hex characters", value, got)
		}
		if seen[got] {
			t.Errorf("Fingerprint(%v) collides with an earlier value", value)
		}
		seen[got] = true
	}
}

func TestFingerprintRejectsUnserializableValues(t *testing.T) {
	if _, err := Fingerprint(make(chan int)); err == nil {
		t.Error("Fingerprint of a channel: want an error")
	}
	if _, err := Fingerprint(func() {}); err == nil {
		t.Error("Fingerprint of a function: want an error")
	}
}

func TestCanonicalJSONSortsKeysAtEveryLevel(t *testing.T) {
	value := map[string]any{
		"z": map[string]any{"b": 2, "a": 1},
		"a": []any{map[string]any{"d": 4, "c": 3}},
	}
	got, err := canonicalJSON(value)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	want := `{"a":[{"c":3,"d":4}],"z":{"a":1,"b":2}}`
	if string(got) != want {
		t.Errorf("canonicalJSON = %s, want %s", got, want)
	}
	if strings.HasSuffix(string(got), "\n") {
		t.Error("canonicalJSON ends with a newline")
	}
}

func idempotencyTestKey(i int) string {
	return "key-" + strconv.Itoa(i)
}

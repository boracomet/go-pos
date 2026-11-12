package core

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"
)

// IdempotencyStore is the key-value store the HTTP handler uses to process each
// provider callback once and to honor Idempotency-Key headers. Values are JSON
// strings.
//
// SetIfAbsent must be atomic (Redis "SET key value NX EX ttl", a unique-key
// insert in SQL). With several application instances use a shared store: the
// in-memory default only deduplicates within one process.
type IdempotencyStore interface {
	// Get returns the stored value, or an empty string when the key is absent or
	// expired.
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttlSeconds int) error
	// SetIfAbsent stores the value only when the key does not exist and reports
	// whether it was stored.
	SetIfAbsent(ctx context.Context, key, value string, ttlSeconds int) (bool, error)
	Delete(ctx context.Context, key string) error
}

// MemoryIdempotencyStore is an in-process store with expiry. It holds at most
// maxEntries keys and evicts the oldest written first. Suitable for a single
// instance and for tests. It is safe for concurrent use.
type MemoryIdempotencyStore struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[string]*list.Element
	order      *list.List
}

type memoryIdempotencyEntry struct {
	key       string
	value     string
	expiresAt time.Time
}

// NewMemoryIdempotencyStore builds a store holding at most maxEntries keys.
// Omitting maxEntries keeps 10000.
func NewMemoryIdempotencyStore(maxEntries ...int) *MemoryIdempotencyStore {
	limit := 10000
	if len(maxEntries) > 0 {
		limit = maxEntries[0]
	}
	return &MemoryIdempotencyStore{
		maxEntries: limit,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

// Get returns the stored value, or an empty string when the key is absent or expired.
func (s *MemoryIdempotencyStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		return "", nil
	}
	entry := element.Value.(*memoryIdempotencyEntry)
	if !time.Now().Before(entry.expiresAt) {
		s.remove(element)
		return "", nil
	}
	return entry.value, nil
}

// Set stores a value, replacing any previous one and moving the key to the end
// of the eviction order.
func (s *MemoryIdempotencyStore) Set(_ context.Context, key, value string, ttlSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store(key, value, ttlSeconds)
	return nil
}

// SetIfAbsent stores the value only when the key does not exist and reports
// whether it was stored.
func (s *MemoryIdempotencyStore) SetIfAbsent(_ context.Context, key, value string, ttlSeconds int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.entries[key]; ok {
		entry := element.Value.(*memoryIdempotencyEntry)
		if time.Now().Before(entry.expiresAt) {
			return false, nil
		}
		s.remove(element)
	}
	s.store(key, value, ttlSeconds)
	return true, nil
}

// store writes an entry and evicts the oldest ones past the limit. Callers hold mu.
func (s *MemoryIdempotencyStore) store(key, value string, ttlSeconds int) {
	if element, ok := s.entries[key]; ok {
		s.remove(element)
	}
	entry := &memoryIdempotencyEntry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(time.Duration(ttlSeconds) * time.Second),
	}
	s.entries[key] = s.order.PushBack(entry)
	for s.order.Len() > s.maxEntries {
		oldest := s.order.Front()
		if oldest == nil {
			break
		}
		s.remove(oldest)
	}
}

// Delete removes a key.
func (s *MemoryIdempotencyStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.entries[key]; ok {
		s.remove(element)
	}
	return nil
}

func (s *MemoryIdempotencyStore) remove(element *list.Element) {
	entry := element.Value.(*memoryIdempotencyEntry)
	delete(s.entries, entry.key)
	s.order.Remove(element)
}

// Fingerprint returns the SHA-256 of a value's canonical JSON (object keys
// sorted at every level), hex encoded. Two deliveries of the same callback body
// produce the same fingerprint, whatever the order of their fields.
func Fingerprint(value any) (string, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum, err := Digest(SHA256, data)
	if err != nil {
		return "", err
	}
	return ToHex(sum), nil
}

// canonicalJSON round-trips through any so that encoding/json sorts object keys;
// HTML escaping is off to keep the bytes identical to a plain JSON serializer.
func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return marshalJSON(parsed)
}

// marshalJSON serializes without HTML escaping and without a trailing newline.
func marshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

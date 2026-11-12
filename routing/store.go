package routing

import (
	"container/list"
	"context"
	"sync"
	"time"
)

const daySeconds = 86_400

// RoutingTTLSeconds is how long a payment -> provider mapping is kept. Refunds
// can arrive months later.
const RoutingTTLSeconds = 90 * daySeconds

// MemoryStoreOptions bounds the in-process routing store.
type MemoryStoreOptions struct {
	// MaxEntries is the hard upper bound. Zero keeps 10000 keys.
	MaxEntries int
	// TTLSeconds is the entry lifetime used when Set is given a negative TTL.
	// Zero keeps 90 days.
	TTLSeconds int
	// Now is the clock seam, in milliseconds since the epoch. Nil keeps
	// time.Now.
	Now func() int64
}

// MemoryStore is an in-process store with expiry and a size bound, so payment
// volume cannot grow memory without limit. It is restart- and process-scoped:
// fine for one instance and for tests, not for several instances. It is safe
// for concurrent use.
type MemoryStore struct {
	mu         sync.Mutex
	maxEntries int
	ttlSeconds int
	now        func() int64
	entries    map[string]*list.Element
	order      *list.List
}

type memoryEntry struct {
	key       string
	value     string
	expiresAt int64
}

// NewMemoryStore builds a bounded store with the default bounds when options is
// nil.
func NewMemoryStore(options *MemoryStoreOptions) *MemoryStore {
	opts := MemoryStoreOptions{}
	if options != nil {
		opts = *options
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = 10_000
	}
	if opts.TTLSeconds == 0 {
		opts.TTLSeconds = RoutingTTLSeconds
	}
	if opts.Now == nil {
		opts.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &MemoryStore{
		maxEntries: opts.MaxEntries,
		ttlSeconds: opts.TTLSeconds,
		now:        opts.Now,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

// Get returns the stored provider, or an empty string when the key is absent or
// expired.
func (s *MemoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		return "", nil
	}
	entry := element.Value.(*memoryEntry)
	if entry.expiresAt <= s.now() {
		s.remove(element)
		return "", nil
	}
	return entry.value, nil
}

// Set stores a provider, moving the key to the end of the eviction order. A
// negative ttlSeconds keeps the store's configured lifetime.
func (s *MemoryStore) Set(_ context.Context, key, value string, ttlSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ttlSeconds < 0 {
		ttlSeconds = s.ttlSeconds
	}
	if element, ok := s.entries[key]; ok {
		s.remove(element)
	}
	entry := &memoryEntry{key: key, value: value, expiresAt: s.now() + int64(ttlSeconds)*1000}
	s.entries[key] = s.order.PushBack(entry)
	for s.order.Len() > s.maxEntries {
		oldest := s.order.Front()
		if oldest == nil {
			break
		}
		s.remove(oldest)
	}
	return nil
}

// Delete forgets a key.
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.entries[key]; ok {
		s.remove(element)
	}
	return nil
}

// Size is the number of remembered keys, including expired ones not yet read.
func (s *MemoryStore) Size(_ context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

// DefaultTTLSeconds is the lifetime Set uses for a negative ttlSeconds.
func (s *MemoryStore) DefaultTTLSeconds() int { return s.ttlSeconds }

func (s *MemoryStore) remove(element *list.Element) {
	entry := element.Value.(*memoryEntry)
	delete(s.entries, entry.key)
	s.order.Remove(element)
}

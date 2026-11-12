package commissionrouter

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/boracomet/go-pos/core"
)

// TTLCacheOptions bounds a cache. There is no unbounded mode.
type TTLCacheOptions struct {
	// MaxEntries is the hard upper bound on retained entries. Must be >= 1.
	MaxEntries int
	// TTLMS is the entry lifetime in milliseconds. Must be > 0.
	TTLMS int64
	// Now is the clock seam in milliseconds since the epoch. Nil keeps time.Now.
	Now func() int64
}

type cacheItem[V any] struct {
	key       string
	value     V
	expiresAt int64
}

// TTLCache is a bounded FIFO cache with a TTL, shared by the BIN cache and the
// provider-routing table. When the cache is full the oldest entry is dropped and
// every entry expires after TTLMS, so card-bin diversity and order volume cannot
// grow memory without limit. It is safe for concurrent use.
type TTLCache[V any] struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      list.List
	maxEntries int
	ttlMS      int64
	now        func() int64
}

// NewTTLCache builds a bounded cache, rejecting a non-positive bound or lifetime.
func NewTTLCache[V any](options TTLCacheOptions) (*TTLCache[V], error) {
	if options.MaxEntries < 1 {
		return nil, core.NewConfigurationError("cache: maxEntries must be an integer >= 1", "")
	}
	if options.TTLMS <= 0 {
		return nil, core.NewConfigurationError("cache: ttlMs must be greater than 0", "")
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &TTLCache[V]{
		entries:    make(map[string]*list.Element),
		maxEntries: options.MaxEntries,
		ttlMS:      options.TTLMS,
		now:        now,
	}, nil
}

// Get returns the cached value, or false when the key is absent or expired. An
// expired entry is dropped on read.
func (c *TTLCache[V]) Get(_ context.Context, key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	element, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	item := element.Value.(*cacheItem[V])
	if item.expiresAt <= c.now() {
		c.remove(element)
		return zero, false
	}
	return item.value, true
}

// Has reports whether the key holds a live entry.
func (c *TTLCache[V]) Has(ctx context.Context, key string) bool {
	_, ok := c.Get(ctx, key)
	return ok
}

// Set stores a value. Re-inserting refreshes the entry's position; a new key
// evicts the oldest first.
func (c *TTLCache[V]) Set(_ context.Context, key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.remove(element)
	}
	if c.order.Len() >= c.maxEntries {
		if oldest := c.order.Front(); oldest != nil {
			c.remove(oldest)
		}
	}
	c.entries[key] = c.order.PushBack(&cacheItem[V]{key: key, value: value, expiresAt: c.now() + c.ttlMS})
}

// Delete forgets a key.
func (c *TTLCache[V]) Delete(_ context.Context, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.remove(element)
	}
}

// Clear forgets every entry.
func (c *TTLCache[V]) Clear(_ context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element)
	c.order.Init()
}

// Size is the number of retained entries, including expired ones not yet read.
func (c *TTLCache[V]) Size(_ context.Context) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *TTLCache[V]) remove(element *list.Element) {
	item := element.Value.(*cacheItem[V])
	delete(c.entries, item.key)
	c.order.Remove(element)
}

package commissionrouter

import (
	"context"
)

const dayMS = 86_400_000

// Store remembers which provider handled a payment, so refunds, cancels and
// status calls reach the same provider. Get returns an empty string when the
// payment is unknown. A store may also implement Delete(ctx, paymentID) error;
// the router itself only reads and writes.
type Store interface {
	Get(ctx context.Context, paymentID string) (string, error)
	Set(ctx context.Context, paymentID, provider string) error
}

// MemoryStoreOptions bounds the in-process routing store.
type MemoryStoreOptions struct {
	// MaxEntries is the hard upper bound. Zero keeps 10000 payments.
	MaxEntries int
	// TTLMS is the entry lifetime in milliseconds. Zero keeps 90 days: refunds
	// can come months later.
	TTLMS int64
	// Now is the clock seam in milliseconds since the epoch. Nil keeps time.Now.
	Now func() int64
}

// MemoryStore is an in-process routing store. It is bounded (the oldest payments
// are forgotten) and every entry expires, so payment volume cannot grow memory
// without limit.
//
// Per-process and restart-forgetting: fine for a single instance and for tests,
// not for a multi-instance deployment. Pass a shared Store there.
type MemoryStore struct {
	cache *TTLCache[string]
}

// NewMemoryStore builds a bounded store with the default bounds when options is
// nil.
func NewMemoryStore(options *MemoryStoreOptions) (*MemoryStore, error) {
	opts := MemoryStoreOptions{}
	if options != nil {
		opts = *options
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = 10_000
	}
	if opts.TTLMS == 0 {
		opts.TTLMS = 90 * dayMS
	}
	cache, err := NewTTLCache[string](TTLCacheOptions{
		MaxEntries: opts.MaxEntries,
		TTLMS:      opts.TTLMS,
		Now:        opts.Now,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryStore{cache: cache}, nil
}

// Get returns the provider that handled the payment, or an empty string.
func (s *MemoryStore) Get(ctx context.Context, paymentID string) (string, error) {
	provider, _ := s.cache.Get(ctx, paymentID)
	return provider, nil
}

// Set remembers the provider of a payment.
func (s *MemoryStore) Set(ctx context.Context, paymentID, provider string) error {
	s.cache.Set(ctx, paymentID, provider)
	return nil
}

// Delete forgets a payment.
func (s *MemoryStore) Delete(ctx context.Context, paymentID string) error {
	s.cache.Delete(ctx, paymentID)
	return nil
}

// Size is the number of remembered payments.
func (s *MemoryStore) Size(ctx context.Context) int { return s.cache.Size(ctx) }

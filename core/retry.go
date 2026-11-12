package core

import "time"

// RetryConfig controls retries. Only idempotent requests are retried: GET, HEAD,
// OPTIONS, or requests explicitly marked Retryable. Payment, refund and cancel
// requests are never retried, because a timeout after the provider received the
// request would otherwise charge or refund twice.
type RetryConfig struct {
	// Attempts is the total number of attempts including the first. 1 disables retries.
	Attempts int
	// Delay between attempts. Defaults to 1s.
	Delay time.Duration
	// StatusCodes trigger a retry. When empty, only network errors are retried.
	StatusCodes []int
}

func (r RetryConfig) delay() time.Duration {
	if r.Delay > 0 {
		return r.Delay
	}
	return time.Second
}

func (r RetryConfig) retriesStatus(status int) bool {
	for _, s := range r.StatusCodes {
		if s == status {
			return true
		}
	}
	return false
}

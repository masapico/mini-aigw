package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// RateLimiter manages in-memory sliding window request counters per API key.
type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
}

var globalRateLimiter = NewRateLimiter()

// NewRateLimiter creates a new thread-safe in-memory rate limiter.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		requests: make(map[string][]time.Time),
	}
	// Start periodic cleaner to avoid memory leak
	go rl.startCleaner(5 * time.Minute)
	return rl
}

// Allow checks if a request is allowed under the sliding 1-minute window.
// Returns (allowed, currentCount, retryAfterSeconds).
func (rl *RateLimiter) Allow(keyID string, rpm int) (bool, int, int) {
	if rpm <= 0 {
		return true, 0, 0
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-1 * time.Minute)

	times := rl.requests[keyID]
	valid := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rpm {
		// Oldest request in current window determines retry-after
		retryAfter := int(valid[0].Add(time.Minute).Sub(now).Seconds())
		if retryAfter <= 0 {
			retryAfter = 1
		}
		rl.requests[keyID] = valid
		return false, len(valid), retryAfter
	}

	valid = append(valid, now)
	rl.requests[keyID] = valid
	return true, len(valid), 0
}

func (rl *RateLimiter) startCleaner(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		windowStart := now.Add(-1 * time.Minute)
		for k, times := range rl.requests {
			valid := make([]time.Time, 0, len(times))
			for _, t := range times {
				if t.After(windowStart) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(rl.requests, k)
			} else {
				rl.requests[k] = valid
			}
		}
		rl.mu.Unlock()
	}
}

// CheckAPIRateLimit checks the rate limit for the APIKey in context.
func CheckAPIRateLimit(limiter *RateLimiter) func(http.Handler) http.Handler {
	if limiter == nil {
		limiter = globalRateLimiter
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAPIKeyFromContext(r.Context())
			if apiKey == nil {
				next.ServeHTTP(w, r)
				return
			}

			if apiKey.RateLimitRPM > 0 {
				allowed, currentCount, retryAfter := limiter.Allow(apiKey.ID, apiKey.RateLimitRPM)
				if !allowed {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"error": map[string]interface{}{
							"message": fmt.Sprintf("Rate limit exceeded: %d requests per minute limit reached (%d requests in past 60s). Please retry after %d seconds.", apiKey.RateLimitRPM, currentCount, retryAfter),
							"type":    "rate_limit_error",
							"code":    "rate_limit_exceeded",
						},
					})
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}


package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"mini-aigw/internal/models"
)

func TestRateLimiter_Allow(t *testing.T) {
	limiter := NewRateLimiter()

	keyID := "test-key-1"
	rpm := 3

	// 1st request -> allowed
	if allowed, count, _ := limiter.Allow(keyID, rpm); !allowed || count != 1 {
		t.Fatalf("Expected 1st request to be allowed, got allowed=%v, count=%d", allowed, count)
	}

	// 2nd request -> allowed
	if allowed, count, _ := limiter.Allow(keyID, rpm); !allowed || count != 2 {
		t.Fatalf("Expected 2nd request to be allowed, got allowed=%v, count=%d", allowed, count)
	}

	// 3rd request -> allowed
	if allowed, count, _ := limiter.Allow(keyID, rpm); !allowed || count != 3 {
		t.Fatalf("Expected 3rd request to be allowed, got allowed=%v, count=%d", allowed, count)
	}

	// 4th request -> blocked
	if allowed, count, retryAfter := limiter.Allow(keyID, rpm); allowed || count != 3 || retryAfter <= 0 {
		t.Fatalf("Expected 4th request to be blocked, got allowed=%v, count=%d, retryAfter=%d", allowed, count, retryAfter)
	}

	// Another key with unlimited rpm (0)
	otherKey := "test-key-unlimited"
	if allowed, _, _ := limiter.Allow(otherKey, 0); !allowed {
		t.Fatalf("Expected unlimited key to always be allowed")
	}
}

func TestCheckAPIRateLimit_Middleware(t *testing.T) {
	limiter := NewRateLimiter()
	mw := CheckAPIRateLimit(limiter)

	apiKey := &models.ApiKey{
		ID:           "key-rpm-2",
		RateLimitRPM: 2,
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req1 := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	req1 = req1.WithContext(context.WithValue(req1.Context(), APIKeyContextKey, apiKey))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), APIKeyContextKey, apiKey))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec2.Code)
	}

	req3 := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	req3 = req3.WithContext(context.WithValue(req3.Context(), APIKeyContextKey, apiKey))
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 Too Many Requests, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Errorf("Expected Retry-After header to be present")
	}
}

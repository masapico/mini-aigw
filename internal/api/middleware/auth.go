package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
	"mini-aigw/internal/models"
)

type contextKey string

const (
	UserContextKey   contextKey = "currentUser"
	APIKeyContextKey contextKey = "currentAPIKey"
)

func GetUserFromContext(ctx context.Context) *models.User {
	if u, ok := ctx.Value(UserContextKey).(*models.User); ok {
		return u
	}
	return nil
}

func GetAPIKeyFromContext(ctx context.Context) *models.ApiKey {
	if k, ok := ctx.Value(APIKeyContextKey).(*models.ApiKey); ok {
		return k
	}
	return nil
}

// SessionAuthMiddleware checks session cookie and populates user in request context
func SessionAuthMiddleware(db *gorm.DB, cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cfg.SessionCookieName)
			if err != nil || cookie.Value == "" {
				next.ServeHTTP(w, r)
				return
			}

			var session models.Session
			if err := db.Preload("User").First(&session, "session_id = ?", cookie.Value).Error; err != nil {
				next.ServeHTTP(w, r)
				return
			}

			if session.IsExpired() {
				// Expired session: delete from DB
				db.Delete(&session)
				next.ServeHTTP(w, r)
				return
			}

			if session.User != nil {
				ctx := context.WithValue(r.Context(), UserContextKey, session.User)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth redirects unauthenticated users to /login
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := GetUserFromContext(r.Context())
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin ensures the authenticated user has admin role
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := GetUserFromContext(r.Context())
		if u == nil || !u.HasRole("admin") {
			http.Error(w, "Forbidden: Admin role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// VerifyAPIKey verifies the Authorization: Bearer <token> header for OpenAI-compatible endpoints
func VerifyAPIKey(db *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Missing or invalid Authorization header. Expected 'Bearer <key>'","type":"invalid_request_error"}}`))
				return
			}

			rawToken := strings.TrimPrefix(authHeader, "Bearer ")
			rawToken = strings.TrimSpace(rawToken)
			keyHash := auth.HashAPIKey(rawToken)

			var apiKey models.ApiKey
			if err := db.First(&apiKey, "key_hash = ?", keyHash).Error; err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key provided","type":"invalid_request_error"}}`))
				return
			}

			ctx := context.WithValue(r.Context(), APIKeyContextKey, &apiKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PurgeExpiredSessions cleans up expired session records
func PurgeExpiredSessions(db *gorm.DB) {
	db.Where("expires_at < ?", time.Now()).Delete(&models.Session{})
}


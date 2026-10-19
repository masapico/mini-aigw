package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"mini-aigw/internal/config"
)

// ResolveBaseURL resolves the effective public base URL taking reverse proxies into account.
func ResolveBaseURL(r *http.Request, cfg *config.Config) string {
	if cfg != nil && cfg.PublicBaseURL != "" {
		return strings.TrimRight(cfg.PublicBaseURL, "/")
	}

	scheme := "http"
	if IsRequestHTTPS(r) {
		scheme = "https"
	}

	host := r.Host
	if fHost := r.Header.Get("X-Forwarded-Host"); fHost != "" {
		host = strings.TrimSpace(strings.Split(fHost, ",")[0])
	}

	prefix := ""
	if fPrefix := r.Header.Get("X-Forwarded-Prefix"); fPrefix != "" {
		prefix = strings.TrimSpace(strings.Split(fPrefix, ",")[0])
	} else if cfg != nil && cfg.BasePath != "" {
		prefix = cfg.BasePath
	}

	if prefix != "" {
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		prefix = strings.TrimRight(prefix, "/")
	}

	return fmt.Sprintf("%s://%s%s", scheme, host, prefix)
}

// IsRequestHTTPS checks if the request is HTTPS (direct or through reverse proxy).
func IsRequestHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto != "" {
		first := strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if first == "https" {
			return true
		}
	}
	if strings.ToLower(r.Header.Get("X-Forwarded-Scheme")) == "https" {
		return true
	}
	return false
}


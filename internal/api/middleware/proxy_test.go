package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"mini-aigw/internal/config"
)

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		reqSetup func() *http.Request
		expected string
	}{
		{
			name: "PublicBaseURL configured",
			cfg: &config.Config{
				PublicBaseURL: "https://custom.domain.com/aigw",
			},
			reqSetup: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://localhost:8000/", nil)
			},
			expected: "https://custom.domain.com/aigw",
		},
		{
			name: "Reverse proxy with X-Forwarded-Host and X-Forwarded-Proto and X-Forwarded-Prefix",
			cfg:  &config.Config{},
			reqSetup: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8000/", nil)
				req.Header.Set("X-Forwarded-Host", "sample.com")
				req.Header.Set("X-Forwarded-Proto", "https")
				req.Header.Set("X-Forwarded-Prefix", "/aigw")
				return req
			},
			expected: "https://sample.com/aigw",
		},
		{
			name: "Reverse proxy with multiple protos in X-Forwarded-Proto",
			cfg:  &config.Config{},
			reqSetup: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8000/", nil)
				req.Header.Set("X-Forwarded-Host", "sample.com")
				req.Header.Set("X-Forwarded-Proto", "https, http")
				return req
			},
			expected: "https://sample.com",
		},
		{
			name: "Direct access fallback with BasePath in config",
			cfg: &config.Config{
				BasePath: "/aigw",
			},
			reqSetup: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://localhost:8000/aigw/v1/models", nil)
			},
			expected: "http://localhost:8000/aigw",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.reqSetup()
			actual := ResolveBaseURL(req, tc.cfg)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}

func TestIsRequestHTTPS(t *testing.T) {
	reqTLS := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	reqTLS.TLS = &tls.ConnectionState{}
	if !IsRequestHTTPS(reqTLS) {
		t.Errorf("expected true for TLS request")
	}

	reqProto := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	reqProto.Header.Set("X-Forwarded-Proto", "https")
	if !IsRequestHTTPS(reqProto) {
		t.Errorf("expected true for X-Forwarded-Proto: https")
	}

	reqScheme := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	reqScheme.Header.Set("X-Forwarded-Scheme", "https")
	if !IsRequestHTTPS(reqScheme) {
		t.Errorf("expected true for X-Forwarded-Scheme: https")
	}

	reqHTTP := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	if IsRequestHTTPS(reqHTTP) {
		t.Errorf("expected false for plain HTTP request")
	}
}


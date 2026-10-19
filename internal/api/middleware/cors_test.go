package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddleware(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := CORSMiddleware(nextHandler)

	// 1. Test OPTIONS preflight
	reqOptions := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	recOptions := httptest.NewRecorder()
	handler.ServeHTTP(recOptions, reqOptions)

	if recOptions.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", recOptions.Code)
	}
	if recOptions.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Allow-Origin '*', got %q", recOptions.Header().Get("Access-Control-Allow-Origin"))
	}
	if recOptions.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("expected Allow-Methods header to be set")
	}

	// 2. Test standard GET / POST
	reqGet := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Errorf("expected 200 for GET, got %d", recGet.Code)
	}
	if recGet.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Allow-Origin '*', got %q", recGet.Header().Get("Access-Control-Allow-Origin"))
	}
}


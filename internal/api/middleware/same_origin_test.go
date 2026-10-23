package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOriginForUnsafeMethods(t *testing.T) {
	h := SameOriginForUnsafeMethods(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"GET cross-site allowed", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"POST no headers", "POST", nil, 200},
		{"POST same-origin", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"POST cross-site", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"DELETE same-site subdomain", "DELETE", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"POST matching origin", "POST", map[string]string{"Origin": "http://example.com"}, 200},
		{"POST foreign origin", "POST", map[string]string{"Origin": "http://evil.test"}, 403},
		{"POST null origin", "POST", map[string]string{"Origin": "null"}, 403},
		{"POST forwarded host", "POST", map[string]string{"Origin": "https://gw.corp", "X-Forwarded-Host": "gw.corp"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "http://example.com/x", nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("got %d want %d", w.Code, c.want)
			}
		})
	}
}

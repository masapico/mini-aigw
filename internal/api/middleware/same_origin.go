package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// SameOriginForUnsafeMethods は状態変更メソッド (POST/PUT/PATCH/DELETE) に対し、
// ブラウザが送出する Sec-Fetch-Site / Origin ヘッダーを検証して CSRF を防ぐ。
// どちらのヘッダーも無い (非ブラウザクライアント等) 場合は許可する。
func SameOriginForUnsafeMethods(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site != "same-origin" && site != "none" {
				http.Error(w, "Forbidden: cross-site request", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			u, err := url.Parse(origin)
			if err != nil || !hostMatches(u.Host, r) {
				http.Error(w, "Forbidden: origin mismatch", http.StatusForbidden)
				return
			}
		} else if origin == "null" {
			http.Error(w, "Forbidden: opaque origin", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func hostMatches(originHost string, r *http.Request) bool {
	if strings.EqualFold(originHost, r.Host) {
		return true
	}
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		for _, h := range strings.Split(fh, ",") {
			if strings.EqualFold(strings.TrimSpace(h), originHost) {
				return true
			}
		}
	}
	return false
}

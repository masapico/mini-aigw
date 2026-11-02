package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestVerifyDownload(t *testing.T) {
	secret := "test-secret"
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour).Unix()
	expStr := strconv.FormatInt(exp, 10)
	sig := SignDownload(secret, "doc-1", exp)

	tampered := []byte(sig)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}

	pastExp := now.Add(-time.Second).Unix()
	pastSig := SignDownload(secret, "doc-1", pastExp)

	tests := []struct {
		name   string
		secret string
		docID  string
		exp    string
		sig    string
		want   DownloadTokenStatus
	}{
		{"valid", secret, "doc-1", expStr, sig, DownloadTokenValid},
		{"expired", secret, "doc-1", strconv.FormatInt(pastExp, 10), pastSig, DownloadTokenExpired},
		{"other document", secret, "doc-2", expStr, sig, DownloadTokenInvalid},
		{"tampered signature", secret, "doc-1", expStr, string(tampered), DownloadTokenInvalid},
		{"extended expiry", secret, "doc-1", strconv.FormatInt(exp+3600, 10), sig, DownloadTokenInvalid},
		{"other secret", "other-secret", "doc-1", expStr, sig, DownloadTokenInvalid},
		{"malformed expiry", secret, "doc-1", "abc", sig, DownloadTokenInvalid},
		{"missing signature", secret, "doc-1", expStr, "", DownloadTokenInvalid},
		{"empty secret", "", "doc-1", expStr, sig, DownloadTokenInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyDownload(tt.secret, tt.docID, tt.exp, tt.sig, now); got != tt.want {
				t.Errorf("VerifyDownload() = %v, want %v", got, tt.want)
			}
		})
	}
}

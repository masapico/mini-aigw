package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// DownloadTokenStatus はダウンロード署名の検証結果。
type DownloadTokenStatus int

const (
	DownloadTokenInvalid DownloadTokenStatus = iota
	DownloadTokenValid
	DownloadTokenExpired
)

// SignDownload はナレッジ文書のダウンロード URL 用の署名を生成する。
// 署名対象は文書 ID と有効期限 (Unix 秒) で、SECRET_KEY を鍵とする HMAC-SHA256 を base64url で返す。
func SignDownload(secret, docID string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("kdl:" + docID + ":" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyDownload は SignDownload で生成した署名と有効期限を検証する。
func VerifyDownload(secret, docID, expStr, sig string, now time.Time) DownloadTokenStatus {
	if secret == "" || docID == "" || expStr == "" || sig == "" {
		return DownloadTokenInvalid
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return DownloadTokenInvalid
	}
	expected := SignDownload(secret, docID, exp)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return DownloadTokenInvalid
	}
	if now.Unix() > exp {
		return DownloadTokenExpired
	}
	return DownloadTokenValid
}

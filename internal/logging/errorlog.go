// Package logging はエラー調査用の永続ログ（JSON Lines 形式の error.log 追記）を提供する。
//
// 既存の log.Printf (stderr) はそのまま残し、障害解析に必要な構造化情報のみをファイルへ追記する。
// base64 本文や抽出テキストなどのペイロードは記録しないこと。
package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

var (
	mu      sync.RWMutex
	logger  = slog.New(slog.NewJSONHandler(io.Discard, nil))
	logFile *os.File
)

// Init は path のファイルを追記モードで開き、エラーロガーを初期化する。
// path が空の場合はファイル出力を行わない。
func Init(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating error log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("opening error log file: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
	}
	logFile = f
	logger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return nil
}

// Close は開いているエラーログファイルを閉じる。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
	logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func current() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

func withRequestID(ctx context.Context, attrs []any) []any {
	if ctx == nil {
		return attrs
	}
	if reqID := chiMiddleware.GetReqID(ctx); reqID != "" {
		return append([]any{"request_id", reqID}, attrs...)
	}
	return attrs
}

// Error はエラーレベルの構造化ログを error.log へ追記する。
func Error(ctx context.Context, msg string, attrs ...any) {
	current().Error(msg, withRequestID(ctx, attrs)...)
}

// Warn は警告レベルの構造化ログを error.log へ追記する。
func Warn(ctx context.Context, msg string, attrs ...any) {
	current().Warn(msg, withRequestID(ctx, attrs)...)
}

// Recoverer は panic を捕捉してスタックトレースを error.log へ記録し、500 を返すミドルウェア。
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				stack := string(debug.Stack())
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, rec, stack)
				Error(r.Context(), "panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(rec),
					"stack", stack,
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":{"message":"Internal server error","type":"api_error"}}`)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

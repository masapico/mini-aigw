package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"mini-aigw/internal/logging"
	"mini-aigw/internal/services"
)

// chatLogMeta はチャットリクエストのエラーログ用メタ情報。本文 (base64 / 抽出テキスト) は保持しない。
type chatLogMeta struct {
	ModelID     string
	Stream      bool
	RAG         bool
	APIKeyID    string
	APIKeyName  string
	BodyBytes   int
	Attachments []services.AttachmentInfo
	StartTime   time.Time
}

func (m *chatLogMeta) totalAttachmentChars() int {
	total := 0
	for _, a := range m.Attachments {
		total += a.ExtractedChars
	}
	return total
}

// bedrockErrorClass は Bedrock / 通信エラーを HTTP ステータスと OpenAI 互換エラー種別へ分類した結果。
type bedrockErrorClass struct {
	Status  int
	Type    string
	Code    string
	Message string
}

// classifyBedrockError は AWS のエラーコードに基づいて適切な HTTP ステータスを決定する。
func classifyBedrockError(err error) bedrockErrorClass {
	c := bedrockErrorClass{Status: http.StatusBadGateway, Type: "api_error", Code: "upstream_error", Message: err.Error()}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		c.Code = apiErr.ErrorCode()
		switch apiErr.ErrorCode() {
		case "ValidationException":
			c.Status = http.StatusBadRequest
			c.Type = "invalid_request_error"
			if looksLikeInputTooLong(apiErr.ErrorMessage()) {
				c.Message += " (添付ファイルが大きすぎる可能性があります。ファイルを分割するか、MAX_TOTAL_ATTACHMENT_CHARS を見直してください)"
			}
		case "ThrottlingException", "ServiceQuotaExceededException", "TooManyRequestsException":
			c.Status = http.StatusTooManyRequests
			c.Type = "rate_limit_error"
		case "ModelTimeoutException":
			c.Status = http.StatusGatewayTimeout
			c.Type = "timeout_error"
		case "ModelNotReadyException", "ServiceUnavailableException":
			c.Status = http.StatusServiceUnavailable
		}
		return c
	}

	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		c.Status = http.StatusGatewayTimeout
		c.Type = "timeout_error"
		c.Code = "timeout"
	case errors.Is(err, context.Canceled):
		// クライアント切断。nginx 互換の 499 を記録用に用いる
		c.Status = 499
		c.Code = "client_closed_request"
	}
	return c
}

func looksLikeInputTooLong(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{"too long", "too many tokens", "token", "context length", "context window", "exceeds"} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// writeJSONError は OpenAI 互換形式のエラーレスポンスを書き込む。
func writeJSONError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]interface{}{"message": message, "type": errType}
	if code != "" {
		body["code"] = code
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": body})
}

// writeBedrockError は Bedrock エラーを分類して JSON エラーレスポンスを返す。
func writeBedrockError(w http.ResponseWriter, err error) bedrockErrorClass {
	c := classifyBedrockError(err)
	writeJSONError(w, c.Status, c.Type, c.Code, c.Message)
	return c
}

// logChatError はチャット処理のエラーを、原因解析に必要な情報と共に error.log へ追記する。
func logChatError(ctx context.Context, stage string, err error, meta *chatLogMeta) {
	c := classifyBedrockError(err)
	attrs := []any{
		"stage", stage,
		"error", err.Error(),
		"status", c.Status,
		"error_code", c.Code,
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		attrs = append(attrs, "aws_error_code", apiErr.ErrorCode(), "aws_error_message", apiErr.ErrorMessage())
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		attrs = append(attrs, "aws_request_id", respErr.ServiceRequestID(), "aws_http_status", respErr.HTTPStatusCode())
	}

	if meta != nil {
		attrs = append(attrs,
			"model", meta.ModelID,
			"stream", meta.Stream,
			"rag", meta.RAG,
			"api_key_id", meta.APIKeyID,
			"api_key_name", meta.APIKeyName,
			"body_bytes", meta.BodyBytes,
			"elapsed_ms", time.Since(meta.StartTime).Milliseconds(),
			"total_attachment_chars", meta.totalAttachmentChars(),
			"attachments", meta.Attachments,
		)
	}
	logging.Error(ctx, "chat completion failed", attrs...)
}

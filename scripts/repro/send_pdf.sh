#!/bin/sh
# 業務チャットと同じ形式 (OpenAI 互換 "file" パート + base64) で PDF を mini-aigw に送信し、
# 添付ファイル起因のエラーを再現するスクリプト。
#
# 使い方:
#   AIGW_API_KEY=sk-xxx scripts/repro/send_pdf.sh error_sample/sample.pdf
#   AIGW_API_KEY=sk-xxx STREAM=true MODEL=jp.anthropic.claude-haiku-5-5 scripts/repro/send_pdf.sh sample.pdf
#
# 環境変数:
#   AIGW_URL      ベース URL (既定: http://localhost:8000/v1)
#   AIGW_API_KEY  API キー (必須)
#   MODEL         モデル ID (未指定時はサーバー既定)
#   STREAM        true でストリーミング (既定: false)
#   PROMPT        質問文 (既定: この資料を要約してください)
set -eu

PDF="${1:?usage: send_pdf.sh <file.pdf>}"
AIGW_URL="${AIGW_URL:-http://localhost:8000/v1}"
: "${AIGW_API_KEY:?AIGW_API_KEY is required}"
STREAM="${STREAM:-false}"
PROMPT="${PROMPT:-この資料を要約してください}"

json_escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

FILENAME="$(json_escape "$(basename "$PDF")")"
MODEL_FIELD=""
if [ -n "${MODEL:-}" ]; then
  MODEL_FIELD="\"model\":\"$(json_escape "$MODEL")\","
fi

BODY="$(mktemp "${TMPDIR:-/tmp}/aigw-repro-XXXXXX")"
trap 'rm -f "$BODY"' EXIT

{
  printf '{%s"stream":%s,"messages":[{"role":"user","content":[' "$MODEL_FIELD" "$STREAM"
  printf '{"type":"text","text":"%s"},' "$(json_escape "$PROMPT")"
  printf '{"type":"file","file":{"filename":"%s","file_data":"data:application/pdf;base64,' "$FILENAME"
  base64 < "$PDF" | tr -d '\n\r'
  printf '"}}]}]}'
} > "$BODY"

echo "file      : $PDF ($(wc -c < "$PDF" | tr -d ' ') bytes)"
echo "body size : $(wc -c < "$BODY" | tr -d ' ') bytes"
echo "endpoint  : $AIGW_URL/chat/completions (stream=$STREAM)"
echo "----------------------------------------"

curl -sS -N -D - \
  -H "Authorization: Bearer $AIGW_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @"$BODY" \
  -w '\n----------------------------------------\nhttp_status=%{http_code} time_total=%{time_total}s\n' \
  "$AIGW_URL/chat/completions"

echo "※ エラー時は data/logs/error.log (ERROR_LOG_FILE) をレスポンスヘッダーの時刻・X-Request-Id 付近で確認してください"

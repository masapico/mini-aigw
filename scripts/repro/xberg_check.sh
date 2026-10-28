#!/bin/sh
# mini-aigw と同じ引数で xberg を実行し、抽出結果の概要 (終了コード・所要時間・文字数) を表示する。
# サーバーホスト上で xberg 単体の問題かどうかを切り分けるためのスクリプト。
#
# 使い方:
#   scripts/repro/xberg_check.sh error_sample/sample.pdf
#   XBERG_PATH=/opt/xberg/xberg XBERG_OCR=true scripts/repro/xberg_check.sh sample.pdf
set -eu

PDF="${1:?usage: xberg_check.sh <file>}"
XBERG="${XBERG_PATH:-./xberg-cli/xberg}"
[ -d "$XBERG" ] && XBERG="$XBERG/xberg"
OCR="${XBERG_OCR:-false}"

OUT="$(mktemp "${TMPDIR:-/tmp}/xberg-out-XXXXXX")"
ERR="$(mktemp "${TMPDIR:-/tmp}/xberg-err-XXXXXX")"
trap 'rm -f "$OUT" "$ERR"' EXIT

START=$(date +%s)
set +e
"$XBERG" extract "$PDF" \
  --content-format markdown \
  --extract-images false \
  --pdf-extract-images false \
  --ocr "$OCR" \
  --no-config-discovery \
  --log-level error > "$OUT" 2> "$ERR"
CODE=$?
set -e
END=$(date +%s)

echo "file        : $PDF ($(wc -c < "$PDF" | tr -d ' ') bytes)"
echo "xberg       : $XBERG (ocr=$OCR)"
echo "exit code   : $CODE"
echo "duration    : $((END - START))s"
echo "output bytes: $(wc -c < "$OUT" | tr -d ' ')"
echo "output chars: $(wc -m < "$OUT" | tr -d ' ')  (MAX_TOTAL_ATTACHMENT_CHARS 既定 50000 と比較)"
echo "--- stderr (tail) ---"
tail -c 2048 "$ERR"

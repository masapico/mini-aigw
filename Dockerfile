# =============================================================================
# Stage 1: Build binary
# =============================================================================
ARG GO_VERSION=alpine
FROM golang:${GO_VERSION} AS builder

WORKDIR /src

# ビルド引数 (バージョン情報)
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=""

# CA証明書等の取得 (依存関係ダウンロード時の安全な通信用)
RUN apk add --no-cache ca-certificates tzdata git

# 依存モジュールを先に取得してビルドキャッシュを活用
COPY go.mod go.sum ./
RUN go mod download

# 全ソースコードをコピー (embed.FS により web/ ディレクトリも内包)
COPY . .

# 純Go SQLite & CGO完全無効化による静的バイナリのビルド
# -s -w でシンボルテーブルとデバッグ情報を除去してバイナリをスリム化
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w \
      -X mini-aigw/internal/version.Version=${VERSION} \
      -X mini-aigw/internal/version.GitCommit=${COMMIT} \
      -X mini-aigw/internal/version.BuildTime=${BUILD_TIME}" \
    -o /bin/mini-aigw \
    ./cmd/mini-aigw

# =============================================================================
# Stage 2: Minimal runtime
# =============================================================================
FROM alpine:3.21 AS runner

# AWS Bedrock/S3 との安全な HTTPS 通信に必要な CA 証明書 & タイムゾーンデータ
# gcompat: 外部CLIツール (xberg 等の glibc 依存 Linux バイナリ) の実行互換ライブラリ
RUN apk add --no-cache ca-certificates tzdata gcompat \
    && addgroup -S appgroup -g 10001 \
    && adduser -S -G appgroup -u 10001 -h /app appuser

WORKDIR /app

# SQLite データベース永続化用および外部ツール (xberg) マウント用のディレクトリを作成
RUN mkdir -p /app/data /opt/xberg && chown -R appuser:appgroup /app /opt/xberg

# ビルドステージから単一バイナリのみをコピー
COPY --from=builder --chown=appuser:appgroup /bin/mini-aigw /app/mini-aigw

# 非rootユーザーに切り替え
USER appuser:appgroup

# コンテナ公開ポート (デフォルト: 8000)
EXPOSE 8000

# 環境変数デフォルト設定
ENV PORT=8000 \
    HOST=0.0.0.0 \
    DATA_DIR=/app/data \
    DATABASE_URL=sqlite:///app/data/gateway.db \
    XBERG_PATH=/opt/xberg \
    PATH="/opt/xberg:${PATH}"

# データ永続化ボリューム
VOLUME ["/app/data"]

# ヘルスチェック (内部実装の /health エンドポイントを定期監視)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:8000/health || exit 1

# エントリポイント
ENTRYPOINT ["/app/mini-aigw"]
CMD ["server"]


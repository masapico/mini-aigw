# mini-aigw 今後の機能・UI 拡充ロードマップ (ROADMAP.md)

本ドキュメントは、閉域AWS環境向けAIゲートウェイ **mini-aigw** を、安全かつ費用対効果の高い「エンタープライズ基幹AIバックエンド」へと発展させるための全体計画およびフェーズ別ロードマップである。

---

## 1. 全体構想とアーキテクチャ像

```
[ 業務チャット / 組織内ツール / 開発者 ]
                 │ (OpenAI互換 API: Bearer sk-live-...)
                 ▼
┌────────────────────────────────────────────────────────┐
│ mini-aigw (Go AI Gateway & Management Console)         │
│                                                        │
│  [流量制御 & 認証]                                     │
│  ├─ APIキー認証 & 権限グループ分離                     │
│  ├─ インメモリ・スライディングウィンドウ RPM レート制限 │
│  └─ セッション管理 (mini-id OIDC / ローカルArgon2id)   │
│                                                        │
│  [コア推論 & FinOps エンジン]                          │
│  ├─ Bedrock Converse API 連携 (Nova, Claude 等)        │
│  ├─ トークン消費量 & USD コスト自動算出                │
│  └─ TTFT (初回トークン速度) / レイテンシ記録           │
│                                                        │
│  [KnowledgeOps (RAG & S3 Vectors)]                     │
│  ├─ Managed KB Pre-filtering (グループ + public)       │
│  ├─ 未解決クエリ分析 (Zero-Hit Query Insights)         │
│  └─ RAG 評価システム (Hit@K, MRR, LLM-as-a-Judge)      │
│                                                        │
│  [完全閉域 内包 Web UI (Bootstrap 5.3 + htmx + Chart.js)]│
│  ├─ コスト・トークン推移ダッシュボード                 │
│  ├─ キー別 / グループ別 / モデル別 集計 & CSV 出力     │
│  ├─ マルチモデル並列比較 (Side-by-Side Playground)     │
│  └─ ナレッジ原本管理 / ドキュメント同期                │
└────────────────────────────────────────────────────────┘
                 │
        ┌────────┴────────┐
        ▼                 ▼
[ Amazon Bedrock ]    [ Amazon S3 ]
(Converse / KB)       (Vectors / Metadata)
```

---

## 2. フェーズ別実装計画

### Phase 1: コスト統制 (FinOps) & トークンオブザーバビリティ 【対応済み1】
- [x] **USD 単価マスタ**: `llm_models` に 1Mトークンあたりの入力/出力単価（USD）を追加。
- [x] **コスト自動計算**: チャット処理完了時に `ChatUsageLog` に概算コスト（USD）と TTFT（初回トークン到達速度）を記録。
- [x] **インメモリ RPM レート制限**: APIキーごとに 1分間あたりの最大リクエスト数を制御し、超過時に `429 Too Many Requests` を返却。
- [x] **閉域 Chart.js 内包**: `web/static/vendor/chartjs/` に UMD 版を配置し、外部通信ゼロでグラフ描画。
- [x] **ダッシュボード集計 & CSV エクスポート**:
  - 直近14日間のトークン＆コスト推移グラフ。
  - APIキー別、所属グループ別、モデル別の利用量・コスト集計テーブル。
  - 按分・請求用の CSV ダウンロード機能。

### Phase 2: マルチモデル並列比較プレイグラウンド (Side-by-Side Playground) 【次回予定】
- [ ] **AWS コンソール風 左右並列比較 UI**:
  - `/rag-test` に「マルチモデル比較」タブを追加。
  - 共通の質問・添付ファイル・RAGグループ条件に対し、モデル A（例: Nova Lite）とモデル B（例: Claude 3.5 Haiku）を選択。
- [ ] **並列ストリーミング推論**:
  - バックエンドで 2つのモデル推論を goroutine で並行実行し、左右同時にストリーミング表示。
  - TTFT（体感応答速度）、生成速度（tokens/s）、消費トークン、概算コスト（USD）をリアルタイム表示。

### Phase 3: KnowledgeOps (RAG 運用高度化 & 品質評価閲覧)
- [x] **未解決クエリ分析 (Zero-Hit Query Insights)**:
  - RAG 検索でヒット件数が 0 件、または類似度スコアが閾値未満だった質問を自動集約（発生頻度・最高スコア・検知理由の記録）。
  - ダッシュボードで「ユーザーが何を尋ねていて、どのナレッジが不足しているか」を可視化（Top 5 リスト & 未対応件数バッジ）。
  - 専用管理コンソール（`/zero-hit-queries`）でのワンクリック解決（消込）/ 対象外トグル、RAGテスト画面へのクエリ引継ぎ、および CSV エクスポート。
- [ ] **RAG 評価レポート閲覧 UI**:
  - `cmd/eval` CLI で生成された過去の評価レポート（`eval_reports/*.html`）を Web 管理コンソールから直接プレビュー。

---

## 3. 将来構想 (Phase 4 以降・必要に応じて検討)

- **スマート・フォールバック (Model Fallback)**:
  - Bedrock のスロットリング（`ThrottlingException`）検知時に、自動で代替モデルへリトライ。
- **PII マスキング & Bedrock Guardrails 連携**:
  - 個人情報・機密情報の自動マスキング。
- **OpenAI 互換エンドポイント拡張**:
  - `/v1/embeddings`（Titan / Cohere Embeddings ラッパー）


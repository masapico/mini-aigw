# RAG 品質評価 & モデル比較ガイド (RAG_EVALUATION_GUIDE.md)

本ドキュメントは、mini-aigw においてナレッジ検索（Retriever）および LLM 回答（Retriever & LLM E2E）の品質評価を実施し、モデル間の差異比較レポートを作成・分析するためのガイドラインである。

---

## 1. 概要とアーキテクチャ

mini-aigw の評価システムは、閉域環境（オンプレミス・VPC内）でも単体で完結して動作する **CLI ツール (`cmd/eval`)** を中心に構築されています。

### 評価の2つのレイヤー
1. **Retriever 単体評価**:
   - Bedrock Knowledge Bases (S3 Vectors) の検索精度を検証します。
   - 指標: **Hit@K**, **MRR (平均逆順位)**, **権限分離 (Permission Isolation / Group Leakage)**。
2. **Retriever & LLM (E2E) 評価 & モデル比較**:
   - 検索されたコンテキストを複数のモデルに投入し、回答品質・レイテンシ・トークン消費量を横並びで比較します。
   - 指標: **レイテンシ (ms)**, **トークン消費 (In/Out)**, **引用遵守率 (Pass/Fail)**, **忠実度 (Faithfulness)**, **質問適合度 (Relevance)**, **模範解答との合致度 (Correctness)**。

---

## 2. 評価データセットの仕様

評価データセットは **JSONL 形式**（1行1JSON）で管理します。Git による変更履歴管理が容易で、テストケースの追加・修正が直感的に行えます。

### サンプルファイル (`testdata/eval/sample_dataset.jsonl`)

```jsonl
{"id":"tc-001","query":"年次有給休暇は何日付与されますか？","group":"public","expected_doc_ids":["doc-rule-leave-01"],"expected_filenames":["就業規則_2025.pdf"],"reference_answer":"入社半年後に10日付与され、その後勤続年数に応じて最大20日まで付与されます。"}
{"id":"tc-002","query":"営業部の出張旅費における日当の支給基準を教えてください。","group":"sales","expected_doc_ids":["doc-sales-travel-02"],"expected_filenames":["営業部旅費規程.docx"],"reference_answer":"日帰り出張の場合は1日あたり2,000円、宿泊を伴う出張の場合は1泊あたり3,500円が支給されます。"}
{"id":"tc-003","query":"他部署の社内ポータルパスワード初期化手順は？","group":"engineering","expected_doc_ids":["doc-eng-portal-01"],"expected_filenames":["ポータル運用手順書.pdf"],"reference_answer":"情報システム部のチケット起票画面より申請し、承認後に初期パスワードが通知されます。"}
{"id":"tc-004","query":"AWS Bedrockのクォータ引き上げ申請の手順を教えてください。","group":"public","expected_doc_ids":[],"expected_filenames":[],"reference_answer":"提供されたナレッジベースには該当情報がありません。AWS Service Quotas コンソールから申請を行ってください。"}
```

### フィールド定義一覧

| フィールド名 | 型 | 必須 | 説明 |
|---|---|:---:|---|
| `id` | string | 任意 | テストケースの一意の識別子（省略時は行番号から自動生成） |
| `query` | string | **必須** | ユーザーが入力する自然文の質問クエリ |
| `group` | string | 任意 | 実行権限グループ（省略時は `public`。指定グループ + public で検索） |
| `expected_doc_ids` | string[] | 任意 | 正解ドキュメントの ID 一覧（Retriever 評価用） |
| `expected_filenames`| string[] | 任意 | 正解ドキュメントのファイル名一覧（部分一致判定にも対応） |
| `reference_answer` | string | 任意 | 人間が記述した模範解答（LLM 正確性評価用） |

> **テストケース設計のコツ**:
> - **権限分離テスト**: `group: "sales"` のケースを用意し、検索結果に `engineering` や他部署のドキュメントが漏洩（リーク）していないか検証できます。
> - **ハルシネーション（非該当）テスト**: ナレッジに存在しない質問（`expected_doc_ids: []`）を含めることで、LLM が嘘をつかず「ナレッジに記載がありません」と誠実に答えているか検証できます。

---

## 3. CLI ツールの使い方

### 3.1 コマンドライン引数一覧

```bash
go run ./cmd/eval [フラグ]
```

| フラグ | デフォルト値 | 説明 |
|---|---|---|
| `-dataset` | `testdata/eval/sample_dataset.jsonl` | 評価データセット (JSONL) のファイルパス |
| `-mode` | `e2e` | `retrieve` (検索のみ) または `e2e` (検索 & 回答生成・採点) |
| `-models` | 設定のモデル一覧 | 比較したいモデルID（カンマ区切りで複数指定可能） |
| `-judge-model` | (なし: ルールベース) | LLM-as-a-Judge で採点に使用する Bedrock モデルID |
| `-output-dir` | `./eval_reports` | レポート出力先ディレクトリ |
| `-top-k` | 設定値 (例: 5) | ナレッジ検索の取得件数 Top-K |
| `-threshold` | 設定値 (例: 0.30) | 類似度スコア閾値 |
| `-dry-run` | `false` | AWS API を呼び出さずモックデータで即時動作確認 |

---

### 3.2 実行例

#### ① 検索エンジン（Retriever）単体評価
LLM 推論を行わず、Bedrock KB の検索ヒット率と権限分離のみを高速・低コストで確認します。
```bash
go run ./cmd/eval -dataset ./testdata/eval/sample_dataset.jsonl -mode retrieve
```

#### ② 複数モデルの品質・速度比較（ルールベース判定）
Judge モデルを指定しない場合、追加の LLM コストや待ち時間なしで、レイテンシ・トークン消費・引用フォーマット遵守率・単語一致度を比較します。
```bash
go run ./cmd/eval \
  -dataset ./testdata/eval/sample_dataset.jsonl \
  -models "jp.amazon.nova-2-lite-v1:0,jp.anthropic.claude-haiku-5-5" \
  -output-dir ./eval_reports
```

#### ③ LLM-as-a-Judge による厳密な品質比較レポート
Judge 用モデル（例: `jp.anthropic.claude-haiku-5-5` や Claude 3.5 Sonnet 等）を指定し、各モデルの回答の「忠実度（ハルシネーションのなさ）」「適合度」「模範解答との合致度」を 1.0 〜 5.0 点で自動採点します。
```bash
go run ./cmd/eval \
  -dataset ./testdata/eval/sample_dataset.jsonl \
  -models "jp.amazon.nova-2-lite-v1:0,jp.anthropic.claude-haiku-5-5" \
  -judge-model "jp.anthropic.claude-haiku-5-5" \
  -output-dir ./eval_reports
```

#### ④ ローカル環境での動作確認（ドライラン）
AWS 認証情報がない環境や CI パイプラインでのシンタックス・レポート出力検証用です。
```bash
go run ./cmd/eval -dry-run
```

---

## 4. 出力レポートの構成

実行完了後、指定したディレクトリ（`./eval_reports`）に以下の3つのファイルがタイムスタンプ付きで自動生成されます。

1. **`eval_report_YYYYMMDD_HHMMSS.html`**:
   - 外部 CDN 不要の自己完結型 HTML レポート。
   - ブラウザで開くだけで、見やすいダッシュボード形式でモデル間比較表やテストケースごとの回答・採点理由を展開して閲覧できます。
2. **`eval_report_YYYYMMDD_HHMMSS.md`**:
   - Pull Request やチャットツール（Slack/Teams 等）にそのまま貼り付け可能な Markdown 形式のレポート。
3. **`eval_report_YYYYMMDD_HHMMSS.json`**:
   - 集計前の全ケース・全モデル実行生データ。他の分析ツールへの連携に利用できます。

---

## 5. 評価指標の解釈と判断基準

| 指標 | 理想値 | 判断基準・アクション |
|---|:---:|---|
| **Hit@K** | 100% | 低い場合、検索クエリの自然文フォールバック、Embedding モデルの見直し、チャンキングサイズの調整を検討します。 |
| **権限分離リーク数** | 0 件 | 1 件でも発生した場合、解決すべきインシデント（グループフィルタの設定不備）として調査が必要です。 |
| **平均レイテンシ** | 要件準拠 | ユーザー対話における応答速度。Nova 2 Lite など高速モデルの優位性を測定します。 |
| **引用遵守率** | 100% | 回答中に `[1]` などの引用番号があり、末尾に「### 根拠資料」リンクが正しく記載されているか。 |
| **忠実度 (Faithfulness)** | 4.5以上 / 5.0 | 4.0 未満の場合、モデルがコンテキストに書かれていない虚偽情報（ハルシネーション）を生成しているリスクがあります。 |
| **正確性 (Correctness)** | 4.5以上 / 5.0 | 模範解答との合致度。業務規定などの厳密な解釈が求められる場面で重要となります。 |


# ダッシュボード改善: 利用推移・コスト分析の期間切り替え

## Context

「利用推移 & コスト分析」は直近14日固定で、API キー別・モデル別集計と数値サマリは全期間の累計になっている。そのため月次の確認・社内按分ができず、CSV も最新 5,000 件で打ち切られ期間指定もできない。

さらに、日次集計は UTC で日付を切っている。`created_at` は `2026-10-19 12:50:54.8+09:00` の形式で保存され、SQLite の `strftime` はこれを UTC に変換する（一方でグラフの日付ラベルはローカル時刻で生成）。このため JST 0〜9 時台の利用は前日に計上される。月次にすると月初 9 時間分が前月に入るので、今回あわせて修正する。

ゴール: 期間を切り替えると、グラフ・数値サマリ・集計表・CSV がすべて同じ期間で揃い、月次の按分がこの画面だけで完結すること。

## 決定事項

- 既定の期間は「今月」。
- 選択肢: 今月 / 先月 / 月指定 / 直近30日 / 直近12ヶ月（月次）/ 任意期間。「直近14日」は廃止（任意期間で代替可能）。
- 期間に連動するもの: グラフ、数値サマリ、API キー別・モデル別集計、監査ログ（期間内の最新10件）、CSV。
- 全期間のまま残すもの: 上部 KPI「累計概算コスト」。
- グループ別コストは対象外（按分ルール未定）。現行タブはドキュメント数のみで実態と合わないので、見出しだけ修正する。

## 実装

### 1. 期間の解釈 — 新規 `internal/api/web/dashboard_period.go`

`parseUsagePeriod(q url.Values, now time.Time) (usagePeriod, error)` を追加する。

- クエリ: `period=this_month|last_month|month|last_30d|last_12m|custom`、`month=YYYY-MM`、`from` / `to=YYYY-MM-DD`。未指定は `this_month`。
- `usagePeriod` の内容: 種別、半開区間 `From` / `To`（`YYYY-MM-DD` 文字列、`To` は翌日）、粒度（day / month）、表示ラベル、比較用の前期間。
- 粒度: 92日以下は日次、それを超えると月次。任意期間の上限は 3年、`to` は今日までに丸める。
- 前期間:
  - 今月: 前月の同じ日数分（`to` の1ヶ月前と `from` の小さい方で丸め、31日問題を回避）
  - 先月・月指定: その前月の全体
  - 直近30日・任意期間: 直前の同じ長さ
  - 直近12ヶ月: その前の12ヶ月
- 不正な値: 画面は「今月」へフォールバックして警告を表示、CSV は 400 を返す。

### 2. 集計 — `internal/api/web/dashboard_handlers.go`

- 集計部分を `loadUsageData(p usagePeriod) map[string]interface{}` に切り出し、全画面と部分更新で共用する。
- 日付の切り方を `strftime` から `substr(created_at, 1, 10)`（月次は `1, 7`）に変更する。保存文字列はローカル時刻なのでそのまま暦日になり、既存データの移行は不要。
- 絞り込みは `created_at >= ? AND created_at < ?` に日付文字列を渡す（既存インデックスが効く）。理由をコードコメントに残す。
- `last_used_at` も `strftime` で UTC 表示になっているため `substr(MAX(created_at), 1, 19)` に修正する。
- 系列はゼロ埋めする。14 固定のループを期間と粒度に応じた生成に置き換える。
- 今月のグラフは今日までを表示する（月末まで空欄にしない）。
- 前期間のコスト・リクエスト数を集計し、増減率を渡す（前期間が 0 なら `-` 表示）。
- テスト用に `DashboardHandler.now func() time.Time` を追加する（既定は `time.Now`）。
- `DailyUsageStat` / `DailyStatsJSON` は月次も扱うので `UsagePoint` / `UsageSeriesJSON` に改名し、`internal/views/render_test.go` も更新する。
- `HX-Request` かつ `HX-Target: dashboard-usage` の場合は部分テンプレートのみ返す（`zero_hit_handlers.go:66` と同じパターン）。管理者のみ。非管理者は通常のページを返し、利用データは含めない。

### 3. CSV — `ExportCSV`

- 画面と同じクエリを受け取り、`parseUsagePeriod` で絞り込む。
- `Limit(5000)` を廃止する（按分用途で黙って切り捨てないため）。`Rows()` + `ScanRows` で1行ずつ書き出し、全件をメモリに載せない。
- ファイル名を `mini_aigw_usage_<from>_<to>.csv` にする。列は変更しない。

### 4. 画面

- 新規 `web/templates/partials/dashboard_usage.html` に `dashboard.html:147-410` の利用推移と集計を移し、`<div id="dashboard-usage">` 内で `{{template "dashboard_usage.html" .}}` として読み込む（`knowledge_table.html` と同じパターン）。
- 期間セレクターは部分テンプレート内に置き、`hx-get="{{basePath}}/dashboard"`、`hx-target="#dashboard-usage"`、`hx-push-url="true"` を指定する（URL で共有・再読み込みできる）。プリセットは選択で即時反映、任意期間は「適用」ボタンで反映する。Bootstrap のフォーム部品のみ使用し、月指定は `<input type="month">`、任意期間は `<input type="date">`。
- 見出しを「利用推移 & コスト分析」+ 期間ラベルに変更する（既存テストの文字列は維持）。説明文の「日次」も粒度に応じて変える。
- 数値サマリを期間表示に変更: 概算コスト（前期間比つき）、リクエスト数、RAG 検索数、消費トークン。表の見出しの「累計」も「期間内」に直す。
- グループタブは「グループ別ドキュメント数」に改名し、期間に連動しない旨を注記する。カードの説明文から「所属グループごと」を削除する。
- チャート:
  - データは canvas の `data-series` 属性で渡す（html/template の二重エスケープを避ける）。
  - スクリプトは `dashboard.html` に残して `renderUsageChart()` に関数化し、`DOMContentLoaded`、`htmx:afterSettle`（対象が `dashboard-usage`）、`htmx:historyRestore` で呼ぶ。
  - 描画前に `Chart.getChart(canvas)?.destroy()` で既存チャートを破棄する。
  - トークン軸に `M` 表記を追加する。

### 5. ドキュメント

`docs/ROADMAP.md:54` の「直近14日間」と `README.md` のダッシュボード節（241行目〜）を更新する。集計の日付境界はサーバーのタイムゾーンに従うため、`TZ` を運用地域に合わせる必要がある旨も README に書く。

## テスト

`internal/api/web/dashboard_period_test.go`（新規。`now` は固定）:

- 全プリセット、年またぎ（1月の先月）、粒度の切り替わり（92 / 93日）
- 31日の前期間丸め、不正な入力、`to` の丸め

`internal/api/web/dashboard_handlers_test.go`（追加）:

- `+09:00` の `2026-10-01 00:30` のログが 09/30 ではなく 10/01・10月に計上される（UTC 不具合の回帰テスト）
- 期間外のログが KPI・API キー別・モデル別から除外され、累計概算コストには含まれる
- ゼロ埋めの件数、月次粒度
- HX ヘッダー付きで部分のみ返り、非管理者には利用データが出ない
- CSV が期間で絞り込まれる、5,000 件超でも欠けない、不正な期間で 400 を返す

## 検証

1. `go vet ./...` と `go test ./...`
2. サーバーを起動し、ログのある環境で管理者としてブラウザ確認する:
   - 各プリセットの切り替えでグラフと表が揃って更新されること
   - URL の再読み込み、戻る操作でチャートが表示されること
   - CSV の合計が画面の値と一致すること
3. 非管理者では利用データが表示されないこと
4. 外部ドメインへの通信が発生していないこと

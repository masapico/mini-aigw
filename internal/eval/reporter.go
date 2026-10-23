package eval

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// GenerateMarkdownReport は Markdown 形式の評価レポートを生成します
func GenerateMarkdownReport(r *EvalReportData) string {
	var sb strings.Builder

	sb.WriteString("# mini-aigw RAG 品質評価 & モデル比較レポート\n\n")
	sb.WriteString(fmt.Sprintf("- **実行日時**: %s\n", r.Timestamp.Format("2006-01-02 15:04:05 MST")))
	sb.WriteString(fmt.Sprintf("- **データセット**: `%s` (全 %d 件)\n", r.DatasetPath, r.TotalCases))
	sb.WriteString(fmt.Sprintf("- **検索設定**: Top-K=%d, ScoreThreshold=%.2f\n", r.TopK, r.ScoreThreshold))
	if r.JudgeModelID != "" {
		sb.WriteString(fmt.Sprintf("- **Judge モデル**: `%s`\n", r.JudgeModelID))
	} else {
		sb.WriteString("- **Judge モデル**: 未指定 (ルールベース自動採点)\n")
	}
	sb.WriteString("\n---\n\n")

	// 1. モデル比較サマリー（E2E時）
	if len(r.ModelSummaries) > 0 {
		sb.WriteString("## 1. モデル別 総合比較サマリー\n\n")
		sb.WriteString("| モデルID | 平均レイテンシ | 平均入力/出力トークン | 引用遵守率 | 忠実度 (Faithfulness) | 適合度 (Relevance) | 正確性 (Correctness) | 成功率 |\n")
		sb.WriteString("|---|---|---|---|---|---|---|---|\n")

		for _, modelID := range r.Models {
			if s, ok := r.ModelSummaries[modelID]; ok {
				sb.WriteString(fmt.Sprintf("| **%s** | **%d ms** | %d / %d tok | %.1f%% | **%.1f / 5.0** | %.1f / 5.0 | %.1f / 5.0 | %.1f%% |\n",
					modelID, s.AvgLatencyMs, s.AvgInputTokens, s.AvgOutputTokens, s.CitationPassRate, s.AvgFaithfulness, s.AvgRelevance, s.AvgCorrectness, s.SuccessRate))
			}
		}
		sb.WriteString("\n---\n\n")
	}

	// 2. Retriever 単体評価サマリー
	sb.WriteString("## 2. ナレッジ検索 (Retriever) 単体評価結果\n\n")
	sb.WriteString(fmt.Sprintf("- **Hit@%d 達成率**: **%.1f%%** (%d 件中)\n", r.TopK, r.HitAtKRate, r.TotalCases))
	sb.WriteString(fmt.Sprintf("- **MRR (Mean Reciprocal Rank)**: **%.2f** (1.00 が最良)\n", r.MeanReciprocalRank))
	if r.TotalLeaks == 0 {
		sb.WriteString("- **権限分離 (Permission Isolation)**: ✅ **0 件 (完全分離確認)**\n")
	} else {
		sb.WriteString(fmt.Sprintf("- **権限分離 (Permission Isolation)**: ⚠️ **%d 件のグループ外リークを検出**\n", r.TotalLeaks))
	}
	sb.WriteString("\n---\n\n")

	// 3. テストケース別 詳細
	sb.WriteString("## 3. テストケース別 詳細結果\n\n")
	for i, c := range r.CaseResults {
		sb.WriteString(fmt.Sprintf("### [%d] %s (ID: %s, Group: `%s`)\n\n", i+1, c.TestCase.Query, c.TestCase.ID, c.TestCase.Group))

		// 検索ヒット
		sb.WriteString("**【検索結果】**:\n")
		sb.WriteString(fmt.Sprintf("- Hit@%d: %s | MRR: %.2f | 権限分離: %s\n",
			r.TopK, formatBoolBadgeMD(c.HitAtK), c.ReciprocalRank, formatBoolBadgeMD(c.IsIsolated)))
		if len(c.RetrievedFilenames) > 0 {
			sb.WriteString(fmt.Sprintf("- 取得ファイル: `%s`\n", strings.Join(c.RetrievedFilenames, "`, `")))
		} else {
			sb.WriteString("- 取得ファイル: (なし)\n")
		}

		if c.TestCase.ReferenceAnswer != "" {
			sb.WriteString(fmt.Sprintf("- **模範回答**: %s\n", c.TestCase.ReferenceAnswer))
		}
		sb.WriteString("\n")

		// モデル別結果
		if len(c.ModelResults) > 0 {
			sb.WriteString("| モデル | レイテンシ | トークン | 忠実度 | 適合度 | 正確性 | 引用遵守 |\n")
			sb.WriteString("|---|---|---|---|---|---|---|\n")

			for _, modelID := range r.Models {
				if mRes, ok := c.ModelResults[modelID]; ok {
					if mRes.Error != "" {
						sb.WriteString(fmt.Sprintf("| **%s** | ERROR | - | - | - | - | %s |\n", modelID, mRes.Error))
					} else {
						sb.WriteString(fmt.Sprintf("| **%s** | %d ms | %d in / %d out | %.1f | %.1f | %.1f | %s |\n",
							modelID, mRes.LatencyMs, mRes.InputTokens, mRes.OutputTokens,
							mRes.Judge.Faithfulness, mRes.Judge.AnswerRelevance, mRes.Judge.Correctness,
							formatBoolBadgeMD(mRes.CitationPass)))
					}
				}
			}

			sb.WriteString("\n<details><summary>各モデルの生成回答を展開</summary>\n\n")
			for _, modelID := range r.Models {
				if mRes, ok := c.ModelResults[modelID]; ok && mRes.AnswerText != "" {
					sb.WriteString(fmt.Sprintf("#### モデル: %s\n```text\n%s\n```\n\n", modelID, mRes.AnswerText))
				}
			}
			sb.WriteString("</details>\n\n")
		}
		sb.WriteString("---\n\n")
	}

	return sb.String()
}

// GenerateHTMLReport は閉域環境でも単独で動作するスタンドアロン HTML レポートを生成します
func GenerateHTMLReport(r *EvalReportData) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>mini-aigw RAG 品質評価 & モデル比較レポート</title>
<style>
:root {
  --bg-color: #f8fafc;
  --text-color: #1e293b;
  --card-bg: #ffffff;
  --border-color: #e2e8f0;
  --primary: #0284c7;
  --success: #16a34a;
  --warning: #d97706;
  --danger: #dc2626;
}
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  background-color: var(--bg-color);
  color: var(--text-color);
  margin: 0;
  padding: 2rem;
  line-height: 1.5;
}
.container { max-width: 1200px; margin: 0 auto; }
h1, h2, h3 { color: #0f172a; margin-top: 1.5rem; }
.meta-box {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 1rem 1.5rem;
  margin-bottom: 2rem;
}
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
.card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 1.25rem;
  box-shadow: 0 1px 3px rgba(0,0,0,0.05);
}
.card-title { font-size: 0.875rem; color: #64748b; font-weight: 600; text-transform: uppercase; }
.card-value { font-size: 1.75rem; font-weight: 700; color: #0f172a; margin-top: 0.25rem; }
table {
  width: 100%;
  border-collapse: collapse;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  overflow: hidden;
  margin-bottom: 2rem;
}
th, td { padding: 0.75rem 1rem; text-align: left; border-bottom: 1px solid var(--border-color); }
th { background-color: #f1f5f9; font-weight: 600; color: #334155; }
tr:last-child td { border-bottom: none; }
.badge {
  display: inline-block;
  padding: 0.25rem 0.5rem;
  border-radius: 9999px;
  font-size: 0.75rem;
  font-weight: 600;
}
.badge-success { background-color: #dcfce7; color: #15803d; }
.badge-danger { background-color: #fee2e2; color: #b91c1c; }
.badge-info { background-color: #e0f2fe; color: #0369a1; }
.case-card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 1.25rem;
  margin-bottom: 1.5rem;
}
pre {
  background: #0f172a;
  color: #f8fafc;
  padding: 1rem;
  border-radius: 6px;
  overflow-x: auto;
  font-size: 0.875rem;
  white-space: pre-wrap;
}
details { margin-top: 0.75rem; }
summary { cursor: pointer; font-weight: 600; color: var(--primary); }
</style>
</head>
<body>
<div class="container">
<h1>📊 mini-aigw RAG 品質評価 & モデル比較レポート</h1>
<div class="meta-box">
  <div><strong>実行日時:</strong> ` + html.EscapeString(r.Timestamp.Format("2006-01-02 15:04:05 MST")) + `</div>
  <div><strong>データセット:</strong> <code>` + html.EscapeString(r.DatasetPath) + `</code> (` + fmt.Sprintf("%d", r.TotalCases) + ` 件)</div>
  <div><strong>検索パラメータ:</strong> Top-K=` + fmt.Sprintf("%d", r.TopK) + `, ScoreThreshold=` + fmt.Sprintf("%.2f", r.ScoreThreshold) + `</div>
  <div><strong>Judge モデル:</strong> <code>` + html.EscapeString(r.JudgeModelID) + `</code></div>
</div>

<h2>Retriever サマリー</h2>
<div class="grid">
  <div class="card" title="ナレッジ検索（Retriever）で正解ドキュメントを上位 K 件（Top-K）以内に取得できた割合。検索再現率を示します。">
    <div class="card-title">🎯 Hit@` + fmt.Sprintf("%d", r.TopK) + ` 達成率</div>
    <div class="card-value">` + fmt.Sprintf("%.1f%%", r.HitAtKRate) + `</div>
  </div>
  <div class="card" title="Mean Reciprocal Rank（平均逆順位）。最良の正解ドキュメントが何位に現れたかの逆数平均（1位=1.00、2位=0.50）。1.00に近いほど上位ランクインです。">
    <div class="card-title">🏆 MRR (平均逆順位)</div>
    <div class="card-value">` + fmt.Sprintf("%.2f", r.MeanReciprocalRank) + `</div>
  </div>
  <div class="card" title="質問者の所属グループ以外のドキュメントが検索結果に混入（漏洩）した件数。マルチテナント権限分離が完全なら 0 件です。">
    <div class="card-title">🛡️ 権限分離リーク数</div>
    <div class="card-value">` + fmt.Sprintf("%d 件", r.TotalLeaks) + `</div>
  </div>
</div>
` + "\n")

	// モデル比較表
	if len(r.ModelSummaries) > 0 {
		sb.WriteString(`<h2>モデル別 総合品質・速度比較</h2>
<table>
<thead>
<tr>
  <th title="評価対象のLLMモデル名">🤖 モデルID</th>
  <th title="プロンプト送信から回答生成完了までの平均時間（ミリ秒）">⏱️ 平均レイテンシ</th>
  <th title="プロンプト入力トークン数 / 回答生成出力トークン数の平均">📝 平均トークン (In / Out)</th>
  <th title="回答内で参照ナレッジの引用記法（[doc_1] 等）が正しく付与されている割合">💬 引用遵守率</th>
  <th title="回答が検索コンテキスト（取得ドキュメント）の事実に基づいているかのスコア (1.0〜5.0)。ハルシネーションの少なさを示します。">✅ 忠実度 (Faithfulness)</th>
  <th title="回答がユーザーの質問意図に対して的確・過不足なく答えているかのスコア (1.0〜5.0)">🎯 適合度 (Relevance)</th>
  <th title="模範解答（Ground Truth）の正解事実と整合しているかのスコア (1.0〜5.0)">✔️ 正確性 (Correctness)</th>
  <th title="正常に回答・評価を完了できたテストケースの割合">📊 成功率</th>
</tr>
</thead>
<tbody>`)
		for _, modelID := range r.Models {
			if s, ok := r.ModelSummaries[modelID]; ok {
				sb.WriteString(fmt.Sprintf(`
<tr>
  <td><strong>%s</strong></td>
  <td><span class="badge badge-info">%d ms</span></td>
  <td>%d / %d</td>
  <td>%.1f%%</td>
  <td><strong>%.1f</strong> / 5.0</td>
  <td>%.1f / 5.0</td>
  <td>%.1f / 5.0</td>
  <td>%.1f%%</td>
</tr>`, html.EscapeString(modelID), s.AvgLatencyMs, s.AvgInputTokens, s.AvgOutputTokens, s.CitationPassRate, s.AvgFaithfulness, s.AvgRelevance, s.AvgCorrectness, s.SuccessRate))
			}
		}
		sb.WriteString(`</tbody></table>`)
	}

	// ケース一覧
	sb.WriteString(`<h2>テストケース別 詳細一覧</h2>`)
	for i, c := range r.CaseResults {
		hitBadge := `<span class="badge badge-danger" title="上位K件内に正解ドキュメントが見つかりませんでした">MISS</span>`
		if c.HitAtK {
			hitBadge = `<span class="badge badge-success" title="上位K件内に正解ドキュメントが含まれています">HIT</span>`
		}
		isoBadge := `<span class="badge badge-danger" title="権限分離違反: 他グループのドキュメントが混入しています">LEAK</span>`
		if c.IsIsolated {
			isoBadge = `<span class="badge badge-success" title="権限分離正常: 他グループのドキュメント混入なし">ISOLATED</span>`
		}

		sb.WriteString(fmt.Sprintf(`
<div class="case-card">
  <h3>[%d] %s</h3>
  <div><strong>ID:</strong> %s | <strong>Group:</strong> <code>%s</code> | <strong>Retriever:</strong> %s | <strong>権限分離:</strong> %s</div>
  <div style="margin-top: 0.5rem;"><strong>取得ファイル:</strong> <code>%s</code></div>`,
			i+1, html.EscapeString(c.TestCase.Query), html.EscapeString(c.TestCase.ID), html.EscapeString(c.TestCase.Group), hitBadge, isoBadge, html.EscapeString(strings.Join(c.RetrievedFilenames, ", "))))

		if c.TestCase.ReferenceAnswer != "" {
			sb.WriteString(`<div style="margin-top: 0.5rem; color: #475569;"><strong>模範解答:</strong> ` + html.EscapeString(c.TestCase.ReferenceAnswer) + `</div>`)
		}

		if len(c.ModelResults) > 0 {
			sb.WriteString(`
  <table style="margin-top: 1rem; margin-bottom: 0.5rem;">
  <thead>
  <tr>
    <th title="評価対象モデル">🤖 モデル</th>
    <th title="生成レイテンシ">⏱️ レイテンシ</th>
    <th title="入力 / 出力トークン">📝 トークン</th>
    <th title="検索コンテキストの事実に基づいているか (1.0〜5.0)">✅ 忠実度</th>
    <th title="質問意図に的確に答えているか (1.0〜5.0)">🎯 適合度</th>
    <th title="模範解答と整合しているか (1.0〜5.0)">✔️ 正確性</th>
    <th title="引用記法を遵守しているか">💬 引用遵守</th>
  </tr>
  </thead>
  <tbody>`)
			for _, modelID := range r.Models {
				if mRes, ok := c.ModelResults[modelID]; ok {
					citBadge := `<span class="badge badge-danger" title="引用記法が欠落しているか不適切です">FAIL</span>`
					if mRes.CitationPass {
						citBadge = `<span class="badge badge-success" title="引用記法が正しく遵守されています">PASS</span>`
					}
					if mRes.Error != "" {
						sb.WriteString(fmt.Sprintf(`<tr><td><strong>%s</strong></td><td colspan="6"><span class="badge badge-danger">ERROR: %s</span></td></tr>`, html.EscapeString(modelID), html.EscapeString(mRes.Error)))
					} else {
						sb.WriteString(fmt.Sprintf(`
  <tr>
    <td><strong>%s</strong></td>
    <td>%d ms</td>
    <td>%d in / %d out</td>
    <td>%.1f</td>
    <td>%.1f</td>
    <td>%.1f</td>
    <td>%s</td>
  </tr>`, html.EscapeString(modelID), mRes.LatencyMs, mRes.InputTokens, mRes.OutputTokens, mRes.Judge.Faithfulness, mRes.Judge.AnswerRelevance, mRes.Judge.Correctness, citBadge))
					}
				}
			}
			sb.WriteString(`</tbody></table>
  <details>
    <summary>モデルごとの生成回答・採点理由を表示</summary>`)
			for _, modelID := range r.Models {
				if mRes, ok := c.ModelResults[modelID]; ok && mRes.AnswerText != "" {
					sb.WriteString(fmt.Sprintf(`
    <h4 style="margin-top: 1rem;">%s:</h4>
    <pre>%s</pre>
    <div style="font-size: 0.85rem; color: #64748b;"><em>Judge 採点理由: %s</em></div>`, html.EscapeString(modelID), html.EscapeString(mRes.AnswerText), html.EscapeString(mRes.Judge.Reasoning)))
				}
			}
			sb.WriteString(`
  </details>`)
		}

		sb.WriteString(`</div>`)
	}

	sb.WriteString(`</div></body></html>`)
	return sb.String()
}

// GenerateJSONReport は JSON 形式のレポートを生成します
func GenerateJSONReport(r *EvalReportData) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// SaveReports は指定ディレクトリに Markdown, HTML, JSON の3形式でレポートを保存します
func SaveReports(r *EvalReportData, outputDir string) ([]string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create report output dir '%s': %w", outputDir, err)
	}

	timePrefix := r.Timestamp.Format("20060102_150405")
	baseName := fmt.Sprintf("eval_report_%s", timePrefix)

	mdPath := filepath.Join(outputDir, baseName+".md")
	htmlPath := filepath.Join(outputDir, baseName+".html")
	jsonPath := filepath.Join(outputDir, baseName+".json")

	// Markdown
	mdContent := GenerateMarkdownReport(r)
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write markdown report: %w", err)
	}

	// HTML
	htmlContent := GenerateHTMLReport(r)
	if err := os.WriteFile(htmlPath, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write HTML report: %w", err)
	}

	// JSON
	jsonContent, err := GenerateJSONReport(r)
	if err != nil {
		return nil, fmt.Errorf("failed to encode JSON report: %w", err)
	}
	if err := os.WriteFile(jsonPath, jsonContent, 0644); err != nil {
		return nil, fmt.Errorf("failed to write JSON report: %w", err)
	}

	return []string{mdPath, htmlPath, jsonPath}, nil
}

func formatBoolBadgeMD(b bool) string {
	if b {
		return "✅ PASS"
	}
	return "❌ FAIL"
}

package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDataset_Load(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "test.jsonl")

	content := `{"id":"tc-1","query":"有給休暇","group":"public","expected_doc_ids":["doc-1"],"expected_filenames":["就業規則.pdf"]}
# コメント行
{"query":"営業日当","expected_filenames":["旅費規程.docx"],"reference_answer":"日当2000円"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create test jsonl: %v", err)
	}

	cases, err := LoadDataset(jsonlPath)
	if err != nil {
		t.Fatalf("LoadDataset failed: %v", err)
	}

	if len(cases) != 2 {
		t.Fatalf("expected 2 test cases, got %d", len(cases))
	}

	if cases[0].ID != "tc-1" || cases[0].Group != "public" {
		t.Errorf("unexpected case 1: %+v", cases[0])
	}
	if cases[1].ID != "tc-003" || cases[1].Group != "public" { // 3行目のため tc-003
		t.Errorf("unexpected case 2 auto-id or default group: %+v", cases[1])
	}
}

func TestMetrics_HitAndMRR(t *testing.T) {
	retDocIDs := []string{"doc-other", "doc-1", "doc-2"}
	retFilenames := []string{"other.pdf", "就業規則.pdf", "memo.txt"}

	expDocIDs := []string{"doc-1"}
	expFilenames := []string{"就業規則.pdf"}

	// Hit@1 は false (1位は doc-other)
	if CalculateHitAtK(retDocIDs, retFilenames, expDocIDs, expFilenames, 1) {
		t.Errorf("expected Hit@1 to be false")
	}

	// Hit@2 は true (2位に doc-1)
	if !CalculateHitAtK(retDocIDs, retFilenames, expDocIDs, expFilenames, 2) {
		t.Errorf("expected Hit@2 to be true")
	}

	// MRR は 1/2 = 0.5
	rr := CalculateReciprocalRank(retDocIDs, retFilenames, expDocIDs, expFilenames)
	if rr != 0.5 {
		t.Errorf("expected RR 0.5, got %f", rr)
	}

	// 該当なしケース（期待値が空で検索結果も空なら Hit/MRR=1.0）
	if !CalculateHitAtK(nil, nil, nil, nil, 5) {
		t.Errorf("expected empty search with empty expectation to hit")
	}
	if CalculateReciprocalRank(nil, nil, nil, nil) != 1.0 {
		t.Errorf("expected empty search with empty expectation to have MRR 1.0")
	}
}

func TestMetrics_PermissionIsolation(t *testing.T) {
	retGroups := []string{"public", "sales", "public"}
	leaked, isolated := CheckPermissionIsolation(retGroups, "sales")
	if !isolated || len(leaked) > 0 {
		t.Errorf("expected isolated, got leaked: %v", leaked)
	}

	retGroupsWithLeak := []string{"public", "finance", "sales"}
	leaked2, isolated2 := CheckPermissionIsolation(retGroupsWithLeak, "sales")
	if isolated2 || len(leaked2) != 1 || leaked2[0] != "finance" {
		t.Errorf("expected finance leak, got: %v (isolated: %v)", leaked2, isolated2)
	}
}

func TestMetrics_CitationCompliance(t *testing.T) {
	// 正常系 (引用あり)
	goodAnswer := "有給休暇は年10日付与されます[1]。\n\n### 根拠資料 (参照ドキュメント)\n- [就業規則.pdf](http://example.com/dl)"
	pass, details := CheckCitationCompliance(goodAnswer, true)
	if !pass {
		t.Errorf("expected pass, got fail: %s", details)
	}

	// 異常系: 引用番号なし
	noBracket := "有給休暇は年10日付与されます。\n\n### 根拠資料\n- 就業規則"
	pass2, _ := CheckCitationCompliance(noBracket, true)
	if pass2 {
		t.Errorf("expected fail due to missing bracket")
	}

	// 検索ヒットなし時の「該当なし」説明
	noHitAnswer := "提供されたナレッジに記載がありませんでした。"
	pass3, _ := CheckCitationCompliance(noHitAnswer, false)
	if !pass3 {
		t.Errorf("expected pass for no hit explanation")
	}
}

func TestReporter_GenerateReports(t *testing.T) {
	report := &EvalReportData{
		DatasetPath:        "test.jsonl",
		Timestamp:          time.Now(),
		TopK:               5,
		ScoreThreshold:     0.30,
		JudgeModelID:       "test-judge",
		Models:             []string{"model-a", "model-b"},
		TotalCases:         1,
		HitAtKRate:         100.0,
		MeanReciprocalRank: 1.0,
		TotalLeaks:         0,
		ModelSummaries: map[string]*ModelSummary{
			"model-a": {
				ModelID:          "model-a",
				AvgLatencyMs:     400,
				AvgInputTokens:   100,
				AvgOutputTokens:  50,
				CitationPassRate: 100.0,
				AvgFaithfulness:  5.0,
				AvgRelevance:     5.0,
				AvgCorrectness:   5.0,
				SuccessRate:      100.0,
			},
		},
		CaseResults: []*TestCaseResult{
			{
				TestCase: TestCase{
					ID:              "tc-01",
					Query:           "有休について",
					Group:           "public",
					ReferenceAnswer: "10日",
				},
				RetrievedFilenames: []string{"就業規則.pdf"},
				HitAtK:             true,
				ReciprocalRank:     1.0,
				IsIsolated:         true,
				ModelResults: map[string]*ModelTestCaseResult{
					"model-a": {
						ModelID:         "model-a",
						AnswerText:      "10日です[1]",
						LatencyMs:       400,
						InputTokens:     100,
						OutputTokens:    50,
						CitationPass:    true,
						CitationDetails: "OK",
						Judge: JudgeScore{
							Faithfulness:    5.0,
							AnswerRelevance: 5.0,
							Correctness:     5.0,
							Reasoning:       "Perfect",
						},
					},
				},
			},
		},
	}

	// Markdown
	md := GenerateMarkdownReport(report)
	if !strings.Contains(md, "mini-aigw RAG 品質評価 & モデル比較レポート") || !strings.Contains(md, "model-a") {
		t.Errorf("markdown report missing expected content: %s", md)
	}

	// HTML
	htmlContent := GenerateHTMLReport(report)
	if !strings.Contains(htmlContent, "<html") || !strings.Contains(htmlContent, "model-a") {
		t.Errorf("html report missing expected tags: %s", htmlContent)
	}

	// SaveReports
	tempDir := t.TempDir()
	paths, err := SaveReports(report, tempDir)
	if err != nil {
		t.Fatalf("SaveReports failed: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(paths))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file does not exist: %s", p)
		}
	}
}


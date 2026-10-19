package eval

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"mini-aigw/internal/services"
)

// ModelTestCaseResult は単一モデルの特定テストケースに対する実行結果です
type ModelTestCaseResult struct {
	ModelID         string     `json:"model_id"`
	AnswerText      string     `json:"answer_text"`
	LatencyMs       int64      `json:"latency_ms"`
	InputTokens     int        `json:"input_tokens"`
	OutputTokens    int        `json:"output_tokens"`
	CitationPass    bool       `json:"citation_pass"`
	CitationDetails string     `json:"citation_details"`
	Judge           JudgeScore `json:"judge"`
	Error           string     `json:"error,omitempty"`
}

// TestCaseResult は1つのテストケースに対する Retriever および各モデルの評価結果です
type TestCaseResult struct {
	TestCase           TestCase                        `json:"test_case"`
	RetrievedDocIDs    []string                        `json:"retrieved_doc_ids"`
	RetrievedFilenames []string                        `json:"retrieved_filenames"`
	RetrievedGroups    []string                        `json:"retrieved_groups"`
	FormattedContexts  []string                        `json:"formatted_contexts"`
	HitAtK             bool                            `json:"hit_at_k"`
	ReciprocalRank     float64                         `json:"reciprocal_rank"`
	LeakedGroups       []string                        `json:"leaked_groups"`
	IsIsolated         bool                            `json:"is_isolated"`
	ModelResults       map[string]*ModelTestCaseResult `json:"model_results"`
}

// ModelSummary は特定モデルの全ケース平均メトリクスです
type ModelSummary struct {
	ModelID          string  `json:"model_id"`
	AvgLatencyMs     int64   `json:"avg_latency_ms"`
	AvgInputTokens   int     `json:"avg_input_tokens"`
	AvgOutputTokens  int     `json:"avg_output_tokens"`
	CitationPassRate float64 `json:"citation_pass_rate"`
	AvgFaithfulness  float64 `json:"avg_faithfulness"`
	AvgRelevance     float64 `json:"avg_relevance"`
	AvgCorrectness   float64 `json:"avg_correctness"`
	SuccessRate      float64 `json:"success_rate"`
}

// EvalReportData は評価全体のレポートデータです
type EvalReportData struct {
	DatasetPath        string                   `json:"dataset_path"`
	Timestamp          time.Time                `json:"timestamp"`
	TopK               int                      `json:"top_k"`
	ScoreThreshold     float64                  `json:"score_threshold"`
	JudgeModelID       string                   `json:"judge_model_id"`
	Models             []string                 `json:"models"`
	TotalCases         int                      `json:"total_cases"`
	HitAtKRate         float64                  `json:"hit_at_k_rate"`
	MeanReciprocalRank float64                  `json:"mean_reciprocal_rank"`
	TotalLeaks         int                      `json:"total_leaks"`
	ModelSummaries     map[string]*ModelSummary `json:"model_summaries"`
	CaseResults        []*TestCaseResult        `json:"case_results"`
}

// Runner は RAG 評価を統括・実行する構造体です
type Runner struct {
	pipeline   *services.RAGPipeline
	bedrockSvc *services.BedrockService
	baseURL    string
}

func NewRunner(pipeline *services.RAGPipeline, bedrockSvc *services.BedrockService, baseURL string) *Runner {
	return &Runner{
		pipeline:   pipeline,
		bedrockSvc: bedrockSvc,
		baseURL:    baseURL,
	}
}

// RunOptions は評価実行時の各種オプションです
type RunOptions struct {
	DatasetPath    string
	TestCases      []TestCase
	Models         []string
	JudgeModelID   string
	TopK           int
	ScoreThreshold float64
	Mode           string                                     // "retrieve" or "e2e"
	DryRun         bool                                       // AWS 呼び出しを行わずモック動作
	OnProgress     func(current, total int, query string)     // 進捗通知コールバック
}

// Run は指定されたオプションに従って評価を実行します
func (r *Runner) Run(ctx context.Context, opts RunOptions) (*EvalReportData, error) {
	if len(opts.TestCases) == 0 {
		return nil, fmt.Errorf("no test cases provided")
	}

	report := &EvalReportData{
		DatasetPath:    opts.DatasetPath,
		Timestamp:      time.Now(),
		TopK:           opts.TopK,
		ScoreThreshold: opts.ScoreThreshold,
		JudgeModelID:   opts.JudgeModelID,
		Models:         opts.Models,
		TotalCases:     len(opts.TestCases),
		ModelSummaries: make(map[string]*ModelSummary),
	}

	var totalHitCount int
	var totalRR float64
	var totalLeaksCount int

	for i, tc := range opts.TestCases {
		if opts.OnProgress != nil {
			opts.OnProgress(i+1, len(opts.TestCases), tc.Query)
		}
		log.Printf("[%d/%d] Evaluating query: '%s' (Group: %s)", i+1, len(opts.TestCases), tc.Query, tc.Group)

		caseRes := &TestCaseResult{
			TestCase:     tc,
			ModelResults: make(map[string]*ModelTestCaseResult),
		}

		if opts.DryRun {
			// ドライラン時のモックデータ
			caseRes.RetrievedDocIDs = tc.ExpectedDocIDs
			caseRes.RetrievedFilenames = tc.ExpectedFilenames
			caseRes.RetrievedGroups = []string{tc.Group}
			caseRes.FormattedContexts = []string{"[1] モックコンテキスト内容"}
		} else {
			// 1. Retriever 実行
			retData, err := r.pipeline.RetrieveAndFormat(ctx, tc.Query, tc.Group, opts.TopK, opts.ScoreThreshold, r.baseURL)
			if err != nil {
				log.Printf("  Error during retrieval: %v", err)
			} else {
				caseRes.RetrievedDocIDs = retData.DocIDs
				caseRes.RetrievedFilenames = retData.Filenames
				caseRes.RetrievedGroups = retData.Groups
				caseRes.FormattedContexts = retData.FormattedContexts
			}
		}

		// Retriever 指標計算
		caseRes.HitAtK = CalculateHitAtK(caseRes.RetrievedDocIDs, caseRes.RetrievedFilenames, tc.ExpectedDocIDs, tc.ExpectedFilenames, opts.TopK)
		caseRes.ReciprocalRank = CalculateReciprocalRank(caseRes.RetrievedDocIDs, caseRes.RetrievedFilenames, tc.ExpectedDocIDs, tc.ExpectedFilenames)
		leaked, isolated := CheckPermissionIsolation(caseRes.RetrievedGroups, tc.Group)
		caseRes.LeakedGroups = leaked
		caseRes.IsIsolated = isolated

		if caseRes.HitAtK {
			totalHitCount++
		}
		totalRR += caseRes.ReciprocalRank
		if !isolated {
			totalLeaksCount += len(leaked)
		}

		// 2. E2E モードの場合はモデル推論 & 採点を実行
		if opts.Mode == "e2e" && len(opts.Models) > 0 {
			contextText := strings.Join(caseRes.FormattedContexts, "\n\n")

			for _, modelID := range opts.Models {
				mRes := &ModelTestCaseResult{ModelID: modelID}

				if opts.DryRun {
					// モック推論
					mRes.AnswerText = fmt.Sprintf("これは %s によるテスト回答です[1]。\n\n### 根拠資料\n- [就業規則](http://example.com)", modelID)
					mRes.LatencyMs = 350
					mRes.InputTokens = 250
					mRes.OutputTokens = 120
					mRes.CitationPass = true
					mRes.CitationDetails = "モック正常判定"
					mRes.Judge = *EvaluateRuleBased(tc.Query, contextText, mRes.AnswerText, tc.ReferenceAnswer)
				} else {
					// 実推論
					temp := 0.0
					ans, lat, inTok, outTok, err := r.pipeline.GenerateAnswer(ctx, modelID, tc.Query, caseRes.FormattedContexts, &temp, nil)
					mRes.LatencyMs = lat
					mRes.InputTokens = inTok
					mRes.OutputTokens = outTok

					if err != nil {
						mRes.Error = err.Error()
					} else {
						mRes.AnswerText = ans
						// 引用フォーマット検証
						pass, det := CheckCitationCompliance(ans, len(caseRes.FormattedContexts) > 0)
						mRes.CitationPass = pass
						mRes.CitationDetails = det

						// Judge 評価
						judgeScore, jErr := EvaluateWithLLM(ctx, r.bedrockSvc, opts.JudgeModelID, tc.Query, contextText, ans, tc.ReferenceAnswer)
						if jErr != nil {
							log.Printf("  Judge error for model %s: %v", modelID, jErr)
							mRes.Judge = *EvaluateRuleBased(tc.Query, contextText, ans, tc.ReferenceAnswer)
						} else {
							mRes.Judge = *judgeScore
						}
					}
				}

				caseRes.ModelResults[modelID] = mRes
			}
		}

		report.CaseResults = append(report.CaseResults, caseRes)
	}

	// Retriever サマリー集計
	if report.TotalCases > 0 {
		report.HitAtKRate = (float64(totalHitCount) / float64(report.TotalCases)) * 100.0
		report.MeanReciprocalRank = totalRR / float64(report.TotalCases)
		report.TotalLeaks = totalLeaksCount
	}

	// モデル別サマリー集計
	if opts.Mode == "e2e" {
		for _, modelID := range opts.Models {
			summary := &ModelSummary{ModelID: modelID}
			var totalLat int64
			var totalInTok, totalOutTok int
			var passCitationCount int
			var totalFaith, totalRel, totalCorr float64
			var successCount int

			for _, cRes := range report.CaseResults {
				if mRes, ok := cRes.ModelResults[modelID]; ok {
					if mRes.Error == "" {
						successCount++
						totalLat += mRes.LatencyMs
						totalInTok += mRes.InputTokens
						totalOutTok += mRes.OutputTokens
						if mRes.CitationPass {
							passCitationCount++
						}
						totalFaith += mRes.Judge.Faithfulness
						totalRel += mRes.Judge.AnswerRelevance
						totalCorr += mRes.Judge.Correctness
					}
				}
			}

			if successCount > 0 {
				summary.AvgLatencyMs = totalLat / int64(successCount)
				summary.AvgInputTokens = totalInTok / successCount
				summary.AvgOutputTokens = totalOutTok / successCount
				summary.CitationPassRate = (float64(passCitationCount) / float64(successCount)) * 100.0
				summary.AvgFaithfulness = totalFaith / float64(successCount)
				summary.AvgRelevance = totalRel / float64(successCount)
				summary.AvgCorrectness = totalCorr / float64(successCount)
				summary.SuccessRate = (float64(successCount) / float64(len(report.CaseResults))) * 100.0
			}

			report.ModelSummaries[modelID] = summary
		}
	}

	return report, nil
}


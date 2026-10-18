package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/eval"
	"mini-aigw/internal/services"
)

func main() {
	var (
		datasetPath    string
		mode           string
		modelsFlag     string
		judgeModelID   string
		outputDir      string
		topK           int
		scoreThreshold float64
		dryRun         bool
	)

	flag.StringVar(&datasetPath, "dataset", "testdata/eval/sample_dataset.jsonl", "評価データセット (JSONL) のファイルパス")
	flag.StringVar(&mode, "mode", "e2e", "評価モード: 'retrieve' (検索のみ) または 'e2e' (検索 & 回答生成・採点)")
	flag.StringVar(&modelsFlag, "models", "", "比較対象モデルIDのカンマ区切り (例: 'jp.amazon.nova-2-lite-v1:0,jp.anthropic.claude-haiku-5-5')")
	flag.StringVar(&judgeModelID, "judge-model", "", "LLM-as-a-Judge 用モデルID (空の場合はルールベース採点)")
	flag.StringVar(&outputDir, "output-dir", "./eval_reports", "レポート出力先ディレクトリ")
	flag.IntVar(&topK, "top-k", 0, "取得件数 Top-K (0 の場合は設定ファイルのデフォルト値)")
	flag.Float64Var(&scoreThreshold, "threshold", 0, "スコア閾値 (0 の場合は設定ファイルのデフォルト値)")
	flag.BoolVar(&dryRun, "dry-run", false, "AWS API 呼び出しを行わずモックデータで動作確認")
	flag.Parse()

	// 1. 設定ロード
	cfg := config.LoadConfig()
	if topK <= 0 {
		topK = cfg.RAGNumberOfResults
	}
	if scoreThreshold <= 0 {
		scoreThreshold = cfg.RAGScoreThreshold
	}

	var targetModels []string
	if modelsFlag != "" {
		for _, m := range strings.Split(modelsFlag, ",") {
			mTrimmed := strings.TrimSpace(m)
			if mTrimmed != "" {
				targetModels = append(targetModels, mTrimmed)
			}
		}
	} else if len(cfg.AvailableModels) > 0 {
		targetModels = cfg.AvailableModels
	} else {
		targetModels = []string{cfg.DefaultModelID}
	}

	fmt.Println("==================================================")
	fmt.Println("🚀 mini-aigw RAG 品質評価 & モデル比較ツール")
	fmt.Println("==================================================")
	fmt.Printf("• データセット: %s\n", datasetPath)
	fmt.Printf("• 評価モード:   %s\n", mode)
	if mode == "e2e" {
		fmt.Printf("• 比較モデル:   %s\n", strings.Join(targetModels, ", "))
		if judgeModelID != "" {
			fmt.Printf("• Judge モデル: %s\n", judgeModelID)
		} else {
			fmt.Println("• Judge モデル: (未指定: ルールベース採点)")
		}
	}
	fmt.Printf("• 検索設定:     Top-K=%d, Threshold=%.2f\n", topK, scoreThreshold)
	fmt.Printf("• 出力先:       %s\n", outputDir)
	if dryRun {
		fmt.Println("• 実行モード:   ⚠️ DRY-RUN (AWS 呼び出しなし)")
	}
	fmt.Println("--------------------------------------------------")

	// 2. データセット読み込み
	testCases, err := eval.LoadDataset(datasetPath)
	if err != nil {
		log.Fatalf("❌ データセット読み込みエラー: %v", err)
	}
	fmt.Printf("✓ %d 件のテストケースを読み込みました。\n\n", len(testCases))

	// 3. サービス初期化
	var bedrockSvc *services.BedrockService
	var knowledgeSvc *services.KnowledgeService
	var ragPipeline *services.RAGPipeline

	if !dryRun {
		database, err := db.InitDB(cfg)
		if err != nil {
			log.Fatalf("❌ DB初期化エラー: %v", err)
		}

		awsBundle, err := services.NewAWSClientBundle(context.Background(), cfg)
		if err != nil {
			log.Fatalf("❌ AWS クライアント初期化エラー: %v", err)
		}

		xbergSvc := services.NewXbergService(cfg)
		bedrockSvc = services.NewBedrockService(awsBundle, xbergSvc, cfg)
		knowledgeSvc = services.NewKnowledgeService(awsBundle, database, cfg)
		ragPipeline = services.NewRAGPipeline(bedrockSvc, knowledgeSvc, cfg)
	}

	runner := eval.NewRunner(ragPipeline, bedrockSvc, cfg.PublicBaseURL)

	// 4. 評価実行
	opts := eval.RunOptions{
		DatasetPath:    datasetPath,
		TestCases:      testCases,
		Models:         targetModels,
		JudgeModelID:   judgeModelID,
		TopK:           topK,
		ScoreThreshold: scoreThreshold,
		Mode:           mode,
		DryRun:         dryRun,
	}

	report, err := runner.Run(context.Background(), opts)
	if err != nil {
		log.Fatalf("❌ 評価実行中にエラーが発生しました: %v", err)
	}

	// 5. レポート保存
	savedPaths, err := eval.SaveReports(report, outputDir)
	if err != nil {
		log.Fatalf("❌ レポート保存エラー: %v", err)
	}

	// 6. コンソール結果サマリー
	fmt.Println("\n==================================================")
	fmt.Println("🏁 評価完了サマリー")
	fmt.Println("==================================================")
	fmt.Printf("• Retriever Hit@%d: %.1f%% (MRR: %.2f)\n", report.TopK, report.HitAtKRate, report.MeanReciprocalRank)
	if report.TotalLeaks == 0 {
		fmt.Println("• 権限分離:         ✅ 0 件 (完全分離)")
	} else {
		fmt.Printf("• 権限分離:         ⚠️ %d 件のリークを検出\n", report.TotalLeaks)
	}

	if mode == "e2e" && len(report.ModelSummaries) > 0 {
		fmt.Println("\n【モデル別比較サマリー】")
		for _, mID := range report.Models {
			if s, ok := report.ModelSummaries[mID]; ok {
				fmt.Printf("▶ %s:\n", mID)
				fmt.Printf("   - 平均レイテンシ:   %d ms\n", s.AvgLatencyMs)
				fmt.Printf("   - 平均トークン:     %d in / %d out\n", s.AvgInputTokens, s.AvgOutputTokens)
				fmt.Printf("   - 引用遵守率:       %.1f%%\n", s.CitationPassRate)
				fmt.Printf("   - 忠実度 (Faith):   %.1f / 5.0\n", s.AvgFaithfulness)
				fmt.Printf("   - 適合度 (Relev):   %.1f / 5.0\n", s.AvgRelevance)
				fmt.Printf("   - 正確性 (Correc):  %.1f / 5.0\n", s.AvgCorrectness)
			}
		}
	}

	fmt.Println("\n【生成された品質レポート】")
	for _, p := range savedPaths {
		fmt.Printf("📄 %s\n", p)
	}
	fmt.Println("==================================================")
}

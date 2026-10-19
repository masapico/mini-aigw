package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"mini-aigw/internal/config"
	"mini-aigw/internal/services"
)

var validIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// EvalReportSummary は一覧画面表示用の軽量サマリーです
type EvalReportSummary struct {
	ID                 string    `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	DatasetPath        string    `json:"dataset_path"`
	TotalCases         int       `json:"total_cases"`
	TopK               int       `json:"top_k"`
	ScoreThreshold     float64   `json:"score_threshold"`
	JudgeModelID       string    `json:"judge_model_id"`
	Models             []string  `json:"models"`
	HitAtKRate         float64   `json:"hit_at_k_rate"`
	MeanReciprocalRank float64   `json:"mean_reciprocal_rank"`
	TotalLeaks         int       `json:"total_leaks"`
	HasHTML            bool      `json:"has_html"`
	HasMD              bool      `json:"has_md"`
	HasJSON            bool      `json:"has_json"`
}

// EvalJobState はバックグラウンド評価ジョブの進捗状態です
type EvalJobState struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"` // "idle", "running", "completed", "failed"
	CurrentCase  int       `json:"current_case"`
	TotalCases   int       `json:"total_cases"`
	CurrentQuery string    `json:"current_query"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
	ReportID     string    `json:"report_id"`
	Error        string    `json:"error"`
}

// Service は RAG 評価の実行・ジョブ管理およびレポートI/Oを統括するサービスです
type Service struct {
	cfg         *config.Config
	ragPipeline *services.RAGPipeline
	bedrockSvc  *services.BedrockService

	mu       sync.RWMutex
	jobState EvalJobState
}

// NewService は Service を初期化します
func NewService(cfg *config.Config, ragPipeline *services.RAGPipeline, bedrockSvc *services.BedrockService) *Service {
	// 出力先ディレクトリの確保
	_ = os.MkdirAll(cfg.EvalReportsDir, 0755)
	_ = os.MkdirAll(cfg.EvalDatasetsDir, 0755)

	return &Service{
		cfg:         cfg,
		ragPipeline: ragPipeline,
		bedrockSvc:  bedrockSvc,
		jobState: EvalJobState{
			Status: "idle",
		},
	}
}

// ListReports は保存されている評価レポート一覧を取得します（新着順）
func (s *Service) ListReports() ([]*EvalReportSummary, error) {
	dir := s.cfg.EvalReportsDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read eval reports dir: %w", err)
	}

	var summaries []*EvalReportSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		baseID := strings.TrimSuffix(entry.Name(), ".json")
		filePath := filepath.Join(dir, entry.Name())

		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var report EvalReportData
		if err := json.Unmarshal(data, &report); err != nil {
			continue
		}

		summary := &EvalReportSummary{
			ID:                 baseID,
			Timestamp:          report.Timestamp,
			DatasetPath:        report.DatasetPath,
			TotalCases:         report.TotalCases,
			TopK:               report.TopK,
			ScoreThreshold:     report.ScoreThreshold,
			JudgeModelID:       report.JudgeModelID,
			Models:             report.Models,
			HitAtKRate:         report.HitAtKRate,
			MeanReciprocalRank: report.MeanReciprocalRank,
			TotalLeaks:         report.TotalLeaks,
			HasJSON:            true,
			HasHTML:            fileExists(filepath.Join(dir, baseID+".html")),
			HasMD:              fileExists(filepath.Join(dir, baseID+".md")),
		}

		summaries = append(summaries, summary)
	}

	// 新着順（日時降順）にソート
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Timestamp.After(summaries[j].Timestamp)
	})

	return summaries, nil
}

// GetReport は指定されたIDの評価レポート全文を取得します
func (s *Service) GetReport(id string) (*EvalReportData, error) {
	if !validIDRegex.MatchString(id) {
		return nil, fmt.Errorf("invalid report id: %s", id)
	}

	jsonPath := filepath.Join(s.cfg.EvalReportsDir, id+".json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("report not found: %w", err)
	}

	var report EvalReportData
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("failed to parse report JSON: %w", err)
	}

	return &report, nil
}

// GetReportFilePath はプレビューまたはダウンロード用の実ファイルパスを返します
func (s *Service) GetReportFilePath(id string, format string) (string, error) {
	if !validIDRegex.MatchString(id) {
		return "", fmt.Errorf("invalid report id: %s", id)
	}

	var ext string
	switch strings.ToLower(format) {
	case "html":
		ext = ".html"
	case "md", "markdown":
		ext = ".md"
	case "json":
		ext = ".json"
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	filePath := filepath.Join(s.cfg.EvalReportsDir, id+ext)
	if !fileExists(filePath) {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	return filePath, nil
}

// DeleteReport は指定されたレポート（JSON, HTML, MD）を削除します
func (s *Service) DeleteReport(id string) error {
	if !validIDRegex.MatchString(id) {
		return fmt.Errorf("invalid report id: %s", id)
	}

	dir := s.cfg.EvalReportsDir
	for _, ext := range []string{".json", ".html", ".md"} {
		p := filepath.Join(dir, id+ext)
		_ = os.Remove(p)
	}
	return nil
}

// ListDatasets はデータセット用ディレクトリ内の .jsonl ファイル一覧を返します
func (s *Service) ListDatasets() ([]string, error) {
	dir := s.cfg.EvalDatasetsDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read datasets dir: %w", err)
	}

	var datasets []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			datasets = append(datasets, entry.Name())
		}
	}
	sort.Strings(datasets)
	return datasets, nil
}

// SaveUploadedDataset はアップロードされた JSONL を検証し、データセットディレクトリへ保存します
func (s *Service) SaveUploadedDataset(filename string, r io.Reader) (string, []TestCase, error) {
	cleanName := filepath.Base(filename)
	if !strings.HasSuffix(cleanName, ".jsonl") {
		cleanName += ".jsonl"
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read uploaded data: %w", err)
	}

	// 検証
	cases, err := ParseDataset(strings.NewReader(string(data)))
	if err != nil {
		return "", nil, fmt.Errorf("invalid JSONL dataset: %w", err)
	}

	targetPath := filepath.Join(s.cfg.EvalDatasetsDir, cleanName)
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return "", nil, fmt.Errorf("failed to save dataset file: %w", err)
	}

	return targetPath, cases, nil
}

// StartJob は非同期で RAG 評価ジョブを開始します
func (s *Service) StartJob(opts RunOptions) (*EvalJobState, error) {
	s.mu.Lock()
	if s.jobState.Status == "running" {
		s.mu.Unlock()
		return nil, fmt.Errorf("an evaluation job is already running")
	}

	jobID := fmt.Sprintf("job-%d", time.Now().UnixNano())
	s.jobState = EvalJobState{
		ID:          jobID,
		Status:      "running",
		TotalCases:  len(opts.TestCases),
		StartedAt:   time.Now(),
		CurrentCase: 0,
	}
	stateCopy := s.jobState
	s.mu.Unlock()

	// バックグラウンドで実行
	go func() {
		runner := NewRunner(s.ragPipeline, s.bedrockSvc, s.cfg.PublicBaseURL)

		opts.OnProgress = func(current, total int, query string) {
			s.mu.Lock()
			s.jobState.CurrentCase = current
			s.jobState.TotalCases = total
			s.jobState.CurrentQuery = query
			s.mu.Unlock()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		report, err := runner.Run(ctx, opts)
		s.mu.Lock()
		defer s.mu.Unlock()

		if err != nil {
			log.Printf("[EvalService] Job %s failed: %v", jobID, err)
			s.jobState.Status = "failed"
			s.jobState.Error = err.Error()
			s.jobState.CompletedAt = time.Now()
			return
		}

		// レポートの保存
		savedPaths, saveErr := SaveReports(report, s.cfg.EvalReportsDir)
		if saveErr != nil {
			log.Printf("[EvalService] Job %s save reports failed: %v", jobID, saveErr)
			s.jobState.Status = "failed"
			s.jobState.Error = fmt.Sprintf("failed to save reports: %v", saveErr)
			s.jobState.CompletedAt = time.Now()
			return
		}

		var reportID string
		if len(savedPaths) > 0 {
			reportID = strings.TrimSuffix(filepath.Base(savedPaths[0]), filepath.Ext(savedPaths[0]))
		}

		s.jobState.Status = "completed"
		s.jobState.ReportID = reportID
		s.jobState.CompletedAt = time.Now()
		log.Printf("[EvalService] Job %s successfully completed. Report: %s", jobID, reportID)
	}()

	return &stateCopy, nil
}

// GetJobState は現在のジョブ状態をスレッドセーフに取得します
func (s *Service) GetJobState() EvalJobState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jobState
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}


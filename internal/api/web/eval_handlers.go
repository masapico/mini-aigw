package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/eval"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

type EvalHandler struct {
	evalService *eval.Service
	db          *gorm.DB
	cfg         *config.Config
	renderer    *views.Renderer
}

func NewEvalHandler(
	evalService *eval.Service,
	db *gorm.DB,
	cfg *config.Config,
	renderer *views.Renderer,
) *EvalHandler {
	return &EvalHandler{
		evalService: evalService,
		db:          db,
		cfg:         cfg,
		renderer:    renderer,
	}
}

// ShowEvalReports は RAG 評価レポート一覧 & 評価実行画面を表示します
func (h *EvalHandler) ShowEvalReports(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	reports, err := h.evalService.ListReports()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list eval reports: %v", err), http.StatusInternalServerError)
		return
	}

	datasets, _ := h.evalService.ListDatasets()

	var activeModels []models.LLMModel
	h.db.Where("is_active = ?", true).Find(&activeModels)

	jobState := h.evalService.GetJobState()

	data := map[string]interface{}{
		"User":         user,
		"IsAdmin":      user.HasRole("admin"),
		"CurrentPath":  "/eval-reports",
		"Reports":      reports,
		"Datasets":     datasets,
		"Models":       activeModels,
		"Config":       h.cfg,
		"JobState":     jobState,
		"DefaultTopK":  h.cfg.RAGNumberOfResults,
		"DefaultScore": h.cfg.RAGScoreThreshold,
	}

	if err := h.renderer.Render(w, "eval_reports.html", data); err != nil {
		http.Error(w, fmt.Sprintf("Render error: %v", err), http.StatusInternalServerError)
	}
}

// HandleRunEval はブラウザから評価ジョブを開始します
func (h *EvalHandler) HandleRunEval(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 32MB までのマルチパート受付
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Invalid form data: "+err.Error(), http.StatusBadRequest)
		return
	}

	mode := strings.TrimSpace(r.FormValue("mode"))
	if mode == "" {
		mode = "e2e"
	}

	topK := h.cfg.RAGNumberOfResults
	if v, err := strconv.Atoi(r.FormValue("top_k")); err == nil && v > 0 {
		topK = v
	}

	scoreThreshold := h.cfg.RAGScoreThreshold
	if v, err := strconv.ParseFloat(r.FormValue("score_threshold"), 64); err == nil && v >= 0 {
		scoreThreshold = v
	}

	dryRun := r.FormValue("dry_run") == "true" || r.FormValue("dry_run") == "on"
	judgeModel := strings.TrimSpace(r.FormValue("judge_model"))

	// 比較対象モデル
	var selectedModels []string
	if modelsForm, ok := r.MultipartForm.Value["models"]; ok {
		for _, m := range modelsForm {
			mTrimmed := strings.TrimSpace(m)
			if mTrimmed != "" {
				selectedModels = append(selectedModels, mTrimmed)
			}
		}
	}
	if len(selectedModels) == 0 {
		selectedModels = []string{h.cfg.DefaultModelID}
	}

	// データセットの解決
	var datasetPath string
	var testCases []eval.TestCase

	file, header, err := r.FormFile("dataset_file")
	if err == nil && header != nil {
		defer file.Close()
		savedPath, cases, parseErr := h.evalService.SaveUploadedDataset(header.Filename, file)
		if parseErr != nil {
			http.Error(w, "データセットの解析に失敗しました: "+parseErr.Error(), http.StatusBadRequest)
			return
		}
		datasetPath = savedPath
		testCases = cases
	} else {
		// 既存の選択データセット
		selectedDataset := strings.TrimSpace(r.FormValue("selected_dataset"))
		if selectedDataset == "" {
			http.Error(w, "評価データセットを選択またはアップロードしてください", http.StatusBadRequest)
			return
		}
		datasetPath = filepath.Join(h.cfg.EvalDatasetsDir, selectedDataset)
		cases, loadErr := eval.LoadDataset(datasetPath)
		if loadErr != nil {
			http.Error(w, "データセットの読み込みに失敗しました: "+loadErr.Error(), http.StatusBadRequest)
			return
		}
		testCases = cases
	}

	opts := eval.RunOptions{
		DatasetPath:    datasetPath,
		TestCases:      testCases,
		Models:         selectedModels,
		JudgeModelID:   judgeModel,
		TopK:           topK,
		ScoreThreshold: scoreThreshold,
		Mode:           mode,
		DryRun:         dryRun,
	}

	jobState, err := h.evalService.StartJob(opts)
	if err != nil {
		http.Error(w, "ジョブの開始に失敗しました: "+err.Error(), http.StatusConflict)
		return
	}

	data := map[string]interface{}{
		"JobState": *jobState,
		"BasePath": h.renderer.GetBasePath(),
	}

	if r.Header.Get("HX-Request") == "true" {
		_ = h.renderer.RenderPartial(w, "eval_job_status.html", data)
		return
	}

	http.Redirect(w, r, "/eval-reports", http.StatusSeeOther)
}

// GetJobStatus は現在のジョブ進捗パーシャルを返します（HTMX ポーリング用）
func (h *EvalHandler) GetJobStatus(w http.ResponseWriter, r *http.Request) {
	jobState := h.evalService.GetJobState()

	data := map[string]interface{}{
		"JobState": jobState,
		"BasePath": h.renderer.GetBasePath(),
	}

	_ = h.renderer.RenderPartial(w, "eval_job_status.html", data)
}

// ShowReportDetail は管理画面デザインでのレポート詳細画面を表示します
func (h *EvalHandler) ShowReportDetail(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing report ID", http.StatusBadRequest)
		return
	}

	report, err := h.evalService.GetReport(id)
	if err != nil {
		http.Error(w, "Report not found: "+err.Error(), http.StatusNotFound)
		return
	}

	data := map[string]interface{}{
		"User":        user,
		"IsAdmin":     user.HasRole("admin"),
		"CurrentPath": "/eval-reports",
		"ReportID":    id,
		"Report":      report,
		"Config":      h.cfg,
	}

	_ = h.renderer.Render(w, "eval_report_detail.html", data)
}

// HandleRawReport は自己完結型 HTML レポートをブラウザで直接プレビュー表示します
func (h *EvalHandler) HandleRawReport(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	id := chi.URLParam(r, "id")
	filePath, err := h.evalService.GetReportFilePath(id, "html")
	if err != nil {
		http.Error(w, "Report not found: "+err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, filePath)
}

// HandleDownloadReport はレポートファイル（HTML/MD/JSON）をダウンロードします
func (h *EvalHandler) HandleDownloadReport(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	id := chi.URLParam(r, "id")
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "html"
	}

	filePath, err := h.evalService.GetReportFilePath(id, format)
	if err != nil {
		http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
		return
	}

	fileName := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

	switch strings.ToLower(format) {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case "md", "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}

	http.ServeFile(w, r, filePath)
}

// HandleDeleteReport はレポートファイルを削除します
func (h *EvalHandler) HandleDeleteReport(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.evalService.DeleteReport(id); err != nil {
		http.Error(w, "Failed to delete report: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// HTMX で該当の行を削除
	w.WriteHeader(http.StatusOK)
}

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/eval"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

func TestEvalHandler(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "eval_handler_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	reportsDir := filepath.Join(tempDir, "eval_reports")
	datasetsDir := filepath.Join(tempDir, "datasets")
	_ = os.MkdirAll(reportsDir, 0755)
	_ = os.MkdirAll(datasetsDir, 0755)

	cfg := &config.Config{
		DatabaseURL:        "sqlite:///" + dbPath,
		EvalReportsDir:     reportsDir,
		EvalDatasetsDir:    datasetsDir,
		RAGNumberOfResults: 5,
		RAGScoreThreshold:  0.30,
		DefaultModelID:     "jp.amazon.nova-2-lite-v1:0",
	}

	database, err := db.InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer func() {
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	_ = db.SeedInitialData(database, cfg)

	renderer, err := views.InitRenderer()
	if err != nil {
		t.Fatalf("InitRenderer failed: %v", err)
	}

	evalSvc := eval.NewService(cfg, nil, nil)
	handler := NewEvalHandler(evalSvc, database, cfg, renderer)

	// Create a dummy report in reportsDir
	reportID := "eval_report_20261019_120000"
	dummyReport := &eval.EvalReportData{
		DatasetPath:    "./testdata/eval/sample_dataset.jsonl",
		Timestamp:      time.Now(),
		TopK:           5,
		ScoreThreshold: 0.3,
		TotalCases:     1,
		HitAtKRate:     100.0,
		CaseResults: []*eval.TestCaseResult{
			{
				TestCase: eval.TestCase{
					ID:    "tc-1",
					Query: "サンプル質問",
					Group: "public",
				},
				HitAtK:     true,
				IsIsolated: true,
			},
		},
	}
	_, err = eval.SaveReports(dummyReport, reportsDir)
	if err != nil {
		t.Fatalf("failed to save dummy report: %v", err)
	}
	// Use actual generated report ID
	reports, _ := evalSvc.ListReports()
	if len(reports) == 0 {
		t.Fatalf("expected at least 1 report")
	}
	reportID = reports[0].ID

	// Admin user context
	adminUser := &models.User{
		Username: "admin",
		Roles:    `["admin"]`,
	}

	// 1. ShowEvalReports
	t.Run("GET /eval-reports", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/eval-reports", nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		handler.ShowEvalReports(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "RAG 品質評価・モデル比較") {
			t.Errorf("expected page title in HTML body")
		}
	})

	// 2. ShowReportDetail
	t.Run("GET /eval-reports/{id}", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/eval-reports/"+reportID, nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", reportID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		handler.ShowReportDetail(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), reportID) {
			t.Errorf("expected report ID in HTML body")
		}
	})

	// 3. HandleRawReport
	t.Run("GET /eval-reports/{id}/raw", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/eval-reports/"+reportID+"/raw", nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", reportID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		handler.HandleRawReport(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("expected text/html, got %s", rec.Header().Get("Content-Type"))
		}
	})

	// 4. HandleDownloadReport
	t.Run("GET /eval-reports/{id}/download?format=json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/eval-reports/"+reportID+"/download?format=json", nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", reportID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		handler.HandleDownloadReport(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("expected attachment Content-Disposition")
		}
	})

	// 5. GetJobStatus
	t.Run("GET /eval-reports/job-status", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/eval-reports/job-status", nil)
		rec := httptest.NewRecorder()
		handler.GetJobStatus(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	// 6. HandleDeleteReport
	t.Run("DELETE /eval-reports/{id}", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/eval-reports/"+reportID, nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey, adminUser)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", reportID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		handler.HandleDeleteReport(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}

		// Verify deleted
		reps, _ := evalSvc.ListReports()
		if len(reps) != 0 {
			t.Errorf("expected 0 reports after delete, got %d", len(reps))
		}
	})
}

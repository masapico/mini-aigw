package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mini-aigw/internal/config"
)

func TestService_ReportsAndJobs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "eval_service_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	reportsDir := filepath.Join(tempDir, "eval_reports")
	datasetsDir := filepath.Join(tempDir, "datasets")
	_ = os.MkdirAll(reportsDir, 0755)
	_ = os.MkdirAll(datasetsDir, 0755)

	cfg := &config.Config{
		EvalReportsDir:  reportsDir,
		EvalDatasetsDir: datasetsDir,
		PublicBaseURL:   "http://localhost:8000",
	}

	svc := NewService(cfg, nil, nil)

	// 1. Dataset saving & listing
	sampleJSONL := `{"id":"tc-1","query":"テストクエリ","group":"public","expected_doc_ids":["doc-1"]}
{"id":"tc-2","query":"第2クエリ","group":"sales"}`
	savedPath, cases, err := svc.SaveUploadedDataset("custom_test.jsonl", strings.NewReader(sampleJSONL))
	if err != nil {
		t.Fatalf("SaveUploadedDataset failed: %v", err)
	}
	if len(cases) != 2 {
		t.Errorf("expected 2 test cases, got %d", len(cases))
	}
	if !strings.HasSuffix(savedPath, "custom_test.jsonl") {
		t.Errorf("unexpected saved path: %s", savedPath)
	}

	datasets, err := svc.ListDatasets()
	if err != nil {
		t.Fatalf("ListDatasets failed: %v", err)
	}
	if len(datasets) != 1 || datasets[0] != "custom_test.jsonl" {
		t.Errorf("expected [custom_test.jsonl], got %v", datasets)
	}

	// 2. Start a DryRun evaluation job
	opts := RunOptions{
		DatasetPath: savedPath,
		TestCases:   cases,
		Mode:        "e2e",
		Models:      []string{"mock-model"},
		TopK:        3,
		DryRun:      true,
	}

	jobState, err := svc.StartJob(opts)
	if err != nil {
		t.Fatalf("StartJob failed: %v", err)
	}
	if jobState.Status != "running" {
		t.Errorf("expected status 'running', got %s", jobState.Status)
	}

	// Wait for job completion
	var finalState EvalJobState
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		finalState = svc.GetJobState()
		if finalState.Status == "completed" || finalState.Status == "failed" {
			break
		}
	}

	if finalState.Status != "completed" {
		t.Fatalf("job failed to complete: %+v", finalState)
	}
	if finalState.ReportID == "" {
		t.Fatalf("expected non-empty report ID")
	}

	// 3. List reports
	reports, err := svc.ListReports()
	if err != nil {
		t.Fatalf("ListReports failed: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].ID != finalState.ReportID {
		t.Errorf("expected report ID %s, got %s", finalState.ReportID, reports[0].ID)
	}
	if !reports[0].HasHTML || !reports[0].HasJSON || !reports[0].HasMD {
		t.Errorf("expected all report formats to exist: %+v", reports[0])
	}

	// 4. GetReport
	repData, err := svc.GetReport(finalState.ReportID)
	if err != nil {
		t.Fatalf("GetReport failed: %v", err)
	}
	if repData.TotalCases != 2 {
		t.Errorf("expected 2 cases in report, got %d", repData.TotalCases)
	}

	// 5. GetReportFilePath & Path Traversal check
	htmlPath, err := svc.GetReportFilePath(finalState.ReportID, "html")
	if err != nil {
		t.Fatalf("GetReportFilePath html failed: %v", err)
	}
	if !strings.HasSuffix(htmlPath, ".html") {
		t.Errorf("expected .html path, got %s", htmlPath)
	}

	// Security: Invalid IDs
	if _, err := svc.GetReport("../etc/passwd"); err == nil {
		t.Errorf("expected error for path traversal attempt, got nil")
	}

	// 6. Delete report
	if err := svc.DeleteReport(finalState.ReportID); err != nil {
		t.Fatalf("DeleteReport failed: %v", err)
	}
	reportsAfterDelete, _ := svc.ListReports()
	if len(reportsAfterDelete) != 0 {
		t.Errorf("expected 0 reports after deletion, got %d", len(reportsAfterDelete))
	}
}


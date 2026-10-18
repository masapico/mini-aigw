package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// TestCase は評価用の1レコード（クエリ、期待結果、模範解答）を表します
type TestCase struct {
	ID                string   `json:"id"`
	Query             string   `json:"query"`
	Group             string   `json:"group"`
	ExpectedDocIDs    []string `json:"expected_doc_ids"`
	ExpectedFilenames []string `json:"expected_filenames"`
	ReferenceAnswer   string   `json:"reference_answer"`
}

// LoadDataset は指定されたパスの JSONL ファイルから評価データセットを読み込みます
func LoadDataset(filePath string) ([]TestCase, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open dataset file '%s': %w", filePath, err)
	}
	defer file.Close()

	var testCases []TestCase
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue // 空行やコメント行はスキップ
		}

		var tc TestCase
		if err := json.Unmarshal([]byte(line), &tc); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %w", lineNum, err)
		}

		// バリデーション
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("tc-%03d", lineNum)
		}
		tc.Query = strings.TrimSpace(tc.Query)
		if tc.Query == "" {
			return nil, fmt.Errorf("line %d: 'query' must not be empty", lineNum)
		}
		if tc.Group == "" {
			tc.Group = "public"
		}

		testCases = append(testCases, tc)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading dataset file: %w", err)
	}

	if len(testCases) == 0 {
		return nil, fmt.Errorf("dataset file '%s' contains no valid test cases", filePath)
	}

	return testCases, nil
}


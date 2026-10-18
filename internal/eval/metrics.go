package eval

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	citationBracketRegex = regexp.MustCompile(`\[\d+\]`)
	sourceHeaderRegex    = regexp.MustCompile(`(?i)(###?\s*根拠資料|根拠資料\s*\(参照ドキュメント\)|参考資料|参照元)`)
)

// MatchFound はドキュメントIDまたはファイル名が正解一覧に合致するか判定します
func MatchFound(docID, filename string, expectedDocIDs, expectedFilenames []string) bool {
	docIDTrimmed := strings.TrimSpace(docID)
	if docIDTrimmed != "" {
		for _, exp := range expectedDocIDs {
			if strings.EqualFold(docIDTrimmed, strings.TrimSpace(exp)) {
				return true
			}
		}
	}

	filenameTrimmed := strings.TrimSpace(filename)
	if filenameTrimmed != "" {
		for _, exp := range expectedFilenames {
			expTrimmed := strings.TrimSpace(exp)
			if expTrimmed != "" && (strings.EqualFold(filenameTrimmed, expTrimmed) || strings.Contains(strings.ToLower(filenameTrimmed), strings.ToLower(expTrimmed))) {
				return true
			}
		}
	}

	return false
}

// CalculateHitAtK は上位 K 件以内に正解が含まれているか判定します
func CalculateHitAtK(retrievedDocIDs, retrievedFilenames, expectedDocIDs, expectedFilenames []string, k int) bool {
	// 正解が未指定（検索ヒットしないことが期待されるテストケース）の場合
	if len(expectedDocIDs) == 0 && len(expectedFilenames) == 0 {
		return len(retrievedDocIDs) == 0
	}

	limit := k
	if limit > len(retrievedDocIDs) {
		limit = len(retrievedDocIDs)
	}

	for i := 0; i < limit; i++ {
		docID := ""
		if i < len(retrievedDocIDs) {
			docID = retrievedDocIDs[i]
		}
		filename := ""
		if i < len(retrievedFilenames) {
			filename = retrievedFilenames[i]
		}

		if MatchFound(docID, filename, expectedDocIDs, expectedFilenames) {
			return true
		}
	}

	return false
}

// CalculateReciprocalRank は最初の正解が出現した順位の逆数 (MRR 計算用) を返します
func CalculateReciprocalRank(retrievedDocIDs, retrievedFilenames, expectedDocIDs, expectedFilenames []string) float64 {
	if len(expectedDocIDs) == 0 && len(expectedFilenames) == 0 {
		if len(retrievedDocIDs) == 0 {
			return 1.0
		}
		return 0.0
	}

	for i := 0; i < len(retrievedDocIDs); i++ {
		docID := retrievedDocIDs[i]
		filename := ""
		if i < len(retrievedFilenames) {
			filename = retrievedFilenames[i]
		}

		if MatchFound(docID, filename, expectedDocIDs, expectedFilenames) {
			return 1.0 / float64(i+1)
		}
	}

	return 0.0
}

// CheckPermissionIsolation は取得されたチャンクに許諾外グループのものが含まれていないか検証します
func CheckPermissionIsolation(retrievedGroups []string, allowedGroup string) (leakedGroups []string, isIsolated bool) {
	allowed := strings.TrimSpace(strings.ToLower(allowedGroup))
	for _, g := range retrievedGroups {
		gNorm := strings.TrimSpace(strings.ToLower(g))
		if gNorm == "" {
			continue
		}
		// public および指定グループは許可
		if gNorm != "public" && gNorm != allowed {
			leakedGroups = append(leakedGroups, g)
		}
	}
	return leakedGroups, len(leakedGroups) == 0
}

// CheckCitationCompliance は回答が引用フォーマットの要件を満たしているか検証します
func CheckCitationCompliance(answerText string, hasRetrievedResults bool) (pass bool, details string) {
	cleanAnswer := strings.TrimSpace(answerText)
	if cleanAnswer == "" {
		return false, "回答が空です"
	}

	if hasRetrievedResults {
		// 1. 本文中に [1], [2] 等の引用番号があるか
		hasBracket := citationBracketRegex.MatchString(cleanAnswer)
		// 2. 末尾に根拠資料セクションがあるか
		hasSourceHeader := sourceHeaderRegex.MatchString(cleanAnswer)

		if hasBracket && hasSourceHeader {
			return true, "引用番号および根拠資料セクションが存在します"
		} else if !hasBracket && !hasSourceHeader {
			return false, "引用番号([1]等)および根拠資料セクションの双方が欠落しています"
		} else if !hasBracket {
			return false, "本文中に引用番号([1]等)が付与されていません"
		} else {
			return false, "末尾に根拠資料セクションが付与されていません"
		}
	} else {
		// 検索ヒットなしの場合、ナレッジにない旨を前置きしているか
		noKnowledgeKeywords := []string{
			"ナレッジには該当情報がありません",
			"該当するドキュメントは見つかりません",
			"ナレッジに記載がありません",
			"該当情報がありません",
			"見つかりませんでした",
			"提供されたナレッジ",
			"該当資料",
		}
		for _, kw := range noKnowledgeKeywords {
			if strings.Contains(cleanAnswer, kw) {
				return true, fmt.Sprintf("未該当時の適切な説明が含まれています ('%s')", kw)
			}
		}
		return true, "未該当時の通常回答"
	}
}


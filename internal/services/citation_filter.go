package services

import (
	"regexp"
	"strconv"
	"strings"
)

var citationRegex = regexp.MustCompile(`\[([1-9][0-9]*)\]`)

var absencePhrases = []string{
	"提供された文書には含まれていない",
	"提供されたナレッジに記載がありません",
	"提供されたナレッジに記載はありません",
	"提供されたナレッジには記載がありません",
	"提供された資料には記載がありません",
	"提供された資料には含まれていません",
	"ナレッジには該当情報がありません",
	"ナレッジに情報がありません",
	"ナレッジに記載がありません",
	"ナレッジに該当する情報はありません",
	"文書には記載がありません",
	"文書には記載されていません",
	"資料には記載がありません",
	"資料には含まれていません",
	"該当する情報は含まれていません",
	"該当情報は含まれていません",
	"情報は見つかりませんでした",
}

// ExtractCitedIndices は回答本文を解析し、実際に引用された 1-based のチャンクインデックスを抽出します。
// 本文に不在表明フレーズ（「文書に含まれていない」等）がある場合、または [N] の引用が一切ない場合は (nil, false) を返します。
func ExtractCitedIndices(answer string, maxIndex int) (map[int]bool, bool) {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return nil, false
	}

	// 1. 不在表明フレーズの確認 (LLMが「ナレッジに記載がない」と断っている場合)
	for _, phrase := range absencePhrases {
		if strings.Contains(trimmed, phrase) {
			return nil, false
		}
	}

	// 2. 本文中の [1], [2] などの引用アンカーを検出
	matches := citationRegex.FindAllStringSubmatch(trimmed, -1)
	if len(matches) == 0 {
		return nil, false
	}

	citedMap := make(map[int]bool)
	for _, m := range matches {
		if len(m) >= 2 {
			if idx, err := strconv.Atoi(m[1]); err == nil && idx >= 1 && idx <= maxIndex {
				citedMap[idx] = true
			}
		}
	}

	if len(citedMap) == 0 {
		return nil, false
	}

	return citedMap, true
}

// FilterInterfaceCitations は map[string]interface{} の citations スライスから、
// 回答で実際に引用されたチャンクのみを抽出します。
func FilterInterfaceCitations(answer string, citations []interface{}) ([]interface{}, bool) {
	if len(citations) == 0 {
		return nil, false
	}

	citedMap, ok := ExtractCitedIndices(answer, len(citations))
	if !ok {
		return nil, false
	}

	var filtered []interface{}
	for _, c := range citations {
		if cMap, ok := c.(map[string]interface{}); ok {
			if idx, ok := cMap["index"].(int); ok && citedMap[idx] {
				filtered = append(filtered, c)
			}
		}
	}

	return filtered, len(filtered) > 0
}

// CitationItem は RAG テスト等で使用される引用構造体です
type CitationItem struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	Group       string `json:"group"`
	DownloadURL string `json:"download_url"`
}

// FilterCitationItems は CitationItem スライスから、回答で実際に引用されたもののみを抽出します。
func FilterCitationItems(answer string, citations []CitationItem) ([]CitationItem, bool) {
	if len(citations) == 0 {
		return nil, false
	}

	citedMap, ok := ExtractCitedIndices(answer, len(citations))
	if !ok {
		return nil, false
	}

	var filtered []CitationItem
	for _, c := range citations {
		if citedMap[c.Index] {
			filtered = append(filtered, c)
		}
	}

	return filtered, len(filtered) > 0
}


package services_test

import (
	"testing"

	"mini-aigw/internal/services"
)

func TestExtractCitedIndices(t *testing.T) {
	// 1. 正常な引用
	ans1 := "育児休業の手続きは [1] の規定に従い申請します。また詳細は [3] に記載されています。"
	indices, ok := services.ExtractCitedIndices(ans1, 5)
	if !ok {
		t.Fatalf("expected citations to be detected")
	}
	if !indices[1] || !indices[3] || indices[2] {
		t.Errorf("expected indices 1 and 3, got: %+v", indices)
	}

	// 2. 引用アンカー [N] が一切ない場合
	ans2 := "一般的なプログラミングの質問について回答します。Goではgoroutineを使用します。"
	_, ok2 := services.ExtractCitedIndices(ans2, 5)
	if ok2 {
		t.Errorf("expected no citations when [N] is absent")
	}

	// 3. 不在表明フレーズ（プルーンの例）
	ans3 := `プルーンの育て方について、種から始める場合、以下の手順が一般的です...
上記の記載は一般的なプルーンの育て方に関する情報であり、プルーンの育成に関する詳細な情報は提供された文書には含まれていないため、より具体的な情報は専門書籍を参照してください。`
	_, ok3 := services.ExtractCitedIndices(ans3, 6)
	if ok3 {
		t.Errorf("expected false when answer explicitly states absence of information in provided documents")
	}
}

func TestFilterCitationItems(t *testing.T) {
	candidates := []services.CitationItem{
		{Index: 1, Filename: "doc1.pdf", Group: "hr"},
		{Index: 2, Filename: "doc2.pdf", Group: "hr"},
		{Index: 3, Filename: "doc3.pdf", Group: "hr"},
	}

	// 本文で [1] だけ引用
	ans := "社内制度は [1] に基づきます。"
	filtered, ok := services.FilterCitationItems(ans, candidates)
	if !ok || len(filtered) != 1 || filtered[0].Index != 1 {
		t.Errorf("expected only doc1.pdf to be retained, got: %+v", filtered)
	}

	// 本文でナレッジ不在を表明
	ansAbsence := "提供されたナレッジに記載がありません。一般的には..."
	filtered2, ok2 := services.FilterCitationItems(ansAbsence, candidates)
	if ok2 || len(filtered2) != 0 {
		t.Errorf("expected empty citations on absence statement, got: %+v", filtered2)
	}
}


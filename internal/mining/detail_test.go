package mining

import "testing"

func TestPronunciationLookupUsesSelectedDictionaryEntry(t *testing.T) {
	enrichment := &EnrichmentView{Candidates: []CandidateView{
		{Written: "恰度", Pronunciation: "ちょうど"},
		{Written: "丁度", Pronunciation: "ちょうど", Selected: true},
	}}
	expression, reading := pronunciationLookup("ちょうど", "", enrichment)
	if expression != "丁度" || reading != "ちょうど" {
		t.Fatalf("lookup = %q, %q", expression, reading)
	}
}

func TestPronunciationLookupKeepsCaptureWithoutDictionaryEntry(t *testing.T) {
	expression, reading := pronunciationLookup("納豆", "なっとう", nil)
	if expression != "納豆" || reading != "なっとう" {
		t.Fatalf("lookup = %q, %q", expression, reading)
	}
}

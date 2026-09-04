package kana

import "testing"

func TestConvert(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "taberu", want: "たべる"},
		{input: "TABERU", want: "タベル"},
		{input: "gakkou", want: "がっこう"},
		{input: "shinbun", want: "しんぶん"},
		{input: "nani", want: "なに"},
		{input: "konnnichiha", want: "こんにちは"},
		{input: "kon'nichiha", want: "こんにちは"},
		{input: "kan'i", want: "かんい"},
		{input: "nn", want: "ん"},
		{input: "nnnn", want: "んん"},
		{input: "nna", want: "んあ"},
		{input: "nnya", want: "んや"},
		{input: "nnnya", want: "んにゃ"},
		{input: "honnyu", want: "ほんゆ"},
		{input: "honnyuu", want: "ほんゆう"},
		{input: "HONNYUU", want: "ホンユウ"},
		{input: "hon'yuu", want: "ほんゆう"},
		{input: "matcha", want: "まっちゃ"},
		{input: "SYUKUDAI", want: "シュクダイ"},
		{input: "xtsu", want: "っ"},
		{input: "lyo", want: "ょ"},
		{input: "SU-PA-", want: "スーパー"},
		{input: "hon-ya", want: "ほんーや"},
		{input: "しろ くろ", want: "しろ くろ"},
		{input: " しろ\tくろ ", want: "しろ くろ"},
		{input: "きょう", want: "きょう"},
		{input: "ＴＡＢＥＲＵ", want: "タベル"},
		{input: "ティー・シャツ", want: "ティー・シャツ"},
		{input: "chekku", want: "ちぇっく"},
		{input: "SHEFU", want: "シェフ"},
		{input: "je", want: "じぇ"},
		{input: "THI", want: "ティ"},
		{input: "DHI", want: "ディ"},
		{input: "wi", want: "うぃ"},
		{input: "WE", want: "ウェ"},
		{input: "tsa", want: "つぁ"},
		{input: "twu", want: "とぅ"},
		{input: "kwa", want: "くぁ"},
		{input: "VYO", want: "ヴョ"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := Convert(test.input)
			if err != nil {
				t.Fatalf("Convert(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Errorf("Convert(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeKatakana(t *testing.T) {
	got, err := Normalize(" タベル ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "たべる" {
		t.Fatalf("Normalize = %q, want たべる", got)
	}
}

func TestConvertRejectsInvalidReadings(t *testing.T) {
	for _, input := range []string{"xyz", "食べる", "たべる!", "たべる1", "🙂", "-", "・", "'"} {
		if _, err := Convert(input); err == nil {
			t.Errorf("Convert(%q) accepted an unsupported reading", input)
		}
	}
}

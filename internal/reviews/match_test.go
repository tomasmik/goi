package reviews

import "testing"

func TestMatchAnswer(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		accepted []string
		want     MatchResult
	}{
		{name: "normalized exact answer", answer: "  To Eat ", accepted: []string{"to eat"}, want: Correct},
		{name: "optional qualifier", answer: "to eat", accepted: []string{"to eat (food)"}, want: Correct},
		{name: "raw imported prefix", answer: "to eat", accepted: []string{"食べる - to eat (food)"}, want: Correct},
		{name: "comma-separated first synonym", answer: "consciousness", accepted: []string{"consciousness, awareness"}, want: Correct},
		{name: "comma-separated later synonym", answer: "awareness", accepted: []string{"consciousness, awareness"}, want: Correct},
		{name: "full-width comma synonym", answer: "awareness", accepted: []string{"consciousness，awareness"}, want: Correct},
		{name: "optional infinitive", answer: "eat", accepted: []string{"to eat"}, want: Correct},
		{name: "stacked optional prefixes", answer: "state", accepted: []string{"to be a state"}, want: Correct},
		{name: "optional answer article", answer: "a dog", accepted: []string{"dog"}, want: Correct},
		{name: "missing letter", answer: "conciousness", accepted: []string{"consciousness"}, want: Correct},
		{name: "missing final letter", answer: "consciousnes", accepted: []string{"consciousness"}, want: Correct},
		{name: "adjacent transposition", answer: "conscoiusness", accepted: []string{"consciousness"}, want: Correct},
		{name: "two missing letters", answer: "estblisment", accepted: []string{"establishment"}, want: Correct},
		{name: "two duplicated letters", answer: "to proovidee", accepted: []string{"to provide"}, want: Correct},
		{name: "short missing final letter", answer: "origi", accepted: []string{"origin"}, want: Correct},
		{name: "short missing first letter", answer: "rigin", accepted: []string{"origin"}, want: Correct},
		{name: "two missing letters in phrase", answer: "to incrse", accepted: []string{"to increase"}, want: Correct},
		{name: "different long word", answer: "unconsciousness", accepted: []string{"consciousness"}, want: Incorrect},
		{name: "meaning-changing prefix", answer: "inaccurate", accepted: []string{"accurate"}, want: Incorrect},
		{name: "meaning-changing prefix after grammar prefix", answer: "to inaccurate", accepted: []string{"to accurate"}, want: Incorrect},
		{name: "different long meaning", answer: "to decrease", accepted: []string{"to increase"}, want: Incorrect},
		{name: "short words stay exact", answer: "accept", accepted: []string{"except"}, want: Incorrect},
		{name: "short verbs stay exact", answer: "to accept", accepted: []string{"to except"}, want: Incorrect},
		{name: "very short typo stays exact", answer: "cay", accepted: []string{"cat"}, want: Incorrect},
		{name: "comma inside qualifier stays intact", answer: "consume", accepted: []string{"take (as in, consume)"}, want: Incorrect},
		{name: "comma before clause stays intact", answer: "surely", accepted: []string{"slowly, but surely"}, want: Incorrect},
		{name: "numeric comma stays intact", answer: "000 items", accepted: []string{"1,000 items"}, want: Incorrect},
		{name: "different meaning", answer: "drink", accepted: []string{"to eat"}, want: Incorrect},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchAnswer(test.answer, test.accepted); got != test.want {
				t.Fatalf("MatchAnswer(%q, %q) = %q, want %q", test.answer, test.accepted, got, test.want)
			}
		})
	}
	if got := NormalizeAnswer(" ＡＢＣ "); got != "abc" {
		t.Fatalf("normalized answer = %q, want abc", got)
	}
}

func TestMatchPronunciationNormalizesRomajiAndScript(t *testing.T) {
	for _, answer := range []string{"taberu", "たべる", "タベル"} {
		got, err := MatchPronunciation(answer, []string{"たべる"})
		if err != nil || got != Correct {
			t.Fatalf("MatchPronunciation(%q) = %q, %v; want correct", answer, got, err)
		}
	}
	got, err := MatchPronunciation("wrong", []string{"たべる"})
	if err == nil || got != Incorrect {
		t.Fatalf("MatchPronunciation(wrong) = %q, %v; want incorrect with conversion error", got, err)
	}
}

func TestMatchPronunciationAcceptsModernRomajiCombinations(t *testing.T) {
	for _, answer := range []string{"chekku", "CHEKKU", "ちぇっく", "チェック"} {
		got, err := MatchPronunciation(answer, []string{"チェック"})
		if err != nil || got != Correct {
			t.Fatalf("MatchPronunciation(%q) = %q, %v; want correct", answer, got, err)
		}
	}
}

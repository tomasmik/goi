package coverage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type countingKnownVocabulary struct {
	calls int
}

func (source *countingKnownVocabulary) KnownExpressionStatuses(context.Context) (map[string]string, error) {
	source.calls++
	return map[string]string{"猫": StatusKnown}, nil
}

func TestAnalyzeReusesKnownVocabularyDuringSubtitleBursts(t *testing.T) {
	source := &countingKnownVocabulary{}
	analyzer, err := NewAnalyzer(source)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := analyzer.Analyze(context.Background(), []Block{{ID: 1, Text: "猫"}}); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls != 1 {
		t.Fatalf("known vocabulary loads = %d, want 1", source.calls)
	}
}

func TestAnalyzeUsesLearnedAndExternalVocabulary(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "昨日", "active", false)
	insertCoverageVocabulary(t, db, "食べる", "active", false)
	insertCoverageVocabulary(t, db, "猫", "unlearned", true)
	insertCoverageVocabulary(t, db, "見る", "suspended", false)
	insertCoverageVocabulary(t, db, "犬", "unlearned", false)
	insertCoverageVocabulary(t, db, "車", "archived", true)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 7, Text: "昨日、食べました。猫と犬と車を見ました。東京と山。"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 5 {
		t.Errorf("known occurrences = %d, want 5", result.Summary.KnownOccurrences)
	}
	if result.Summary.TotalOccurrences != 7 {
		t.Errorf("total occurrences = %d, want 7", result.Summary.TotalOccurrences)
	}
	if result.Summary.UnknownUnique != 2 {
		t.Errorf("unique unknown expressions = %d, want 2", result.Summary.UnknownUnique)
	}
	if result.Summary.ExcludedNames != 1 {
		t.Errorf("excluded names = %d, want 1", result.Summary.ExcludedNames)
	}
	if len(result.Blocks) != 1 || result.Blocks[0].ID != 7 {
		t.Fatalf("blocks = %#v, want the requested block ID", result.Blocks)
	}

	eating := tokenWithSurface(t, result.Blocks[0].Tokens, "食べました")
	if eating.Expression != "食べる" {
		t.Errorf("食べました expression = %q, want 食べる", eating.Expression)
	}
	if eating.Status != StatusKnown {
		t.Errorf("食べました status = %q, want %q", eating.Status, StatusKnown)
	}
	if eating.Reading != "たべました" {
		t.Errorf("食べました reading = %q, want たべました", eating.Reading)
	}
	if tokenWithSurface(t, result.Blocks[0].Tokens, "犬").Status != StatusUnknown {
		t.Error("ordinary unlearned vocabulary was treated as known")
	}
	if tokenWithSurface(t, result.Blocks[0].Tokens, "車").Status != StatusKnown {
		t.Error("archived external vocabulary was not treated as known")
	}
	for _, token := range result.Blocks[0].Tokens {
		if token.Surface == "東京" {
			t.Error("proper name was included in the coverage denominator")
		}
	}
}

func TestAnalyzeMarksActiveAndSuspendedLeechesAsKnown(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	activeID := insertCoverageVocabulary(t, db, "猫", "active", false)
	suspendedID := insertCoverageVocabulary(t, db, "犬", "suspended", false)
	if _, err := db.Exec(`
		INSERT INTO leech_states (vocabulary_id, active, ever_leech, marked_at, auto_suspended_at)
		VALUES (?, 1, 1, 1, NULL), (?, 1, 1, 1, NULL)`, activeID, suspendedID); err != nil {
		t.Fatal(err)
	}

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "猫と犬"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tokenWithSurface(t, result.Blocks[0].Tokens, "猫").Status; got != StatusLeech {
		t.Fatalf("active leech status = %q", got)
	}
	if got := tokenWithSurface(t, result.Blocks[0].Tokens, "犬").Status; got != StatusSuspendedLeech {
		t.Fatalf("suspended leech status = %q", got)
	}
	if result.Summary.KnownOccurrences != 2 || result.Summary.TotalOccurrences != 2 {
		t.Fatalf("leech coverage summary = %#v", result.Summary)
	}
}

func TestAnalyzeMatchesKanaExpressionAndReturnsUTF16Offsets(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "ねこ", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "😀ネコを食べる"}})
	if err != nil {
		t.Fatal(err)
	}

	cat := tokenWithSurface(t, result.Blocks[0].Tokens, "ネコ")
	if cat.Status != StatusKnown {
		t.Errorf("ネコ status = %q, want %q", cat.Status, StatusKnown)
	}
	if cat.StartUTF16 != 2 || cat.EndUTF16 != 4 {
		t.Errorf("ネコ offsets = [%d,%d), want [2,4)", cat.StartUTF16, cat.EndUTF16)
	}
	eating := tokenWithSurface(t, result.Blocks[0].Tokens, "食べる")
	if eating.StartUTF16 != 5 || eating.EndUTF16 != 8 {
		t.Errorf("食べる offsets = [%d,%d), want [5,8)", eating.StartUTF16, eating.EndUTF16)
	}
	if result.Summary.KnownOccurrences != 1 || result.Summary.TotalOccurrences != 2 {
		t.Errorf("summary = %#v, want 1 of 2 occurrences known", result.Summary)
	}
}

func TestAnalyzeDoesNotTreatHomophonesAsKnown(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	id := insertCoverageVocabulary(t, db, "橋", "active", false)
	if _, err := db.ExecContext(ctx, `
		UPDATE vocabulary
		SET pronunciation = 'はし', normalized_pronunciation = 'はし'
		WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "箸を使う。"}})
	if err != nil {
		t.Fatal(err)
	}

	chopsticks := tokenWithSurface(t, result.Blocks[0].Tokens, "箸")
	if chopsticks.Status != StatusUnknown {
		t.Errorf("箸 status = %q, want %q", chopsticks.Status, StatusUnknown)
	}
}

func TestAnalyzeMatchesExplicitlyKnownInflectedSurface(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "食べました", "unlearned", true)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "寿司を食べました。"}})
	if err != nil {
		t.Fatal(err)
	}

	eating := tokenWithSurface(t, result.Blocks[0].Tokens, "食べました")
	if eating.Status != StatusKnown {
		t.Errorf("食べました status = %q, want %q", eating.Status, StatusKnown)
	}
	if eating.Expression != "食べる" {
		t.Errorf("食べました expression = %q, want 食べる", eating.Expression)
	}
}

func TestAnalyzeNormalizesPotentialKuru(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "来る", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "明日来れたら行く。"}})
	if err != nil {
		t.Fatal(err)
	}

	coming := tokenWithSurface(t, result.Blocks[0].Tokens, "来れたら")
	if coming.Expression != "来る" || coming.Status != StatusKnown {
		t.Fatalf("来れたら token = %#v, want known 来る", coming)
	}
}

func TestAnalyzeMatchesKnownExpressionsAcrossTokenizerTokens(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "勉強する", "active", false)
	insertCoverageVocabulary(t, db, "気をつける", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "勉強する。気をつける。"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 2 || result.Summary.TotalOccurrences != 2 || result.Summary.UnknownUnique != 0 {
		t.Fatalf("summary = %#v, want both expressions counted once as known", result.Summary)
	}
	if len(result.Blocks[0].Tokens) != 2 {
		t.Fatalf("tokens = %#v, want one token per known expression", result.Blocks[0].Tokens)
	}
	studying := tokenWithSurface(t, result.Blocks[0].Tokens, "勉強する")
	if studying.Expression != "勉強する" || studying.Status != StatusKnown {
		t.Errorf("勉強する token = %#v, want the complete known expression", studying)
	}
	if studying.Reading != "べんきょうする" {
		t.Errorf("勉強する reading = %q, want べんきょうする", studying.Reading)
	}
	careful := tokenWithSurface(t, result.Blocks[0].Tokens, "気をつける")
	if careful.Expression != "気をつける" || careful.Status != StatusKnown {
		t.Errorf("気をつける token = %#v, want the particle inside the known expression", careful)
	}
	if careful.Reading != "きをつける" {
		t.Errorf("気をつける reading = %q, want きをつける", careful.Reading)
	}
}

func TestAnalyzeMatchesInflectedMultiTokenExpression(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "気をつける", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "😀気をつけました。"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 1 || result.Summary.TotalOccurrences != 1 {
		t.Fatalf("summary = %#v, want the inflected expression counted once as known", result.Summary)
	}
	matched := tokenWithSurface(t, result.Blocks[0].Tokens, "気をつけました")
	if matched.Expression != "気をつける" || matched.Status != StatusKnown {
		t.Errorf("matched token = %#v, want dictionary-form expression", matched)
	}
	if matched.StartUTF16 != 2 || matched.EndUTF16 != 9 {
		t.Errorf("matched offsets = [%d,%d), want [2,9)", matched.StartUTF16, matched.EndUTF16)
	}
}

func TestAnalyzeMatchesInflectionBeforeFinalExpressionToken(t *testing.T) {
	tests := []struct {
		name       string
		known      string
		text       string
		surface    string
		expression string
	}{
		{
			name:       "first token",
			known:      "食べること",
			text:       "食べたこと。",
			surface:    "食べたこと",
			expression: "食べること",
		},
		{
			name:       "intermediate token",
			known:      "気をつけること",
			text:       "気をつけたこと。",
			surface:    "気をつけたこと",
			expression: "気をつけること",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openCoverageTestDatabase(t)
			insertCoverageVocabulary(t, db, test.known, "active", false)

			analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
			if err != nil {
				t.Fatal(err)
			}
			result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: test.text}})
			if err != nil {
				t.Fatal(err)
			}

			if result.Summary.KnownOccurrences != 1 || result.Summary.TotalOccurrences != 1 || result.Summary.UnknownUnique != 0 {
				t.Fatalf("summary = %#v, want one known expression", result.Summary)
			}
			if len(result.Blocks[0].Tokens) != 1 {
				t.Fatalf("tokens = %#v, want one grouped expression", result.Blocks[0].Tokens)
			}
			matched := result.Blocks[0].Tokens[0]
			if matched.Surface != test.surface || matched.Expression != test.expression || matched.Status != StatusKnown {
				t.Fatalf("matched token = %#v, want surface %q and expression %q", matched, test.surface, test.expression)
			}
		})
	}
}

func TestAnalyzeMatchesMixedFormExpression(t *testing.T) {
	tests := []struct {
		name    string
		known   string
		text    string
		surface string
	}{
		{
			name:    "final inflection",
			known:   "食べてみる",
			text:    "食べてみた。",
			surface: "食べてみた",
		},
		{
			name:    "followed by noun",
			known:   "食べてみること",
			text:    "食べてみたこと。",
			surface: "食べてみたこと",
		},
		{
			name:    "alternating forms",
			known:   "食べることになっている",
			text:    "食べたことになっていた。",
			surface: "食べたことになっていた",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openCoverageTestDatabase(t)
			insertCoverageVocabulary(t, db, test.known, "active", false)

			analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
			if err != nil {
				t.Fatal(err)
			}
			result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: test.text}})
			if err != nil {
				t.Fatal(err)
			}

			if result.Summary.KnownOccurrences != 1 || result.Summary.TotalOccurrences != 1 || result.Summary.UnknownUnique != 0 {
				t.Fatalf("summary = %#v, want one known expression", result.Summary)
			}
			if len(result.Blocks[0].Tokens) != 1 {
				t.Fatalf("tokens = %#v, want one grouped expression", result.Blocks[0].Tokens)
			}
			matched := result.Blocks[0].Tokens[0]
			if matched.Surface != test.surface || matched.Expression != test.known || matched.Status != StatusKnown {
				t.Fatalf("matched token = %#v, want surface %q and expression %q", matched, test.surface, test.known)
			}
		})
	}
}

func TestAnalyzeDoesNotOvermatchMixedFormExpression(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "食べてみる", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "食べてみせた。"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 0 {
		t.Fatalf("summary = %#v, want the different verb to remain unknown", result.Summary)
	}
}

func TestAnalyzePrefersLongestKnownExpression(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "気", "active", false)
	insertCoverageVocabulary(t, db, "つける", "active", false)
	insertCoverageVocabulary(t, db, "気をつける", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "気をつける。"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 1 || result.Summary.TotalOccurrences != 1 {
		t.Fatalf("summary = %#v, want the longest expression counted once", result.Summary)
	}
	if len(result.Blocks[0].Tokens) != 1 || result.Blocks[0].Tokens[0].Surface != "気をつける" {
		t.Fatalf("tokens = %#v, want only the longest expression", result.Blocks[0].Tokens)
	}
}

func TestAnalyzeDoesNotMatchExpressionsAcrossPunctuation(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	insertCoverageVocabulary(t, db, "猫。犬", "active", false)

	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "猫。犬"}})
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.KnownOccurrences != 0 || result.Summary.TotalOccurrences != 2 || result.Summary.UnknownUnique != 2 {
		t.Fatalf("summary = %#v, want two separate unknown words", result.Summary)
	}
}

func TestAnalyzeExcludesFillers(t *testing.T) {
	ctx, db := openCoverageTestDatabase(t)
	analyzer, err := NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(ctx, []Block{{ID: 1, Text: "えーと、猫です。"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.TotalOccurrences != 1 || len(result.Blocks[0].Tokens) != 1 || result.Blocks[0].Tokens[0].Surface != "猫" {
		t.Fatalf("analysis = %#v, want only 猫 as the lexical expression", result)
	}
}

func tokenWithSurface(t *testing.T, tokens []Token, surface string) Token {
	t.Helper()
	for _, token := range tokens {
		if token.Surface == surface {
			return token
		}
	}
	t.Fatalf("token %q not found in %#v", surface, tokens)
	return Token{}
}

func openCoverageTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "coverage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func insertCoverageVocabulary(t *testing.T, db *sql.DB, expression, status string, knownElsewhere bool) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	var knownElsewhereAt any
	if knownElsewhere {
		knownElsewhereAt = now
	}
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, status, known_elsewhere_at,
			created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)`,
		expression, expression, status, knownElsewhereAt, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

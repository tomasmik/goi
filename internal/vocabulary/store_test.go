package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/reviews"
)

func TestCreateStoresManualExampleWithVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:         "食べる",
		Pronunciation:      "たべる",
		Meanings:           []string{"to eat"},
		SourceLabel:        "Kitchen notes",
		ExampleSentence:    "毎朝パンを食べる。",
		ExampleTranslation: "I eat bread every morning.",
		ExampleTarget:      "食べる",
	})
	if err != nil {
		t.Fatal(err)
	}

	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 {
		t.Fatalf("examples = %+v, want one", item.Examples)
	}
	example := item.Examples[0]
	if example.Origin != examples.OriginManual || example.Sentence != "毎朝パンを食べる。" || example.Translation != "I eat bread every morning." {
		t.Fatalf("example = %+v", example)
	}
	if example.TargetSurface != "食べる" || example.SourceTitle != "" || !example.HasTarget {
		t.Fatalf("example context = %+v", example)
	}
}

func TestCreateStoresReadingAndMeaningOrderOnVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id, err := NewStore(db).Create(ctx, CreateInput{
		Expression:    "コンピューター",
		Pronunciation: "コンピューター",
		Meanings:      []string{"computer", "electronic computer", "computing device"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var pronunciation, normalizedPronunciation string
	if err := db.QueryRow(`
		SELECT pronunciation, normalized_pronunciation
		FROM vocabulary
		WHERE id = ?`, id).Scan(&pronunciation, &normalizedPronunciation); err != nil {
		t.Fatal(err)
	}
	if pronunciation != "コンピューター" || normalizedPronunciation != "こんぴゅーたー" {
		t.Fatalf("reading = %q (%q), want katakana display and hiragana normalization", pronunciation, normalizedPronunciation)
	}

	item, err := NewStore(db).Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"computer", "electronic computer", "computing device"}
	if !slices.Equal(item.Meanings, want) {
		t.Fatalf("meanings = %q, want %q", item.Meanings, want)
	}
}

func TestUpdateReplacesOneMediaAssetPerPurpose(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
		Audio:         &media.Upload{Kind: media.KindAudio, MimeType: "audio/mpeg", Content: []byte("first audio")},
		Picture:       &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("picture")},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, id, item.ContentRevision, CreateInput{
		Expression:    item.Expression,
		Pronunciation: item.Pronunciation,
		Meanings:      item.Meanings,
		Audio:         &media.Upload{Kind: media.KindAudio, MimeType: "audio/mpeg", Content: []byte("replacement audio")},
	}); err != nil {
		t.Fatal(err)
	}

	var associations, mediaCount, oldAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary_media WHERE vocabulary_id = ?", id).Scan(&associations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM media_content
		WHERE content = ?`, []byte("first audio")).Scan(&oldAudioCount); err != nil {
		t.Fatal(err)
	}
	if associations != 2 || mediaCount != 2 || oldAudioCount != 0 {
		t.Fatalf("media after replacement = %d associations, %d assets, %d old audio; want 2, 2, 0", associations, mediaCount, oldAudioCount)
	}

	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, id, item.ContentRevision, CreateInput{
		Expression:    item.Expression,
		Pronunciation: item.Pronunciation,
		Meanings:      item.Meanings,
		RemovePicture: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary_media WHERE vocabulary_id = ?", id).Scan(&associations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if associations != 1 || mediaCount != 1 {
		t.Fatalf("media after removal = %d associations and %d assets, want 1 and 1", associations, mediaCount)
	}
}

func TestUpdateRejectsReplacingAndRemovingTheSameMedia(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(ctx, id, item.ContentRevision, CreateInput{
		Expression:    item.Expression,
		Pronunciation: item.Pronunciation,
		Meanings:      item.Meanings,
		Audio:         &media.Upload{Kind: media.KindAudio, MimeType: "audio/mpeg", Content: []byte("audio")},
		RemoveAudio:   true,
	})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "either a replacement audio") {
		t.Fatalf("Update error = %v, want replacement/removal conflict", err)
	}
}

func TestCreateIdentifiesInvalidInput(t *testing.T) {
	ctx, db := openTestDatabase(t)
	_, err := NewStore(db).Create(ctx, CreateInput{
		Expression:    "見る",
		Pronunciation: "q",
		Meanings:      []string{"to see"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create error = %v, want invalid input", err)
	}
	if got := err.Error(); !strings.Contains(got, `pronunciation: unsupported romaji near "q"`) {
		t.Fatalf("create error = %q, want readable pronunciation error", got)
	}
}

func TestDuplicateErrorIsSafeForUsers(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{Expression: "見る", Pronunciation: "みる", Meanings: []string{"to see"}}
	id, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, input)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("create error = %v, want duplicate", err)
	}
	userError, ok := err.(interface{ UserMessage() string })
	if !ok || userError.UserMessage() != fmt.Sprintf("vocabulary already exists as item %d", id) {
		t.Fatalf("duplicate user message = %v", err)
	}
}

func TestCreateAllowsOnlyExplicitDuplicates(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{Expression: "見る", Pronunciation: "みる", Meanings: []string{"to see"}}
	canonicalID, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	duplicateInput := input
	duplicateInput.AllowDuplicate = true
	duplicateInput.Meanings = []string{"to look at"}
	duplicateID, err := store.Create(ctx, duplicateInput)
	if err != nil {
		t.Fatal(err)
	}
	if duplicateID == canonicalID {
		t.Fatalf("explicit duplicate reused vocabulary %d", canonicalID)
	}

	if _, err := store.Create(ctx, input); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("ordinary third create error = %v, want duplicate", err)
	}
	if err := store.Update(ctx, duplicateID, 1, CreateInput{
		Expression: "見る", Pronunciation: "みる", Meanings: []string{"to observe"},
	}); err != nil {
		t.Fatalf("update explicit duplicate: %v", err)
	}

	var live, marked int
	if err := db.QueryRow(`
		SELECT COUNT(*), SUM(is_duplicate)
		FROM vocabulary
		WHERE normalized_expression = '見る'`).Scan(&live, &marked); err != nil {
		t.Fatal(err)
	}
	if live != 2 || marked != 1 {
		t.Fatalf("matching vocabulary = %d live, %d explicit duplicates", live, marked)
	}
}

func TestDeletedVocabularyCanBeCreatedAgain(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{Expression: "見る", Pronunciation: "みる", Meanings: []string{"to see"}}
	deletedID, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyAction(ctx, deletedID, ActionDelete); err != nil {
		t.Fatal(err)
	}

	liveID, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if liveID == deletedID {
		t.Fatalf("re-created vocabulary reused deleted row %d", deletedID)
	}
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM vocabulary
		WHERE normalized_expression = '見る'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("matching rows = %d, want one", count)
	}
}

func TestListTreatsSearchMetacharactersLiterally(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	for _, input := range []CreateInput{
		{Expression: "割引%", Pronunciation: "わりびき", Meanings: []string{"under_score"}},
		{Expression: "割引X", Pronunciation: "わりびき", Meanings: []string{"underscore"}},
	} {
		if _, err := store.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	items, err := store.ListPage(ctx, "%", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Expression != "割引%" {
		t.Fatalf("percent search = %+v, want 割引%%", items)
	}

	items, err = store.ListPage(ctx, "_", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Expression != "割引%" {
		t.Fatalf("underscore search = %+v, want the literal underscore meaning", items)
	}
}

func TestListPageBoundsAndCountsSearchResults(t *testing.T) {
	ctx, db := openTestDatabase(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		expression := fmt.Sprintf("match-%03d", index)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
			VALUES (?, ?, 'unlearned', ?, ?)`, expression, expression, index, index); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('other', 'other', 'unlearned', 200, 200)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	count, err := store.ListCount(ctx, "match")
	if err != nil {
		t.Fatal(err)
	}
	if count != 101 {
		t.Fatalf("matching count = %d, want 101", count)
	}
	first, err := store.ListPage(ctx, "match", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ListPage(ctx, "match", maximumListPageSize, maximumListPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != maximumListPageSize || len(second) != 1 || second[0].Expression != "match-000" {
		t.Fatalf("pages = %d and %+v, want 100 items then match-000", len(first), second)
	}
	if _, err := store.ListPage(ctx, "", maximumListPageSize+1, 0); err == nil {
		t.Fatal("oversized vocabulary page was accepted")
	}
}

func TestListPageFiltersVocabularyStatus(t *testing.T) {
	ctx, db := openTestDatabase(t)
	now := time.Now().UTC().Unix()
	for _, row := range []struct {
		expression string
		status     string
		knownAt    any
	}{
		{expression: "learning", status: "active"},
		{expression: "not started", status: "unlearned"},
		{expression: "known", status: "unlearned", knownAt: now},
		{expression: "suspended", status: "suspended"},
		{expression: "archived", status: "archived"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO vocabulary (
				expression, normalized_expression, status, known_elsewhere_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?)`, row.expression, row.expression, row.status, row.knownAt, now, now); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(db)
	for _, test := range []struct {
		filter ListFilter
		want   string
	}{
		{filter: ListFilterLearning, want: "learning"},
		{filter: ListFilterNotStarted, want: "not started"},
		{filter: ListFilterKnown, want: "known"},
		{filter: ListFilterSuspended, want: "suspended"},
		{filter: ListFilterArchived, want: "archived"},
	} {
		items, err := store.ListPageFiltered(ctx, "", test.filter, maximumListPageSize, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Expression != test.want {
			t.Fatalf("filter %q = %+v, want %q", test.filter, items, test.want)
		}
	}
}

func TestListPageSortsVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	for _, row := range []struct {
		expression string
		createdAt  int64
		updatedAt  int64
	}{
		{expression: "あ", createdAt: 10, updatedAt: 30},
		{expression: "い", createdAt: 30, updatedAt: 10},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO vocabulary (
				expression, normalized_expression, status, created_at, updated_at
			) VALUES (?, ?, 'unlearned', ?, ?)`, row.expression, row.expression, row.createdAt, row.updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(db)
	tests := []struct {
		sort ListSort
		want string
	}{
		{sort: ListSortUpdated, want: "あ"},
		{sort: ListSortNewest, want: "い"},
		{sort: ListSortExpression, want: "あ"},
	}
	for _, test := range tests {
		items, err := store.ListPageSorted(ctx, "", ListFilterAll, test.sort, maximumListPageSize, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 || items[0].Expression != test.want {
			t.Fatalf("sort %q = %+v, want %q first", test.sort, items, test.want)
		}
	}
}

func TestListSearchesPronunciation(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, search := range []string{"たべる", "タベル"} {
		items, err := store.ListPage(ctx, search, maximumListPageSize, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Expression != "食べる" {
			t.Fatalf("List(%q) = %+v, want 食べる", search, items)
		}
	}
}

func TestAddKnownCreatesSparseVocabularyFromCommonSeparators(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)

	result, err := store.AddKnown(ctx, " 食べる, 見る、読む; 書く；聞く\n話す　食べる ")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 6 || result.MarkedKnown != 0 || result.AlreadyKnown != 0 {
		t.Fatalf("result = %+v, want six created words", result)
	}

	var knownCount, meaningCount, nonEmptyPronunciationCount, srsCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM vocabulary
		WHERE status = 'unlearned' AND known_elsewhere_at IS NOT NULL`).Scan(&knownCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings").Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE pronunciation <> ''").Scan(&nonEmptyPronunciationCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states").Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if knownCount != 6 || meaningCount != 0 || nonEmptyPronunciationCount != 0 || srsCount != 0 {
		t.Fatalf("stored counts = known %d, meanings %d, readings %d, SRS %d", knownCount, meaningCount, nonEmptyPronunciationCount, srsCount)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = '食べる'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.KnownElsewhere || item.StatusLabel != "Known elsewhere" || item.StatusClass != "known" {
		t.Fatalf("sparse item status = %+v", item)
	}
	if item.Pronunciation != "" || len(item.Meanings) != 0 || item.CanResetProgress {
		t.Fatalf("sparse item content or actions = %+v", item)
	}
}

func TestAddKnownPreservesExistingLearningState(t *testing.T) {
	ctx, db := openTestDatabase(t)
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('新しい', '新しい', 'unlearned', ?, ?),
		       ('済む', '済む', 'active', ?, ?),
		       ('保留', '保留', 'unlearned', ?, ?),
		       ('古い', '古い', 'archived', ?, ?)`,
		now, now, now, now, now, now, now, now,
	); err != nil {
		t.Fatal(err)
	}
	var activeID, reservedID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = '済む'").Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 3, ?)", activeID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = '保留'").Scan(&reservedID); err != nil {
		t.Fatal(err)
	}
	lesson, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lesson.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position)
		VALUES (?, ?, 0)`, lessonID, reservedID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	result, err := store.AddKnown(ctx, "新しい 済む 保留 古い 初めて")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.MarkedKnown != 2 || result.AlreadyKnown != 1 || result.SkippedActiveLesson != 1 {
		t.Fatalf("result = %+v", result)
	}

	var marked, reserved, archived sql.NullInt64
	if err := db.QueryRow("SELECT known_elsewhere_at FROM vocabulary WHERE normalized_expression = '新しい'").Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT known_elsewhere_at FROM vocabulary WHERE id = ?", reservedID).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT known_elsewhere_at FROM vocabulary WHERE normalized_expression = '古い'").Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if !marked.Valid || reserved.Valid || !archived.Valid {
		t.Fatalf("known markers = marked %v, reserved %v, archived %v", marked, reserved, archived)
	}
	var activeStatus string
	var activeSRSCount int
	if err := db.QueryRow("SELECT status FROM vocabulary WHERE id = ?", activeID).Scan(&activeStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?", activeID).Scan(&activeSRSCount); err != nil {
		t.Fatal(err)
	}
	if activeStatus != "active" || activeSRSCount != 1 {
		t.Fatalf("active state = %q with %d SRS rows", activeStatus, activeSRSCount)
	}
}

func TestAddKnownAcceptsMoreThanFiveHundredWords(t *testing.T) {
	ctx, db := openTestDatabase(t)
	words := make([]string, 0, 501)
	for index := 0; index < 501; index++ {
		words = append(words, fmt.Sprintf("単語%d", index))
	}

	result, err := NewStore(db).AddKnown(ctx, strings.Join(words, " "))
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != len(words) {
		t.Fatalf("created = %d, want %d", result.Created, len(words))
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(words) {
		t.Fatalf("vocabulary count = %d, want %d", count, len(words))
	}
}

func TestKnownCountIncludesLearnedAndExternalVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, status, known_elsewhere_at, created_at, updated_at
		)
		VALUES ('external', 'external', 'unlearned', ?, ?, ?),
		       ('learning', 'learning', 'active', NULL, ?, ?),
		       ('paused', 'paused', 'suspended', NULL, ?, ?),
		       ('future', 'future', 'unlearned', NULL, ?, ?),
		       ('archived', 'archived', 'archived', ?, ?, ?),
		       ('archived learned', 'archived learned', 'archived', NULL, ?, ?)`,
		now, now, now,
		now, now,
		now, now,
		now, now,
		now, now,
		now, now, now,
	); err != nil {
		t.Fatal(err)
	}
	var archivedLearnedID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = 'archived learned'").Scan(&archivedLearnedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO srs_states (vocabulary_id, stage) VALUES (?, 8)", archivedLearnedID); err != nil {
		t.Fatal(err)
	}
	count, err := NewStore(db).KnownCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("known count = %d, want 5", count)
	}
	expressions, err := NewStore(db).KnownExpressionStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"external", "learning", "paused", "archived", "archived learned"} {
		if expressions[expression] != "known" {
			t.Errorf("known expressions = %v, missing %q", expressions, expression)
		}
	}
	for _, expression := range []string{"future"} {
		if _, known := expressions[expression]; known {
			t.Errorf("known expressions = %v, unexpectedly contains %q", expressions, expression)
		}
	}
}

func TestKnownExpressionsAreSortedAndDeduplicated(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.AddKnown(ctx, "beta alpha"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, status, known_elsewhere_at,
			is_duplicate, created_at, updated_at
		)
		VALUES ('ALPHA duplicate', 'alpha', 'unlearned', ?, 1, ?, ?),
		       ('future', 'future', 'unlearned', NULL, 0, ?, ?)`,
		now, now, now, now, now,
	); err != nil {
		t.Fatal(err)
	}

	expressions, err := store.KnownExpressions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(expressions, []string{"alpha", "beta"}) {
		t.Fatalf("known expressions = %q", expressions)
	}
}

func TestValidateInputRejectsInvalidText(t *testing.T) {
	valid := CreateInput{
		Expression:    "見る",
		Pronunciation: "みる",
		Meanings:      []string{"to see"},
	}
	tests := []struct {
		name   string
		change func(*CreateInput)
		want   string
	}{
		{
			name: "invalid UTF-8",
			change: func(input *CreateInput) {
				input.Expression = string([]byte{0xff})
			},
			want: "expression must be valid UTF-8",
		},
		{
			name: "single-line control character",
			change: func(input *CreateInput) {
				input.SourceLabel = "book\nchapter"
			},
			want: "source label contains a control character",
		},
		{
			name: "expression too long",
			change: func(input *CreateInput) {
				input.Expression = strings.Repeat("食", maxExpressionRunes+1)
			},
			want: "expression must be at most 256 characters",
		},
		{
			name: "pronunciation too long",
			change: func(input *CreateInput) {
				input.Pronunciation = strings.Repeat("あ", maxPronunciationRunes+1)
			},
			want: "pronunciation must be at most 256 characters",
		},
		{
			name: "pronunciation contains kanji",
			change: func(input *CreateInput) {
				input.Pronunciation = "見る"
			},
			want: "pronunciation: unsupported reading character '見'",
		},
		{
			name: "meanings too long",
			change: func(input *CreateInput) {
				input.Meanings = []string{strings.Repeat("a", maxMeaningsRunes+1)}
			},
			want: "meanings must be at most 2000 characters",
		},
		{
			name: "notes too long",
			change: func(input *CreateInput) {
				input.Notes = strings.Repeat("a", maxNotesRunes+1)
			},
			want: "notes must be at most 2000 characters",
		},
		{
			name: "source label too long",
			change: func(input *CreateInput) {
				input.SourceLabel = strings.Repeat("a", maxSourceLabelRunes+1)
			},
			want: "source label must be at most 300 characters",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.change(&input)
			_, err := validateInput(input)
			if !errors.Is(err, ErrInvalidInput) || err.Error() != test.want {
				t.Fatalf("validateInput() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUpdateUsesContentRevision(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "読む",
		Pronunciation: "よむ",
		Meanings:      []string{"to read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Update(ctx, id, original.ContentRevision, CreateInput{
		Expression:    "読み込む",
		Pronunciation: "よみこむ",
		Meanings:      []string{"to load"},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ContentRevision != original.ContentRevision+1 || updated.Expression != "読み込む" {
		t.Fatalf("updated item = %+v", updated)
	}

	err = store.Update(ctx, id, original.ContentRevision, CreateInput{
		Expression:    "古い編集",
		Pronunciation: "ふるいへんしゅう",
		Meanings:      []string{"stale edit"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want ErrRevisionConflict", err)
	}
	afterConflict, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterConflict.ContentRevision != updated.ContentRevision || afterConflict.Expression != updated.Expression {
		t.Fatalf("stale update changed item from %+v to %+v", updated, afterConflict)
	}
}

func TestUpdateAllowsSparseFieldsOnlyForKnownElsewhereVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	ordinaryID, err := store.Create(ctx, CreateInput{
		Expression:    "学ぶ",
		Pronunciation: "まなぶ",
		Meanings:      []string{"to learn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := store.Get(ctx, ordinaryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, ordinaryID, ordinary.ContentRevision, CreateInput{Expression: "学び直す"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ordinary sparse update error = %v, want ErrInvalidInput", err)
	}
	unchanged, err := store.Get(ctx, ordinaryID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Expression != ordinary.Expression || unchanged.ContentRevision != ordinary.ContentRevision || unchanged.Pronunciation == "" || len(unchanged.Meanings) == 0 {
		t.Fatalf("ordinary vocabulary became sparse: %+v", unchanged)
	}

	if _, err := store.AddKnown(ctx, "既知語"); err != nil {
		t.Fatal(err)
	}
	knownItems, err := store.ListPage(ctx, "既知語", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(knownItems) != 1 {
		t.Fatalf("known items = %+v", knownItems)
	}
	known, err := store.Get(ctx, knownItems[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, known.ID, known.ContentRevision, CreateInput{
		Expression: "既知の語",
		Notes:      "Reference only",
	}); err != nil {
		t.Fatal(err)
	}
	updatedKnown, err := store.Get(ctx, known.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedKnown.Expression != "既知の語" || updatedKnown.Notes != "Reference only" || updatedKnown.Pronunciation != "" || len(updatedKnown.Meanings) != 0 {
		t.Fatalf("updated known vocabulary = %+v", updatedKnown)
	}
	if updatedKnown.ContentRevision != known.ContentRevision+1 || !updatedKnown.KnownElsewhere {
		t.Fatalf("known vocabulary revision/state = %+v", updatedKnown)
	}
}

func TestSparseKnownVocabularyMustBeCompletedBeforeLessons(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.AddKnown(ctx, "既知語"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListPage(ctx, "既知語", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("known vocabulary = %+v", items)
	}
	id := items[0].ID

	err = store.ApplyAction(ctx, id, ActionLearn)
	if err == nil || !strings.Contains(err.Error(), "add a reading and meaning") {
		t.Fatalf("move sparse vocabulary to lessons error = %v", err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "unlearned" || !item.KnownElsewhere {
		t.Fatalf("sparse vocabulary changed after rejected action: %+v", item)
	}

	if err := store.Update(ctx, id, item.ContentRevision, CreateInput{
		Expression:    item.Expression,
		Pronunciation: "きちご",
		Meanings:      []string{"known word"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyAction(ctx, id, ActionLearn); err != nil {
		t.Fatal(err)
	}
	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "unlearned" || item.KnownElsewhere {
		t.Fatalf("completed vocabulary was not moved to lessons: %+v", item)
	}
}

func TestGetReturnsAvailableActions(t *testing.T) {
	tests := []struct {
		name                 string
		status               string
		withSRS              bool
		wantMarkKnown        bool
		wantToggleSuspension bool
		wantResetProgress    bool
	}{
		{name: "unlearned", status: "unlearned", wantMarkKnown: true},
		{name: "active", status: "active", withSRS: true, wantMarkKnown: true, wantToggleSuspension: true, wantResetProgress: true},
		{name: "suspended", status: "suspended", withSRS: true, wantMarkKnown: true, wantToggleSuspension: true, wantResetProgress: true},
		{name: "archived before learning", status: "archived"},
		{name: "archived after learning", status: "archived", withSRS: true, wantResetProgress: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			id := insertVocabulary(t, db, test.status)
			if _, err := db.Exec(`
				INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
				VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
				t.Fatal(err)
			}
			if test.withSRS {
				var suspendedAt any
				if test.status == "suspended" {
					suspendedAt = int64(123)
				}
				if _, err := db.Exec(`
					INSERT INTO srs_states (vocabulary_id, stage, due_at, suspended_at)
					VALUES (?, 3, 456, ?)`, id, suspendedAt); err != nil {
					t.Fatal(err)
				}
			}

			item, err := NewStore(db).Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if item.CanMarkKnown != test.wantMarkKnown {
				t.Errorf("CanMarkKnown = %t, want %t", item.CanMarkKnown, test.wantMarkKnown)
			}
			if item.CanToggleSuspension != test.wantToggleSuspension {
				t.Errorf("CanToggleSuspension = %t, want %t", item.CanToggleSuspension, test.wantToggleSuspension)
			}
			if item.CanResetProgress != test.wantResetProgress {
				t.Errorf("CanResetProgress = %t, want %t", item.CanResetProgress, test.wantResetProgress)
			}
		})
	}
}

func TestGetReturnsLeechState(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO leech_states (vocabulary_id, active, ever_leech, marked_at)
		VALUES (?, 1, 1, 100)`, id); err != nil {
		t.Fatal(err)
	}

	item, err := NewStore(db).Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.LeechActive || item.LeechSuspended || item.FormerLeech {
		t.Fatalf("active leech flags = active %t, suspended %t, former %t", item.LeechActive, item.LeechSuspended, item.FormerLeech)
	}
	if _, err := db.Exec(`
		UPDATE leech_states
		SET active = 0, marked_at = NULL, cleared_at = 101
		WHERE vocabulary_id = ?`, id); err != nil {
		t.Fatal(err)
	}

	item, err = NewStore(db).Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.LeechActive || !item.FormerLeech {
		t.Fatalf("former leech flags = active %t, former %t", item.LeechActive, item.FormerLeech)
	}
}

func TestMarkKnownElsewhereRemovesReviewStateAndKeepsCardHistory(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, 3, 456)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO leech_states (vocabulary_id, active, ever_leech, marked_at)
		VALUES (?, 1, 1, 100)`, id); err != nil {
		t.Fatal(err)
	}
	sessionResult, err := db.Exec("INSERT INTO review_sessions (kind, status) VALUES ('normal', 'completed')")
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'completed')`, sessionID, id)
	if err != nil {
		t.Fatal(err)
	}
	sessionItemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at,
			first_attempt_correct_count, prompt_count
		) VALUES (?, 'success', 2, 3, 1, 1, 1)`, sessionItemID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if err := store.ApplyAction(ctx, id, ActionMarkKnown); err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "unlearned" || !item.KnownElsewhere || item.CanMarkKnown {
		t.Fatalf("known-elsewhere state = %+v", item)
	}
	if len(item.Meanings) != 1 || item.Meanings[0] != "to eat" {
		t.Fatalf("meanings after marking known = %v", item.Meanings)
	}
	if len(item.ReviewHistory) != 1 || !item.FormerLeech || item.LeechActive {
		t.Fatalf("history and leech state after marking known = %+v", item)
	}
	assertVocabularyState(t, db, id, "unlearned", false, false)

	if err := store.ApplyAction(ctx, id, ActionLearn); err != nil {
		t.Fatal(err)
	}
	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.KnownElsewhere || item.Status != "unlearned" {
		t.Fatalf("word was not returned to lessons: %+v", item)
	}
}

func TestArchiveRestoresPreviousLearningState(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		withSRS   bool
		suspended bool
	}{
		{name: "unlearned", status: "unlearned"},
		{name: "active", status: "active", withSRS: true},
		{name: "suspended", status: "suspended", withSRS: true, suspended: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			id := insertVocabulary(t, db, test.status)
			if test.withSRS {
				var suspendedAt any
				if test.suspended {
					suspendedAt = int64(123)
				}
				if _, err := db.Exec(`
					INSERT INTO srs_states (vocabulary_id, stage, due_at, suspended_at)
					VALUES (?, 3, 456, ?)`, id, suspendedAt); err != nil {
					t.Fatal(err)
				}
			}

			store := NewStore(db)
			if err := store.ApplyAction(ctx, id, ActionArchive); err != nil {
				t.Fatal(err)
			}
			assertVocabularyState(t, db, id, "archived", test.withSRS, test.suspended)

			if err := store.ApplyAction(ctx, id, ActionArchive); err != nil {
				t.Fatal(err)
			}
			assertVocabularyState(t, db, id, test.status, test.withSRS, test.suspended)
		})
	}
}

func TestSuspendRejectsUnlearnedVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "unlearned")

	err := NewStore(db).ApplyAction(ctx, id, ActionSuspend)
	if err == nil || !strings.Contains(err.Error(), "unlearned vocabulary") {
		t.Fatalf("suspend error = %v", err)
	}
	assertVocabularyState(t, db, id, "unlearned", false, false)
}

func TestArchiveAndResetRejectActiveReview(t *testing.T) {
	for _, action := range []Action{ActionArchive, ActionReset} {
		t.Run(string(action), func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			id := insertVocabulary(t, db, "active")
			if _, err := db.Exec(`
				INSERT INTO srs_states (vocabulary_id, stage, due_at)
				VALUES (?, 3, 456)`, id); err != nil {
				t.Fatal(err)
			}
			reviewResult, err := db.Exec(`
				INSERT INTO review_sessions (kind, status)
				VALUES ('normal', 'active')`)
			if err != nil {
				t.Fatal(err)
			}
			reviewID, err := reviewResult.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
				VALUES (?, ?, 0, 'current')`, reviewID, id); err != nil {
				t.Fatal(err)
			}

			err = NewStore(db).ApplyAction(ctx, id, action)
			if err == nil || !strings.Contains(err.Error(), "review session") {
				t.Fatalf("%s error = %v", action, err)
			}
			assertVocabularyState(t, db, id, "active", true, false)
		})
	}
}

func TestMarkKnownElsewhereAbandonsActiveReview(t *testing.T) {
	for _, status := range []string{"active", "paused"} {
		t.Run(status, func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			id := insertVocabulary(t, db, "active")
			if _, err := db.Exec(`
				INSERT INTO srs_states (vocabulary_id, stage, due_at)
				VALUES (?, 3, 456)`, id); err != nil {
				t.Fatal(err)
			}
			result, err := db.Exec(`
				INSERT INTO review_sessions (kind, status)
				VALUES ('normal', ?)`, status)
			if err != nil {
				t.Fatal(err)
			}
			sessionID, err := result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
				VALUES (?, ?, 0, 'current')`, sessionID, id); err != nil {
				t.Fatal(err)
			}

			store := NewStore(db)
			if err := store.ApplyAction(ctx, id, ActionMarkKnown); err != nil {
				t.Fatal(err)
			}
			item, err := store.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if item.Status != "unlearned" || !item.KnownElsewhere {
				t.Fatalf("known-elsewhere state = %+v", item)
			}
			assertVocabularyState(t, db, id, "unlearned", false, false)

			var gotStatus string
			var completedAt sql.NullInt64
			if err := db.QueryRow(`
				SELECT status, completed_at
				FROM review_sessions
				WHERE id = ?`, sessionID).Scan(&gotStatus, &completedAt); err != nil {
				t.Fatal(err)
			}
			if gotStatus != "abandoned" || !completedAt.Valid {
				t.Fatalf("review session state = %q, completed %v", gotStatus, completedAt)
			}
		})
	}
}

func TestMarkKnownElsewhereAbandonsActiveLesson(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "unlearned")
	result, err := db.Exec(`
		INSERT INTO lesson_sessions (status)
		VALUES ('active')`)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position)
		VALUES (?, ?, 0)`, sessionID, id); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if err := store.ApplyAction(ctx, id, ActionMarkKnown); err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "unlearned" || !item.KnownElsewhere {
		t.Fatalf("known-elsewhere state = %+v", item)
	}

	var gotStatus string
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", sessionID).Scan(&gotStatus); err != nil {
		t.Fatal(err)
	}
	if gotStatus != "abandoned" {
		t.Fatalf("lesson session state = %q, want abandoned", gotStatus)
	}
}

func TestDeleteAbandonsActiveReview(t *testing.T) {
	for _, status := range []string{"active", "paused"} {
		t.Run(status, func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			id := insertVocabulary(t, db, "active")
			if _, err := db.Exec(`
				INSERT INTO srs_states (vocabulary_id, stage, due_at)
				VALUES (?, 3, 456)`, id); err != nil {
				t.Fatal(err)
			}
			reviewResult, err := db.Exec(`
				INSERT INTO review_sessions (kind, status)
				VALUES ('normal', ?)`, status)
			if err != nil {
				t.Fatal(err)
			}
			reviewID, err := reviewResult.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
				VALUES (?, ?, 0, 'current')`, reviewID, id); err != nil {
				t.Fatal(err)
			}

			if err := NewStore(db).ApplyAction(ctx, id, ActionDelete); err != nil {
				t.Fatal(err)
			}
			assertVocabularyDeleted(t, db, id)
			var gotStatus string
			var gotCompletedAt sql.NullInt64
			if err := db.QueryRow(`
				SELECT status, completed_at
				FROM review_sessions
				WHERE id = ?`, reviewID).Scan(&gotStatus, &gotCompletedAt); err != nil {
				t.Fatal(err)
			}
			if gotStatus != "abandoned" || !gotCompletedAt.Valid {
				t.Fatalf("review session state = %q, completed %v", gotStatus, gotCompletedAt)
			}
		})
	}
}

func TestProgressChangesRejectCompletedItemInActiveReview(t *testing.T) {
	ctx, db := openTestDatabase(t)
	completedID := insertVocabulary(t, db, "active")
	pendingID := insertVocabulary(t, db, "active")
	for _, id := range []int64{completedID, pendingID} {
		if _, err := db.Exec(`
			INSERT INTO srs_states (vocabulary_id, stage, due_at)
			VALUES (?, 3, 456)`, id); err != nil {
			t.Fatal(err)
		}
	}
	reviewResult, err := db.Exec(`
		INSERT INTO review_sessions (kind, status)
		VALUES ('normal', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	reviewID, err := reviewResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'completed'),
		       (?, ?, 1, 'pending')`,
		reviewID, completedID, reviewID, pendingID); err != nil {
		t.Fatal(err)
	}

	err = NewStore(db).ApplyAction(ctx, completedID, ActionReset)
	if err == nil || !strings.Contains(err.Error(), "review session") {
		t.Fatalf("reset error = %v", err)
	}
	assertVocabularyState(t, db, completedID, "active", true, false)
}

func TestDeleteAbandonsActiveLesson(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "unlearned")
	lessonResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status)
		VALUES ('active')`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position)
		VALUES (?, ?, 0)`, lessonID, id); err != nil {
		t.Fatal(err)
	}

	if err := NewStore(db).ApplyAction(ctx, id, ActionDelete); err != nil {
		t.Fatal(err)
	}
	assertVocabularyDeleted(t, db, id)
	var status string
	if err := db.QueryRow(`
		SELECT status
		FROM lesson_sessions
		WHERE id = ?`, lessonID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "abandoned" {
		t.Fatalf("lesson session state = %q, want abandoned", status)
	}
}

func TestDeleteRemovesOwnedVocabularyData(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	for _, statement := range []string{
		"INSERT INTO meanings (vocabulary_id, position, text, normalized_text) VALUES (?, 0, 'cat', 'cat')",
		"INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 3, 456)",
		"INSERT INTO mistake_visibility (vocabulary_id, hidden_at) VALUES (?, 1)",
	} {
		if _, err := db.Exec(statement, id); err != nil {
			t.Fatal(err)
		}
	}

	mediaResult, err := db.Exec(`
		INSERT INTO media (kind, mime_type, sha256, created_at)
		VALUES ('audio', 'audio/mpeg', ?, 1)`, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := mediaResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_content (media_id, content) VALUES (?, X'01')", mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id)
		VALUES (?, 'pronunciation', ?)`, id, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO vocabulary_examples (
			vocabulary_id, origin, sentence, created_at, updated_at
		) VALUES (?, 'manual', '猫がいる。', 1, 1)`, id); err != nil {
		t.Fatal(err)
	}

	lessonResult, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position)
		VALUES (?, ?, 0)`, lessonID, id); err != nil {
		t.Fatal(err)
	}

	reviewResult, err := db.Exec("INSERT INTO review_sessions (kind, status) VALUES ('normal', 'active')")
	if err != nil {
		t.Fatal(err)
	}
	reviewID, err := reviewResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'completed')`, reviewID, id)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_prompts (session_item_id, prompt_type, position, status)
		VALUES (?, 'meaning', 0, 'passed')`, itemID); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at,
			first_attempt_correct_count, prompt_count
		) VALUES (?, 'success', 2, 3, 1, 1, 1)`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	resultID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE review_sessions SET last_undo_result_id = ? WHERE id = ?", resultID, reviewID); err != nil {
		t.Fatal(err)
	}

	captureResult, err := db.Exec(`
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, source_kind,
			capture_nonce, request_hash, vocabulary_id, status, created_at
		) VALUES ('猫', '猫', '猫', 'video', ?, ?, ?, 'accepted', 1)`,
		"11111111111111111111111111111111", strings.Repeat("b", 64), id)
	if err != nil {
		t.Fatal(err)
	}
	captureID, err := captureResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO vocabulary_examples (
			vocabulary_id, mining_capture_id, origin, sentence, created_at, updated_at
		) VALUES (?, ?, 'mined', '猫がいる。', 1, 1)`, id, captureID); err != nil {
		t.Fatal(err)
	}

	if err := NewStore(db).ApplyAction(ctx, id, ActionDelete); err != nil {
		t.Fatal(err)
	}
	assertVocabularyDeleted(t, db, id)
	for _, table := range []string{
		"meanings", "srs_states", "mistake_visibility", "vocabulary_media",
		"vocabulary_examples", "lesson_session_items", "review_session_items",
		"review_prompts", "review_results", "mining_captures", "media", "media_content",
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rows", table, count)
		}
	}
	var tombstones int
	if err := db.QueryRow("SELECT COUNT(*) FROM mining_capture_tombstones").Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Errorf("capture tombstones = %d, want 1", tombstones)
	}
	var reviewStatus string
	var undoBoundary int64
	if err := db.QueryRow("SELECT status, last_undo_result_id FROM review_sessions WHERE id = ?", reviewID).Scan(&reviewStatus, &undoBoundary); err != nil {
		t.Fatal(err)
	}
	if reviewStatus != "abandoned" || undoBoundary != resultID {
		t.Errorf("review session = %q with undo boundary %d", reviewStatus, undoBoundary)
	}
	var lessonStatus string
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", lessonID).Scan(&lessonStatus); err != nil {
		t.Fatal(err)
	}
	if lessonStatus != "abandoned" {
		t.Errorf("lesson session = %q, want abandoned", lessonStatus)
	}
}

func TestDeleteAbandonsReviewLinkedToAffectedLesson(t *testing.T) {
	for _, reviewStatus := range []string{"active", "paused"} {
		t.Run(reviewStatus, func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			vocabularyIDs := make([]int64, 6)
			for i := range vocabularyIDs {
				vocabularyIDs[i] = insertVocabulary(t, db, "unlearned")
			}

			lessonResult, err := db.Exec(`
				INSERT INTO lesson_sessions (status, phase, current_batch)
				VALUES ('active', 'study', 0)`)
			if err != nil {
				t.Fatal(err)
			}
			lessonID, err := lessonResult.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			for position, vocabularyID := range vocabularyIDs {
				if _, err := db.Exec(`
					INSERT INTO lesson_session_items (session_id, vocabulary_id, position, batch_number)
					VALUES (?, ?, ?, ?)`, lessonID, vocabularyID, position, position/5); err != nil {
					t.Fatal(err)
				}
			}

			reviewResult, err := db.Exec(`
				INSERT INTO review_sessions (kind, status, lesson_session_id)
				VALUES ('extra', ?, ?)`, reviewStatus, lessonID)
			if err != nil {
				t.Fatal(err)
			}
			reviewID, err := reviewResult.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
				VALUES (?, ?, 0, 'current')`, reviewID, vocabularyIDs[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				UPDATE lesson_sessions
				SET phase = 'review'
				WHERE id = ?`, lessonID); err != nil {
				t.Fatal(err)
			}

			deletedID := vocabularyIDs[5]
			if err := NewStore(db).ApplyAction(ctx, deletedID, ActionDelete); err != nil {
				t.Fatal(err)
			}
			assertVocabularyDeleted(t, db, deletedID)

			var lessonStatus string
			if err := db.QueryRow(`
				SELECT status
				FROM lesson_sessions
				WHERE id = ?`, lessonID).Scan(&lessonStatus); err != nil {
				t.Fatal(err)
			}
			if lessonStatus != "abandoned" {
				t.Fatalf("lesson session state = %q, want abandoned", lessonStatus)
			}

			var gotReviewStatus string
			var reviewCompletedAt sql.NullInt64
			var linkedLessonID int64
			if err := db.QueryRow(`
				SELECT status, completed_at, lesson_session_id
				FROM review_sessions
				WHERE id = ?`, reviewID).Scan(&gotReviewStatus, &reviewCompletedAt, &linkedLessonID); err != nil {
				t.Fatal(err)
			}
			if gotReviewStatus != "abandoned" || !reviewCompletedAt.Valid || linkedLessonID != lessonID {
				t.Fatalf("review session state = %q, completed %v, lesson %d", gotReviewStatus, reviewCompletedAt, linkedLessonID)
			}
		})
	}
}

func TestResetAfterCompletedReviewMakesResultNonUndoable(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, 3, ?)`, id, time.Now().UTC().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	reviewStore := reviews.NewStore(db)
	reviewID, err := reviewStore.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for {
		state, err := reviewStore.State(ctx, reviewID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "completed" {
			break
		}
		answer := "to eat"
		if state.PromptType == "pronunciation" {
			answer = "たべる"
		}
		if _, err := reviewStore.Answer(ctx, reviewID, state.PromptID, answer); err != nil {
			t.Fatal(err)
		}
	}

	if err := NewStore(db).ApplyAction(ctx, id, ActionReset); err != nil {
		t.Fatal(err)
	}
	assertVocabularyState(t, db, id, "unlearned", false, false)
	if err := reviewStore.Undo(ctx, reviewID); err == nil {
		t.Fatal("undo succeeded after vocabulary progress was reset")
	}
}

func openTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "vocabulary.sqlite"))
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

func insertVocabulary(t *testing.T, db *sql.DB, status string) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	var sequence int
	if err := db.QueryRow("SELECT COUNT(*) + 1 FROM vocabulary").Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	expression := fmt.Sprintf("食べる%d", sequence)
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		)
		VALUES (?, ?, 'たべる', 'たべる', ?, ?, ?)`, expression, expression, status, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertVocabularyState(t *testing.T, db *sql.DB, id int64, wantStatus string, wantSRS, wantSuspended bool) {
	t.Helper()
	var status string
	var srsID, suspendedAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT v.status, ss.vocabulary_id, ss.suspended_at
		FROM vocabulary v
		LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(&status, &srsID, &suspendedAt); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus {
		t.Errorf("status = %q, want %q", status, wantStatus)
	}
	if srsID.Valid != wantSRS {
		t.Errorf("SRS state present = %t, want %t", srsID.Valid, wantSRS)
	}
	if suspendedAt.Valid != wantSuspended {
		t.Errorf("SRS state suspended = %t, want %t", suspendedAt.Valid, wantSuspended)
	}
}

func assertVocabularyDeleted(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("vocabulary %d still exists", id)
	}
}

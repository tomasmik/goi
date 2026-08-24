package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/textnorm"
)

type Store struct {
	db       *sql.DB
	examples *examples.Store
}

type ListItem struct {
	ID             int64
	Expression     string
	Pronunciation  string
	Meaning        string
	Status         string
	StatusLabel    string
	StatusClass    string
	KnownElsewhere bool
	LeechActive    bool
	LeechSuspended bool
	FormerLeech    bool
}

type Item struct {
	ID                  int64
	ContentRevision     int64
	Expression          string
	Pronunciation       string
	Meanings            []string
	Status              string
	StatusLabel         string
	StatusClass         string
	KnownElsewhere      bool
	Notes               string
	SourceLabel         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Media               []MediaItem
	Examples            []examples.Example
	CanMarkKnown        bool
	CanToggleSuspension bool
	CanResetProgress    bool
	LeechActive         bool
	LeechSuspended      bool
	FormerLeech         bool
	StageLabel          string
	NextReview          time.Time
	ReviewCount         int
	FirstTryCorrect     int
	PromptCount         int
	ReviewHistory       []ReviewHistoryItem
}

type ReviewHistoryItem struct {
	ReviewedAt time.Time
	Outcome    string
	StageFrom  string
	StageTo    string
}

type MediaItem struct {
	ID          int64
	Kind        string
	SourceName  string
	SourceURL   string
	LicenseName string
	LicenseURL  string
}

type CreateInput struct {
	Expression         string
	Pronunciation      string
	Meanings           []string
	Notes              string
	SourceLabel        string
	AllowDuplicate     bool
	AllowSparse        bool
	ExampleSentence    string
	ExampleTranslation string
	ExampleTarget      string
	Audio              *media.Upload
	Picture            *media.Upload
	RemoveAudio        bool
	RemovePicture      bool
}

type AddKnownResult struct {
	Created             int
	MarkedKnown         int
	AlreadyKnown        int
	SkippedActiveLesson int
}

type knownCandidate struct {
	id             int64
	status         string
	knownElsewhere bool
	hasSRS         bool
	reserved       bool
}

type knownCandidateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (result AddKnownResult) Added() int {
	return result.Created + result.MarkedKnown
}

type validatedInput struct {
	expression    string
	pronunciation string
	meanings      []string
	notes         string
	sourceLabel   string
	audio         *media.Upload
	picture       *media.Upload
	removeAudio   bool
	removePicture bool
}

type Action string
type ListFilter string
type ListSort string

var (
	ErrDuplicate        = errors.New("vocabulary already exists")
	ErrInvalidInput     = errors.New("invalid vocabulary input")
	ErrRevisionConflict = errors.New("vocabulary content revision conflict")
)

type validationError string

func (err validationError) Error() string {
	return string(err)
}

func (err validationError) Is(target error) bool {
	return target == ErrInvalidInput
}

func (err validationError) UserMessage() string {
	return string(err)
}

type duplicateError struct {
	id int64
}

type revisionConflictError struct{}

func (revisionConflictError) Error() string {
	return "vocabulary changed after the edit form was opened"
}

func (revisionConflictError) UserMessage() string {
	return "This word changed after you opened the edit form. Your text and removal choices are still here; review them and save again to replace the newer version."
}

func (revisionConflictError) Is(target error) bool {
	return target == ErrRevisionConflict
}

func (err duplicateError) Error() string {
	return fmt.Sprintf("vocabulary already exists as item %d", err.id)
}

func (err duplicateError) UserMessage() string {
	return err.Error()
}

func (err duplicateError) Is(target error) bool {
	return target == ErrDuplicate
}

type lifecycleState struct {
	status         string
	srsID          sql.NullInt64
	suspendedAt    sql.NullInt64
	knownElsewhere bool
	complete       bool
}

const (
	maxExpressionRunes    = 256
	maxPronunciationRunes = 256
	maxMeaningsRunes      = 2000
	maxNotesRunes         = 2000
	maxSourceLabelRunes   = 300
	maximumListPageSize   = 100

	ListFilterAll        ListFilter = ""
	ListFilterLearning   ListFilter = "learning"
	ListFilterNotStarted ListFilter = "not_started"
	ListFilterKnown      ListFilter = "known"
	ListFilterSuspended  ListFilter = "suspended"
	ListFilterArchived   ListFilter = "archived"

	ListSortUpdated    ListSort = ""
	ListSortNewest     ListSort = "newest"
	ListSortExpression ListSort = "expression"

	ActionSuspend      Action = "suspend"
	ActionArchive      Action = "archive"
	ActionReset        Action = "reset"
	ActionDelete       Action = "delete"
	ActionHideLeech    Action = "hide-leech"
	ActionRestoreLeech Action = "restore-leech"
	ActionLearn        Action = "learn"
	ActionMarkKnown    Action = "mark-known"
)

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, examples: examples.NewStore(db)}
}

func (s *Store) ListCount(ctx context.Context, search string) (int, error) {
	return s.ListCountFiltered(ctx, search, ListFilterAll)
}

func (s *Store) ListCountFiltered(ctx context.Context, search string, status ListFilter) (int, error) {
	filter, args := vocabularyListFilter(search, status)
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary v"+filter, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count vocabulary: %w", err)
	}
	return count, nil
}

func (s *Store) ListPage(ctx context.Context, search string, limit, offset int) ([]ListItem, error) {
	return s.ListPageFiltered(ctx, search, ListFilterAll, limit, offset)
}

func (s *Store) ListPageFiltered(ctx context.Context, search string, status ListFilter, limit, offset int) ([]ListItem, error) {
	return s.ListPageSorted(ctx, search, status, ListSortUpdated, limit, offset)
}

func (s *Store) ListPageSorted(ctx context.Context, search string, status ListFilter, sort ListSort, limit, offset int) ([]ListItem, error) {
	if limit <= 0 || limit > maximumListPageSize {
		return nil, fmt.Errorf("vocabulary page size must be between 1 and %d", maximumListPageSize)
	}
	if offset < 0 {
		return nil, errors.New("vocabulary page offset must not be negative")
	}
	filter, args := vocabularyListFilter(search, status)
	return s.list(ctx, filter, args, vocabularyListOrder(sort), limit, offset)
}

func normalizeListFilter(value string) ListFilter {
	filter := ListFilter(value)
	switch filter {
	case ListFilterLearning, ListFilterNotStarted, ListFilterKnown, ListFilterSuspended, ListFilterArchived:
		return filter
	default:
		return ListFilterAll
	}
}

func normalizeListSort(value string) ListSort {
	sort := ListSort(value)
	switch sort {
	case ListSortNewest, ListSortExpression:
		return sort
	default:
		return ListSortUpdated
	}
}

func vocabularyListOrder(sort ListSort) string {
	switch normalizeListSort(string(sort)) {
	case ListSortNewest:
		return "v.created_at DESC, v.id DESC"
	case ListSortExpression:
		return "v.normalized_expression ASC, v.id ASC"
	default:
		return "v.updated_at DESC, v.id DESC"
	}
}

func vocabularyListFilter(search string, status ListFilter) (string, []any) {
	search = escapeLike(textnorm.Normalize(search))
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 3)
	switch normalizeListFilter(string(status)) {
	case ListFilterLearning:
		conditions = append(conditions, "v.status = 'active'")
	case ListFilterNotStarted:
		conditions = append(conditions, "v.status = 'unlearned' AND v.known_elsewhere_at IS NULL")
	case ListFilterKnown:
		conditions = append(conditions, "v.known_elsewhere_at IS NOT NULL")
	case ListFilterSuspended:
		conditions = append(conditions, "v.status = 'suspended'")
	case ListFilterArchived:
		conditions = append(conditions, "v.status = 'archived'")
	}
	if search != "" {
		conditions = append(conditions, `(v.normalized_expression LIKE '%' || ? || '%' ESCAPE '\'
			OR v.normalized_pronunciation LIKE '%' || ? || '%' ESCAPE '\'
			OR EXISTS (
			SELECT 1 FROM meanings m WHERE m.vocabulary_id = v.id AND m.normalized_text LIKE '%' || ? || '%' ESCAPE '\'
		))`)
		args = append(args, search, kana.ToHiragana(search), search)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func (s *Store) list(ctx context.Context, filter string, args []any, order string, limit, offset int) ([]ListItem, error) {
	query := `
		SELECT v.id, v.expression, v.pronunciation,
		       COALESCE((SELECT m.text FROM meanings m WHERE m.vocabulary_id = v.id AND m.position = 0), ''),
		       v.status, v.known_elsewhere_at IS NOT NULL,
		       COALESCE(ls.active, 0),
		       COALESCE(ls.active = 1 AND v.status = 'suspended', 0),
		       COALESCE(ls.ever_leech = 1 AND ls.active = 0, 0)
		FROM vocabulary v
		LEFT JOIN leech_states ls ON ls.vocabulary_id = v.id` + filter + " ORDER BY " + order
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list vocabulary: %w", err)
	}
	defer rows.Close()

	items := make([]ListItem, 0)
	for rows.Next() {
		var item ListItem
		if err := rows.Scan(
			&item.ID, &item.Expression, &item.Pronunciation, &item.Meaning, &item.Status, &item.KnownElsewhere,
			&item.LeechActive, &item.LeechSuspended, &item.FormerLeech,
		); err != nil {
			return nil, fmt.Errorf("scan vocabulary list: %w", err)
		}
		item.StatusLabel, item.StatusClass = displayStatus(item.Status, item.KnownElsewhere)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vocabulary list: %w", err)
	}
	return items, nil
}

func (s *Store) KnownCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary "+knownVocabularyWhere).Scan(&count); err != nil {
		return 0, fmt.Errorf("count known vocabulary: %w", err)
	}
	return count, nil
}

func (s *Store) KnownExpressions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT expression, normalized_expression
		FROM vocabulary
		`+knownVocabularyWhere+`
		ORDER BY normalized_expression, id`)
	if err != nil {
		return nil, fmt.Errorf("load known vocabulary expressions: %w", err)
	}
	defer rows.Close()

	expressions := make([]string, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var expression, normalized string
		if err := rows.Scan(&expression, &normalized); err != nil {
			return nil, fmt.Errorf("scan known vocabulary expression: %w", err)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		expressions = append(expressions, expression)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate known vocabulary expressions: %w", err)
	}
	return expressions, nil
}

func (s *Store) LeechStatus(ctx context.Context, id int64) (string, error) {
	var active, everLeech, suspended bool
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(ls.active, 0), COALESCE(ls.ever_leech, 0),
		       COALESCE(ls.active = 1 AND v.status = 'suspended', 0)
		FROM vocabulary v
		LEFT JOIN leech_states ls ON ls.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(&active, &everLeech, &suspended)
	if err != nil {
		return "", err
	}
	if active && suspended {
		return "suspended_leech", nil
	}
	if active {
		return "leech", nil
	}
	if everLeech {
		return "former_leech", nil
	}
	return "", nil
}

func (s *Store) StatusCount(ctx context.Context, filter ListFilter) (int, error) {
	return s.ListCountFiltered(ctx, "", filter)
}

const knownVocabularyWhere = `
	WHERE (
	      status IN ('active', 'suspended')
	      OR known_elsewhere_at IS NOT NULL
	      OR EXISTS (SELECT 1 FROM srs_states ss WHERE ss.vocabulary_id = vocabulary.id)
	  )`

func (s *Store) KnownExpressionStatuses(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT normalized_expression,
		       CASE
		           WHEN ls.active = 1 AND vocabulary.status = 'suspended' THEN 'suspended_leech'
		           WHEN ls.active = 1 THEN 'leech'
		           ELSE 'known'
		       END
		FROM vocabulary
		LEFT JOIN leech_states ls ON ls.vocabulary_id = vocabulary.id
		`+knownVocabularyWhere)
	if err != nil {
		return nil, fmt.Errorf("load known vocabulary: %w", err)
	}
	defer rows.Close()

	expressions := make(map[string]string)
	for rows.Next() {
		var expression, status string
		if err := rows.Scan(&expression, &status); err != nil {
			return nil, fmt.Errorf("scan known vocabulary: %w", err)
		}
		if existing := expressions[expression]; coverageStatusPriority(status) > coverageStatusPriority(existing) {
			expressions[expression] = status
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate known vocabulary: %w", err)
	}
	return expressions, nil
}

func coverageStatusPriority(status string) int {
	switch status {
	case "suspended_leech":
		return 3
	case "leech":
		return 2
	case "known":
		return 1
	default:
		return 0
	}
}

const knownCandidateQuery = `
	SELECT v.id, v.status, v.known_elsewhere_at IS NOT NULL,
	       EXISTS (SELECT 1 FROM srs_states ss WHERE ss.vocabulary_id = v.id),
	       EXISTS (
	           SELECT 1
	           FROM lesson_session_items lsi
	           JOIN lesson_sessions ls ON ls.id = lsi.session_id
	           WHERE lsi.vocabulary_id = v.id AND ls.status = 'active'
	       )
	FROM vocabulary v
	WHERE v.normalized_expression = ?
	ORDER BY v.is_duplicate, v.id DESC
	LIMIT 1`

func loadKnownCandidate(ctx context.Context, queryer knownCandidateQueryer, normalizedExpression string) (knownCandidate, bool, error) {
	var candidate knownCandidate
	err := queryer.QueryRowContext(ctx, knownCandidateQuery, normalizedExpression).Scan(
		&candidate.id,
		&candidate.status,
		&candidate.knownElsewhere,
		&candidate.hasSRS,
		&candidate.reserved,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return knownCandidate{}, false, nil
	}
	if err != nil {
		return knownCandidate{}, false, err
	}
	return candidate, true, nil
}

func classifyKnownCandidate(result *AddKnownResult, candidate knownCandidate, found bool) error {
	if !found {
		result.Created++
		return nil
	}
	switch candidate.status {
	case "active", "suspended":
		result.AlreadyKnown++
	case "unlearned":
		if candidate.knownElsewhere {
			result.AlreadyKnown++
		} else if candidate.reserved {
			result.SkippedActiveLesson++
		} else if candidate.hasSRS {
			return fmt.Errorf("unlearned vocabulary %d unexpectedly has an SRS state", candidate.id)
		} else {
			result.MarkedKnown++
		}
	case "archived":
		if candidate.knownElsewhere || candidate.hasSRS {
			result.AlreadyKnown++
		} else {
			result.MarkedKnown++
		}
	default:
		return fmt.Errorf("cannot mark vocabulary %d with status %q as known", candidate.id, candidate.status)
	}
	return nil
}

func (s *Store) PreviewKnown(ctx context.Context, value string) (AddKnownResult, error) {
	expressions, err := parseKnownExpressions(value)
	if err != nil {
		return AddKnownResult{}, err
	}
	var result AddKnownResult
	for _, expression := range expressions {
		candidate, found, err := loadKnownCandidate(ctx, s.db, textnorm.Normalize(expression))
		if err != nil {
			return AddKnownResult{}, fmt.Errorf("load known vocabulary %q: %w", expression, err)
		}
		if err := classifyKnownCandidate(&result, candidate, found); err != nil {
			return AddKnownResult{}, err
		}
	}
	return result, nil
}

func (s *Store) AddKnown(ctx context.Context, value string) (AddKnownResult, error) {
	expressions, err := parseKnownExpressions(value)
	if err != nil {
		return AddKnownResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AddKnownResult{}, fmt.Errorf("begin known vocabulary transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Unix()
	var result AddKnownResult
	for _, expression := range expressions {
		normalizedExpression := textnorm.Normalize(expression)
		candidate, found, err := loadKnownCandidate(ctx, tx, normalizedExpression)
		if err != nil {
			return AddKnownResult{}, fmt.Errorf("load known vocabulary %q: %w", expression, err)
		}
		if !found {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO vocabulary (
					expression, normalized_expression, status, known_elsewhere_at, created_at, updated_at
				)
				VALUES (?, ?, 'unlearned', ?, ?, ?)`,
				expression, normalizedExpression, now, now, now,
			); err != nil {
				return AddKnownResult{}, fmt.Errorf("insert known vocabulary %q: %w", expression, err)
			}
			result.Created++
			continue
		}

		switch candidate.status {
		case "active", "suspended":
			result.AlreadyKnown++
		case "unlearned":
			if candidate.knownElsewhere {
				result.AlreadyKnown++
				continue
			}
			if candidate.reserved {
				result.SkippedActiveLesson++
				continue
			}
			if candidate.hasSRS {
				return AddKnownResult{}, fmt.Errorf("unlearned vocabulary %d unexpectedly has an SRS state", candidate.id)
			}
			update, err := tx.ExecContext(ctx, `
				UPDATE vocabulary
				SET known_elsewhere_at = ?, updated_at = ?, content_revision = content_revision + 1
				WHERE id = ? AND status = 'unlearned' AND known_elsewhere_at IS NULL`,
				now, now, candidate.id,
			)
			if err != nil {
				return AddKnownResult{}, fmt.Errorf("mark vocabulary %d as known elsewhere: %w", candidate.id, err)
			}
			affected, err := update.RowsAffected()
			if err != nil {
				return AddKnownResult{}, fmt.Errorf("check known vocabulary %d: %w", candidate.id, err)
			}
			if affected != 1 {
				return AddKnownResult{}, fmt.Errorf("vocabulary %d changed while marking it known", candidate.id)
			}
			result.MarkedKnown++
		case "archived":
			if candidate.knownElsewhere || candidate.hasSRS {
				result.AlreadyKnown++
				continue
			}
			update, err := tx.ExecContext(ctx, `
				UPDATE vocabulary
				SET known_elsewhere_at = ?, updated_at = ?, content_revision = content_revision + 1
				WHERE id = ? AND status = 'archived' AND known_elsewhere_at IS NULL`,
				now, now, candidate.id,
			)
			if err != nil {
				return AddKnownResult{}, fmt.Errorf("mark archived vocabulary %d as known elsewhere: %w", candidate.id, err)
			}
			affected, err := update.RowsAffected()
			if err != nil {
				return AddKnownResult{}, fmt.Errorf("check archived vocabulary %d: %w", candidate.id, err)
			}
			if affected != 1 {
				return AddKnownResult{}, fmt.Errorf("archived vocabulary %d changed while marking it known", candidate.id)
			}
			result.MarkedKnown++
		default:
			return AddKnownResult{}, fmt.Errorf("cannot mark vocabulary %d with status %q as known", candidate.id, candidate.status)
		}
	}
	if err := tx.Commit(); err != nil {
		return AddKnownResult{}, fmt.Errorf("commit known vocabulary: %w", err)
	}
	return result, nil
}

func parseKnownExpressions(value string) ([]string, error) {
	if !utf8.ValidString(value) {
		return nil, validationError("known words must be valid UTF-8")
	}
	parts := strings.FieldsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || strings.ContainsRune(",、;；，", character)
	})
	expressions := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		expression, err := cleanInputText(part, maxExpressionRunes, "word", false)
		if err != nil {
			return nil, err
		}
		normalizedExpression := textnorm.Normalize(expression)
		if normalizedExpression == "" {
			continue
		}
		if _, exists := seen[normalizedExpression]; exists {
			continue
		}
		seen[normalizedExpression] = struct{}{}
		expressions = append(expressions, expression)
	}
	if len(expressions) == 0 {
		return nil, validationError("enter at least one word")
	}
	return expressions, nil
}

func displayStatus(status string, knownElsewhere bool) (string, string) {
	if status == "unlearned" && knownElsewhere {
		return "Known elsewhere", "known"
	}
	switch status {
	case "active":
		return "Studying", status
	case "unlearned":
		return "Not started", status
	case "suspended":
		return "Suspended", status
	case "archived":
		return "Archived", status
	}
	return status, status
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

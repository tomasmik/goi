package mining

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/textnorm"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type BulkAcceptResult struct {
	Added       int
	Attached    int
	NeedsReview int
}

type resolvedCandidate struct {
	revision  int64
	candidate candidateAcceptance
}

func (s *Store) BulkAcceptCandidates(ctx context.Context, ids []int64, candidates map[int64]resolvedCandidate) (BulkAcceptResult, error) {
	ids, err := validatedCaptureIDs(ids)
	if err != nil {
		return BulkAcceptResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BulkAcceptResult{}, fmt.Errorf("begin bulk mining acceptance: %w", err)
	}
	defer tx.Rollback()
	result := BulkAcceptResult{}
	for _, id := range ids {
		state, err := loadTransitionState(ctx, tx, id)
		if err != nil {
			return BulkAcceptResult{}, err
		}
		if state.status != StatusPending {
			return BulkAcceptResult{}, validationError("some selected captures changed; reload and try again")
		}
		resolved, ok := candidates[id]
		if !ok {
			result.NeedsReview++
			continue
		}
		if state.revision != resolved.revision {
			return BulkAcceptResult{}, validationError("some selected captures changed; reload and try again")
		}
		candidate := resolved.candidate
		if strings.TrimSpace(candidate.reading) == "" || !hasMeaning(candidate.meanings) {
			result.NeedsReview++
			continue
		}
		expression := candidate.written
		if strings.TrimSpace(expression) == "" {
			expression = state.expression
		}
		var vocabularyID int64
		var pronunciation string
		var matchingVocabulary int
		err = tx.QueryRowContext(ctx, `
			SELECT v.id, v.normalized_pronunciation, (
				SELECT COUNT(*) FROM vocabulary matches
				WHERE matches.normalized_expression = v.normalized_expression
			)
			FROM vocabulary v
			WHERE v.normalized_expression = ?
			ORDER BY v.is_duplicate, v.id LIMIT 1`, textnorm.Normalize(expression)).Scan(
			&vocabularyID, &pronunciation, &matchingVocabulary,
		)
		switch {
		case err == nil:
			if matchingVocabulary != 1 || pronunciation != "" && pronunciation != kana.ToHiragana(candidate.reading) {
				result.NeedsReview++
				continue
			}
			if err := s.vocabulary.CompleteSparseInTx(ctx, tx, vocabularyID, candidate.reading, candidate.meanings); err != nil {
				return BulkAcceptResult{}, fmt.Errorf("complete existing vocabulary from mining candidate: %w", err)
			}
			if err := s.attachInTx(ctx, tx, id, state.revision, vocabularyID, state, minedExampleDetails{}); err != nil {
				return BulkAcceptResult{}, err
			}
			result.Attached++
		case errors.Is(err, sql.ErrNoRows):
			state.expression = expression
			if _, err := s.acceptInTx(ctx, tx, id, state.revision, state, vocabulary.CreateInput{
				Pronunciation: candidate.reading,
				Meanings:      candidate.meanings,
			}); err != nil {
				return BulkAcceptResult{}, err
			}
			result.Added++
		default:
			return BulkAcceptResult{}, fmt.Errorf("find vocabulary for clear dictionary match: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return BulkAcceptResult{}, fmt.Errorf("commit bulk mining acceptance: %w", err)
	}
	return result, nil
}

func (s *Store) AcceptCandidate(ctx context.Context, captureID, captureRevision int64, candidate candidateAcceptance, input vocabulary.CreateInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin mining candidate acceptance: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, captureID)
	if err != nil {
		return 0, err
	}
	if err := requirePendingRevision(state, captureRevision); err != nil {
		return 0, err
	}
	if strings.TrimSpace(candidate.reading) == "" || !hasMeaning(candidate.meanings) {
		return 0, ErrStaleEnrichment
	}
	if strings.TrimSpace(input.Pronunciation) == "" {
		input.Pronunciation = candidate.reading
	}
	if !hasMeaning(input.Meanings) {
		input.Meanings = candidate.meanings
	}
	if strings.TrimSpace(candidate.written) != "" {
		state.expression = candidate.written
	}
	vocabularyID, err := s.acceptInTx(ctx, tx, captureID, captureRevision, state, input)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit mining candidate acceptance: %w", err)
	}
	return vocabularyID, nil
}

func (s *Store) attachCandidateWithExample(ctx context.Context, captureID, captureRevision int64, candidate candidateAcceptance, input vocabulary.CreateInput, example minedExampleDetails) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin mining candidate attachment: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, captureID)
	if err != nil {
		return 0, err
	}
	if err := requirePendingRevision(state, captureRevision); err != nil {
		return 0, err
	}
	if strings.TrimSpace(candidate.reading) == "" || !hasMeaning(candidate.meanings) {
		return 0, ErrStaleEnrichment
	}
	expression := candidate.written
	if strings.TrimSpace(expression) == "" {
		expression = state.expression
	}
	var vocabularyID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM vocabulary
		WHERE normalized_expression = ?
		ORDER BY is_duplicate, id LIMIT 1`, textnorm.Normalize(expression)).Scan(&vocabularyID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, validationError("matching vocabulary item was not found")
	}
	if err != nil {
		return 0, fmt.Errorf("load vocabulary for mining candidate attachment: %w", err)
	}
	if strings.TrimSpace(input.Pronunciation) == "" {
		input.Pronunciation = candidate.reading
	}
	if !hasMeaning(input.Meanings) {
		input.Meanings = candidate.meanings
	}
	if err := s.vocabulary.CompleteSparseInTx(ctx, tx, vocabularyID, input.Pronunciation, input.Meanings); err != nil {
		return 0, fmt.Errorf("complete existing vocabulary from mining candidate: %w", err)
	}
	if err := s.attachInTx(ctx, tx, captureID, captureRevision, vocabularyID, state, example); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit mining candidate attachment: %w", err)
	}
	return vocabularyID, nil
}

func (s *Store) existingVocabularyID(ctx context.Context, expression string) (int64, error) {
	var vocabularyID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM vocabulary
		WHERE normalized_expression = ?
		ORDER BY is_duplicate, id LIMIT 1`, textnorm.Normalize(expression)).Scan(&vocabularyID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find vocabulary for mining candidate: %w", err)
	}
	return vocabularyID, nil
}

type candidateAcceptance struct {
	written  string
	reading  string
	meanings []string
}

func hasMeaning(meanings []string) bool {
	for _, meaning := range meanings {
		if strings.TrimSpace(meaning) != "" {
			return true
		}
	}
	return false
}

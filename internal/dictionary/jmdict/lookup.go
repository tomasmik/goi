package jmdict

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/textnorm"
)

func (c *Cache) Lookup(ctx context.Context, expression, reading string) (Match, error) {
	match := Match{SourceCreated: c.metadata.Created, SourceVersion: c.metadata.Version}
	normalizedExpression := textnorm.Normalize(expression)
	if normalizedExpression == "" {
		match.State = MatchNone
		return match, nil
	}
	normalizedReading := normalizeReading(reading)

	pairs := make(map[string]lookupPair)
	if err := c.loadWrittenPairs(ctx, normalizedExpression, normalizedReading, pairs); err != nil {
		return Match{}, err
	}
	if isKana(expression) {
		if err := c.loadReadingPairs(ctx, normalizeReading(expression), normalizedReading, pairs); err != nil {
			return Match{}, err
		}
	}

	ranked := make([]rankedCandidate, 0, len(pairs))
	for _, pair := range pairs {
		senses, err := c.loadSenses(ctx, pair)
		if err != nil {
			return Match{}, err
		}
		if len(senses) == 0 {
			continue
		}
		ranked = append(ranked, rankedCandidate{
			Candidate: Candidate{
				EntrySequence: pair.sequence,
				Written:       pair.written,
				Reading:       pair.reading,
				MatchType:     pair.matchType(),
				Priority:      pair.combinedPriority(),
				SourceOrder:   pair.sourceOrder,
				Senses:        senses,
			},
			exactWritten:    pair.exactWritten,
			writtenPriority: pair.writtenPriority,
			readingPriority: pair.readingPriority,
		})
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].exactWritten != ranked[right].exactWritten {
			return ranked[left].exactWritten
		}
		if ranked[left].readingPriority != ranked[right].readingPriority {
			return ranked[left].readingPriority < ranked[right].readingPriority
		}
		if ranked[left].writtenPriority != ranked[right].writtenPriority {
			return ranked[left].writtenPriority < ranked[right].writtenPriority
		}
		if ranked[left].SourceOrder != ranked[right].SourceOrder {
			return ranked[left].SourceOrder < ranked[right].SourceOrder
		}
		if ranked[left].EntrySequence != ranked[right].EntrySequence {
			return ranked[left].EntrySequence < ranked[right].EntrySequence
		}
		if ranked[left].Written != ranked[right].Written {
			return ranked[left].Written < ranked[right].Written
		}
		return ranked[left].Reading < ranked[right].Reading
	})
	match.Candidates = make([]Candidate, len(ranked))
	for index := range ranked {
		match.Candidates[index] = ranked[index].Candidate
	}
	switch len(match.Candidates) {
	case 0:
		match.State = MatchNone
	case 1:
		match.State = MatchReady
	default:
		match.State = MatchAmbiguous
	}
	return match, nil
}

type lookupPair struct {
	sequence        int64
	kanjiID         sql.NullInt64
	readingID       int64
	written         string
	reading         string
	writtenPriority int
	readingPriority int
	sourceOrder     int
	exactWritten    bool
}

type rankedCandidate struct {
	Candidate
	exactWritten    bool
	writtenPriority int
	readingPriority int
}

func (pair lookupPair) matchType() string {
	if pair.exactWritten {
		return "written"
	}
	return "reading"
}

func (pair lookupPair) combinedPriority() int {
	return pair.readingPriority*(unrankedPriority+1) + pair.writtenPriority
}

func (c *Cache) loadWrittenPairs(ctx context.Context, expression, reading string, pairs map[string]lookupPair) error {
	rows, err := c.db.QueryContext(ctx, `
		SELECT e.ent_seq, k.id, r.id, k.text, r.text,
		       k.priority_rank, r.priority_rank, e.source_order
		FROM jmdict_kanji k
		JOIN jmdict_entries e ON e.ent_seq = k.ent_seq
		JOIN jmdict_reading_kanji rk ON rk.kanji_id = k.id
		JOIN jmdict_readings r ON r.id = rk.reading_id
		WHERE k.normalized_text = ? AND (? = '' OR r.normalized_hiragana = ?)`, expression, reading, reading)
	if err != nil {
		return fmt.Errorf("look up JMdict written form: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pair lookupPair
		if err := rows.Scan(
			&pair.sequence, &pair.kanjiID, &pair.readingID, &pair.written, &pair.reading,
			&pair.writtenPriority, &pair.readingPriority, &pair.sourceOrder,
		); err != nil {
			return fmt.Errorf("scan JMdict written match: %w", err)
		}
		pair.exactWritten = true
		mergeLookupPair(pairs, pair)
	}
	return rows.Err()
}

func (c *Cache) loadReadingPairs(ctx context.Context, expression, suppliedReading string, pairs map[string]lookupPair) error {
	if suppliedReading != "" && expression != suppliedReading {
		return nil
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT e.ent_seq, k.id, r.id, COALESCE(k.text, ''), r.text,
		       COALESCE(k.priority_rank, ?), r.priority_rank, e.source_order
		FROM jmdict_readings r
		JOIN jmdict_entries e ON e.ent_seq = r.ent_seq
		LEFT JOIN jmdict_reading_kanji rk ON rk.reading_id = r.id
		LEFT JOIN jmdict_kanji k ON k.id = rk.kanji_id
		WHERE r.normalized_hiragana = ?`, unrankedPriority, expression)
	if err != nil {
		return fmt.Errorf("look up JMdict reading: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pair lookupPair
		if err := rows.Scan(
			&pair.sequence, &pair.kanjiID, &pair.readingID, &pair.written, &pair.reading,
			&pair.writtenPriority, &pair.readingPriority, &pair.sourceOrder,
		); err != nil {
			return fmt.Errorf("scan JMdict reading match: %w", err)
		}
		mergeLookupPair(pairs, pair)
	}
	return rows.Err()
}

func (c *Cache) loadSenses(ctx context.Context, pair lookupPair) ([]Sense, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT s.id, s.position
		FROM jmdict_senses s
		WHERE s.ent_seq = ?
		  AND (NOT EXISTS (SELECT 1 FROM jmdict_sense_kanji sk WHERE sk.sense_id = s.id)
		       OR (? IS NOT NULL AND EXISTS (
		           SELECT 1 FROM jmdict_sense_kanji sk WHERE sk.sense_id = s.id AND sk.kanji_id = ?
		       )))
		  AND (NOT EXISTS (SELECT 1 FROM jmdict_sense_reading sr WHERE sr.sense_id = s.id)
		       OR EXISTS (
		           SELECT 1 FROM jmdict_sense_reading sr WHERE sr.sense_id = s.id AND sr.reading_id = ?
		       ))
		ORDER BY s.position`, pair.sequence, nullableInt64(pair.kanjiID), nullableInt64(pair.kanjiID), pair.readingID)
	if err != nil {
		return nil, fmt.Errorf("load applicable JMdict senses: %w", err)
	}
	defer rows.Close()
	type senseRow struct {
		id     int64
		number int
	}
	var senseRows []senseRow
	for rows.Next() {
		var value senseRow
		if err := rows.Scan(&value.id, &value.number); err != nil {
			return nil, fmt.Errorf("scan applicable JMdict sense: %w", err)
		}
		senseRows = append(senseRows, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	senses := make([]Sense, 0, len(senseRows))
	for _, row := range senseRows {
		sense := Sense{Number: row.number}
		glossRows, err := c.db.QueryContext(ctx, `SELECT text, language, gloss_type FROM jmdict_glosses WHERE sense_id = ? ORDER BY position`, row.id)
		if err != nil {
			return nil, fmt.Errorf("load JMdict glosses: %w", err)
		}
		for glossRows.Next() {
			var gloss Gloss
			if err := glossRows.Scan(&gloss.Text, &gloss.Language, &gloss.Type); err != nil {
				glossRows.Close()
				return nil, err
			}
			sense.Glosses = append(sense.Glosses, gloss)
		}
		if err := glossRows.Err(); err != nil {
			glossRows.Close()
			return nil, err
		}
		if err := glossRows.Close(); err != nil {
			return nil, err
		}
		posRows, err := c.db.QueryContext(ctx, `SELECT value FROM jmdict_pos WHERE sense_id = ? ORDER BY position`, row.id)
		if err != nil {
			return nil, fmt.Errorf("load JMdict parts of speech: %w", err)
		}
		for posRows.Next() {
			var value string
			if err := posRows.Scan(&value); err != nil {
				posRows.Close()
				return nil, err
			}
			sense.PartsOfSpeech = append(sense.PartsOfSpeech, value)
		}
		if err := posRows.Err(); err != nil {
			posRows.Close()
			return nil, err
		}
		if err := posRows.Close(); err != nil {
			return nil, err
		}
		senses = append(senses, sense)
	}
	return senses, nil
}

func normalizeReading(value string) string {
	return kana.ToHiragana(textnorm.Normalize(value))
}

func isKana(value string) bool {
	found := false
	for _, character := range textnorm.Normalize(value) {
		switch {
		case character >= 'ぁ' && character <= 'ゖ':
			found = true
		case character >= 'ァ' && character <= 'ヺ':
			found = true
		case character == 'ー' || character == '・' || character == 'ゝ' || character == 'ゞ' || character == 'ヽ' || character == 'ヾ':
		default:
			return false
		}
	}
	return found
}

func pairKey(pair lookupPair) string {
	return fmt.Sprintf("%d/%d/%d", pair.sequence, pair.kanjiID.Int64, pair.readingID)
}

func mergeLookupPair(pairs map[string]lookupPair, pair lookupPair) {
	key := pairKey(pair)
	existing, exists := pairs[key]
	if !exists {
		pairs[key] = pair
		return
	}
	pair.exactWritten = pair.exactWritten || existing.exactWritten
	pair.writtenPriority = min(pair.writtenPriority, existing.writtenPriority)
	pair.readingPriority = min(pair.readingPriority, existing.readingPriority)
	pair.sourceOrder = min(pair.sourceOrder, existing.sourceOrder)
	if pair.written == "" {
		pair.written = existing.written
	}
	if pair.reading == "" {
		pair.reading = existing.reading
	}
	pairs[key] = pair
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

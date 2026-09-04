package dictionary

import (
	"context"
	"errors"
	"log/slog"

	"github.com/tomasmik/goi/internal/dictionary/jiten"
	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type LookupService struct {
	Dictionary *jmdict.Manager
	Frequency  *jiten.Manager
}

func (s *LookupService) Lookup(ctx context.Context, expression, reading string) (jmdict.Match, error) {
	if s.Dictionary == nil {
		return jmdict.Match{}, jmdict.ErrUnavailable
	}
	match, err := s.Dictionary.Lookup(ctx, expression, reading)
	if err != nil || len(match.Candidates) == 0 || s.Frequency == nil {
		return match, err
	}
	pairs := make([]jiten.Pair, len(match.Candidates))
	for index, candidate := range match.Candidates {
		pairs[index] = jiten.Pair{Written: candidate.Written, Reading: candidate.Reading}
	}
	ranks, err := s.Frequency.Lookup(ctx, pairs)
	if ctx.Err() != nil {
		return jmdict.Match{}, ctx.Err()
	}
	if err != nil {
		if !errors.Is(err, jiten.ErrUnavailable) {
			slog.WarnContext(ctx, "could not look up Jiten frequency ranks", "error", err)
		}
		return match, nil
	}
	// Mining identifies candidates by position, so frequency updates must not reorder them.
	for index, rank := range ranks {
		match.Candidates[index].GlobalRank = rank.Global
		match.Candidates[index].NovelRank = rank.Novel
	}
	return match, nil
}

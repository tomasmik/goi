package srs

import "time"

type Stage int

const (
	StageNew Stage = iota
	StageOne
	StageTwo
	StageThree
	StageFour
	StageFive
	StageSix
	StageSeven
	StageEight
	StageEvergreen
)

func Advance(stage Stage, success, sixMonthReviewEnabled bool) Stage {
	if !success {
		switch {
		case stage == StageEvergreen:
			return StageEvergreen
		case stage == StageNew:
			return StageNew
		case stage == StageEight:
			return StageSix
		case stage >= StageFive:
			return StageThree
		default:
			return stage - 1
		}
	}
	if stage == StageEvergreen {
		return StageEvergreen
	}
	if stage == StageSeven && !sixMonthReviewEnabled {
		return StageEvergreen
	}
	return stage + 1
}

func DueAt(stage Stage, from time.Time) time.Time {
	switch stage {
	case StageNew:
		return from.Add(4 * time.Hour)
	case StageOne:
		return from.Add(8 * time.Hour)
	case StageTwo:
		return from.AddDate(0, 0, 1)
	case StageThree:
		return from.AddDate(0, 0, 2)
	case StageFour:
		return from.AddDate(0, 0, 7)
	case StageFive:
		return from.AddDate(0, 0, 14)
	case StageSix:
		return from.AddDate(0, 1, 0)
	case StageSeven:
		return from.AddDate(0, 4, 0)
	case StageEight:
		return from.AddDate(0, 6, 0)
	case StageEvergreen:
		return time.Time{}
	default:
		return from
	}
}

func NextReview(stage Stage, success, sixMonthReviewEnabled bool, from time.Time) (Stage, time.Time) {
	next := Advance(stage, success, sixMonthReviewEnabled)
	return next, DueAt(next, from)
}

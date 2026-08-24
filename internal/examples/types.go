package examples

import (
	"errors"
	"time"
)

type Origin string

const (
	OriginManual    Origin = "manual"
	OriginMined     Origin = "mined"
	OriginGenerated Origin = "generated"
)

var (
	ErrInvalidInput      = errors.New("invalid vocabulary example input")
	ErrAlreadyExists     = errors.New("vocabulary already has an example")
	ErrVocabularyChanged = errors.New("vocabulary changed while generating an example")
)

type Example struct {
	ID                  int64
	VocabularyID        int64
	MiningCaptureID     *int64
	Origin              Origin
	Sentence            string
	Translation         string
	TargetSurface       string
	SourceTitle         string
	SourceURL           string
	SourcePositionMS    *int64
	SentenceAudioID     int64
	SentenceAudioIDs    []int64
	VideoFrameID        int64
	Provider            string
	Model               string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	SourceLink          string
	SourcePositionLabel string
	BeforeTarget        string
	MatchedTarget       string
	AfterTarget         string
	HasTarget           bool
}

type Input struct {
	MiningCaptureID  *int64
	Origin           Origin
	Sentence         string
	Translation      string
	TargetSurface    string
	SourceTitle      string
	SourceURL        string
	SourcePositionMS *int64
	Provider         string
	Model            string
}

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

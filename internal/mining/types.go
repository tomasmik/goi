package mining

import (
	"errors"
	"time"

	"github.com/tomasmik/goi/internal/media"
)

type Status string

type SourceKind string

func (status Status) Label() string {
	switch status {
	case StatusPending:
		return "Needs review"
	case StatusAccepted:
		return "Added"
	case StatusDiscarded:
		return "Discarded"
	default:
		return string(status)
	}
}

const (
	StatusPending   Status = "pending"
	StatusAccepted  Status = "accepted"
	StatusDiscarded Status = "discarded"
)

const (
	SourceManual SourceKind = "manual"
	SourceWeb    SourceKind = "web"
	SourceVideo  SourceKind = "video"
	SourceEbook  SourceKind = "ebook"
	SourceOther  SourceKind = "other"
)

var (
	ErrInvalidInput      = errors.New("invalid mining input")
	ErrNonceConflict     = errors.New("capture nonce was already used for different input")
	ErrCaptureDeleted    = errors.New("capture nonce belongs to a deleted capture")
	ErrRevisionConflict  = errors.New("capture was changed by another request")
	ErrInvalidTransition = errors.New("capture cannot make that transition")
)

type Capture struct {
	ID                     int64
	RawText                string
	Expression             string
	NormalizedExpression   string
	ContextText            string
	SourceKind             SourceKind
	SourceTitle            string
	SourceURL              string
	SourcePositionMS       *int64
	SuggestedEntrySequence *int64
	CaptureNonce           string
	Revision               int64
	Status                 Status
	VocabularyID           *int64
	ExistingVocabularyID   *int64
	SentenceAudioID        int64
	SentenceAudioIDs       []int64
	VideoFrameID           int64
	PronunciationAudioID   int64
	CreatedAt              time.Time
}

type CreateInput struct {
	RawText                string
	Expression             string
	ContextText            string
	SourceKind             SourceKind
	SourceTitle            string
	SourceURL              string
	SourcePositionMS       *int64
	SuggestedEntrySequence *int64
	CaptureNonce           string
}

type UpdateInput struct {
	Expression       string
	ContextText      string
	SourceKind       SourceKind
	SourceTitle      string
	SourceURL        string
	SourcePositionMS *int64
}

type CaptureMediaInput struct {
	SentenceAudio *media.Upload
	VideoFrame    *media.Upload
}

type validationError string

func (err validationError) Error() string {
	return string(err)
}

func (err validationError) Is(target error) bool {
	return target == ErrInvalidInput
}

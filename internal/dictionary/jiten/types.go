package jiten

import (
	"errors"
	"time"
)

const CacheFilename = "jiten.sqlite"

const (
	Global = "global"
	Novel  = "novel"
)

var ErrUnavailable = errors.New("Jiten frequency cache is unavailable")

type Pair struct {
	Written string
	Reading string
}

type Ranks struct {
	Global *int
	Novel  *int
}

type SourceStatus struct {
	Corpus         string
	Available      bool
	Revision       string
	DownloadedAt   time.Time
	SHA256         string
	RowCount       int
	LastCheck      time.Time
	RefreshRunning bool
	Error          string
}

type RefreshResult string

const (
	Updated   RefreshResult = "updated"
	Unchanged RefreshResult = "unchanged"
	Partial   RefreshResult = "partial"
	Failed    RefreshResult = "failed"
)

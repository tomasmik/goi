package jmdict

import "time"

const (
	Version            = "1.10"
	CacheSchemaVersion = 1
	SourceURL          = "https://www.edrdg.org/pub/Nihongo/JMdict_e_NG.gz"
)

type Source struct {
	URL          string
	Created      string
	Version      string
	DownloadedAt time.Time
	SHA256       string
	ETag         string
	LastModified string
}

type Metadata struct {
	Source
	EntryCount int
}

type Entry struct {
	Sequence int64
	Order    int
	Kanji    []Kanji
	Readings []Reading
	Senses   []Sense
}

type Kanji struct {
	Text       string
	Priorities []string
}

type Reading struct {
	Text       string
	NoKanji    bool
	Restricted []string
	Priorities []string
}

type Sense struct {
	Number             int
	RestrictedKanji    []string
	RestrictedReadings []string
	PartsOfSpeech      []string
	Glosses            []Gloss
}

type Gloss struct {
	Text     string
	Language string
	Type     string
}

type Candidate struct {
	EntrySequence int64
	Written       string
	Reading       string
	MatchType     string
	Priority      int
	GlobalRank    *int
	NovelRank     *int
	SourceOrder   int
	Senses        []Sense
}

type MatchState string

const (
	MatchReady     MatchState = "ready"
	MatchAmbiguous MatchState = "ambiguous"
	MatchNone      MatchState = "no_match"
)

type Match struct {
	State         MatchState
	SourceCreated string
	SourceVersion string
	Candidates    []Candidate
}

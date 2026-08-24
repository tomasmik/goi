package coverage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/ikawaha/kagome-dict/ipa"
	"github.com/ikawaha/kagome/v2/tokenizer"
	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/textnorm"
)

const (
	StatusKnown          = "known"
	StatusUnknown        = "unknown"
	StatusLeech          = "leech"
	StatusSuspendedLeech = "suspended_leech"
)

type Block struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type Token struct {
	Surface    string `json:"surface"`
	Expression string `json:"expression"`
	Reading    string `json:"reading,omitempty"`
	StartUTF16 int    `json:"start_utf16"`
	EndUTF16   int    `json:"end_utf16"`
	Status     string `json:"status"`
}

type Summary struct {
	KnownOccurrences int `json:"known_occurrences"`
	TotalOccurrences int `json:"total_occurrences"`
	UnknownUnique    int `json:"unknown_unique"`
	ExcludedNames    int `json:"excluded_names"`
}

type BlockResult struct {
	ID     int     `json:"id"`
	Tokens []Token `json:"tokens"`
}

type Result struct {
	Summary Summary       `json:"summary"`
	Blocks  []BlockResult `json:"blocks"`
}

type Analyzer struct {
	known         knownVocabularySource
	tokenizer     *tokenizer.Tokenizer
	knownMu       sync.Mutex
	knownCache    knownVocabulary
	knownLoadedAt time.Time
}

const knownVocabularyCacheTTL = time.Second

type knownVocabularySource interface {
	KnownExpressionStatuses(context.Context) (map[string]string, error)
}

type knownVocabulary struct {
	expressions map[string]string
	prefixes    map[string]struct{}
}

type lexicalSpan struct {
	morph      tokenizer.Token
	firstMorph int
	lastMorph  int
	start      int
	end        int
	surface    string
	expression string
	excluded   bool
}

type expressionMatch struct {
	lastSpan   int
	surface    string
	expression string
	status     string
}

func NewAnalyzer(known knownVocabularySource) (*Analyzer, error) {
	if known == nil {
		return nil, errors.New("known vocabulary is required")
	}
	morphologicalAnalyzer, err := tokenizer.New(ipa.Dict(), tokenizer.OmitBosEos())
	if err != nil {
		return nil, fmt.Errorf("initialize Japanese tokenizer: %w", err)
	}
	return &Analyzer{known: known, tokenizer: morphologicalAnalyzer}, nil
}

func (a *Analyzer) Analyze(ctx context.Context, blocks []Block) (Result, error) {
	known, err := a.loadKnown(ctx)
	if err != nil {
		return Result{}, err
	}

	result := Result{Blocks: make([]BlockResult, 0, len(blocks))}
	unknownExpressions := make(map[string]struct{})
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("analyze Japanese text: %w", err)
		}
		blockResult := BlockResult{ID: block.ID, Tokens: []Token{}}
		offsets := utf16Offsets(block.Text)
		morphs := a.tokenizer.Tokenize(block.Text)
		spans := buildLexicalSpans(morphs)
		for index := 0; index < len(spans); index++ {
			span := spans[index]
			if span.excluded {
				result.Summary.ExcludedNames++
				continue
			}
			if matched, ok := longestKnownExpression(morphs, spans, index, known); ok {
				result.Summary.KnownOccurrences++
				result.Summary.TotalOccurrences++
				blockResult.Tokens = append(blockResult.Tokens, Token{
					Surface:    matched.surface,
					Expression: matched.expression,
					Reading:    morphReading(morphs[span.firstMorph:spans[matched.lastSpan].lastMorph]),
					StartUTF16: offsetAt(offsets, span.start),
					EndUTF16:   offsetAt(offsets, spans[matched.lastSpan].end),
					Status:     matched.status,
				})
				index = matched.lastSpan
				continue
			}

			status := StatusUnknown
			if knownStatus, ok := statusForMorph(span.morph, span.surface, known); ok {
				status = knownStatus
				result.Summary.KnownOccurrences++
			} else {
				unknownExpressions[comparisonForm(span.expression)] = struct{}{}
			}
			result.Summary.TotalOccurrences++
			blockResult.Tokens = append(blockResult.Tokens, Token{
				Surface:    span.surface,
				Expression: span.expression,
				Reading:    morphReading(morphs[span.firstMorph:span.lastMorph]),
				StartUTF16: offsetAt(offsets, span.start),
				EndUTF16:   offsetAt(offsets, span.end),
				Status:     status,
			})
		}
		result.Blocks = append(result.Blocks, blockResult)
	}
	result.Summary.UnknownUnique = len(unknownExpressions)
	return result, nil
}

func morphReading(morphs []tokenizer.Token) string {
	var reading strings.Builder
	for _, morph := range morphs {
		value, ok := morph.Reading()
		if !ok || value == "" || value == "*" {
			return ""
		}
		reading.WriteString(value)
	}
	return kana.ToHiragana(reading.String())
}

func buildLexicalSpans(morphs []tokenizer.Token) []lexicalSpan {
	spans := make([]lexicalSpan, 0, len(morphs))
	for index := 0; index < len(morphs); index++ {
		morph := morphs[index]
		if !containsJapanese(morph.Surface) || !isLexical(morph) {
			continue
		}

		lastMorph := index + 1
		end := morph.End
		var surface strings.Builder
		surface.WriteString(morph.Surface)
		for hasInflectionTail(morph) && lastMorph < len(morphs) && morphs[lastMorph].Start == end && isAuxiliary(morphs[lastMorph]) {
			surface.WriteString(morphs[lastMorph].Surface)
			end = morphs[lastMorph].End
			lastMorph++
		}
		spans = append(spans, lexicalSpan{
			morph:      morph,
			firstMorph: index,
			lastMorph:  lastMorph,
			start:      morph.Start,
			end:        end,
			surface:    surface.String(),
			expression: baseForm(morph),
			excluded:   isProperName(morph),
		})
		index = lastMorph - 1
	}
	return spans
}

func longestKnownExpression(morphs []tokenizer.Token, spans []lexicalSpan, firstSpan int, known knownVocabulary) (expressionMatch, bool) {
	if firstSpan+1 >= len(spans) {
		return expressionMatch{}, false
	}

	var surface strings.Builder
	surface.WriteString(spans[firstSpan].surface)
	candidates := nextKnownExpressionCandidates([]string{""}, "", spans[firstSpan], known)
	if len(candidates) == 0 {
		return expressionMatch{}, false
	}
	var longest expressionMatch
	found := false
	for lastSpan := firstSpan + 1; lastSpan < len(spans); lastSpan++ {
		current := spans[lastSpan]
		previous := spans[lastSpan-1]
		if current.excluded || !canJoinExpression(morphs, previous.lastMorph, current.firstMorph) {
			break
		}

		var connector strings.Builder
		appendMorphSurfaces(&connector, morphs[previous.lastMorph:current.firstMorph])
		connectorText := connector.String()
		surface.WriteString(connectorText)
		surface.WriteString(current.surface)
		groupedSurface := surface.String()
		candidates = nextKnownExpressionCandidates(candidates, connectorText, current, known)
		if len(candidates) == 0 {
			break
		}
		for _, candidate := range candidates {
			if status, ok := known.status(candidate); ok {
				longest = expressionMatch{
					lastSpan:   lastSpan,
					surface:    groupedSurface,
					expression: candidate,
					status:     status,
				}
				found = true
				break
			}
		}
	}
	return longest, found
}

func nextKnownExpressionCandidates(prefixes []string, connector string, span lexicalSpan, known knownVocabulary) []string {
	forms := []string{span.expression}
	if span.surface != span.expression {
		forms = append(forms, span.surface)
	}

	capacity := len(prefixes) * len(forms)
	candidates := make([]string, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	for _, prefix := range prefixes {
		for _, form := range forms {
			candidate := prefix + connector + form
			if !known.hasPrefix(candidate) {
				continue
			}
			normalized := comparisonForm(candidate)
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func canJoinExpression(morphs []tokenizer.Token, first, last int) bool {
	if first <= 0 || first > last || last >= len(morphs) {
		return false
	}
	expectedStart := morphs[first-1].End
	for _, morph := range morphs[first:last] {
		if morph.Start != expectedStart {
			return false
		}
		partOfSpeech := morph.POS()
		if len(partOfSpeech) == 0 {
			return false
		}
		switch partOfSpeech[0] {
		case "助詞", "助動詞":
		default:
			return false
		}
		expectedStart = morph.End
	}
	return expectedStart == morphs[last].Start
}

func appendMorphSurfaces(builder *strings.Builder, morphs []tokenizer.Token) {
	for _, morph := range morphs {
		builder.WriteString(morph.Surface)
	}
}

func isAuxiliary(morph tokenizer.Token) bool {
	partOfSpeech := morph.POS()
	return len(partOfSpeech) > 0 && partOfSpeech[0] == "助動詞"
}

func hasInflectionTail(morph tokenizer.Token) bool {
	partOfSpeech := morph.POS()
	return len(partOfSpeech) > 0 && (partOfSpeech[0] == "動詞" || partOfSpeech[0] == "形容詞")
}

func (a *Analyzer) loadKnown(ctx context.Context) (knownVocabulary, error) {
	a.knownMu.Lock()
	defer a.knownMu.Unlock()
	if !a.knownLoadedAt.IsZero() && time.Since(a.knownLoadedAt) < knownVocabularyCacheTTL {
		return a.knownCache, nil
	}
	expressions, err := a.known.KnownExpressionStatuses(ctx)
	if err != nil {
		return knownVocabulary{}, fmt.Errorf("load known vocabulary: %w", err)
	}

	known := knownVocabulary{
		expressions: make(map[string]string),
		prefixes:    make(map[string]struct{}),
	}
	for expression, status := range expressions {
		known.add(expression, status)
	}
	a.knownCache = known
	a.knownLoadedAt = time.Now()
	return known, nil
}

func (known knownVocabulary) add(value, status string) {
	normalized := comparisonForm(value)
	if normalized == "" {
		return
	}
	known.expressions[normalized] = status
	runes := []rune(normalized)
	for length := 1; length <= len(runes); length++ {
		known.prefixes[string(runes[:length])] = struct{}{}
	}
}

func (known knownVocabulary) status(value string) (string, bool) {
	status, ok := known.expressions[comparisonForm(value)]
	return status, ok
}

func (known knownVocabulary) hasPrefix(value string) bool {
	_, ok := known.prefixes[comparisonForm(value)]
	return ok
}

func statusForMorph(morph tokenizer.Token, groupedSurface string, known knownVocabulary) (string, bool) {
	candidates := []string{morph.Surface, groupedSurface, baseForm(morph)}
	for _, candidate := range candidates {
		if status, ok := known.status(candidate); ok {
			return status, true
		}
	}
	return "", false
}

func baseForm(morph tokenizer.Token) string {
	if base, ok := morph.BaseForm(); ok && base != "" && base != "*" {
		// IPA treats this common ら抜き potential as a separate verb, while
		// learned vocabulary and JMdict index it under 来る.
		if base == "来れる" {
			return "来る"
		}
		return base
	}
	return morph.Surface
}

func isLexical(morph tokenizer.Token) bool {
	partOfSpeech := morph.POS()
	if len(partOfSpeech) == 0 {
		return false
	}
	switch partOfSpeech[0] {
	case "助詞", "助動詞", "記号", "補助記号", "空白", "フィラー":
		return false
	case "名詞":
		return len(partOfSpeech) < 2 || partOfSpeech[1] != "数"
	default:
		return true
	}
}

func isProperName(morph tokenizer.Token) bool {
	partOfSpeech := morph.POS()
	return len(partOfSpeech) >= 2 && partOfSpeech[0] == "名詞" && partOfSpeech[1] == "固有名詞"
}

func containsJapanese(value string) bool {
	for _, character := range value {
		if unicode.In(character, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

func comparisonForm(value string) string {
	return kana.ToHiragana(textnorm.Normalize(value))
}

func utf16Offsets(value string) []int {
	offsets := make([]int, 1, len([]rune(value))+1)
	units := 0
	for _, character := range value {
		units += utf16.RuneLen(character)
		offsets = append(offsets, units)
	}
	return offsets
}

func offsetAt(offsets []int, runeIndex int) int {
	if runeIndex < 0 {
		return 0
	}
	if runeIndex >= len(offsets) {
		return offsets[len(offsets)-1]
	}
	return offsets[runeIndex]
}

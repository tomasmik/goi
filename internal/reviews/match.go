package reviews

import (
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/tomasmik/goi/internal/kana"
)

type MatchResult string

const (
	Correct   MatchResult = "correct"
	Incorrect MatchResult = "incorrect"
)

func NormalizeAnswer(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(value))), " ")
}

func MatchAnswer(answer string, accepted []string) MatchResult {
	answerVariants := make([]string, 0, 4)
	addMeaningVariant(&answerVariants, answer)
	if len(answerVariants) == 0 {
		return Incorrect
	}
	for _, answerVariant := range answerVariants {
		for _, expected := range accepted {
			for _, expectedVariant := range meaningVariants(expected) {
				if answerVariant == expectedVariant || isMinorEnglishTypo(answerVariant, expectedVariant) {
					return Correct
				}
			}
		}
	}
	return Incorrect
}

func normalizeMeaningAnswer(value string) string {
	return strings.Trim(NormalizeAnswer(value), ".,;:!?")
}

func meaningVariants(value string) []string {
	segments := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ';' || r == '；'
	})
	variants := make([]string, 0, len(segments)*4)
	for _, segment := range segments {
		addMeaningSegmentVariants(&variants, segment)
		for _, synonym := range commaSeparatedSynonyms(segment) {
			addMeaningSegmentVariants(&variants, synonym)
		}
	}
	return variants
}

func addMeaningSegmentVariants(variants *[]string, value string) {
	addMeaningVariant(variants, value)
	for _, separator := range []string{" - ", " – ", " — ", ":", "："} {
		if index := strings.Index(value, separator); index >= 0 {
			addMeaningVariant(variants, value[index+len(separator):])
			return
		}
	}
}

func commaSeparatedSynonyms(value string) []string {
	runes := []rune(value)
	parts := make([]string, 0, 3)
	start := 0
	depth := 0
	for index, character := range runes {
		switch character {
		case '(', '（':
			depth++
		case ')', '）':
			if depth > 0 {
				depth--
			}
		case ',', '，':
			if depth == 0 && !numericSeparator(runes, index) {
				parts = append(parts, strings.TrimSpace(string(runes[start:index])))
				start = index + 1
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(string(runes[start:])))
	for index, part := range parts {
		if part == "" || (index > 0 && startsMeaningClause(part)) {
			return nil
		}
	}
	return parts
}

func numericSeparator(value []rune, index int) bool {
	return index > 0 && index+1 < len(value) &&
		value[index-1] >= '0' && value[index-1] <= '9' &&
		value[index+1] >= '0' && value[index+1] <= '9'
}

func startsMeaningClause(value string) bool {
	value = normalizeMeaningAnswer(value)
	for _, prefix := range []string{
		"and ", "or ", "but ", "as ", "especially ", "usually ", "often ",
		"when ", "while ", "with ", "without ", "for ", "in ", "on ",
		"of ", "which ", "that ",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func addMeaningVariant(variants *[]string, value string) {
	normalized := normalizeMeaningAnswer(value)
	withoutQualifier := normalizeMeaningAnswer(removeParentheticalText(normalized))
	for _, candidate := range []string{normalized, withoutQualifier} {
		for candidate != "" {
			appendUnique(variants, candidate)
			stripped := stripMeaningPrefix(candidate)
			if stripped == candidate {
				break
			}
			candidate = stripped
		}
	}
}

func stripMeaningPrefix(value string) string {
	for _, prefix := range []string{"to be ", "to ", "an ", "a ", "the "} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func appendUnique(values *[]string, value string) {
	if value == "" {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func removeParentheticalText(value string) string {
	runes := []rune(value)
	result := make([]rune, 0, len(runes))
	depth := 0
	for _, r := range runes {
		switch r {
		case '(', '（':
			depth++
		case ')', '）':
			if depth > 0 {
				depth--
			} else {
				result = append(result, r)
			}
		default:
			if depth == 0 {
				result = append(result, r)
			}
		}
	}
	return strings.Join(strings.Fields(string(result)), " ")
}

func isMinorEnglishTypo(answer, expected string) bool {
	answerRunes := []rune(answer)
	expectedRunes := []rune(expected)
	if !isEnglishText(answerRunes) || !isEnglishText(expectedRunes) {
		return false
	}
	answerCore := stripMeaningPrefix(answer)
	expectedCore := stripMeaningPrefix(expected)
	letterCount := englishLetterCount([]rune(expectedCore))
	if letterCount < 5 {
		return false
	}
	maxEdits := 1
	allowSubstitution := false
	if letterCount >= 7 {
		maxEdits = 2
	}
	if letterCount >= 8 {
		allowSubstitution = true
	}
	if differsByMeaningPrefix(answerCore, expectedCore) {
		return false
	}
	return matchesWithMinorEnglishTypos(answerRunes, expectedRunes, maxEdits, allowSubstitution)
}

func differsByMeaningPrefix(answer, expected string) bool {
	for _, prefix := range []string{"un", "in", "im", "ir", "il", "non", "dis", "de", "re", "mis"} {
		if answer == prefix+expected || expected == prefix+answer {
			return true
		}
	}
	return false
}

func matchesWithMinorEnglishTypos(answer, expected []rune, maxEdits int, allowSubstitution bool) bool {
	var match func(answerIndex, expectedIndex, edits, substitutions int) bool
	match = func(answerIndex, expectedIndex, edits, substitutions int) bool {
		for answerIndex < len(answer) && expectedIndex < len(expected) &&
			answer[answerIndex] == expected[expectedIndex] {
			answerIndex++
			expectedIndex++
		}

		if answerIndex == len(answer) || expectedIndex == len(expected) {
			return edits+len(answer)-answerIndex+len(expected)-expectedIndex <= maxEdits
		}
		if edits == maxEdits {
			return false
		}
		if match(answerIndex+1, expectedIndex, edits+1, substitutions) ||
			match(answerIndex, expectedIndex+1, edits+1, substitutions) {
			return true
		}
		if answerIndex+1 < len(answer) && expectedIndex+1 < len(expected) &&
			answer[answerIndex] == expected[expectedIndex+1] &&
			answer[answerIndex+1] == expected[expectedIndex] &&
			match(answerIndex+2, expectedIndex+2, edits+1, substitutions) {
			return true
		}
		return allowSubstitution && substitutions == 0 &&
			match(answerIndex+1, expectedIndex+1, edits+1, substitutions+1)
	}
	return match(0, 0, 0, 0)
}

func englishLetterCount(value []rune) int {
	count := 0
	for _, r := range value {
		if r >= 'a' && r <= 'z' {
			count++
		}
	}
	return count
}

func isEnglishText(value []rune) bool {
	for _, r := range value {
		if r < 'a' || r > 'z' {
			switch r {
			case ' ', '-', '\'':
			default:
				return false
			}
		}
	}
	return true
}

func MatchPronunciation(answer string, accepted []string) (MatchResult, error) {
	normalized, err := kana.Normalize(answer)
	if err != nil {
		return Incorrect, err
	}
	for _, expected := range accepted {
		if normalized == kana.ToHiragana(NormalizeAnswer(expected)) {
			return Correct, nil
		}
	}
	return Incorrect, nil
}

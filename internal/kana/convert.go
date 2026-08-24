package kana

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var syllables = map[string]string{
	"a": "あ", "i": "い", "u": "う", "e": "え", "o": "お",
	"ka": "か", "ki": "き", "ku": "く", "ke": "け", "ko": "こ",
	"ga": "が", "gi": "ぎ", "gu": "ぐ", "ge": "げ", "go": "ご",
	"sa": "さ", "shi": "し", "su": "す", "se": "せ", "so": "そ",
	"si": "し",
	"za": "ざ", "ji": "じ", "zi": "じ", "zu": "ず", "ze": "ぜ", "zo": "ぞ",
	"ta": "た", "chi": "ち", "ti": "ち", "tsu": "つ", "tu": "つ", "te": "て", "to": "と",
	"da": "だ", "di": "ぢ", "du": "づ", "de": "で", "do": "ど",
	"na": "な", "ni": "に", "nu": "ぬ", "ne": "ね", "no": "の",
	"ha": "は", "hi": "ひ", "fu": "ふ", "hu": "ふ", "he": "へ", "ho": "ほ",
	"ba": "ば", "bi": "び", "bu": "ぶ", "be": "べ", "bo": "ぼ",
	"pa": "ぱ", "pi": "ぴ", "pu": "ぷ", "pe": "ぺ", "po": "ぽ",
	"ma": "ま", "mi": "み", "mu": "む", "me": "め", "mo": "も",
	"ya": "や", "yi": "い", "yu": "ゆ", "ye": "いぇ", "yo": "よ",
	"ra": "ら", "ri": "り", "ru": "る", "re": "れ", "ro": "ろ",
	"wa": "わ", "wi": "うぃ", "we": "うぇ", "wo": "を",
	"wha": "うぁ", "whi": "うぃ", "whe": "うぇ", "who": "うぉ",
	"kya": "きゃ", "kyu": "きゅ", "kyo": "きょ",
	"gya": "ぎゃ", "gyu": "ぎゅ", "gyo": "ぎょ",
	"sha": "しゃ", "shu": "しゅ", "she": "しぇ", "sho": "しょ",
	"sya": "しゃ", "syu": "しゅ", "syo": "しょ",
	"ja": "じゃ", "ju": "じゅ", "je": "じぇ", "jo": "じょ",
	"jya": "じゃ", "jyu": "じゅ", "jyo": "じょ",
	"cha": "ちゃ", "chu": "ちゅ", "che": "ちぇ", "cho": "ちょ",
	"tya": "ちゃ", "tyu": "ちゅ", "tyo": "ちょ",
	"nya": "にゃ", "nyu": "にゅ", "nyo": "にょ",
	"hya": "ひゃ", "hyu": "ひゅ", "hyo": "ひょ",
	"bya": "びゃ", "byu": "びゅ", "byo": "びょ",
	"pya": "ぴゃ", "pyu": "ぴゅ", "pyo": "ぴょ",
	"mya": "みゃ", "myu": "みゅ", "myo": "みょ",
	"rya": "りゃ", "ryu": "りゅ", "ryo": "りょ",
	"tsa": "つぁ", "tsi": "つぃ", "tse": "つぇ", "tso": "つぉ",
	"tha": "てゃ", "thi": "てぃ", "thu": "てゅ", "the": "てぇ", "tho": "てょ",
	"dha": "でゃ", "dhi": "でぃ", "dhu": "でゅ", "dhe": "でぇ", "dho": "でょ",
	"twa": "とぁ", "twi": "とぃ", "twu": "とぅ", "twe": "とぇ", "two": "とぉ",
	"dwa": "どぁ", "dwi": "どぃ", "dwu": "どぅ", "dwe": "どぇ", "dwo": "どぉ",
	"kwa": "くぁ", "kwi": "くぃ", "kwe": "くぇ", "kwo": "くぉ",
	"gwa": "ぐぁ", "gwi": "ぐぃ", "gwe": "ぐぇ", "gwo": "ぐぉ",
	"fa": "ふぁ", "fi": "ふぃ", "fe": "ふぇ", "fo": "ふぉ",
	"fya": "ふゃ", "fyu": "ふゅ", "fyo": "ふょ",
	"va": "ゔぁ", "vi": "ゔぃ", "vu": "ゔ", "ve": "ゔぇ", "vo": "ゔぉ",
	"vya": "ゔゃ", "vyu": "ゔゅ", "vyo": "ゔょ",
	"xa": "ぁ", "xi": "ぃ", "xu": "ぅ", "xe": "ぇ", "xo": "ぉ",
	"la": "ぁ", "li": "ぃ", "lu": "ぅ", "le": "ぇ", "lo": "ぉ",
	"xya": "ゃ", "xyu": "ゅ", "xyo": "ょ", "xtu": "っ", "xtsu": "っ", "xwa": "ゎ",
	"lya": "ゃ", "lyu": "ゅ", "lyo": "ょ", "ltu": "っ", "ltsu": "っ", "lwa": "ゎ",
}

func Convert(input string) (string, error) {
	input = strings.Join(strings.Fields(norm.NFKC.String(input)), " ")
	runes := []rune(input)
	var output strings.Builder
	for index := 0; index < len(runes); {
		if runes[index] == '-' {
			output.WriteRune('ー')
			index++
			continue
		}
		if !isASCIIAlphaRune(runes[index]) && runes[index] != '\'' {
			output.WriteRune(runes[index])
			index++
			continue
		}
		end := index
		for end < len(runes) && (isASCIIAlphaRune(runes[end]) || runes[end] == '\'') {
			end++
		}
		run := string(runes[index:end])
		converted, err := convertRun(run)
		if err != nil {
			return "", err
		}
		if isUpperRun(run) {
			converted = ToKatakana(converted)
		}
		output.WriteString(converted)
		index = end
	}
	converted := output.String()
	hasKana := false
	for _, character := range converted {
		if !isReadingCharacter(character) {
			return "", fmt.Errorf("unsupported reading character %q", character)
		}
		hasKana = hasKana || isKanaCharacter(character)
	}
	if !hasKana {
		return "", errors.New("reading must include kana")
	}
	return converted, nil
}

func Normalize(input string) (string, error) {
	converted, err := Convert(strings.TrimSpace(input))
	if err != nil {
		return "", err
	}
	return ToHiragana(converted), nil
}

func ToHiragana(input string) string {
	var output strings.Builder
	for _, character := range input {
		if character >= 'ァ' && character <= 'ヶ' {
			output.WriteRune(character - ('ァ' - 'ぁ'))
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func ToKatakana(input string) string {
	var output strings.Builder
	for _, character := range input {
		if character >= 'ぁ' && character <= 'ゖ' {
			output.WriteRune(character + ('ァ' - 'ぁ'))
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func convertRun(input string) (string, error) {
	input = strings.ToLower(input)
	var output strings.Builder
	for index := 0; index < len(input); {
		if input[index] == '\'' {
			index++
			continue
		}
		if index+1 < len(input) && input[index] == input[index+1] && isConsonant(input[index]) && input[index] != 'n' {
			output.WriteString("っ")
			index++
			continue
		}
		if strings.HasPrefix(input[index:], "tch") {
			output.WriteString("っ")
			index++
			continue
		}
		if input[index] == 'n' && index+2 == len(input) && input[index+1] == 'n' {
			output.WriteString("ん")
			index += 2
			continue
		}
		if input[index] == 'n' && (index+1 == len(input) || input[index+1] == '\'' || (isConsonant(input[index+1]) && input[index+1] != 'y')) {
			output.WriteString("ん")
			index++
			if index < len(input) && input[index] == '\'' {
				index++
			}
			continue
		}
		matched := false
		for length := 4; length >= 1; length-- {
			if index+length > len(input) {
				continue
			}
			if syllable, ok := syllables[input[index:index+length]]; ok {
				output.WriteString(syllable)
				index += length
				matched = true
				break
			}
		}
		if !matched {
			return "", fmt.Errorf("unsupported romaji near %q", input[index:])
		}
	}
	return output.String(), nil
}

func isASCIIAlphaRune(value rune) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isConsonant(value byte) bool {
	return isASCIIAlphaRune(rune(value)) && !strings.ContainsRune("aeiou", unicode.ToLower(rune(value)))
}

func isUpperRun(value string) bool {
	hasLetter := false
	for _, character := range value {
		if unicode.IsLetter(character) {
			hasLetter = true
			if unicode.IsLower(character) {
				return false
			}
		}
	}
	return hasLetter
}

func isReadingCharacter(character rune) bool {
	if character == ' ' || character == 'ー' || character == '・' {
		return true
	}
	return isKanaCharacter(character)
}

func isKanaCharacter(character rune) bool {
	return character >= 'ぁ' && character <= 'ゖ' ||
		character >= 'ゝ' && character <= 'ゟ' ||
		character >= 'ァ' && character <= 'ヺ' ||
		character >= 'ヽ' && character <= 'ヿ' ||
		character >= 'ㇰ' && character <= 'ㇿ'
}

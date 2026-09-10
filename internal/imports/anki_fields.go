package imports

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"

	"github.com/tomasmik/goi/internal/textnorm"
)

type Mapping struct {
	ExpressionField    int  `json:"expression_field"`
	PronunciationField int  `json:"pronunciation_field"`
	MeaningField       int  `json:"meaning_field"`
	NotesField         int  `json:"notes_field"`
	ExampleField       int  `json:"example_field"`
	TranslationField   int  `json:"translation_field"`
	AudioField         int  `json:"audio_field"`
	PictureField       int  `json:"picture_field"`
	KnownElsewhere     bool `json:"known_elsewhere"`
	AllowDuplicate     bool `json:"allow_duplicate"`
	ExtendedFields     bool `json:"extended_fields"`
}

func (mapping Mapping) validate(fieldCount int) error {
	type mappedField struct {
		name  string
		index int
	}
	required := []mappedField{
		{name: "expression", index: mapping.ExpressionField},
	}
	if !mapping.KnownElsewhere {
		required = append(required,
			mappedField{name: "pronunciation", index: mapping.PronunciationField},
			mappedField{name: "meaning", index: mapping.MeaningField},
		)
	}
	for _, field := range required {
		if field.index < 0 || field.index >= fieldCount {
			return invalidMapping(fmt.Sprintf("select a valid %s field", field.name))
		}
	}
	if !mapping.KnownElsewhere && (mapping.ExpressionField == mapping.PronunciationField ||
		mapping.ExpressionField == mapping.MeaningField ||
		mapping.PronunciationField == mapping.MeaningField) {
		return invalidMapping("expression, pronunciation, and meaning must use different fields")
	}
	optional := []mappedField{
		{name: "audio", index: mapping.AudioField},
		{name: "picture", index: mapping.PictureField},
	}
	if mapping.ExtendedFields {
		optional = append(optional,
			mappedField{name: "notes", index: mapping.NotesField},
			mappedField{name: "example", index: mapping.ExampleField},
			mappedField{name: "translation", index: mapping.TranslationField},
		)
	}
	for _, field := range optional {
		if field.index < -1 || field.index >= fieldCount {
			return invalidMapping(fmt.Sprintf("select a valid %s field", field.name))
		}
	}
	return nil
}

func field(note Note, index int) string {
	if index < 0 || index >= len(note.Fields) {
		return ""
	}
	return note.Fields[index]
}

func cleanImportedMeanings(expression, value string) []string {
	value = cleanImportedText(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ';' || r == '；'
	})
	meanings := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		for _, prefix := range []string{"• ", "· ", "- ", "– ", "— "} {
			part = strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
		part = stripExpressionPrefix(expression, part)
		if part == "" || textnorm.Normalize(part) == textnorm.Normalize(expression) {
			continue
		}
		duplicate := false
		for _, meaning := range meanings {
			if strings.EqualFold(meaning, part) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			meanings = append(meanings, part)
		}
	}
	return meanings
}

func cleanImportedText(value string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(value))
	var text strings.Builder
	skipDepth := 0
	lastTextWasBreak := false
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			return strings.TrimSpace(text.String())
		case html.TextToken:
			if skipDepth == 0 {
				value := tokenizer.Text()
				text.Write(value)
				if len(value) > 0 {
					lastTextWasBreak = value[len(value)-1] == '\n'
				}
			}
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if skipDepth > 0 {
				if !voidAnkiElement(tag) {
					skipDepth++
				}
				continue
			}
			if ignoredAnkiElement(tag) {
				skipDepth = 1
				continue
			}
			if tag == "br" || blockAnkiElement(tag) {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		case html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if skipDepth == 0 && string(name) == "br" {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if blockAnkiElement(string(name)) {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		}
	}
}

func appendTextBreak(text *strings.Builder, lastTextWasBreak *bool) {
	if text.Len() > 0 && !*lastTextWasBreak {
		text.WriteByte('\n')
		*lastTextWasBreak = true
	}
}

func ignoredAnkiElement(tag string) bool {
	switch tag {
	case "rt", "rp", "script", "style":
		return true
	default:
		return false
	}
}

func voidAnkiElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func blockAnkiElement(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "div", "footer", "header", "h1", "h2", "h3", "h4", "h5", "h6", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tbody", "td", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func stripExpressionPrefix(expression, value string) string {
	if expression == "" {
		return value
	}
	for _, separator := range []string{" - ", " – ", " — ", ":", "："} {
		index := strings.Index(value, separator)
		if index < 0 || textnorm.Normalize(value[:index]) != textnorm.Normalize(expression) {
			continue
		}
		return strings.TrimSpace(value[index+len(separator):])
	}
	return value
}

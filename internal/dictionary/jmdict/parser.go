package jmdict

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxDTDSize = 4 << 20

var (
	doubleQuotedEntity = regexp.MustCompile(`(?s)<!ENTITY\s+([A-Za-z0-9_.:-]+)\s+"([^"]*)"\s*>`)
	singleQuotedEntity = regexp.MustCompile(`(?s)<!ENTITY\s+([A-Za-z0-9_.:-]+)\s+'([^']*)'\s*>`)
)

type xmlEntry struct {
	Sequence string       `xml:"ent_seq"`
	Kanji    []xmlKanji   `xml:"k_ele"`
	Readings []xmlReading `xml:"r_ele"`
	Senses   []xmlSense   `xml:"sense"`
}

type xmlKanji struct {
	Text       string   `xml:"keb"`
	Priorities []string `xml:"ke_pri"`
}

type xmlReading struct {
	Text       string    `xml:"reb"`
	NoKanji    *struct{} `xml:"re_nokanji"`
	Restricted []string  `xml:"re_restr"`
	Priorities []string  `xml:"re_pri"`
}

type xmlSense struct {
	RestrictedKanji    []string   `xml:"stagk"`
	RestrictedReadings []string   `xml:"stagr"`
	PartsOfSpeech      []string   `xml:"pos"`
	Glosses            []xmlGloss `xml:"gloss"`
}

type xmlGloss struct {
	Text     string `xml:",chardata"`
	Language string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Type     string `xml:"g_type,attr"`
}

func Parse(reader io.ReadSeeker, visit func(Entry) error) (Metadata, error) {
	if visit == nil {
		return Metadata{}, errors.New("JMdict entry visitor is required")
	}
	entities, err := readEntities(reader)
	if err != nil {
		return Metadata{}, err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("rewind JMdict XML: %w", err)
	}

	decoder := xml.NewDecoder(bufio.NewReader(reader))
	decoder.Strict = true
	decoder.Entity = entities

	root, err := findRoot(decoder)
	if err != nil {
		return Metadata{}, err
	}
	created := attribute(root, "created")
	version := attribute(root, "version")
	if version != Version {
		return Metadata{}, fmt.Errorf("unsupported JMdict version %q", version)
	}
	if _, err := time.Parse("2006-01-02", created); err != nil {
		return Metadata{}, fmt.Errorf("invalid JMdict creation date %q", created)
	}

	metadata := Metadata{Source: Source{Created: created, Version: version}}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return Metadata{}, errors.New("JMdict root was not closed")
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("parse JMdict XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local != "entry" {
				return Metadata{}, fmt.Errorf("unexpected JMdict element %q", value.Name.Local)
			}
			var source xmlEntry
			if err := decoder.DecodeElement(&source, &value); err != nil {
				return Metadata{}, fmt.Errorf("parse JMdict entry %d: %w", metadata.EntryCount+1, err)
			}
			entry, err := convertEntry(source, metadata.EntryCount)
			if err != nil {
				return Metadata{}, fmt.Errorf("validate JMdict entry %d: %w", metadata.EntryCount+1, err)
			}
			if err := visit(entry); err != nil {
				return Metadata{}, err
			}
			metadata.EntryCount++
		case xml.EndElement:
			if value.Name.Local == root.Name.Local {
				if metadata.EntryCount == 0 {
					return Metadata{}, errors.New("JMdict contains no entries")
				}
				if err := finishDocument(decoder); err != nil {
					return Metadata{}, err
				}
				return metadata, nil
			}
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return Metadata{}, errors.New("unexpected text below JMdict root")
			}
		}
	}
}

func finishDocument(decoder *xml.Decoder) error {
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finish JMdict XML: %w", err)
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return errors.New("unexpected content after JMdict root")
			}
		case xml.Comment, xml.ProcInst:
		default:
			return errors.New("unexpected content after JMdict root")
		}
	}
}

func readEntities(reader io.ReadSeeker) (map[string]string, error) {
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind JMdict XML: %w", err)
	}
	buffer := make([]byte, maxDTDSize+1)
	count, err := io.ReadFull(reader, buffer)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read JMdict DTD: %w", err)
	}
	buffer = buffer[:count]
	end := strings.Index(string(buffer), "]>")
	if end < 0 {
		if count > maxDTDSize {
			return nil, errors.New("JMdict DTD exceeds size limit")
		}
		return nil, errors.New("JMdict internal DTD is missing or incomplete")
	}
	if end+2 > maxDTDSize {
		return nil, errors.New("JMdict DTD exceeds size limit")
	}
	dtd := string(buffer[:end+2])
	entities := make(map[string]string)
	for _, expression := range []*regexp.Regexp{doubleQuotedEntity, singleQuotedEntity} {
		for _, match := range expression.FindAllStringSubmatch(dtd, -1) {
			entities[match[1]] = html.UnescapeString(match[2])
		}
	}
	if len(entities) == 0 {
		return nil, errors.New("JMdict DTD contains no entities")
	}
	return entities, nil
}

func findRoot(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("read JMdict root: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			start := value
			if start.Name.Local != "JMdict" {
				return xml.StartElement{}, fmt.Errorf("unexpected root element %q", start.Name.Local)
			}
			return start, nil
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return xml.StartElement{}, errors.New("unexpected text before JMdict root")
			}
		}
	}
}

func convertEntry(source xmlEntry, order int) (Entry, error) {
	sequence, err := strconv.ParseInt(strings.TrimSpace(source.Sequence), 10, 64)
	if err != nil || sequence <= 0 {
		return Entry{}, fmt.Errorf("invalid sequence %q", source.Sequence)
	}
	if len(source.Readings) == 0 || len(source.Senses) == 0 {
		return Entry{}, errors.New("entry must contain a reading and a sense")
	}
	entry := Entry{Sequence: sequence, Order: order}
	for _, value := range source.Kanji {
		if strings.TrimSpace(value.Text) == "" {
			return Entry{}, errors.New("entry contains an empty written form")
		}
		entry.Kanji = append(entry.Kanji, Kanji{Text: strings.TrimSpace(value.Text), Priorities: trimValues(value.Priorities)})
	}
	for _, value := range source.Readings {
		if strings.TrimSpace(value.Text) == "" {
			return Entry{}, errors.New("entry contains an empty reading")
		}
		entry.Readings = append(entry.Readings, Reading{Text: strings.TrimSpace(value.Text), NoKanji: value.NoKanji != nil, Restricted: trimValues(value.Restricted), Priorities: trimValues(value.Priorities)})
	}
	var inheritedPartsOfSpeech []string
	for index, value := range source.Senses {
		partsOfSpeech := trimValues(value.PartsOfSpeech)
		if len(partsOfSpeech) == 0 {
			partsOfSpeech = append([]string(nil), inheritedPartsOfSpeech...)
		} else {
			inheritedPartsOfSpeech = append([]string(nil), partsOfSpeech...)
		}
		sense := Sense{Number: index + 1, RestrictedKanji: trimValues(value.RestrictedKanji), RestrictedReadings: trimValues(value.RestrictedReadings), PartsOfSpeech: partsOfSpeech}
		for _, gloss := range value.Glosses {
			language := gloss.Language
			if language == "" {
				language = "eng"
			}
			if language != "eng" || strings.TrimSpace(gloss.Text) == "" {
				continue
			}
			sense.Glosses = append(sense.Glosses, Gloss{Text: strings.TrimSpace(gloss.Text), Language: language, Type: strings.TrimSpace(gloss.Type)})
		}
		if len(sense.Glosses) == 0 {
			return Entry{}, fmt.Errorf("sense %d contains no English gloss", sense.Number)
		}
		entry.Senses = append(entry.Senses, sense)
	}
	return entry, nil
}

func attribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func trimValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

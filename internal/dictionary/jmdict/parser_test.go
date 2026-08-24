package jmdict

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDictionary = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE JMdict [
<!ELEMENT JMdict (entry*)>
<!ENTITY v1 "Ichidan verb">
<!ENTITY adj-na "adjectival noun">
]>
<JMdict created="2026-07-25" version="1.10">
  <entry>
    <ent_seq>1001</ent_seq>
    <k_ele><keb>食べる</keb><ke_pri>ichi1</ke_pri></k_ele>
    <k_ele><keb>喰べる</keb></k_ele>
    <r_ele><reb>たべる</reb><re_pri>ichi1</re_pri></r_ele>
    <sense><stagk>食べる</stagk><pos>&v1;</pos><gloss>to eat</gloss></sense>
    <sense><stagk>喰べる</stagk><gloss g_type="fig">to consume</gloss></sense>
  </entry>
  <entry>
    <ent_seq>1002</ent_seq>
    <r_ele><reb>きれい</reb><re_nokanji/></r_ele>
    <sense><pos>&adj-na;</pos><gloss>pretty</gloss><gloss xml:lang="dut">mooi</gloss></sense>
  </entry>
  <entry>
    <ent_seq>1003</ent_seq>
    <k_ele><keb>開く</keb></k_ele>
    <k_ele><keb>空く</keb></k_ele>
    <r_ele><reb>ひらく</reb><re_restr>開く</re_restr></r_ele>
    <r_ele><reb>あく</reb><re_restr>開く</re_restr><re_restr>空く</re_restr></r_ele>
    <sense><stagr>ひらく</stagr><gloss>to open</gloss></sense>
    <sense><stagr>あく</stagr><gloss>to become empty</gloss></sense>
  </entry>
</JMdict>`

func TestParseResolvesEntitiesAndInheritsPartsOfSpeech(t *testing.T) {
	var entries []Entry
	metadata, err := Parse(strings.NewReader(testDictionary), func(entry Entry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Created != "2026-07-25" || metadata.Version != Version || metadata.EntryCount != 3 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if got := entries[0].Senses[0].PartsOfSpeech; len(got) != 1 || got[0] != "Ichidan verb" {
		t.Fatalf("first sense parts of speech = %#v", got)
	}
	if got := entries[0].Senses[1].PartsOfSpeech; len(got) != 1 || got[0] != "Ichidan verb" {
		t.Fatalf("inherited parts of speech = %#v", got)
	}
	if got := entries[1].Senses[0].Glosses; len(got) != 1 || got[0].Text != "pretty" || got[0].Language != "eng" {
		t.Fatalf("English glosses = %#v", got)
	}
}

func TestParseRejectsUnknownVersionAndMalformedEntries(t *testing.T) {
	tests := []struct {
		name string
		xml  string
	}{
		{name: "version", xml: strings.Replace(testDictionary, `version="1.10"`, `version="2.0"`, 1)},
		{name: "truncated", xml: strings.TrimSuffix(testDictionary, "</JMdict>")},
		{name: "bad restriction", xml: strings.Replace(testDictionary, "<re_restr>開く</re_restr>", "<re_restr>", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(test.xml), func(Entry) error { return nil }); err == nil {
				t.Fatal("Parse succeeded")
			}
		})
	}
}

func TestReadEntitiesRejectsDTDOverLimit(t *testing.T) {
	prefix := `<!DOCTYPE JMdict [<!ENTITY test "value">`
	suffix := "]>"
	padding := maxDTDSize + 1 - len(prefix) - len(suffix)
	document := prefix + strings.Repeat(" ", padding) + suffix

	if _, err := readEntities(strings.NewReader(document)); err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("readEntities error = %v, want DTD size limit", err)
	}
}

func TestCacheLookupHonorsReadingAndSenseRestrictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	source := Source{URL: SourceURL, DownloadedAt: time.Unix(100, 0).UTC(), SHA256: strings.Repeat("a", 64)}
	metadata, err := Build(context.Background(), strings.NewReader(testDictionary), path, source)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.EntryCount != 3 || metadata.URL != SourceURL {
		t.Fatalf("metadata = %#v", metadata)
	}
	cache, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	match, err := cache.Lookup(context.Background(), "食べる", "")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchReady || len(match.Candidates) != 1 {
		t.Fatalf("食べる match = %#v", match)
	}
	if senses := match.Candidates[0].Senses; len(senses) != 1 || senses[0].Glosses[0].Text != "to eat" {
		t.Fatalf("食べる senses = %#v", senses)
	}

	match, err = cache.Lookup(context.Background(), "たべる", "")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchAmbiguous || len(match.Candidates) != 2 {
		t.Fatalf("たべる match = %#v", match)
	}

	match, err = cache.Lookup(context.Background(), "開く", "ひらく")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchReady || match.Candidates[0].Senses[0].Glosses[0].Text != "to open" {
		t.Fatalf("開く/ひらく match = %#v", match)
	}
	match, err = cache.Lookup(context.Background(), "空く", "ひらく")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchNone {
		t.Fatalf("空く/ひらく match = %#v", match)
	}
}

func TestOpenRejectsIncompleteOrInconsistentCache(t *testing.T) {
	tests := []struct {
		name   string
		damage string
	}{
		{name: "missing lookup index", damage: "DROP INDEX jmdict_kanji_lookup"},
		{name: "metadata count mismatch", damage: "UPDATE jmdict_meta SET entry_count = entry_count + 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jmdict.sqlite")
			if _, err := Build(context.Background(), strings.NewReader(testDictionary), path, Source{DownloadedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			db, err := openSQLite(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.damage); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			cache, err := Open(path)
			if err == nil {
				cache.Close()
				t.Fatal("Open accepted an invalid cache")
			}
		})
	}
}

func TestCacheLookupNormalizesKatakana(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	if _, err := Build(context.Background(), strings.NewReader(testDictionary), path, Source{DownloadedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cache, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	match, err := cache.Lookup(context.Background(), "キレイ", "")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchReady || match.Candidates[0].Reading != "きれい" {
		t.Fatalf("キレイ match = %#v", match)
	}
}

func TestCacheLookupPreservesExactKanaWrittenMatch(t *testing.T) {
	dictionary := strings.Replace(
		testDictionary,
		`<entry>
    <ent_seq>1002</ent_seq>`,
		`<entry>
    <ent_seq>1002</ent_seq>
    <k_ele><keb>キレイ</keb><ke_pri>ichi1</ke_pri></k_ele>`,
		1,
	)
	dictionary = strings.Replace(dictionary, `<r_ele><reb>きれい</reb><re_nokanji/></r_ele>`, `<r_ele><reb>きれい</reb></r_ele>`, 1)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	if _, err := Build(context.Background(), strings.NewReader(dictionary), path, Source{DownloadedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cache, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	match, err := cache.Lookup(context.Background(), "キレイ", "")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchReady || len(match.Candidates) != 1 {
		t.Fatalf("match = %#v", match)
	}
	candidate := match.Candidates[0]
	if candidate.MatchType != "written" || candidate.Written != "キレイ" || candidate.Reading != "きれい" {
		t.Fatalf("candidate = %#v, want the exact written match", candidate)
	}
}

func TestPriorityRankRecognizesOnlyJMdictPriorityTags(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   int
	}{
		{name: "empty", want: unrankedPriority},
		{name: "unknown", values: []string{"popular"}, want: unrankedPriority},
		{name: "short frequency", values: []string{"nf1"}, want: unrankedPriority},
		{name: "zero frequency", values: []string{"nf00"}, want: unrankedPriority},
		{name: "high frequency", values: []string{"nf49"}, want: unrankedPriority},
		{name: "first tier", values: []string{"ichi1"}, want: 0},
		{name: "second tier", values: []string{"news2"}, want: 10},
		{name: "first frequency", values: []string{"nf01"}, want: 21},
		{name: "last frequency", values: []string{"nf48"}, want: 68},
		{name: "best recognized value", values: []string{"unknown", "nf27", "spec2"}, want: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := priorityRank(test.values); got != test.want {
				t.Fatalf("priorityRank(%q) = %d, want %d", test.values, got, test.want)
			}
		})
	}
}

func TestCacheLookupRanksReadingPriorityIndependently(t *testing.T) {
	dictionary := strings.Replace(
		testDictionary,
		`<r_ele><reb>たべる</reb><re_pri>ichi1</re_pri></r_ele>`,
		`<r_ele><reb>たべる</reb><re_pri>ichi1</re_pri></r_ele><r_ele><reb>くう</reb></r_ele>`,
		1,
	)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	if _, err := Build(context.Background(), strings.NewReader(dictionary), path, Source{DownloadedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cache, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	match, err := cache.Lookup(context.Background(), "食べる", "")
	if err != nil {
		t.Fatal(err)
	}
	if match.State != MatchAmbiguous || len(match.Candidates) != 2 {
		t.Fatalf("match = %#v", match)
	}
	if match.Candidates[0].Reading != "たべる" {
		t.Fatalf("first reading = %q, want the prioritized reading", match.Candidates[0].Reading)
	}
	if match.Candidates[0].Priority >= match.Candidates[1].Priority {
		t.Fatalf("priorities = %d, %d", match.Candidates[0].Priority, match.Candidates[1].Priority)
	}
}

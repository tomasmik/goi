package jiten

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCache(t *testing.T) *cache {
	t.Helper()
	c, err := openCache(filepath.Join(t.TempDir(), CacheFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.db.Close() })
	return c
}

func importFixture(t *testing.T, c *cache, corpus, data string) SourceStatus {
	t.Helper()
	source, err := c.importCSV(context.Background(), strings.NewReader(data), SourceStatus{
		Corpus: corpus, Revision: "fixture-1", SHA256: "fixture-sha", DownloadedAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func requireRank(t *testing.T, got *int, want int) {
	t.Helper()
	if want == 0 && got == nil {
		return
	}
	if got == nil || *got != want {
		t.Fatalf("rank = %v, want %d (zero means absent)", got, want)
	}
}

func TestImportMatchesFormsAndDeduplicates(t *testing.T) {
	c := testCache(t)
	source := importFixture(t, c, Global, "\ufeffWord,Form,Rank\n猫,ねこ,1536\n猫,ねこ,211769\n猫,ネコ,1800\n今日,きょう,94\nかわいい,かわいい,550\n可愛い,かわいい,1968\nネコ,ネコ,9145\n\"a,b\",えー,123456\n")
	if source.RowCount != 6 {
		t.Fatalf("stored keys = %d", source.RowCount)
	}
	importFixture(t, c, Novel, "Word,Form,Rank\n猫,ねこ,9999\n猫,ねこ,1468\n")
	pairs := []Pair{
		{" 猫 ", "ﾈｺ"}, {"今日", "こんにち"}, {"", "かわいい"}, {"可愛い", "かわいい"},
		{"ねこ", "ねこ"}, {"ネコ", "ねこ"}, {"a,b", "えー"},
	}
	ranks, err := c.lookup(context.Background(), pairs)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1536, 0, 550, 1968, 0, 9145, 123456} {
		requireRank(t, ranks[i].Global, want)
		if i != 0 {
			requireRank(t, ranks[i].Novel, 0)
		}
	}
	requireRank(t, ranks[0].Novel, 1468)
}

func TestInvalidImportPreservesPreviousCorpus(t *testing.T) {
	c := testCache(t)
	importFixture(t, c, Global, "Word,Form,Rank\n猫,ねこ,1536\n")
	for _, bad := range []string{
		"Word,Form,Rank\n", "word,Form,Rank\n猫,ねこ,1\n",
		"Word,Form,Rank\n猫,ねこ,2\n犬,いぬ,0\n", "Word,Form,Rank\n猫,ねこ,-1\n",
		"Word,Form,Rank\n猫,ねこ,1.5\n", "Word,Form,Rank\n猫,ねこ,2147483648\n",
		"Word,Form,Rank\n猫,ねこ,1,extra\n", "Word,Form,Rank\n猫, ,1\n",
		"Word,Form,Rank\n\xff,ねこ,1\n",
	} {
		if _, err := c.importCSV(context.Background(), strings.NewReader(bad), SourceStatus{Corpus: Global, Revision: "bad"}); err == nil {
			t.Fatalf("accepted invalid CSV %q", bad)
		}
	}
	ranks, err := c.lookup(context.Background(), []Pair{{"猫", "ねこ"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRank(t, ranks[0].Global, 1536)
	sources, err := c.sources(context.Background())
	if err != nil || len(sources) != 1 || sources[0].Revision != "fixture-1" {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
}

func TestImportDoesNotBlockReadersAndCancellationRollsBack(t *testing.T) {
	c := testCache(t)
	importFixture(t, c, Global, "Word,Form,Rank\n猫,ねこ,1536\n")
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.importCSV(ctx, reader, SourceStatus{Corpus: Global, Revision: "new"})
		done <- err
	}()
	if _, err := io.WriteString(writer, "Word,Form,Rank\n猫,ねこ,7\n"); err != nil {
		t.Fatal(err)
	}
	// A second write cannot be consumed until the first row has been inserted in the transaction.
	if _, err := io.WriteString(writer, "犬,いぬ,8\n"); err != nil {
		t.Fatal(err)
	}
	readContext, stopRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopRead()
	ranks, err := c.lookup(readContext, []Pair{{"猫", "ねこ"}, {"犬", "いぬ"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRank(t, ranks[0].Global, 1536)
	requireRank(t, ranks[1].Global, 0)
	cancel()
	writer.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("import error = %v", err)
	}
	ranks, err = c.lookup(context.Background(), []Pair{{"猫", "ねこ"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRank(t, ranks[0].Global, 1536)
}

func TestOpenPreservesUnrecognizedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), CacheFilename)
	data := []byte("not a frequency cache")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCache(path); err == nil {
		t.Fatal("accepted unrecognized file")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("file changed: %q, %v", got, err)
	}
}

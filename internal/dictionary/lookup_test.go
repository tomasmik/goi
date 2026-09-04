package dictionary

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tomasmik/goi/internal/dictionary/jiten"
	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type frequencyTransport struct{ rank string }

func (f *frequencyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body := "Word,Form,Rank\n開く,あく," + f.rank + "\n空く,あく,7\n"
	if strings.HasSuffix(request.URL.Path, "/index") {
		title := "Jiten"
		if request.URL.Query().Get("mediaType") == "Novel" {
			title = "Jiten (Novel)"
		}
		body = `{"title":"` + title + `","revision":"fixture","frequencyMode":"rank-based"}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestLookupAddsRanksWithoutChangingCandidatePositions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE JMdict [<!ELEMENT JMdict (entry*)><!ENTITY v1 "Ichidan verb">]>
<JMdict created="2026-09-02" version="1.10">
<entry><ent_seq>100</ent_seq><k_ele><keb>開く</keb><ke_pri>ichi1</ke_pri></k_ele><r_ele><reb>あく</reb></r_ele><sense><gloss>to open</gloss></sense></entry>
<entry><ent_seq>101</ent_seq><k_ele><keb>空く</keb></k_ele><r_ele><reb>あく</reb></r_ele><sense><gloss>to become empty</gloss></sense></entry>
</JMdict>`
	if _, err := jmdict.Build(ctx, strings.NewReader(xml), path, jmdict.Source{}); err != nil {
		t.Fatal(err)
	}
	manager, err := jmdict.NewManager(jmdict.ManagerConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	transport := &frequencyTransport{rank: "39"}
	frequency, err := jiten.NewManager(jiten.ManagerConfig{Path: filepath.Join(t.TempDir(), jiten.CacheFilename), Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	defer frequency.Close()
	lookup := &LookupService{Dictionary: manager, Frequency: frequency}
	before, err := lookup.Lookup(ctx, "あく", "")
	if err != nil || len(before.Candidates) != 2 {
		t.Fatalf("initial match = %#v, %v", before, err)
	}
	for _, rank := range []string{"39", "99999"} {
		transport.rank = rank
		if _, err := frequency.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := lookup.Lookup(ctx, "あく", "")
		if err != nil {
			t.Fatal(err)
		}
		for index := range after.Candidates {
			candidate := &after.Candidates[index]
			if candidate.GlobalRank == nil || candidate.NovelRank == nil {
				t.Fatal("missing frequency ranks")
			}
			candidate.GlobalRank, candidate.NovelRank = nil, nil
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("frequency changed dictionary selection: %#v", after)
		}
	}
	frequency.Close()
	after, err := lookup.Lookup(ctx, "あく", "")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("unavailable frequency broke definitions: %#v, %v", after, err)
	}
}

package pronunciation

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestLibraryFallsBackToLinguaLibre(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "raw.githubusercontent.com" {
			return testResponse(http.StatusNotFound, "text/plain", nil), nil
		}
		page := fmt.Sprintf(`{
			"pageid": 42,
			"title": "File:LL-Q5287 (jpn)-Speaker-日本 (にほん).wav",
			"imageinfo": [{
				"url": %q,
				"descriptionurl": "https://commons.wikimedia.org/wiki/File:recording.wav",
				"mime": "audio/wav",
				"extmetadata": {
					"Categories": {"value": "CC-Zero|Lingua Libre pronunciation-jpn"},
					"License": {"value": "cc0"}
				}
			}]
		}`, "https://upload.test/audio.wav")
		return testResponse(http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"query":{"pages":[%s]}}`, page))), nil
	})
	client := &http.Client{Transport: transport}
	library := &Library{
		tofugu: NewTofugu(client),
		commons: &Commons{
			client:     client,
			apiURL:     "https://commons.test/w/api.php",
			uploadHost: "upload.test",
		},
	}

	recordings, err := library.Search(context.Background(), "日本", "にほん")
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 || recordings[0].SourceName != "Lingua Libre" {
		t.Fatalf("recordings = %#v", recordings)
	}
}

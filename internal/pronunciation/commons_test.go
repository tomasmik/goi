package pronunciation

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCommonsFindsAndDownloadsExactCC0JapaneseRecording(t *testing.T) {
	audio := silentWAV()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("User-Agent") != userAgent {
			return testResponse(http.StatusForbidden, "text/plain", []byte("missing user agent")), nil
		}
		if request.URL.Host == "upload.test" {
			return testResponse(http.StatusOK, "audio/wav", audio), nil
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
	client := &Commons{
		client:     &http.Client{Transport: transport},
		apiURL:     "https://commons.test/w/api.php",
		uploadHost: "upload.test",
	}
	recordings, err := client.Search(context.Background(), "日本", "にほん")
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 || recordings[0].ID != 42 {
		t.Fatalf("recordings = %#v", recordings)
	}
	if recordings[0].Label != "にほん" {
		t.Fatalf("recording label = %q", recordings[0].Label)
	}
	upload, err := client.Download(context.Background(), recordings[0].ID, "日本", "にほん")
	if err != nil {
		t.Fatal(err)
	}
	if upload.MimeType != "audio/x-wav" && upload.MimeType != "audio/wav" {
		t.Fatalf("MIME type = %q", upload.MimeType)
	}
	if string(upload.Content) != string(audio) {
		t.Fatal("downloaded audio did not match")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testResponse(status int, contentType string, content []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(string(content))),
	}
}

func TestCommonsRejectsWrongLanguageLicenseAndNearMatches(t *testing.T) {
	client := NewCommons(nil)
	tests := []commonsPage{
		commonsTestPage("File:LL-Q5287 (fra)-Speaker-日本.wav", "Lingua Libre pronunciation-jpn", "cc0"),
		commonsTestPage("File:LL-Q5287 (jpn)-Speaker-日本語.wav", "Lingua Libre pronunciation-jpn", "cc0"),
		commonsTestPage("File:LL-Q5287 (jpn)-Speaker-日本.wav", "Lingua Libre pronunciation-fra", "cc0"),
		commonsTestPage("File:LL-Q5287 (jpn)-Speaker-日本.wav", "Lingua Libre pronunciation-jpn", "cc-by-sa-4.0"),
	}
	for _, page := range tests {
		if recording, ok := client.recording(page, []string{"日本", "にほん"}); ok {
			t.Fatalf("accepted unsafe recording %#v", recording)
		}
	}
}

func commonsTestPage(title, category, license string) commonsPage {
	return commonsPage{
		PageID: 1,
		Title:  title,
		ImageInfo: []commonsImageInfo{{
			URL:  "https://upload.wikimedia.org/audio.wav",
			MIME: "audio/wav",
			Metadata: commonsMetadata{
				Categories: metadataValue{Value: category},
				License:    metadataValue{Value: license},
			},
		}},
	}
}

func silentWAV() []byte {
	content := make([]byte, 46)
	copy(content[0:4], "RIFF")
	binary.LittleEndian.PutUint32(content[4:8], uint32(len(content)-8))
	copy(content[8:12], "WAVE")
	copy(content[12:16], "fmt ")
	binary.LittleEndian.PutUint32(content[16:20], 16)
	binary.LittleEndian.PutUint16(content[20:22], 1)
	binary.LittleEndian.PutUint16(content[22:24], 1)
	binary.LittleEndian.PutUint32(content[24:28], 8_000)
	binary.LittleEndian.PutUint32(content[28:32], 16_000)
	binary.LittleEndian.PutUint16(content[32:34], 2)
	binary.LittleEndian.PutUint16(content[34:36], 16)
	copy(content[36:40], "data")
	binary.LittleEndian.PutUint32(content[40:44], 2)
	return content
}

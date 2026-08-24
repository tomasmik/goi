package pronunciation

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestTofuguFindsAndDownloadsExactRecording(t *testing.T) {
	const expression = "納豆"
	const reading = "なっとう"
	requests := make([]string, 0, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.Header.Get("User-Agent") != userAgent {
			return testResponse(http.StatusForbidden, "text/plain", []byte("missing user agent")), nil
		}
		if request.Method == http.MethodHead {
			return testResponse(http.StatusOK, "audio/mpeg", nil), nil
		}
		return testResponse(http.StatusOK, "audio/mpeg", testMP3()), nil
	})
	provider := NewTofugu(&http.Client{Transport: transport})

	recordings, err := provider.Search(context.Background(), expression, reading)
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 {
		t.Fatalf("recordings = %#v", recordings)
	}
	recording := recordings[0]
	if recording.Label != reading || recording.SourceName != "Tofugu / WaniKani" || recording.LicenseName != "CC BY-SA 4.0" {
		t.Fatalf("recording = %#v", recording)
	}
	if !strings.Contains(recording.SourceURL, "%E7%B4%8D%E8%B1%86") {
		t.Fatalf("source URL = %q", recording.SourceURL)
	}

	upload, err := provider.Download(context.Background(), recording.ID, expression, reading)
	if err != nil {
		t.Fatal(err)
	}
	if upload.MimeType != "audio/mpeg" || len(upload.Content) == 0 {
		t.Fatalf("upload = %#v", upload)
	}
	if len(requests) != 2 || !strings.HasPrefix(requests[0], "HEAD ") || !strings.HasPrefix(requests[1], "GET ") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestTofuguReturnsNoResultForMissingRecording(t *testing.T) {
	provider := NewTofugu(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusNotFound, "text/plain", nil), nil
	})})
	recordings, err := provider.Search(context.Background(), "砂場", "すなば")
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 0 {
		t.Fatalf("recordings = %#v", recordings)
	}
}

func TestTofuguRejectsRecordingIDForDifferentWord(t *testing.T) {
	provider := NewTofugu(nil)
	_, err := provider.Download(context.Background(), tofuguRecordingID("納豆", "なっとう"), "日本", "にほん")
	if !errors.Is(err, ErrRecordingUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func testMP3() []byte {
	return append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}, make([]byte, 128)...)
}

package pronunciation

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tomasmik/goi/internal/media"
)

const (
	tofuguAudioBaseURL  = "https://raw.githubusercontent.com/tofugu/japanese-vocabulary-pronunciation-audio/master/lib/mp3/"
	tofuguSourceBaseURL = "https://github.com/tofugu/japanese-vocabulary-pronunciation-audio/blob/master/lib/mp3/"
	tofuguSourceName    = "Tofugu / WaniKani"
	tofuguLicenseName   = "CC BY-SA 4.0"
	tofuguLicenseURL    = "https://creativecommons.org/licenses/by-sa/4.0/"
	tofuguIDFlag        = int64(1) << 62
)

type Tofugu struct {
	client *http.Client
}

func NewTofugu(client *http.Client) *Tofugu {
	if client == nil {
		client = defaultClient()
	}
	return &Tofugu{client: client}
}

func (t *Tofugu) Search(ctx context.Context, expression, reading string) ([]Recording, error) {
	expression, reading = strings.TrimSpace(expression), strings.TrimSpace(reading)
	if expression == "" || reading == "" {
		return nil, nil
	}
	filename := tofuguFilename(expression, reading)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, tofuguAudioURL(filename), nil)
	if err != nil {
		return nil, fmt.Errorf("create Tofugu audio lookup request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	response, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Tofugu pronunciation audio: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query Tofugu pronunciation audio: server returned %s", response.Status)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "audio/") {
		return nil, fmt.Errorf("query Tofugu pronunciation audio: server returned %q", contentType)
	}
	return []Recording{{
		ID:          tofuguRecordingID(expression, reading),
		Label:       reading,
		SourceName:  tofuguSourceName,
		SourceURL:   tofuguSourceURL(filename),
		LicenseName: tofuguLicenseName,
		LicenseURL:  tofuguLicenseURL,
	}}, nil
}

func (t *Tofugu) Download(ctx context.Context, recordingID int64, expression, reading string) (media.Upload, error) {
	expression, reading = strings.TrimSpace(expression), strings.TrimSpace(reading)
	if !isTofuguRecording(recordingID) || recordingID != tofuguRecordingID(expression, reading) {
		return media.Upload{}, ErrRecordingUnavailable
	}
	filename := tofuguFilename(expression, reading)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tofuguAudioURL(filename), nil)
	if err != nil {
		return media.Upload{}, fmt.Errorf("create Tofugu audio request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	response, err := t.client.Do(req)
	if err != nil {
		return media.Upload{}, fmt.Errorf("download Tofugu pronunciation audio: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return media.Upload{}, ErrRecordingUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return media.Upload{}, fmt.Errorf("download Tofugu pronunciation audio: server returned %s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, media.MaxUploadBytes+1))
	if err != nil {
		return media.Upload{}, fmt.Errorf("read Tofugu pronunciation audio: %w", err)
	}
	if int64(len(content)) > media.MaxUploadBytes {
		return media.Upload{}, fmt.Errorf("Tofugu pronunciation audio exceeds the %d byte limit", media.MaxUploadBytes)
	}
	upload, err := media.Prepare(media.KindAudio, filename, content)
	if err != nil {
		return media.Upload{}, fmt.Errorf("prepare Tofugu pronunciation audio: %w", err)
	}
	upload.SourceName = tofuguSourceName
	upload.SourceURL = tofuguSourceURL(filename)
	upload.LicenseName = tofuguLicenseName
	upload.LicenseURL = tofuguLicenseURL
	return upload, nil
}

func tofuguAudioURL(filename string) string {
	return tofuguAudioBaseURL + url.PathEscape(filename)
}

func tofuguFilename(expression, reading string) string {
	return expression + "【" + reading + "】.mp3"
}

func tofuguSourceURL(filename string) string {
	return tofuguSourceBaseURL + url.PathEscape(filename)
}

func tofuguRecordingID(expression, reading string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(expression))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(reading))
	return tofuguIDFlag | int64(hash.Sum64()&uint64(tofuguIDFlag-1))
}

func isTofuguRecording(recordingID int64) bool {
	return recordingID&tofuguIDFlag != 0
}

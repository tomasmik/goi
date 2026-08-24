package pronunciation

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tomasmik/goi/internal/media"
)

type Library struct {
	commons *Commons
	tofugu  *Tofugu
}

func NewLibrary(client *http.Client) *Library {
	if client == nil {
		client = defaultClient()
	}
	return &Library{
		commons: NewCommons(client),
		tofugu:  NewTofugu(client),
	}
}

func (l *Library) Search(ctx context.Context, expression, reading string) ([]Recording, error) {
	tofuguRecordings, tofuguErr := l.tofugu.Search(ctx, expression, reading)
	if len(tofuguRecordings) > 0 {
		return tofuguRecordings, nil
	}
	commonsRecordings, commonsErr := l.commons.Search(ctx, expression, reading)
	if len(commonsRecordings) > 0 {
		return commonsRecordings, nil
	}
	return nil, errors.Join(tofuguErr, commonsErr)
}

func (l *Library) Download(ctx context.Context, recordingID int64, expression, reading string) (media.Upload, error) {
	if isTofuguRecording(recordingID) {
		return l.tofugu.Download(ctx, recordingID, expression, reading)
	}
	return l.commons.Download(ctx, recordingID, expression, reading)
}

func defaultClient() *http.Client {
	return &http.Client{Timeout: 12 * time.Second}
}

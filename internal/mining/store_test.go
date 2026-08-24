package mining

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/reviews"
	"github.com/tomasmik/goi/internal/vocabulary"
)

func TestCaptureMediaFollowsMinedExample(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, replayed, err := store.Create(ctx, CreateInput{
		RawText:      "食べました",
		Expression:   "食べる",
		ContextText:  "寿司を食べました。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000081",
	})
	if err != nil || replayed {
		t.Fatalf("create capture = replayed %t, error %v", replayed, err)
	}
	firstMedia := CaptureMediaInput{
		SentenceAudio: &media.Upload{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("first audio")},
		VideoFrame:    &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("frame")},
	}
	if err := store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, firstMedia); err != nil {
		t.Fatal(err)
	}
	withMedia, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if withMedia.SentenceAudioID == 0 || withMedia.VideoFrameID == 0 {
		t.Fatalf("capture media IDs = audio %d, frame %d", withMedia.SentenceAudioID, withMedia.VideoFrameID)
	}
	replacementMedia := CaptureMediaInput{
		SentenceAudio: &media.Upload{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("replacement audio")},
	}
	if err := store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, replacementMedia); err != nil {
		t.Fatal(err)
	}
	replaced, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.SentenceAudioID == withMedia.SentenceAudioID || replaced.VideoFrameID != withMedia.VideoFrameID {
		t.Fatalf("replaced capture media = audio %d, frame %d", replaced.SentenceAudioID, replaced.VideoFrameID)
	}
	var mediaCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 2 {
		t.Fatalf("media count after replacement = %d, want 2", mediaCount)
	}

	vocabularyID, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	example, err := examples.NewStore(db).Preferred(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if example.SentenceAudioID != replaced.SentenceAudioID || example.VideoFrameID != replaced.VideoFrameID {
		t.Fatalf("example media = audio %d, frame %d", example.SentenceAudioID, example.VideoFrameID)
	}
	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Revision != capture.Revision+1 {
		t.Fatalf("accepted revision = %d, want %d", accepted.Revision, capture.Revision+1)
	}
	if err := store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, replacementMedia); err != nil {
		t.Fatalf("attach after normal acceptance race: %v", err)
	}
	if err := store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, replacementMedia); err != nil {
		t.Fatalf("idempotent accepted attachment: %v", err)
	}
	if err := store.AttachMedia(ctx, capture.ID, accepted.Revision, capture.CaptureNonce, replacementMedia); err != nil {
		t.Fatalf("attach at accepted revision: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 2 {
		t.Fatalf("media count after accepted replay = %d, want 2", mediaCount)
	}
	if err := store.Delete(ctx, capture.ID, accepted.Revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 0 {
		t.Fatalf("media count after capture deletion = %d, want 0", mediaCount)
	}
}

func TestAddedAudioTracksAndEditedExampleFollowAcceptedCapture(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		RawText:      "育てる",
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000082",
	})
	if err != nil {
		t.Fatal(err)
	}
	audio := []media.Upload{
		{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("first audio")},
		{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("second audio")},
	}
	if err := store.AddMedia(ctx, capture.ID, capture.Revision, audio, nil, nil); err != nil {
		t.Fatal(err)
	}
	withAudio, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(withAudio.SentenceAudioIDs) != 2 {
		t.Fatalf("sentence audio IDs = %v, want two tracks", withAudio.SentenceAudioIDs)
	}
	if err := store.RemoveMedia(ctx, capture.ID, capture.Revision, withAudio.SentenceAudioIDs[0]); err != nil {
		t.Fatal(err)
	}

	vocabularyID, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Pronunciation:      "そだてる",
		Meanings:           []string{"to raise", "to grow"},
		ExampleSentence:    "この畑でぶどうを育てています。",
		ExampleTranslation: "We grow grapes in this field.",
		ExampleTarget:      "育てています",
	})
	if err != nil {
		t.Fatal(err)
	}
	example, err := examples.NewStore(db).Preferred(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if example.Sentence != "この畑でぶどうを育てています。" || example.Translation != "We grow grapes in this field." || example.TargetSurface != "育てています" {
		t.Fatalf("mined example = %+v", example)
	}
	if len(example.SentenceAudioIDs) != 1 || example.SentenceAudioIDs[0] != withAudio.SentenceAudioIDs[1] {
		t.Fatalf("example audio IDs = %v, want remaining track %d", example.SentenceAudioIDs, withAudio.SentenceAudioIDs[1])
	}
}

func TestPronunciationAudioMovesToAcceptedVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "日本",
		ContextText:  "日本に住んでいます。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000097",
	})
	if err != nil {
		t.Fatal(err)
	}
	audio := media.Upload{
		Kind:        media.KindAudio,
		MimeType:    "audio/wav",
		Content:     []byte("word audio"),
		SourceName:  "Test recordings",
		SourceURL:   "https://example.com/recording",
		LicenseName: "CC0",
		LicenseURL:  "https://creativecommons.org/publicdomain/zero/1.0/",
	}
	if err := store.SetPronunciationAudio(ctx, capture.ID, capture.Revision, audio); err != nil {
		t.Fatal(err)
	}
	withAudio, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if withAudio.PronunciationAudioID == 0 {
		t.Fatal("capture has no pronunciation audio")
	}
	vocabularyID, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Pronunciation: "にほん",
		Meanings:      []string{"Japan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var savedMediaID int64
	if err := db.QueryRowContext(ctx, `
		SELECT media_id FROM vocabulary_media
		WHERE vocabulary_id = ? AND purpose = 'pronunciation'`, vocabularyID).Scan(&savedMediaID); err != nil {
		t.Fatal(err)
	}
	if savedMediaID != withAudio.PronunciationAudioID {
		t.Fatalf("saved pronunciation media = %d, want %d", savedMediaID, withAudio.PronunciationAudioID)
	}
	item, err := vocabulary.NewStore(db).Get(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Media) != 1 {
		t.Fatalf("saved media = %+v, want one recording", item.Media)
	}
	if item.Media[0].SourceName != audio.SourceName || item.Media[0].SourceURL != audio.SourceURL ||
		item.Media[0].LicenseName != audio.LicenseName || item.Media[0].LicenseURL != audio.LicenseURL {
		t.Fatalf("saved media attribution = %+v, want %+v", item.Media[0], audio)
	}
}

func TestCaptureMediaRejectsStaleRevision(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "食べる",
		ContextText:  "寿司を食べる。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000084",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{
		Expression:  capture.Expression,
		ContextText: "ラーメンを食べる。",
		SourceKind:  SourceVideo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision == capture.Revision {
		t.Fatal("editing the context did not advance the capture revision")
	}
	err = store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, CaptureMediaInput{
		VideoFrame: &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("old frame")},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale media attachment error = %v", err)
	}
	var mediaCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 0 {
		t.Fatalf("media count after stale attachment = %d, want 0", mediaCount)
	}
}

func TestEditingCaptureContextRemovesStaleMedia(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	position := int64(1000)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:       "猫",
		ContextText:      "猫がいます。",
		SourceKind:       SourceVideo,
		SourceURL:        "https://www.youtube.com/watch?v=example",
		SourcePositionMS: &position,
		CaptureNonce:     "00000000000000000000000000000088",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachMedia(ctx, capture.ID, capture.Revision, capture.CaptureNonce, CaptureMediaInput{
		SentenceAudio: &media.Upload{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("audio")},
		VideoFrame:    &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("frame")},
	}); err != nil {
		t.Fatal(err)
	}

	renamed, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{
		Expression:       "ネコ",
		ContextText:      capture.ContextText,
		SourceKind:       capture.SourceKind,
		SourceURL:        capture.SourceURL,
		SourcePositionMS: capture.SourcePositionMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.SentenceAudioID == 0 || renamed.VideoFrameID == 0 {
		t.Fatalf("expression-only edit removed valid media: %+v", renamed)
	}

	updated, err := store.Update(ctx, capture.ID, renamed.Revision, UpdateInput{
		Expression:       renamed.Expression,
		ContextText:      "猫が寝ています。",
		SourceKind:       renamed.SourceKind,
		SourceURL:        renamed.SourceURL,
		SourcePositionMS: renamed.SourcePositionMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SentenceAudioID != 0 || updated.VideoFrameID != 0 {
		t.Fatalf("context edit retained stale media: %+v", updated)
	}
	var mediaCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 0 {
		t.Fatalf("unused media after context edit = %d, want 0", mediaCount)
	}
}

func TestCaptureMediaRequiresContextAndMatchingNonce(t *testing.T) {
	for _, test := range []struct {
		name      string
		context   string
		nonce     string
		wantError error
	}{
		{
			name:      "context",
			nonce:     "00000000000000000000000000000085",
			wantError: ErrInvalidInput,
		},
		{
			name:      "nonce",
			context:   "猫がいます。",
			nonce:     "00000000000000000000000000000087",
			wantError: ErrInvalidInput,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openMiningTestDatabase(t)
			store := NewStore(db)
			capture, _, err := store.Create(ctx, CreateInput{
				Expression:   "猫",
				ContextText:  test.context,
				SourceKind:   SourceVideo,
				CaptureNonce: "00000000000000000000000000000086",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = store.AttachMedia(ctx, capture.ID, capture.Revision, test.nonce, CaptureMediaInput{
				VideoFrame: &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("frame")},
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("media attachment error = %v, want %v", err, test.wantError)
			}
			var mediaCount int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
				t.Fatal(err)
			}
			if mediaCount != 0 {
				t.Fatalf("media count = %d, want 0", mediaCount)
			}
		})
	}
}

func TestResolvingCaptureCreatesMinedExample(t *testing.T) {
	for _, resolution := range []string{"accept", "attach"} {
		t.Run(resolution, func(t *testing.T) {
			ctx, db := openMiningTestDatabase(t)
			store := NewStore(db)
			position := int64(72_000)
			capture, replayed, err := store.Create(ctx, CreateInput{
				RawText:          "食べました",
				Expression:       "食べる",
				ContextText:      "昨日、駅で寿司を食べました。",
				SourceKind:       SourceVideo,
				SourceTitle:      "Japanese vlog",
				SourceURL:        "https://www.youtube.com/watch?v=example",
				SourcePositionMS: &position,
				CaptureNonce:     "00000000000000000000000000000009",
			})
			if err != nil || replayed {
				t.Fatalf("create capture = replayed %t, error %v", replayed, err)
			}

			var vocabularyID int64
			switch resolution {
			case "accept":
				vocabularyID, err = store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
					Pronunciation: "たべる",
					Meanings:      []string{"to eat"},
				})
			case "attach":
				vocabularyID, err = vocabulary.NewStore(db).Create(ctx, vocabulary.CreateInput{
					Expression:    "食べる",
					Pronunciation: "たべる",
					Meanings:      []string{"to eat"},
				})
				if err == nil {
					err = store.Attach(ctx, capture.ID, capture.Revision, vocabularyID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}

			items, err := examples.NewStore(db).List(ctx, vocabularyID)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 {
				t.Fatalf("examples = %+v, want one", items)
			}
			example := items[0]
			if example.Origin != examples.OriginMined || example.MiningCaptureID == nil || *example.MiningCaptureID != capture.ID {
				t.Fatalf("example provenance = %+v", example)
			}
			if example.Sentence != "昨日、駅で寿司を食べました。" || example.TargetSurface != "食べました" || !example.HasTarget {
				t.Fatalf("example context = %+v", example)
			}
			if example.SourceTitle != "Japanese vlog" || example.SourcePositionLabel != "1:12" || !strings.Contains(example.SourceLink, "t=72s") {
				t.Fatalf("example source = %+v", example)
			}
		})
	}
}

func TestCreatePreservesEncountersAndReplaysNonce(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	position := int64(12_500)
	input := CreateInput{
		RawText:          " ＡＢＣ ",
		Expression:       " ＡＢＣ ",
		ContextText:      "first context\r\nsecond line",
		SourceKind:       SourceVideo,
		SourceTitle:      "A lesson",
		SourceURL:        "https://user:secret@example.com/watch?v=1",
		SourcePositionMS: &position,
		CaptureNonce:     "00000000000000000000000000000001",
	}

	first, replayed, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first capture was reported as a replay")
	}
	if first.NormalizedExpression != "abc" || first.ContextText != "first context\nsecond line" {
		t.Fatalf("capture = %#v", first)
	}
	if first.SourceURL != "https://example.com/watch?v=1" {
		t.Fatalf("source URL = %q", first.SourceURL)
	}

	replay, replayed, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || replay.ID != first.ID {
		t.Fatalf("replay = (%d, %t), want (%d, true)", replay.ID, replayed, first.ID)
	}

	changed := input
	changed.ContextText = "different"
	if _, _, err := store.Create(ctx, changed); !errors.Is(err, ErrNonceConflict) {
		t.Fatalf("changed nonce replay error = %v, want nonce conflict", err)
	}

	input.CaptureNonce = "00000000000000000000000000000002"
	second, replayed, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || second.ID == first.ID {
		t.Fatalf("repeated encounter = (%d, %t), first ID %d", second.ID, replayed, first.ID)
	}

	items, err := store.ListPage(ctx, StatusPending, maximumCapturePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != second.ID {
		t.Fatalf("pending items = %#v", items)
	}
}

func TestDeletedCaptureNonceCannotBeReplayed(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{
		Expression:   "猫",
		ContextText:  "猫がいます。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000089",
	}
	capture, _, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, capture.ID, capture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(ctx, input); !errors.Is(err, ErrCaptureDeleted) {
		t.Fatalf("deleted capture replay error = %v, want deleted capture", err)
	}
	changed := input
	changed.ContextText = "別の文です。"
	if _, _, err := store.Create(ctx, changed); !errors.Is(err, ErrCaptureDeleted) {
		t.Fatalf("changed deleted nonce error = %v, want deleted capture", err)
	}
	var captures, tombstones int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mining_captures").Scan(&captures); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mining_capture_tombstones").Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if captures != 0 || tombstones != 1 {
		t.Fatalf("deleted capture state = %d captures, %d tombstones", captures, tombstones)
	}
}

func TestDeleteAndNonceReplayCannotRecreateCapture(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{
		Expression:   "犬",
		SourceKind:   SourceWeb,
		CaptureNonce: "00000000000000000000000000000090",
	}
	capture, _, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	deleteResult := make(chan error, 1)
	type createResult struct {
		capture  Capture
		replayed bool
		err      error
	}
	createResults := make(chan createResult, 1)
	go func() {
		<-start
		deleteResult <- store.Delete(ctx, capture.ID, capture.Revision)
	}()
	go func() {
		<-start
		created, replayed, err := store.Create(ctx, input)
		createResults <- createResult{capture: created, replayed: replayed, err: err}
	}()
	close(start)
	if err := <-deleteResult; err != nil {
		t.Fatal(err)
	}
	created := <-createResults
	if created.err == nil {
		if !created.replayed || created.capture.ID != capture.ID {
			t.Fatalf("concurrent create = capture %d, replayed %t", created.capture.ID, created.replayed)
		}
	} else if !errors.Is(created.err, ErrCaptureDeleted) {
		t.Fatalf("concurrent replay error = %v", created.err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mining_captures").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("captures after delete and replay = %d, want 0", count)
	}
}

func TestListPageBoundsAndCountsStatus(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	pendingIDs := insertMiningCaptures(t, ctx, db, StatusPending, 0, maximumCapturePageSize+1)
	insertMiningCaptures(t, ctx, db, StatusDiscarded, 1000, 1)

	count, err := store.ListCount(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if count != maximumCapturePageSize+1 {
		t.Fatalf("pending count = %d, want %d", count, maximumCapturePageSize+1)
	}
	first, err := store.ListPage(ctx, StatusPending, maximumCapturePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ListPage(ctx, StatusPending, maximumCapturePageSize, maximumCapturePageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != maximumCapturePageSize || first[0].ID != pendingIDs[maximumCapturePageSize] || first[len(first)-1].ID != pendingIDs[1] {
		t.Fatalf("first page IDs = %d through %d across %d captures", first[0].ID, first[len(first)-1].ID, len(first))
	}
	if len(second) != 1 || second[0].ID != pendingIDs[0] {
		t.Fatalf("second page = %+v, want oldest pending capture", second)
	}

	for _, test := range []struct {
		name   string
		status Status
		limit  int
		offset int
	}{
		{name: "zero limit", status: StatusPending, limit: 0},
		{name: "oversized limit", status: StatusPending, limit: maximumCapturePageSize + 1},
		{name: "negative offset", status: StatusPending, limit: maximumCapturePageSize, offset: -1},
		{name: "invalid status", status: Status("unknown"), limit: maximumCapturePageSize},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.ListPage(ctx, test.status, test.limit, test.offset); err == nil {
				t.Fatal("invalid mining page was accepted")
			}
		})
	}
	if _, err := store.ListCount(ctx, Status("unknown")); err == nil {
		t.Fatal("invalid mining count status was accepted")
	}
}

func TestConcurrentNonceReplayCreatesOneCapture(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	input := CreateInput{
		Expression:   "猫",
		SourceKind:   SourceManual,
		CaptureNonce: "00000000000000000000000000000003",
	}

	type result struct {
		capture  Capture
		replayed bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			capture, replayed, err := store.Create(ctx, input)
			results <- result{capture: capture, replayed: replayed, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var id int64
	replays := 0
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if id == 0 {
			id = item.capture.ID
		} else if item.capture.ID != id {
			t.Fatalf("capture IDs = %d and %d", id, item.capture.ID)
		}
		if item.replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("replayed results = %d, want 1", replays)
	}
}

func TestUpdateAndLifecycleUseRevisions(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "犬", "00000000000000000000000000000004")

	unchanged, err := store.Update(ctx, capture.ID, 1, UpdateInput{Expression: "犬", SourceKind: SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 {
		t.Fatalf("unchanged revision = %d, want 1", unchanged.Revision)
	}

	updated, err := store.Update(ctx, capture.ID, 1, UpdateInput{
		Expression:  "いぬ",
		ContextText: "かわいい犬",
		SourceKind:  SourceWeb,
		SourceURL:   "https://example.com/page",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.NormalizedExpression != "いぬ" {
		t.Fatalf("updated capture = %#v", updated)
	}
	if _, err := store.Update(ctx, capture.ID, 1, UpdateInput{Expression: "犬", SourceKind: SourceManual}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	if err := store.Discard(ctx, capture.ID, 2); err != nil {
		t.Fatal(err)
	}
	discarded, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Status != StatusDiscarded || discarded.Revision != 3 {
		t.Fatalf("discarded capture = %#v", discarded)
	}
	if err := store.Discard(ctx, capture.ID, 3); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second discard error = %v", err)
	}
	if err := store.Restore(ctx, capture.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, capture.ID, 3); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := store.Delete(ctx, capture.ID, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, capture.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get deleted capture error = %v", err)
	}
}

func TestAcceptIsAtomicAndCreatesUnlearnedVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "食べる", "00000000000000000000000000000005")

	_, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Expression:    "飲む",
		Pronunciation: "not kana",
		Meanings:      []string{"to eat"},
		SourceLabel:   "spoofed source",
	})
	if !errors.Is(err, vocabulary.ErrInvalidInput) {
		t.Fatalf("invalid acceptance error = %v", err)
	}
	stillPending, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != StatusPending || stillPending.Revision != 1 {
		t.Fatalf("capture changed after failed acceptance: %#v", stillPending)
	}
	var vocabularyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vocabulary`).Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("vocabulary count after failed acceptance = %d", vocabularyCount)
	}

	vocabularyID, err := store.Accept(ctx, capture.ID, 1, vocabulary.CreateInput{
		Expression:    "飲む",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
		SourceLabel:   "spoofed source",
	})
	if err != nil {
		t.Fatal(err)
	}
	var status, expression, sourceLabel string
	if err := db.QueryRow(`SELECT status, expression, source_label FROM vocabulary WHERE id = ?`, vocabularyID).Scan(&status, &expression, &sourceLabel); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || expression != "食べる" || sourceLabel != "Mining inbox" {
		t.Fatalf("vocabulary = status %q, expression %q, source %q", status, expression, sourceLabel)
	}
	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusAccepted || accepted.VocabularyID == nil || *accepted.VocabularyID != vocabularyID {
		t.Fatalf("accepted capture = %#v", accepted)
	}
	if _, err := store.Accept(ctx, capture.ID, accepted.Revision, vocabulary.CreateInput{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second acceptance error = %v", err)
	}

	if _, err := db.Exec(`DELETE FROM vocabulary WHERE id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	accepted, err = store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusAccepted || accepted.VocabularyID != nil {
		t.Fatalf("capture after vocabulary deletion = %#v", accepted)
	}
}

func TestAttachRequiresAnExactExistingVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	vocabularyStore := vocabulary.NewStore(db)
	catID, err := vocabularyStore.Create(ctx, vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dogID, err := vocabularyStore.Create(ctx, vocabulary.CreateInput{
		Expression: "犬", Pronunciation: "いぬ", Meanings: []string{"dog"},
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000006")
	if capture.ExistingVocabularyID == nil || *capture.ExistingVocabularyID != catID {
		t.Fatalf("existing vocabulary ID = %v, want %d", capture.ExistingVocabularyID, catID)
	}
	if err := store.Attach(ctx, capture.ID, 1, dogID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched attach error = %v", err)
	}
	if err := store.Attach(ctx, capture.ID, 1, catID); err != nil {
		t.Fatal(err)
	}
	var meaning string
	if err := db.QueryRow(`SELECT text FROM meanings WHERE vocabulary_id = ?`, catID).Scan(&meaning); err != nil {
		t.Fatal(err)
	}
	if meaning != "cat" {
		t.Fatalf("meaning after attach = %q", meaning)
	}
	var status string
	var srsCount int
	if err := db.QueryRow(`SELECT status FROM vocabulary WHERE id = ?`, catID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?`, catID).Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || srsCount != 0 {
		t.Fatalf("unlearned duplicate after attach = status %q, SRS states %d", status, srsCount)
	}
}

func TestAttachQueuesVocabularyKnownElsewhereForLesson(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	vocabularyStore := vocabulary.NewStore(db)
	vocabularyID, err := vocabularyStore.Create(ctx, vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vocabularyStore.AddKnown(ctx, "猫"); err != nil {
		t.Fatal(err)
	}

	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000061")
	if err := store.Attach(ctx, capture.ID, capture.Revision, vocabularyID); err != nil {
		t.Fatal(err)
	}

	var status string
	var knownElsewhere sql.NullInt64
	var lessonCompletedAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT v.status, v.known_elsewhere_at, v.lesson_completed_at
		FROM vocabulary v
		WHERE v.id = ?`, vocabularyID).Scan(
		&status, &knownElsewhere, &lessonCompletedAt,
	); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || knownElsewhere.Valid || lessonCompletedAt.Valid {
		t.Fatalf("queued vocabulary = status %q, known elsewhere %v, lesson completed %v", status, knownElsewhere.Valid, lessonCompletedAt.Valid)
	}
	var srsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?", vocabularyID).Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if srsCount != 0 {
		t.Fatalf("queued vocabulary has %d SRS states", srsCount)
	}
	available, err := lessons.NewStore(db).AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("available lessons = %d, want 1", available)
	}
}

func TestAttachDoesNotQueueSparseVocabularyWithoutDictionaryDetails(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).AddKnown(ctx, "猫"); err != nil {
		t.Fatal(err)
	}
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = '猫'").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}

	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000063")
	err := store.Attach(ctx, capture.ID, capture.Revision, vocabularyID)
	if err == nil || !strings.Contains(err.Error(), "add a reading and meaning") {
		t.Fatalf("sparse attachment error = %v", err)
	}

	var status string
	var knownElsewhere sql.NullInt64
	if err := db.QueryRow("SELECT status, known_elsewhere_at FROM vocabulary WHERE id = ?", vocabularyID).Scan(&status, &knownElsewhere); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || !knownElsewhere.Valid {
		t.Fatalf("sparse vocabulary changed to status %q, known elsewhere %v", status, knownElsewhere.Valid)
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPending || stored.VocabularyID != nil {
		t.Fatalf("capture changed after failed attachment: %#v", stored)
	}
}

func TestAttachQueuesSuspendedVocabularyForLesson(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	vocabularyID, err := vocabulary.NewStore(db).Create(ctx, vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE vocabulary SET status = 'suspended', lesson_completed_at = 1 WHERE id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at, last_reviewed_at, suspended_at)
		VALUES (?, 8, NULL, 1, 1)`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO leech_states (
			vocabulary_id, active, ever_leech, marked_at, failures_since_mark, auto_suspended_at
		) VALUES (?, 1, 1, 1, 3, 1)`, vocabularyID); err != nil {
		t.Fatal(err)
	}

	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000062")
	if err := store.Attach(ctx, capture.ID, capture.Revision, vocabularyID); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM vocabulary WHERE id = ?", vocabularyID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var srsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?", vocabularyID).Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || srsCount != 0 {
		t.Fatalf("queued vocabulary = status %q, SRS states %d", status, srsCount)
	}
	var leechActive, everLeech int
	var leechSuspendedAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT active, ever_leech, auto_suspended_at
		FROM leech_states
		WHERE vocabulary_id = ?`, vocabularyID).Scan(&leechActive, &everLeech, &leechSuspendedAt); err != nil {
		t.Fatal(err)
	}
	if leechActive != 0 || everLeech != 1 || leechSuspendedAt.Valid {
		t.Fatalf("reset leech = active %d, ever %d, suspended %v", leechActive, everLeech, leechSuspendedAt)
	}
	leechStatus, err := vocabulary.NewStore(db).LeechStatus(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if leechStatus != "former_leech" {
		t.Fatalf("leech status = %q, want former_leech", leechStatus)
	}
}

func TestAttachAbandonsActiveReviewBeforeResettingVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	vocabularyID, err := vocabulary.NewStore(db).Create(ctx, vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET status = 'active', lesson_completed_at = 1 WHERE id = ?", vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 0, 1)", vocabularyID); err != nil {
		t.Fatal(err)
	}
	reviewSessionID, err := reviews.NewStore(db).StartNormal(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}

	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000064")
	if err := store.Attach(ctx, capture.ID, capture.Revision, vocabularyID); err != nil {
		t.Fatal(err)
	}

	var reviewStatus, vocabularyStatus string
	if err := db.QueryRow("SELECT status FROM review_sessions WHERE id = ?", reviewSessionID).Scan(&reviewStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT status FROM vocabulary WHERE id = ?", vocabularyID).Scan(&vocabularyStatus); err != nil {
		t.Fatal(err)
	}
	var srsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?", vocabularyID).Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if reviewStatus != "abandoned" || vocabularyStatus != "unlearned" || srsCount != 0 {
		t.Fatalf("mined review reset = review %q, vocabulary %q, SRS states %d", reviewStatus, vocabularyStatus, srsCount)
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusAccepted || stored.VocabularyID == nil || *stored.VocabularyID != vocabularyID {
		t.Fatalf("attached capture = %#v", stored)
	}
}

func TestCaptureValidation(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	nonce := "00000000000000000000000000000007"
	tests := []CreateInput{
		{Expression: "", CaptureNonce: nonce},
		{Expression: "猫", CaptureNonce: "short"},
		{Expression: "猫\x00", CaptureNonce: nonce},
		{Expression: "猫", SourceURL: "javascript:alert(1)", CaptureNonce: nonce},
		{Expression: "猫", SourceURL: "https://example.com/" + strings.Repeat("a", 2048), CaptureNonce: nonce},
	}
	negative := int64(-1)
	tests = append(tests, CreateInput{Expression: "猫", SourcePositionMS: &negative, CaptureNonce: nonce})
	for index, input := range tests {
		if _, _, err := store.Create(ctx, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d error = %v, want invalid input", index, err)
		}
	}
}

func TestAcceptTruncatesLongURLUsedAsVocabularySource(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	sourceURL := "https://example.com/" + strings.Repeat("a", 500)
	capture, replayed, err := store.Create(ctx, CreateInput{
		Expression:   "猫",
		SourceKind:   SourceWeb,
		SourceURL:    sourceURL,
		CaptureNonce: "00000000000000000000000000000070",
	})
	if err != nil || replayed {
		t.Fatalf("create capture = replayed %t, error %v", replayed, err)
	}
	vocabularyID, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Pronunciation: "ねこ",
		Meanings:      []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := vocabulary.NewStore(db).Get(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	want := string([]rune(sourceURL)[:maxTitleRunes])
	if item.SourceLabel != want {
		t.Fatalf("source label = %q, want %q", item.SourceLabel, want)
	}
}

func openMiningTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mining.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func createMiningCapture(t *testing.T, ctx context.Context, store *Store, expression, nonce string) Capture {
	t.Helper()
	capture, replayed, err := store.Create(ctx, CreateInput{
		Expression: expression, SourceKind: SourceManual, CaptureNonce: nonce,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("new test capture was reported as replayed")
	}
	return capture
}

func insertMiningCaptures(t *testing.T, ctx context.Context, db *sql.DB, status Status, first, count int) []int64 {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	ids := make([]int64, 0, count)
	for offset := 0; offset < count; offset++ {
		index := first + offset
		expression := fmt.Sprintf("capture-%03d", index)
		result, err := tx.ExecContext(ctx, `
			INSERT INTO mining_captures (
				raw_text, expression, normalized_expression, source_kind,
				capture_nonce, request_hash, status, created_at
			) VALUES (?, ?, ?, 'manual', ?, ?, ?, ?)`,
			expression, expression, expression,
			fmt.Sprintf("%032x", index+1), fmt.Sprintf("%064x", index+1), status, index)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

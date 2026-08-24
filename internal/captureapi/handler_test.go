package captureapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/mining"
)

type fakeCaptureCreator struct {
	input    mining.CreateInput
	capture  mining.Capture
	replayed bool
	err      error
}

func (f *fakeCaptureCreator) Create(_ context.Context, input mining.CreateInput) (mining.Capture, bool, error) {
	f.input = input
	return f.capture, f.replayed, f.err
}

func (*fakeCaptureCreator) AttachMedia(context.Context, int64, int64, string, mining.CaptureMediaInput) error {
	return nil
}

func TestStatusRequiresSecureBearerAuthentication(t *testing.T) {
	router, token, _ := captureAPITestRouter(t, &fakeCaptureCreator{})

	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
	request.TLS = &tls.ConnectionState{}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing bearer response = %d, headers = %v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("insecure remote response = %d, body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("loopback response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestStatusAcceptsPlainHTTPFromPrivateNetworks(t *testing.T) {
	router, token, _ := captureAPITestRouter(t, &fakeCaptureCreator{})

	for _, remoteAddress := range []string{
		"10.0.0.4:4321",
		"172.20.0.2:4321",
		"192.168.1.8:4321",
		"[fe80::1]:4321",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
		request.RemoteAddr = remoteAddress
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("remote address %q response = %d, body = %s", remoteAddress, response.Code, response.Body.String())
		}
	}
}

func TestStatusAcceptsHTTPSFromTrustedProxy(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	created, err := store.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, nil, nil, nil, true).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted HTTPS proxy response = %d, body = %s", response.Code, response.Body.String())
	}

	router = chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, nil, nil, nil, false).Routes(router)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted forwarded scheme response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestTrustedProxyRequiresForwardedHTTPSEvenFromLoopback(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	created, err := store.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, nil, nil, nil, true).Routes(router)

	for _, forwardedProto := range []string{"", "http"} {
		request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
		request.RemoteAddr = "127.0.0.1:4321"
		request.Header.Set("Authorization", "Bearer "+created.Plaintext)
		if forwardedProto != "" {
			request.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("forwarded proto %q response = %d, body = %s", forwardedProto, response.Code, response.Body.String())
		}
	}
}

func TestForwardedClientAddressDoesNotGrantLoopbackAccess(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	created, err := store.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, nil, nil, nil, true).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/status", nil)
	request.RemoteAddr = "192.0.2.1:4321"
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	request.Header.Set("X-Real-IP", "127.0.0.1")
	request.Header.Set("True-Client-IP", "127.0.0.1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("spoofed loopback response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateCapture(t *testing.T) {
	position := int64(12_345)
	entrySequence := int64(1358280)
	creator := &fakeCaptureCreator{capture: mining.Capture{ID: 42, Revision: 3, Status: mining.StatusPending}}
	router, token, _ := captureAPITestRouter(t, creator)
	body := `{
		"raw_text":"食べました",
		"expression":"食べる",
		"context_text":"昨日、寿司を食べました。",
		"source_kind":"video",
		"source_title":"Japanese lesson",
		"source_url":"https://example.com/watch?v=1",
		"source_position_ms":12345,
		"suggested_entry_sequence":1358280,
		"capture_nonce":"00000000000000000000000000000001"
	}`
	response := serveCaptureAPI(router, token, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result captureResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != 42 || result.Revision != 3 || result.Status != mining.StatusPending || result.Replayed || result.ReviewURL != "/mining/captures/42" {
		t.Fatalf("response = %#v", result)
	}
	want := mining.CreateInput{
		RawText:                "食べました",
		Expression:             "食べる",
		ContextText:            "昨日、寿司を食べました。",
		SourceKind:             mining.SourceVideo,
		SourceTitle:            "Japanese lesson",
		SourceURL:              "https://example.com/watch?v=1",
		SourcePositionMS:       &position,
		SuggestedEntrySequence: &entrySequence,
		CaptureNonce:           "00000000000000000000000000000001",
	}
	if creator.input.RawText != want.RawText || creator.input.Expression != want.Expression ||
		creator.input.ContextText != want.ContextText || creator.input.SourceKind != want.SourceKind ||
		creator.input.SourceTitle != want.SourceTitle || creator.input.SourceURL != want.SourceURL ||
		creator.input.SourcePositionMS == nil || *creator.input.SourcePositionMS != *want.SourcePositionMS ||
		creator.input.SuggestedEntrySequence == nil || *creator.input.SuggestedEntrySequence != *want.SuggestedEntrySequence ||
		creator.input.CaptureNonce != want.CaptureNonce {
		t.Fatalf("capture input = %#v, want %#v", creator.input, want)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestCreateCapturePersistsSentence(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "Reading browser")
	if err != nil {
		t.Fatal(err)
	}
	miningStore := mining.NewStore(db)
	router := chi.NewRouter()
	NewHandler(tokens, miningStore, nil, nil, nil, nil, false).Routes(router)
	response := serveCaptureAPI(router, created.Plaintext, `{
		"raw_text":"食べました",
		"expression":"食べました",
		"context_text":"昨日、寿司を食べました。",
		"source_kind":"video",
		"source_title":"Japanese lesson",
		"source_url":"https://www.youtube.com/watch?v=example",
		"source_position_ms":48210,
		"capture_nonce":"00000000000000000000000000000009"
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	capture, err := miningStore.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if capture.ContextText != "昨日、寿司を食べました。" || capture.SourcePositionMS == nil || *capture.SourcePositionMS != 48210 {
		t.Fatalf("capture context = %#v", capture)
	}
}

func TestAttachCaptureMedia(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "YouTube browser")
	if err != nil {
		t.Fatal(err)
	}
	miningStore := mining.NewStore(db)
	router := chi.NewRouter()
	NewHandler(tokens, miningStore, nil, nil, nil, nil, false).Routes(router)
	nonce := "00000000000000000000000000000082"
	response := serveCaptureAPI(router, created.Plaintext, `{
		"expression":"猫",
		"context_text":"猫がいます。",
		"source_kind":"video",
		"capture_nonce":"`+nonce+`"
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create response = %d, body = %s", response.Code, response.Body.String())
	}
	var capture captureResponse
	if err := json.Unmarshal(response.Body.Bytes(), &capture); err != nil {
		t.Fatal(err)
	}
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}

	audioBytes := silentWAV()
	response = serveCaptureMedia(t, router, created.Plaintext, capture.ID, capture.Revision, nonce, audioBytes, pngBytes)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":1`) {
		t.Fatalf("media response = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := miningStore.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.VideoFrameID == 0 || stored.SentenceAudioID == 0 {
		t.Fatalf("capture media = frame %d, audio %d", stored.VideoFrameID, stored.SentenceAudioID)
	}
	response = serveCaptureMedia(t, router, created.Plaintext, capture.ID, capture.Revision, nonce, audioBytes, pngBytes)
	if response.Code != http.StatusOK {
		t.Fatalf("replayed media response = %d, body = %s", response.Code, response.Body.String())
	}
	var mediaCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 2 {
		t.Fatalf("media count after replay = %d, want 2", mediaCount)
	}
	response = serveCaptureMedia(t, router, created.Plaintext, capture.ID, capture.Revision+1, nonce, audioBytes, pngBytes)
	if response.Code != http.StatusConflict {
		t.Fatalf("wrong revision response = %d, body = %s", response.Code, response.Body.String())
	}
	response = serveCaptureMedia(t, router, created.Plaintext, capture.ID, capture.Revision, "00000000000000000000000000000083", audioBytes, pngBytes)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong nonce response = %d, body = %s", response.Code, response.Body.String())
	}
	if err := miningStore.Discard(ctx, capture.ID, capture.Revision); err != nil {
		t.Fatal(err)
	}
	response = serveCaptureMedia(t, router, created.Plaintext, capture.ID, capture.Revision, nonce, audioBytes, pngBytes)
	if response.Code != http.StatusConflict {
		t.Fatalf("discarded capture response = %d, body = %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/extension/v1/captures/"+strconv.FormatInt(capture.ID, 10)+"/media",
		strings.NewReader("not multipart"),
	)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType || !strings.Contains(response.Body.String(), `"code":"unsupported_media_type"`) {
		t.Fatalf("unsupported media request = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateCaptureReportsReplayAndStoreErrors(t *testing.T) {
	tests := []struct {
		name     string
		creator  *fakeCaptureCreator
		status   int
		response string
	}{
		{
			name:     "replay",
			creator:  &fakeCaptureCreator{capture: mining.Capture{ID: 7, Revision: 4, Status: mining.StatusPending}, replayed: true},
			status:   http.StatusOK,
			response: `"revision":4`,
		},
		{
			name:     "invalid capture",
			creator:  &fakeCaptureCreator{err: mining.ErrInvalidInput},
			status:   http.StatusUnprocessableEntity,
			response: `"code":"invalid_capture"`,
		},
		{
			name:     "nonce conflict",
			creator:  &fakeCaptureCreator{err: mining.ErrNonceConflict},
			status:   http.StatusConflict,
			response: `"code":"nonce_conflict"`,
		},
		{
			name:     "deleted capture",
			creator:  &fakeCaptureCreator{err: mining.ErrCaptureDeleted},
			status:   http.StatusConflict,
			response: `"code":"capture_deleted"`,
		},
		{
			name:     "storage failure",
			creator:  &fakeCaptureCreator{err: errors.New("database failed")},
			status:   http.StatusInternalServerError,
			response: `"code":"internal_error"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, token, _ := captureAPITestRouter(t, test.creator)
			response := serveCaptureAPI(router, token, `{"expression":"猫","capture_nonce":"00000000000000000000000000000002"}`)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.response) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCreateCaptureRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
	}{
		{name: "media type", contentType: "text/plain", body: `{}`, status: http.StatusUnsupportedMediaType},
		{name: "null", contentType: "application/json", body: `null`, status: http.StatusBadRequest},
		{name: "unknown field", contentType: "application/json", body: `{"expression":"猫","unknown":true}`, status: http.StatusBadRequest},
		{name: "trailing value", contentType: "application/json", body: `{} {}`, status: http.StatusBadRequest},
		{name: "too large", contentType: "application/json", body: `{"expression":"` + strings.Repeat("a", captureBodyLimit) + `"}`, status: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator := &fakeCaptureCreator{}
			router, token, _ := captureAPITestRouter(t, creator)
			request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/captures", strings.NewReader(test.body))
			request.TLS = &tls.ConnectionState{}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if creator.input.Expression != "" {
				t.Fatalf("creator received invalid input: %#v", creator.input)
			}
		})
	}
}

func captureAPITestRouter(t *testing.T, creator CaptureService) (http.Handler, string, *Store) {
	t.Helper()
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	created, err := store.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, creator, nil, nil, nil, nil, false).Routes(router)
	return router, created.Plaintext, store
}

func serveCaptureAPI(router http.Handler, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/captures", strings.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func serveCaptureMedia(t *testing.T, router http.Handler, token string, captureID, expectedRevision int64, nonce string, sentenceAudio, videoFrame []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("capture_nonce", nonce); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("expected_revision", strconv.FormatInt(expectedRevision, 10)); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		field    string
		filename string
		content  []byte
	}{
		{field: "sentence_audio", filename: "sentence.wav", content: sentenceAudio},
		{field: "video_frame", filename: "frame.png", content: videoFrame},
	} {
		if file.content == nil {
			continue
		}
		part, err := writer.CreateFormFile(file.field, file.filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/captures/"+strconv.FormatInt(captureID, 10)+"/media", &body)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
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

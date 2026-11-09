package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestPreparePNG(t *testing.T) {
	content, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}

	upload, err := Prepare(KindImage, "pixel.png", content)
	if err != nil {
		t.Fatal(err)
	}
	if upload.MimeType != "image/png" {
		t.Fatalf("MIME type = %q, want image/png", upload.MimeType)
	}
}

func TestPrepareRejectsOversizedImageDimensions(t *testing.T) {
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 8001, 1))); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(KindImage, "wide.png", content.Bytes())
	if err == nil || !strings.Contains(err.Error(), "image dimensions are outside the supported range") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestWebMAudioContainerIsAllowed(t *testing.T) {
	if !isAllowed(KindAudio, "video/webm", "sentence.webm") {
		t.Fatal("audio-only WebM container was rejected")
	}
	if isAllowed(KindImage, "video/webm", "sentence.webm") {
		t.Fatal("WebM container was accepted as an image")
	}
}

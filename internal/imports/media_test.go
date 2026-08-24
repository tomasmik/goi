package imports

import (
	"testing"

	"github.com/tomasmik/goi/internal/media"
)

func TestMediaReferenceDecodesHTMLEntities(t *testing.T) {
	tests := []struct {
		name  string
		value string
		kind  media.Kind
		want  string
	}{
		{name: "double-quoted image", value: `<img src="a&amp;b.png">`, kind: media.KindImage, want: "a&b.png"},
		{name: "single-quoted image", value: `<img src='cover&#32;art.png'>`, kind: media.KindImage, want: "cover art.png"},
		{name: "audio", value: `[sound:a&amp;b.mp3]`, kind: media.KindAudio, want: "a&b.mp3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mediaReference(test.value, test.kind); got != test.want {
				t.Fatalf("mediaReference() = %q, want %q", got, test.want)
			}
		})
	}
}

package textnorm

import "testing"

func TestNormalize(t *testing.T) {
	if got, want := Normalize("  ＧｏＩ 食べる  "), "goi 食べる"; got != want {
		t.Fatalf("Normalize() = %q, want %q", got, want)
	}
}

package examples

import (
	"net/url"
	"testing"
)

func TestLinkForSourceAddsYouTubeTimestampSafely(t *testing.T) {
	position := int64(3_725_900)
	link := LinkForSource("https://user:password@youtu.be/abc?list=one#chapter", &position)
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.User != nil || parsed.Query().Get("t") != "3725s" || parsed.Query().Get("list") != "one" {
		t.Fatalf("YouTube link = %q", link)
	}
	if got := FormatSourcePosition(&position); got != "1:02:05" {
		t.Fatalf("position label = %q, want 1:02:05", got)
	}

	if got := LinkForSource("https://notyoutube.com/watch?v=abc", &position); got != "https://notyoutube.com/watch?v=abc" {
		t.Fatalf("non-YouTube link = %q", got)
	}
	if got := LinkForSource("javascript:alert(1)", &position); got != "" {
		t.Fatalf("unsafe link = %q, want empty", got)
	}
}

func TestSplitTargetReturnsSafeTemplateParts(t *testing.T) {
	before, matched, after, found := SplitTarget("昨日、寿司を食べました。", "食べました")
	if before != "昨日、寿司を" || matched != "食べました" || after != "。" || !found {
		t.Fatalf("split = (%q, %q, %q, %t)", before, matched, after, found)
	}
	before, matched, after, found = SplitTarget("昨日、寿司を食べました。", "飲む")
	if before != "昨日、寿司を食べました。" || matched != "" || after != "" || found {
		t.Fatalf("missing split = (%q, %q, %q, %t)", before, matched, after, found)
	}
}

package jmdict

import "testing"

func TestCommonnessScoreUsesJMdictRankingBands(t *testing.T) {
	combined := func(reading, written int) int { return reading*1001 + written }
	for _, test := range []struct {
		name     string
		priority int
		want     int
	}{
		{name: "first tier", priority: combined(0, 0), want: 75},
		{name: "second tier", priority: combined(10, 10), want: 35},
		{name: "highest frequency band", priority: combined(21, 21), want: 100},
		{name: "middle frequency band", priority: combined(44, 44), want: 54},
		{name: "lowest frequency band", priority: combined(68, 68), want: 6},
		{name: "written form priority", priority: combined(1000, 21), want: 100},
		{name: "reading priority", priority: combined(10, 1000), want: 35},
		{name: "unranked", priority: combined(1000, 1000), want: 1},
		{name: "invalid", priority: -1, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CommonnessScore(test.priority); got != test.want {
				t.Fatalf("CommonnessScore(%d) = %d, want %d", test.priority, got, test.want)
			}
		})
	}
}

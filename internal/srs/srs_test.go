package srs

import (
	"testing"
	"time"
)

func TestNextReview(t *testing.T) {
	base := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		stage     Stage
		success   bool
		sixMonth  bool
		wantStage Stage
		wantDue   time.Time
	}{
		{name: "new success", stage: StageNew, success: true, wantStage: StageOne, wantDue: base.Add(8 * time.Hour)},
		{name: "stage one failure", stage: StageOne, success: false, wantStage: StageNew, wantDue: base.Add(4 * time.Hour)},
		{name: "seven day failure", stage: StageFour, success: false, wantStage: StageThree, wantDue: base.AddDate(0, 0, 2)},
		{name: "fourteen day failure", stage: StageFive, success: false, wantStage: StageThree, wantDue: base.AddDate(0, 0, 2)},
		{name: "one month failure", stage: StageSix, success: false, wantStage: StageThree, wantDue: base.AddDate(0, 0, 2)},
		{name: "four month failure", stage: StageSeven, success: false, wantStage: StageThree, wantDue: base.AddDate(0, 0, 2)},
		{name: "four month success burns by default", stage: StageSeven, success: true, wantStage: StageEvergreen},
		{name: "four month success schedules six months when enabled", stage: StageSeven, success: true, sixMonth: true, wantStage: StageEight, wantDue: base.AddDate(0, 6, 0)},
		{name: "six month failure returns to one month", stage: StageEight, success: false, wantStage: StageSix, wantDue: base.AddDate(0, 1, 0)},
		{name: "six month success burns", stage: StageEight, success: true, sixMonth: true, wantStage: StageEvergreen},
		{name: "evergreen stays evergreen", stage: StageEvergreen, success: true, wantStage: StageEvergreen},
		{name: "evergreen failure stays evergreen", stage: StageEvergreen, success: false, wantStage: StageEvergreen},
		{name: "new failure stays new", stage: StageNew, success: false, wantStage: StageNew, wantDue: base.Add(4 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stage, due := NextReview(tt.stage, tt.success, tt.sixMonth, base)
			if stage != tt.wantStage {
				t.Fatalf("stage = %d, want %d", stage, tt.wantStage)
			}
			if !due.Equal(tt.wantDue) {
				t.Fatalf("due = %s, want %s", due, tt.wantDue)
			}
		})
	}
}

func TestAdvanceCoversEveryStage(t *testing.T) {
	tests := []struct {
		name     string
		stage    Stage
		success  bool
		sixMonth bool
		want     Stage
	}{
		{name: "new success", stage: StageNew, success: true, want: StageOne},
		{name: "eight hour success", stage: StageOne, success: true, want: StageTwo},
		{name: "one day success", stage: StageTwo, success: true, want: StageThree},
		{name: "two day success", stage: StageThree, success: true, want: StageFour},
		{name: "seven day success", stage: StageFour, success: true, want: StageFive},
		{name: "fourteen day success", stage: StageFive, success: true, want: StageSix},
		{name: "one month success", stage: StageSix, success: true, want: StageSeven},
		{name: "four month success burns", stage: StageSeven, success: true, want: StageEvergreen},
		{name: "four month success reaches optional six month stage", stage: StageSeven, success: true, sixMonth: true, want: StageEight},
		{name: "six month success burns", stage: StageEight, success: true, sixMonth: true, want: StageEvergreen},
		{name: "existing six month success burns when option is disabled", stage: StageEight, success: true, want: StageEvergreen},
		{name: "burned success stays burned", stage: StageEvergreen, success: true, want: StageEvergreen},
		{name: "new failure stays new", stage: StageNew, want: StageNew},
		{name: "eight hour failure returns to four hours", stage: StageOne, want: StageNew},
		{name: "one day failure returns to eight hours", stage: StageTwo, want: StageOne},
		{name: "two day failure returns to one day", stage: StageThree, want: StageTwo},
		{name: "seven day failure returns to two days", stage: StageFour, want: StageThree},
		{name: "fourteen day failure returns to two days", stage: StageFive, want: StageThree},
		{name: "one month failure returns to two days", stage: StageSix, want: StageThree},
		{name: "four month failure returns to two days", stage: StageSeven, want: StageThree},
		{name: "six month failure returns to one month", stage: StageEight, want: StageSix},
		{name: "burned failure stays burned", stage: StageEvergreen, want: StageEvergreen},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Advance(test.stage, test.success, test.sixMonth); got != test.want {
				t.Fatalf("Advance(%d, %t, %t) = %d, want %d", test.stage, test.success, test.sixMonth, got, test.want)
			}
		})
	}
}

func TestDueAt(t *testing.T) {
	base := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		stage Stage
		want  time.Time
	}{
		{stage: StageNew, want: base.Add(4 * time.Hour)},
		{stage: StageOne, want: base.Add(8 * time.Hour)},
		{stage: StageTwo, want: base.AddDate(0, 0, 1)},
		{stage: StageThree, want: base.AddDate(0, 0, 2)},
		{stage: StageFour, want: base.AddDate(0, 0, 7)},
		{stage: StageFive, want: base.AddDate(0, 0, 14)},
		{stage: StageSix, want: base.AddDate(0, 1, 0)},
		{stage: StageSeven, want: base.AddDate(0, 4, 0)},
		{stage: StageEight, want: base.AddDate(0, 6, 0)},
		{stage: StageEvergreen},
	}
	for _, test := range tests {
		if got := DueAt(test.stage, base); !got.Equal(test.want) {
			t.Errorf("DueAt(%d) = %s, want %s", test.stage, got, test.want)
		}
	}
}

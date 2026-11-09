package settings

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValuesFromRequest(t *testing.T) {
	form := url.Values{
		"time_zone":             {"Asia/Tokyo"},
		"lesson_window_hours":   {"12"},
		"extra_study_limit":     {"8"},
		"retry_count":           {"3"},
		"review_mode":           {"self_grade"},
		"review_order":          {"stage_descending"},
		"review_card_order":     {"spaced"},
		"review_auto_advance":   {"on"},
		"leech_failure_count":   {"5"},
		"leech_suspend_count":   {"3"},
		"leech_recovery_streak": {"3"},
		"six_month_review":      {"on"},
		"theme":                 {"dark"},
		"audio_enabled":         {"on"},
	}
	request := formRequest(t, form)

	values, err := valuesFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want := Values{
		TimeZone:            "Asia/Tokyo",
		LessonWindowHours:   12,
		ExtraStudyLimit:     8,
		RetryCount:          3,
		ReviewMode:          "self_grade",
		ReviewOrder:         "stage_descending",
		ReviewCardOrder:     "spaced",
		ReviewAutoAdvance:   true,
		LeechFailureCount:   5,
		LeechSuspendCount:   3,
		LeechRecoveryStreak: 3,
		SixMonthReview:      true,
		Theme:               "dark",
		AudioEnabled:        true,
	}
	if values != want {
		t.Fatalf("valuesFromRequest() = %+v, want %+v", values, want)
	}
}

func TestValuesFromRequestRejectsInvalidNumbers(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{name: "lesson window", field: "lesson_window_hours", want: "recent lesson window must be a number"},
		{name: "study list size", field: "extra_study_limit", want: "extra-study list size must be a number"},
		{name: "answer attempts", field: "retry_count", want: "answer attempts must be a number"},
		{name: "leech failures", field: "leech_failure_count", want: "leech failure count must be a number"},
		{name: "leech suspension", field: "leech_suspend_count", want: "leech suspension count must be a number"},
		{name: "leech recovery", field: "leech_recovery_streak", want: "leech recovery streak must be a number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"time_zone":             {"UTC"},
				"lesson_window_hours":   {"12"},
				"extra_study_limit":     {"8"},
				"retry_count":           {"3"},
				"leech_failure_count":   {"5"},
				"leech_suspend_count":   {"3"},
				"leech_recovery_streak": {"3"},
				"theme":                 {"light"},
			}
			form.Set(test.field, "not-a-number")

			values, err := valuesFromRequest(formRequest(t, form))
			if err == nil || err.Error() != test.want {
				t.Fatalf("valuesFromRequest() error = %v, want %q", err, test.want)
			}
			if test.field != "lesson_window_hours" && values.LessonWindowHours != 12 {
				t.Fatalf("lesson window = %d, want 12", values.LessonWindowHours)
			}
			if test.field != "extra_study_limit" && values.ExtraStudyLimit != 8 {
				t.Fatalf("extra-study list size = %d, want 8", values.ExtraStudyLimit)
			}
			if test.field != "retry_count" && values.RetryCount != 3 {
				t.Fatalf("answer attempts = %d, want 3", values.RetryCount)
			}
		})
	}
}

func formRequest(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return request
}

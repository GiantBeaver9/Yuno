package db

import (
	"testing"
	"time"
)

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestExpandYearlyBirthday(t *testing.T) {
	// A birthday first entered in 1990, all-day.
	base := Event{
		Title:      "Mum's birthday",
		StartsAt:   "1990-04-22T00:00:00Z",
		EndsAt:     "1990-04-22T23:59:00Z",
		AllDay:     true,
		Recurrence: "yearly",
	}
	winStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	occ := expandInWindow(base, winStart, winEnd)
	if len(occ) != 1 {
		t.Fatalf("want 1 occurrence in 2026, got %d: %+v", len(occ), occ)
	}
	if got := occ[0].StartsAt; got != "2026-04-22T00:00:00Z" {
		t.Errorf("occurrence start = %s, want 2026-04-22T00:00:00Z", got)
	}
	if occ[0].Title != "Mum's birthday" {
		t.Errorf("lost title on occurrence")
	}
}

func TestExpandWeeklyAcrossMonth(t *testing.T) {
	base := Event{
		Title:      "Bins out",
		StartsAt:   "2026-07-01T07:00:00Z",
		EndsAt:     "2026-07-01T07:15:00Z",
		Recurrence: "weekly",
	}
	winStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	occ := expandInWindow(base, winStart, winEnd)
	// Jul 1, 8, 15, 22, 29 = 5 occurrences.
	if len(occ) != 5 {
		t.Fatalf("want 5 weekly occurrences in July, got %d: %+v", len(occ), starts(occ))
	}
	if occ[0].StartsAt != "2026-07-01T07:00:00Z" || occ[4].StartsAt != "2026-07-29T07:00:00Z" {
		t.Errorf("unexpected weekly bounds: %v", starts(occ))
	}
}

func TestExpandDailyDurationPreserved(t *testing.T) {
	base := Event{
		Title:      "Standup",
		StartsAt:   "2026-07-06T09:00:00Z",
		EndsAt:     "2026-07-06T09:30:00Z",
		Recurrence: "daily",
	}
	winStart := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 7, 10, 23, 59, 0, 0, time.UTC)
	occ := expandInWindow(base, winStart, winEnd)
	if len(occ) != 5 { // 6,7,8,9,10
		t.Fatalf("want 5 daily occurrences, got %d", len(occ))
	}
	for _, o := range occ {
		s, _ := time.Parse(time.RFC3339, o.StartsAt)
		e, _ := time.Parse(time.RFC3339, o.EndsAt)
		if e.Sub(s) != 30*time.Minute {
			t.Errorf("duration not preserved for %s", o.StartsAt)
		}
	}
}

func TestRecurrenceUntilBounds(t *testing.T) {
	base := Event{
		Title:           "Course",
		StartsAt:        "2026-07-01T18:00:00Z",
		EndsAt:          "2026-07-01T19:00:00Z",
		Recurrence:      "weekly",
		RecurrenceUntil: "2026-07-15T00:00:00Z",
	}
	winStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	occ := expandInWindow(base, winStart, winEnd)
	// Jul 1 and Jul 8 only (Jul 15 is after 00:00 bound).
	if len(occ) != 2 {
		t.Fatalf("want 2 bounded occurrences, got %d: %v", len(occ), starts(occ))
	}
}

func TestNextOccurrenceAfter(t *testing.T) {
	base := Event{Title: "BDay", StartsAt: "1990-12-25T00:00:00Z", EndsAt: "1990-12-25T23:59:00Z", Recurrence: "yearly"}
	after := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	next, ok := NextOccurrenceAfter(base, after)
	if !ok || next.StartsAt != "2026-12-25T00:00:00Z" {
		t.Fatalf("next yearly = %v ok=%v, want 2026-12-25", next.StartsAt, ok)
	}

	// Non-recurring one-off already in the past -> no next.
	past := Event{Title: "Trip", StartsAt: "2026-01-01T00:00:00Z", EndsAt: "2026-01-05T00:00:00Z", Recurrence: "none"}
	if _, ok := NextOccurrenceAfter(past, after); ok {
		t.Errorf("past one-off should have no next occurrence")
	}

	// Future one-off -> itself.
	trip := Event{Title: "Trip", StartsAt: "2026-09-01T00:00:00Z", EndsAt: "2026-09-08T00:00:00Z", Recurrence: "none"}
	if n, ok := NextOccurrenceAfter(trip, after); !ok || n.StartsAt != "2026-09-01T00:00:00Z" {
		t.Errorf("future one-off next = %v ok=%v", n.StartsAt, ok)
	}
}

func TestListEventsExpandsRecurring(t *testing.T) {
	st, err := Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateEvent(Event{Title: "Weekly sync", StartsAt: "2026-07-02T15:00:00Z", EndsAt: "2026-07-02T15:30:00Z", Recurrence: "weekly"}); err != nil {
		t.Fatal(err)
	}
	from := rfc(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	to := rfc(time.Date(2026, 7, 31, 23, 59, 0, 0, time.UTC))
	list, err := st.ListEvents(from, to)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range list {
		if e.Title == "Weekly sync" {
			count++
		}
	}
	if count != 5 { // Jul 2,9,16,23,30
		t.Fatalf("want 5 expanded weekly occurrences via ListEvents, got %d", count)
	}
}

func starts(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.StartsAt
	}
	return out
}

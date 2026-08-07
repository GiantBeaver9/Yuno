package db

import (
	"sort"
	"time"
)

// Event is a private calendar entry. Times are stored as UTC RFC3339 strings;
// the client renders them in local time. reminded is server-only bookkeeping for
// the reminder scheduler and is never exposed in JSON.
//
// Recurrence turns an event into a repeating series: "none" (default), "daily",
// "weekly", "monthly", or "yearly". RecurrenceUntil optionally bounds the series
// (RFC3339, empty = forever). Countdown flags an event the digest should show a
// days-until countdown for (birthdays, trips).
type Event struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Notes           string `json:"notes"`
	Location        string `json:"location"`
	Attendees       string `json:"attendees"`
	StartsAt        string `json:"startsAt"`
	EndsAt          string `json:"endsAt"`
	AllDay          bool   `json:"allDay"`
	Color           string `json:"color"`
	Recurrence      string `json:"recurrence"`
	RecurrenceUntil string `json:"recurrenceUntil"`
	Countdown       bool   `json:"countdown"`
	Important       bool   `json:"important"`
	Reminded        bool   `json:"-"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

const eventCols = `id, title, notes, location, attendees, starts_at, ends_at, all_day, color, recurrence, recurrence_until, countdown, important, reminded, created_at, updated_at`

// scanEvent reads one event row, translating the INTEGER bool columns.
func scanEvent(sc interface{ Scan(...any) error }) (Event, error) {
	var (
		e         Event
		allDay    int
		countdown int
		important int
		reminded  int
	)
	err := sc.Scan(&e.ID, &e.Title, &e.Notes, &e.Location, &e.Attendees,
		&e.StartsAt, &e.EndsAt, &allDay, &e.Color, &e.Recurrence, &e.RecurrenceUntil,
		&countdown, &important, &reminded, &e.CreatedAt, &e.UpdatedAt)
	e.AllDay = allDay != 0
	e.Countdown = countdown != 0
	e.Important = important != 0
	e.Reminded = reminded != 0
	if e.Recurrence == "" {
		e.Recurrence = "none"
	}
	return e, err
}

// ListEvents returns events overlapping the optional [from, to] window (both
// RFC3339 strings; empty means unbounded), ordered by start time. Recurring
// events are expanded into their concrete occurrences within the window.
func (s *Store) ListEvents(from, to string) ([]Event, error) {
	winStart, winEnd := parseWindow(from, to)

	// Non-recurring events via the existing string-overlap filter.
	out, err := s.queryNonRecurring(from, to)
	if err != nil {
		return nil, err
	}

	// Recurring events: fetch each series once and expand in-window.
	rows, err := s.db.Query(`SELECT ` + eventCols + ` FROM events WHERE recurrence != 'none' AND recurrence != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		base, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, expandInWindow(base, winStart, winEnd)...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt < out[j].StartsAt })
	return out, nil
}

func (s *Store) queryNonRecurring(from, to string) ([]Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE (recurrence = 'none' OR recurrence = '')`
	var args []any
	if from != "" {
		q += ` AND ends_at >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND starts_at <= ?`
		args = append(args, to)
	}
	q += ` ORDER BY starts_at`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) getEvent(id int64) (Event, error) {
	return scanEvent(s.db.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
}

// GetEvent fetches a single event by id (sql.ErrNoRows when absent).
func (s *Store) GetEvent(id int64) (Event, error) {
	return s.getEvent(id)
}

func (s *Store) CreateEvent(e Event) (Event, error) {
	res, err := s.db.Exec(`INSERT INTO events (title, notes, location, attendees, starts_at, ends_at, all_day, color, recurrence, recurrence_until, countdown, important) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Title, e.Notes, e.Location, e.Attendees, e.StartsAt, e.EndsAt, b2i(e.AllDay), e.Color, normRecurrence(e.Recurrence), e.RecurrenceUntil, b2i(e.Countdown), b2i(e.Important))
	if err != nil {
		return Event{}, err
	}
	id, _ := res.LastInsertId()
	return s.getEvent(id)
}

func (s *Store) UpdateEvent(e Event) (Event, error) {
	// reminded is persisted too: callers clear it when the start time moves so a
	// rescheduled event re-arms its reminder.
	if _, err := s.db.Exec(`UPDATE events SET title=?, notes=?, location=?, attendees=?, starts_at=?, ends_at=?, all_day=?, color=?, recurrence=?, recurrence_until=?, countdown=?, important=?, reminded=?, updated_at=datetime('now') WHERE id=?`,
		e.Title, e.Notes, e.Location, e.Attendees, e.StartsAt, e.EndsAt, b2i(e.AllDay), e.Color, normRecurrence(e.Recurrence), e.RecurrenceUntil, b2i(e.Countdown), b2i(e.Important), b2i(e.Reminded), e.ID); err != nil {
		return Event{}, err
	}
	return s.getEvent(e.ID)
}

func (s *Store) DeleteEvent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE id=?`, id)
	return err
}

// EventsStartingBetween returns timed (non all-day, non-recurring) events not yet
// reminded whose start falls within [startInclusive, endInclusive]. Used by the
// reminder scheduler. Recurring events are excluded because the one-shot reminded
// flag doesn't fit a repeating series (they're typically all-day anyway).
func (s *Store) EventsStartingBetween(startInclusive, endInclusive string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT `+eventCols+` FROM events WHERE all_day=0 AND reminded=0 AND (recurrence='none' OR recurrence='') AND starts_at >= ? AND starts_at <= ? ORDER BY starts_at`,
		startInclusive, endInclusive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// MarkReminded flags an event so the scheduler won't notify about it twice.
func (s *Store) MarkReminded(id int64) error {
	_, err := s.db.Exec(`UPDATE events SET reminded=1 WHERE id=?`, id)
	return err
}

// CountdownEvents returns the base rows flagged as countdowns (any recurrence).
// The caller computes each one's next occurrence with NextOccurrenceAfter.
func (s *Store) CountdownEvents() ([]Event, error) {
	rows, err := s.db.Query(`SELECT ` + eventCols + ` FROM events WHERE countdown=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// --- recurrence engine ---

func normRecurrence(r string) string {
	switch r {
	case "daily", "weekly", "monthly", "yearly":
		return r
	default:
		return "none"
	}
}

// addRecurrence advances t by n periods of the given recurrence. Calendar-aware
// via AddDate (so yearly birthdays and monthly events land on the right date).
func addRecurrence(t time.Time, rec string, n int) time.Time {
	switch rec {
	case "daily":
		return t.AddDate(0, 0, n)
	case "weekly":
		return t.AddDate(0, 0, 7*n)
	case "monthly":
		return t.AddDate(0, n, 0)
	case "yearly":
		return t.AddDate(n, 0, 0)
	default:
		return t
	}
}

// startIndex estimates the occurrence index whose start is at or just before
// winStart, so expansion doesn't iterate from the epoch for old base dates.
func startIndex(base, winStart time.Time, rec string) int {
	var n int
	switch rec {
	case "daily":
		n = int(winStart.Sub(base).Hours()/24) - 1
	case "weekly":
		n = int(winStart.Sub(base).Hours()/(24*7)) - 1
	case "monthly":
		n = (winStart.Year()-base.Year())*12 + int(winStart.Month()) - int(base.Month()) - 1
	case "yearly":
		n = winStart.Year() - base.Year() - 1
	}
	if n < 0 {
		return 0
	}
	return n
}

const maxOccurrences = 3000 // safety cap for a single series expansion

// expandInWindow returns the concrete occurrences of a recurring event whose
// [start,end] overlaps [winStart, winEnd]. Each occurrence keeps the series id
// and fields, with StartsAt/EndsAt shifted; times are UTC RFC3339.
func expandInWindow(base Event, winStart, winEnd time.Time) []Event {
	rec := normRecurrence(base.Recurrence)
	if rec == "none" {
		return nil
	}
	bs, err1 := time.Parse(time.RFC3339, base.StartsAt)
	be, err2 := time.Parse(time.RFC3339, base.EndsAt)
	if err1 != nil {
		return nil
	}
	if err2 != nil {
		be = bs
	}
	dur := be.Sub(bs)
	if dur < 0 {
		dur = 0
	}
	var until time.Time
	hasUntil := false
	if base.RecurrenceUntil != "" {
		if u, err := time.Parse(time.RFC3339, base.RecurrenceUntil); err == nil {
			until, hasUntil = u, true
		}
	}

	var out []Event
	start := startIndex(bs, winStart, rec)
	for i := 0; i < maxOccurrences; i++ {
		n := start + i
		occStart := addRecurrence(bs, rec, n)
		if occStart.After(winEnd) {
			break
		}
		if hasUntil && occStart.After(until) {
			break
		}
		occEnd := occStart.Add(dur)
		if !occEnd.Before(winStart) { // overlaps the window
			e := base
			e.StartsAt = occStart.UTC().Format(time.RFC3339)
			e.EndsAt = occEnd.UTC().Format(time.RFC3339)
			out = append(out, e)
		}
	}
	return out
}

// NextOccurrenceAfter returns the event's next occurrence starting at or after
// `after`. For non-recurring events that's the event itself if it's still ahead.
// ok is false when there is no future occurrence (past one-off, or past the
// recurrence-until bound).
func NextOccurrenceAfter(base Event, after time.Time) (Event, bool) {
	bs, err := time.Parse(time.RFC3339, base.StartsAt)
	if err != nil {
		return Event{}, false
	}
	be, err := time.Parse(time.RFC3339, base.EndsAt)
	if err != nil {
		be = bs
	}
	dur := be.Sub(bs)
	rec := normRecurrence(base.Recurrence)

	if rec == "none" {
		if bs.Before(after) {
			return Event{}, false
		}
		return base, true
	}

	var until time.Time
	hasUntil := false
	if base.RecurrenceUntil != "" {
		if u, err := time.Parse(time.RFC3339, base.RecurrenceUntil); err == nil {
			until, hasUntil = u, true
		}
	}
	start := startIndex(bs, after, rec)
	for i := 0; i < maxOccurrences; i++ {
		occStart := addRecurrence(bs, rec, start+i)
		if occStart.Before(after) {
			continue
		}
		if hasUntil && occStart.After(until) {
			return Event{}, false
		}
		e := base
		e.StartsAt = occStart.UTC().Format(time.RFC3339)
		e.EndsAt = occStart.Add(dur).UTC().Format(time.RFC3339)
		return e, true
	}
	return Event{}, false
}

// parseWindow resolves the [from,to] query strings into a bounded time window,
// defaulting to [-1y, +2y] around now when a bound is missing so recurring
// expansion always terminates.
func parseWindow(from, to string) (time.Time, time.Time) {
	now := time.Now().UTC()
	winStart := now.AddDate(-1, 0, 0)
	winEnd := now.AddDate(2, 0, 0)
	if from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			winStart = t
		}
	}
	if to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			winEnd = t
		}
	}
	return winStart, winEnd
}

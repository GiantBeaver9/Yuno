package telegram

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/stats"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/weather"
)

// DigestOptions tune the daily digest. Populated from config in main.
type DigestOptions struct {
	AgingDays    int     // open todos older than this are "aging"
	DiskAlertPct float64 // flag disk usage at/above this percent
	TempAlertC   float64 // flag CPU temp at/above this many °C
	WeatherUnit  string  // "celsius" or "fahrenheit"
	LLMNarrate   bool    // route the assembled digest through the LLM hook
}

// ConfigureDigest sets the digest tuning options (called once at startup).
func (b *Bot) ConfigureDigest(o DigestOptions) { b.digest = o }

// RunDailyDigest fires the per-subscriber digest at hour:min local time daily.
func (b *Bot) RunDailyDigest(ctx context.Context, hour, min int) {
	for {
		next := nextDailyTime(time.Now(), hour, min)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			b.sendDigests(ctx)
		}
	}
}

// nextDailyTime returns the next occurrence of hour:min at or after now.
func nextDailyTime(now time.Time, hour, min int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// sendDigests builds the shared digest once, then sends a per-user copy (with
// that user's weather) to every opted-in, allow-listed subscriber.
func (b *Bot) sendDigests(ctx context.Context) {
	subs, err := b.store.DigestSubscribers()
	if err != nil {
		log.Printf("telegram: digest subscribers: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	data := b.buildShared()
	for _, chatID := range subs {
		if !b.allowedChat(chatID) {
			continue // dropped from the allowlist since subscribing
		}
		d := data
		d.Weather = b.weatherLines(ctx, chatID)
		d.Goals = b.openGoals(chatID)
		if b.digest.LLMNarrate {
			if narrated, ok := b.narrateDigest(ctx, d); ok {
				_ = b.Send(chatID, narrated)
				continue
			}
		}
		_ = b.Send(chatID, renderDigest(d))
	}
}

// allowedChat reports whether a chat may receive messages (empty allowlist =
// discovery mode, nobody gets the digest).
func (b *Bot) allowedChat(chatID int64) bool {
	return len(b.allowed) > 0 && b.allowed[chatID]
}

// Countdown is one flagged event with its next occurrence.
type Countdown struct {
	Title string
	Days  int
	When  time.Time
}

// DigestData is the fully-assembled digest, ready to render deterministically or
// hand to the LLM hook.
type DigestData struct {
	Date          time.Time
	Today         []db.Event
	ImportantSoon []db.Event
	Countdowns    []Countdown
	Todos         []db.Todo // open, non-aging
	AgingTodos    []db.Todo // open, older than AgingDays
	Alerts        []string  // health threshold breaches
	Weather       []string  // per-user, rendered lines (set per subscriber)
	Goals         []db.Goal // per-user open goals (set per subscriber)
}

// openGoals returns a user's not-yet-done goals for the digest.
func (b *Bot) openGoals(chatID int64) []db.Goal {
	all, err := b.store.ListGoals(chatID)
	if err != nil {
		return nil
	}
	var open []db.Goal
	for _, g := range all {
		if !g.Done {
			open = append(open, g)
		}
	}
	return open
}

// buildShared assembles everything common to all subscribers (weather is added
// per user afterwards). Deterministic, no LLM.
func (b *Bot) buildShared() DigestData {
	now := time.Now()
	loc := now.Location()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	todayEnd := todayStart.Add(24 * time.Hour)
	d := DigestData{Date: now}

	// Today's events (recurring expanded by ListEvents).
	if evs, err := b.store.ListEvents(todayStart.UTC().Format(time.RFC3339), todayEnd.UTC().Format(time.RFC3339)); err == nil {
		d.Today = evs
	}

	// Important events across the next 14 days (excluding today).
	if evs, err := b.store.ListEvents(todayEnd.UTC().Format(time.RFC3339), todayStart.AddDate(0, 0, 14).UTC().Format(time.RFC3339)); err == nil {
		for _, e := range evs {
			if e.Important {
				d.ImportantSoon = append(d.ImportantSoon, e)
			}
		}
	}

	// Countdowns: next occurrence of each flagged event within ~400 days.
	if bases, err := b.store.CountdownEvents(); err == nil {
		horizon := todayStart.AddDate(0, 0, 400)
		for _, base := range bases {
			next, ok := db.NextOccurrenceAfter(base, todayStart)
			if !ok {
				continue
			}
			ns, err := time.Parse(time.RFC3339, next.StartsAt)
			if err != nil || ns.After(horizon) {
				continue
			}
			nsLocal := ns.In(loc)
			day := time.Date(nsLocal.Year(), nsLocal.Month(), nsLocal.Day(), 0, 0, 0, 0, loc)
			days := int(day.Sub(todayStart).Hours()/24 + 0.5)
			d.Countdowns = append(d.Countdowns, Countdown{Title: base.Title, Days: days, When: nsLocal})
		}
		sort.Slice(d.Countdowns, func(i, j int) bool { return d.Countdowns[i].Days < d.Countdowns[j].Days })
	}

	// Todos: split open into aging vs the rest.
	if todos, err := b.store.ListTodos(); err == nil {
		agingCut := now.AddDate(0, 0, -b.digest.AgingDays)
		for _, t := range todos {
			if t.Done {
				continue
			}
			created, err := time.Parse("2006-01-02 15:04:05", t.CreatedAt)
			if b.digest.AgingDays > 0 && err == nil && created.Before(agingCut) {
				d.AgingTodos = append(d.AgingTodos, t)
			} else {
				d.Todos = append(d.Todos, t)
			}
		}
	}

	// Health alerts: only when a threshold is crossed.
	if s, err := stats.Collect(); err == nil {
		if b.digest.DiskAlertPct > 0 && s.DiskTotal > 0 {
			pct := float64(s.DiskUsed) / float64(s.DiskTotal) * 100
			if pct >= b.digest.DiskAlertPct {
				d.Alerts = append(d.Alerts, fmt.Sprintf("Disk %.0f%% full (%s/%s)", pct, fmtGB(s.DiskUsed), fmtGB(s.DiskTotal)))
			}
		}
		if b.digest.TempAlertC > 0 && s.TempC != nil && *s.TempC >= b.digest.TempAlertC {
			d.Alerts = append(d.Alerts, fmt.Sprintf("CPU temperature %.0f°C", *s.TempC))
		}
	}

	return d
}

// weatherLines fetches a rendered weather line per saved location for a chat.
// Failures (offline, bad coord) are skipped so the digest still sends.
func (b *Bot) weatherLines(ctx context.Context, chatID int64) []string {
	locs, err := b.store.UserLocations(chatID)
	if err != nil || len(locs) == 0 {
		return nil
	}
	var lines []string
	for _, l := range locs {
		r, err := weather.Fetch(ctx, l.Lat, l.Lon, b.digest.WeatherUnit)
		if err != nil {
			log.Printf("telegram: weather %q for chat %d: %v", l.Label, chatID, err)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s — %s, now %.0f%s (H %.0f / L %.0f)",
			l.Label, weather.Describe(r.Code), r.Now, r.Unit, r.Max, r.Min))
	}
	return lines
}

// renderDigest turns assembled data into the plain-text message.
func renderDigest(d DigestData) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "☀️ Good morning — %s\n", d.Date.Format("Monday, 2 January 2006"))

	sb.WriteString("\n📅 Today\n")
	if len(d.Today) == 0 {
		sb.WriteString("• Nothing scheduled.\n")
	} else {
		for _, e := range d.Today {
			sb.WriteString("• " + eventLine(e) + "\n")
		}
	}

	if len(d.ImportantSoon) > 0 {
		sb.WriteString("\n❗ Important — next 2 weeks\n")
		for _, e := range d.ImportantSoon {
			when := ""
			if t, err := time.Parse(time.RFC3339, e.StartsAt); err == nil {
				when = t.Local().Format("Mon 2 Jan") + " — "
			}
			sb.WriteString("• " + when + e.Title + "\n")
		}
	}

	if len(d.Countdowns) > 0 {
		sb.WriteString("\n⏳ Countdowns\n")
		for _, c := range d.Countdowns {
			sb.WriteString("• " + countdownLine(c) + "\n")
		}
	}

	if len(d.Todos) > 0 {
		fmt.Fprintf(&sb, "\n✅ Todos (%d)\n", len(d.Todos))
		for _, t := range d.Todos {
			sb.WriteString("• " + t.Text + "\n")
		}
	}
	if len(d.AgingTodos) > 0 {
		fmt.Fprintf(&sb, "\n⏳ Aging todos (%d)\n", len(d.AgingTodos))
		for _, t := range d.AgingTodos {
			sb.WriteString("• " + t.Text + "\n")
		}
	}

	if len(d.Goals) > 0 {
		sb.WriteString("\n🎯 Goals\n")
		for _, g := range d.Goals {
			line := g.Title
			if g.TargetDate != "" {
				if t, err := time.Parse(time.RFC3339, g.TargetDate); err == nil {
					line += " — by " + t.Local().Format("2 Jan")
				}
			}
			sb.WriteString("• " + line + "\n")
		}
	}

	if len(d.Weather) > 0 {
		sb.WriteString("\n🌦 Weather\n")
		for _, w := range d.Weather {
			sb.WriteString("• " + w + "\n")
		}
	}

	if len(d.Alerts) > 0 {
		sb.WriteString("\n⚠️ Alerts\n")
		for _, a := range d.Alerts {
			sb.WriteString("• " + a + "\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// eventLine renders a single event for the Today section (important marked ❗).
func eventLine(e db.Event) string {
	var b strings.Builder
	if e.Important {
		b.WriteString("❗ ")
	}
	if e.AllDay {
		b.WriteString("all day — ")
	} else if t, err := time.Parse(time.RFC3339, e.StartsAt); err == nil {
		b.WriteString(t.Local().Format("15:04") + " — ")
	}
	b.WriteString(e.Title)
	if e.Location != "" {
		b.WriteString(" @ " + e.Location)
	}
	if e.Attendees != "" {
		b.WriteString(" (" + e.Attendees + ")")
	}
	return b.String()
}

func countdownLine(c Countdown) string {
	when := c.When.Format("Mon 2 Jan")
	switch {
	case c.Days == 0:
		return fmt.Sprintf("%s — today! (%s)", c.Title, when)
	case c.Days == 1:
		return fmt.Sprintf("%s — tomorrow (%s)", c.Title, when)
	default:
		return fmt.Sprintf("%s — in %d days (%s)", c.Title, c.Days, when)
	}
}

func fmtGB(b uint64) string { return fmt.Sprintf("%.0fGB", float64(b)/(1<<30)) }

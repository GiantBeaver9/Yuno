package telegram

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
)

func TestNextDailyTime(t *testing.T) {
	// Target still ahead today -> fires today.
	base := time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC)
	if got, want := nextDailyTime(base, 6, 0), time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("ahead-today: got %v, want %v", got, want)
	}
	// Target already passed today -> rolls to tomorrow.
	base = time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)
	if got, want := nextDailyTime(base, 6, 0), time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("passed-today: got %v, want %v", got, want)
	}
	// Exactly now counts as passed (fires next day, never double-fires).
	base = time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC)
	if got, want := nextDailyTime(base, 6, 0), time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("exactly-now: got %v, want %v", got, want)
	}
}

func TestBuildDigest(t *testing.T) {
	st, err := db.Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	if _, err := st.CreateEvent(db.Event{
		Title:    "Team standup",
		Location: "Zoom",
		StartsAt: start.UTC().Format(time.RFC3339),
		EndsAt:   start.Add(30 * time.Minute).UTC().Format(time.RFC3339),
		Color:    "#5b9dff",
	}); err != nil {
		t.Fatal(err)
	}

	b := &Bot{store: st, digest: DigestOptions{AgingDays: 7, DiskAlertPct: 90, TempAlertC: 75, WeatherUnit: "celsius"}}
	out := renderDigest(b.buildShared())
	for _, want := range []string{"Good morning", "Today", "Team standup", "Zoom"} {
		if !strings.Contains(out, want) {
			t.Errorf("digest missing %q; got:\n%s", want, out)
		}
	}
}

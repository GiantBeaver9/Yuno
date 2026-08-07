package telegram

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// RunReminders polls the store every 60s (plus an immediate first pass) for
// timed events starting within `lead` and pushes a one-time reminder for each,
// marking it reminded so it never fires twice. Runs until ctx is cancelled.
func (b *Bot) RunReminders(ctx context.Context, lead time.Duration) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	b.checkReminders(lead) // fire an immediate pass so a just-due event isn't missed
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkReminders(lead)
		}
	}
}

// checkReminders finds and pushes reminders for events coming due within lead.
func (b *Bot) checkReminders(lead time.Duration) {
	now := time.Now().UTC()
	windowEnd := now.Add(lead)

	events, err := b.store.EventsStartingBetween(now.Format(time.RFC3339), windowEnd.Format(time.RFC3339))
	if err != nil {
		log.Printf("telegram: reminder query: %v", err)
		return
	}

	for _, ev := range events {
		start, perr := time.Parse(time.RFC3339, ev.StartsAt)
		if perr != nil {
			log.Printf("telegram: bad event time %q: %v", ev.StartsAt, perr)
			continue
		}
		mins := int(time.Until(start).Round(time.Minute).Minutes())

		var msg strings.Builder
		fmt.Fprintf(&msg, "⏰ %s starts at %s (in ~%d min).",
			ev.Title, start.Local().Format("Mon 15:04"), mins)
		if ev.Location != "" {
			fmt.Fprintf(&msg, "\n📍 %s", ev.Location)
		}
		if ev.Attendees != "" {
			fmt.Fprintf(&msg, "\n👥 %s", ev.Attendees)
		}

		b.Broadcast(msg.String())
		if err := b.store.MarkReminded(ev.ID); err != nil {
			log.Printf("telegram: mark reminded %d: %v", ev.ID, err)
		}
	}
}

package db

import "time"

// seed populates first-run example data so a fresh database isn't empty. It is
// a no-op once any tiles exist. The seeded todos double as onboarding tips for
// enabling the optional LLM assistant and Telegram bot.
func (s *Store) seed() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tiles`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	tiles := []Tile{
		{Title: "Pi-hole", URL: "http://pi.hole/admin", Icon: "🛡️", Description: "Network ad-blocking", Position: 1},
		{Title: "Router", URL: "http://192.168.1.1", Icon: "📡", Description: "Home router admin", Position: 2},
	}
	for _, t := range tiles {
		if _, err := s.CreateTile(t); err != nil {
			return err
		}
	}

	// Onboarding tips captured as todos (see the README).
	todos := []string{
		"Set LLM_BASE_URL (LM Studio or Ollama) to enable the on-device assistant",
		"Set TELEGRAM_BOT_TOKEN to chat with the assistant and get event reminders from your phone",
	}
	for i, txt := range todos {
		if _, err := s.CreateTodo(Todo{Text: txt, Position: i + 1}); err != nil {
			return err
		}
	}

	if _, err := s.CreateNote(Note{Body: "Welcome to your Pi home server dashboard. Add service tiles, jot notes here, and track tasks in the todo list."}); err != nil {
		return err
	}

	// One example calendar event a couple of days out, on the next round hour.
	start := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Hour)
	end := start.Add(time.Hour)
	_, err := s.CreateEvent(Event{
		Title:    "Example: server maintenance window",
		Notes:    "Delete me — this is a seeded example event.",
		StartsAt: start.Format(time.RFC3339),
		EndsAt:   end.Format(time.RFC3339),
		Color:    "#5b9dff",
	})
	return err
}

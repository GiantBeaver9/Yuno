package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// cmdAsk answers a one-shot question using LM Studio's native MCP tools.
func (b *Bot) cmdAsk(ctx context.Context, chatID int64, query string) {
	if b.mcp == nil {
		_ = b.Send(chatID, "MCP isn't configured. Set MCP_SERVERS (and LLM_API_TOKEN) on the server to enable /ask.")
		return
	}
	if query == "" {
		_ = b.Send(chatID, "Usage: /ask <question> — answered using your LM Studio MCP tools.")
		return
	}
	_, _ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})
	reply, err := b.mcp.Ask(ctx, query)
	if err != nil {
		log.Printf("telegram: /ask for chat %d: %v", chatID, err)
		_ = b.Send(chatID, "Sorry — the MCP request failed.")
		return
	}
	_ = b.Send(chatID, reply)
}

// cmdDigest toggles or reports a chat's daily-digest subscription.
func (b *Bot) cmdDigest(chatID int64, args []string) {
	if len(args) == 0 {
		on, _ := b.store.DigestEnabled(chatID)
		state := "off"
		if on {
			state = "on"
		}
		_ = b.Send(chatID, fmt.Sprintf("Daily digest is %s. Use /digest on or /digest off.", state))
		return
	}
	switch strings.ToLower(args[0]) {
	case "on":
		if err := b.store.SetDigest(chatID, true); err != nil {
			_ = b.Send(chatID, "Couldn't save that, sorry.")
			return
		}
		_ = b.Send(chatID, "Daily digest is ON — you'll get it each morning. Add weather with /weather add.")
	case "off":
		if err := b.store.SetDigest(chatID, false); err != nil {
			_ = b.Send(chatID, "Couldn't save that, sorry.")
			return
		}
		_ = b.Send(chatID, "Daily digest is OFF.")
	default:
		_ = b.Send(chatID, "Usage: /digest on | off")
	}
}

// cmdWeather manages a chat's saved weather locations.
func (b *Bot) cmdWeather(chatID int64, args []string) {
	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		locs, err := b.store.UserLocations(chatID)
		if err != nil {
			_ = b.Send(chatID, "Couldn't read your locations, sorry.")
			return
		}
		if len(locs) == 0 {
			_ = b.Send(chatID, "No weather locations saved. Add one:\n/weather add <label> <lat> <lon>\ne.g. /weather add Home 51.51 -0.13")
			return
		}
		var sb strings.Builder
		sb.WriteString("Your weather locations:\n")
		for _, l := range locs {
			fmt.Fprintf(&sb, "• %s (%.4f, %.4f)\n", l.Label, l.Lat, l.Lon)
		}
		sb.WriteString("\nRemove one with /weather remove <label>")
		_ = b.Send(chatID, strings.TrimRight(sb.String(), "\n"))
		return
	}

	switch strings.ToLower(args[0]) {
	case "add":
		// label may contain spaces, so take the last two tokens as lat/lon.
		rest := args[1:]
		if len(rest) < 3 {
			_ = b.Send(chatID, "Usage: /weather add <label> <lat> <lon>\ne.g. /weather add New York 40.71 -74.01")
			return
		}
		lat, err1 := strconv.ParseFloat(rest[len(rest)-2], 64)
		lon, err2 := strconv.ParseFloat(rest[len(rest)-1], 64)
		label := strings.TrimSpace(strings.Join(rest[:len(rest)-2], " "))
		if err1 != nil || err2 != nil || label == "" {
			_ = b.Send(chatID, "Couldn't parse that. Usage: /weather add <label> <lat> <lon>")
			return
		}
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			_ = b.Send(chatID, "Latitude must be between -90 and 90, longitude -180 and 180.")
			return
		}
		if _, err := b.store.AddUserLocation(chatID, label, lat, lon); err != nil {
			_ = b.Send(chatID, "Couldn't save that, sorry.")
			return
		}
		_ = b.Send(chatID, fmt.Sprintf("Added %s (%.4f, %.4f). It'll appear in your daily digest.", label, lat, lon))
	case "remove", "rm", "delete":
		if len(args) < 2 {
			_ = b.Send(chatID, "Usage: /weather remove <label>")
			return
		}
		label := strings.TrimSpace(strings.Join(args[1:], " "))
		ok, err := b.store.RemoveUserLocation(chatID, label)
		if err != nil {
			_ = b.Send(chatID, "Couldn't remove that, sorry.")
			return
		}
		if !ok {
			_ = b.Send(chatID, "No location called "+label+".")
			return
		}
		_ = b.Send(chatID, "Removed "+label+".")
	default:
		_ = b.Send(chatID, "Usage: /weather [list] | add <label> <lat> <lon> | remove <label>")
	}
}

// cmdProjects lists a user's tracked projects (read-only; richer management is
// via natural language, e.g. "log an update on project 2: 60% done, 1 bug fixed").
func (b *Bot) cmdProjects(chatID int64) {
	ps, err := b.store.ListProjects(chatID)
	if err != nil {
		_ = b.Send(chatID, "Couldn't read your projects, sorry.")
		return
	}
	if len(ps) == 0 {
		_ = b.Send(chatID, "No projects yet. Try: \"track a new project called Gauntlet capstone\".")
		return
	}
	var sb strings.Builder
	sb.WriteString("📊 Your projects\n")
	for _, p := range ps {
		fp := ""
		if p.FirstPass {
			fp = " ✓ first pass"
		}
		fmt.Fprintf(&sb, "\n#%d %s — %.0f%%%s\n", p.ID, p.Name, p.PercentComplete, fp)
		if p.Bugs > 0 {
			fmt.Fprintf(&sb, "   %d open bug(s)\n", p.Bugs)
		}
		if p.EstEngHours > 0 || p.EstAgentHours > 0 {
			fmt.Fprintf(&sb, "   ~%.0fh eng / %.0fh agent\n", p.EstEngHours, p.EstAgentHours)
		}
	}
	_ = b.Send(chatID, strings.TrimRight(sb.String(), "\n"))
}

// cmdCheckins lists a user's recent self check-ins.
func (b *Bot) cmdCheckins(chatID int64) {
	cs, err := b.store.ListCheckins(chatID, 7)
	if err != nil {
		_ = b.Send(chatID, "Couldn't read your check-ins, sorry.")
		return
	}
	if len(cs) == 0 {
		_ = b.Send(chatID, "No check-ins yet. Try: \"check in — mood 4, energy 3, focus 5, shipped the login flow\".")
		return
	}
	var sb strings.Builder
	sb.WriteString("📝 Recent check-ins\n")
	for _, c := range cs {
		fmt.Fprintf(&sb, "\n%s — mood %d, energy %d, focus %d\n", fmtStamp(c.CreatedAt), c.Mood, c.Energy, c.Focus)
		if c.Note != "" {
			fmt.Fprintf(&sb, "   %s\n", c.Note)
		}
	}
	_ = b.Send(chatID, strings.TrimRight(sb.String(), "\n"))
}

// cmdGoals lists a user's goals.
func (b *Bot) cmdGoals(chatID int64) {
	gs, err := b.store.ListGoals(chatID)
	if err != nil {
		_ = b.Send(chatID, "Couldn't read your goals, sorry.")
		return
	}
	if len(gs) == 0 {
		_ = b.Send(chatID, "No goals yet. Try: \"add a goal to finish the capstone by Aug 1\".")
		return
	}
	var sb strings.Builder
	sb.WriteString("🎯 Your goals\n")
	for _, g := range gs {
		mark := "▫️"
		if g.Done {
			mark = "✅"
		}
		line := fmt.Sprintf("%s #%d %s", mark, g.ID, g.Title)
		if g.TargetDate != "" {
			if t, err := time.Parse(time.RFC3339, g.TargetDate); err == nil {
				line += " (by " + t.Local().Format("2 Jan") + ")"
			}
		}
		sb.WriteString(line + "\n")
	}
	_ = b.Send(chatID, strings.TrimRight(sb.String(), "\n"))
}

// fmtStamp renders a stored SQLite datetime ("2006-01-02 15:04:05" UTC) as a
// friendly local date, falling back to the raw value.
func fmtStamp(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Local().Format("Mon 2 Jan 15:04")
	}
	return s
}

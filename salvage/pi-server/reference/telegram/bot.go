// Package telegram implements a minimal Telegram Bot API client (no external
// library) that fronts the local LLM agent. It lets the user chat with their
// Pi's assistant — and act on the calendar/todos — from their phone over the
// internet, and pushes event reminders via long-polling + a reminder scheduler.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/llm"
)

// maxHistory caps how many messages we retain per chat so context stays bounded
// (whole tool-call/result groups are kept naturally by trimming from the front).
const maxHistory = 20

// telegramMax is Telegram's hard per-message limit; we chunk below it.
const telegramMax = 4000

// Bot is a long-polling Telegram client bound to one agent + store.
type Bot struct {
	token   string
	allowed map[int64]bool
	agent   *llm.Agent
	store   *db.Store
	http    *http.Client

	digest DigestOptions
	mcp    *llm.MCPClient // optional: powers /ask via LM Studio native MCP

	mu   sync.Mutex
	hist map[int64][]llm.Message
}

// ConfigureMCP enables the /ask command against LM Studio's native MCP endpoint.
func (b *Bot) ConfigureMCP(c *llm.MCPClient) { b.mcp = c }

// New builds a Bot. allowedIDs whitelists chat ids; an empty list puts the bot
// in "discovery mode" where it only reveals a caller's chat id.
func New(token string, allowedIDs []int64, agent *llm.Agent, store *db.Store) *Bot {
	allowed := make(map[int64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	return &Bot{
		token:   token,
		allowed: allowed,
		agent:   agent,
		store:   store,
		http:    &http.Client{Timeout: 65 * time.Second}, // > long-poll timeout
		hist:    make(map[int64][]llm.Message),
	}
}

// --- Telegram wire types (subset) ---

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

// Run long-polls getUpdates and handles messages until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram: getUpdates: %v", err)
			// Back off so a persistent failure doesn't hot-spin.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1 // ack: next poll starts after this update
			if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
				continue
			}
			b.handleMessage(ctx, u.Message)
		}
	}
}

// getUpdates issues one long-poll (timeout=30) starting at offset.
func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	payload := map[string]any{"timeout": 30, "offset": offset}
	raw, err := b.call(ctx, "getUpdates", payload)
	if err != nil {
		return nil, err
	}
	var updates []tgUpdate
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// handleMessage applies the whitelist, handles commands, and otherwise runs the
// agent over the chat's history.
func (b *Bot) handleMessage(ctx context.Context, m *tgMessage) {
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)

	// Discovery mode: no whitelist configured — reveal the chat id and stop.
	if len(b.allowed) == 0 {
		_ = b.Send(chatID, fmt.Sprintf(
			"Your Telegram chat ID is %d. Add it to TELEGRAM_ALLOWED_CHAT_IDS (comma-separated) and restart to enable this bot.",
			chatID))
		return
	}
	// Enforce the whitelist silently for everyone else.
	if !b.allowed[chatID] {
		log.Printf("telegram: ignoring message from unlisted chat %d", chatID)
		return
	}

	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		switch fields[0] {
		case "/start", "/help":
			_ = b.Send(chatID, helpText)
		case "/clear":
			b.mu.Lock()
			delete(b.hist, chatID)
			b.mu.Unlock()
			_ = b.Send(chatID, "Context cleared.")
		case "/digest":
			b.cmdDigest(chatID, fields[1:])
		case "/weather":
			b.cmdWeather(chatID, fields[1:])
		case "/projects":
			b.cmdProjects(chatID)
		case "/checkins":
			b.cmdCheckins(chatID)
		case "/goals":
			b.cmdGoals(chatID)
		case "/ask":
			b.cmdAsk(ctx, chatID, strings.TrimSpace(strings.TrimPrefix(text, "/ask")))
		default:
			_ = b.Send(chatID, "Unknown command. Try /help.")
		}
		return
	}

	// Show a typing indicator while the (possibly slow) local model works.
	_, _ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})

	if b.agent == nil {
		_ = b.Send(chatID, "The local LLM isn't configured on the server yet.")
		return
	}

	// Append the new user turn under the lock, snapshot the history to run on.
	b.mu.Lock()
	b.hist[chatID] = append(b.hist[chatID], llm.Message{Role: "user", Content: text})
	convo := append([]llm.Message(nil), b.hist[chatID]...)
	b.mu.Unlock()

	reply, appended, err := b.agent.Run(ctx, convo, chatID)
	if err != nil {
		log.Printf("telegram: agent run for chat %d: %v", chatID, err)
		_ = b.Send(chatID, "Sorry — I hit an error talking to the local model.")
		return
	}

	// Persist what the turn generated and trim to the last maxHistory entries.
	b.mu.Lock()
	b.hist[chatID] = append(b.hist[chatID], appended...)
	if n := len(b.hist[chatID]); n > maxHistory {
		b.hist[chatID] = append([]llm.Message(nil), b.hist[chatID][n-maxHistory:]...)
	}
	// Trimming from the front can slice into the middle of a tool-call group,
	// leaving the window starting on a `tool` result whose assistant tool_calls
	// parent was dropped — which strict OpenAI-compatible servers reject. Drop
	// any such orphaned leading tool messages. (The tail is always a plain
	// assistant message, so an assistant tool_calls turn never loses its results.)
	for len(b.hist[chatID]) > 0 && b.hist[chatID][0].Role == "tool" {
		b.hist[chatID] = b.hist[chatID][1:]
	}
	b.mu.Unlock()

	if strings.TrimSpace(reply) == "" {
		reply = "(no reply)"
	}
	_ = b.Send(chatID, reply)
}

// Send delivers text to a chat, splitting into <=telegramMax chunks. Plain text
// (no parse_mode) sidesteps Telegram's markdown-escaping pitfalls.
func (b *Bot) Send(chatID int64, text string) error {
	for _, chunk := range splitMessage(text, telegramMax) {
		payload := map[string]any{"chat_id": chatID, "text": chunk}
		if _, err := b.call(context.Background(), "sendMessage", payload); err != nil {
			return err
		}
	}
	return nil
}

// Broadcast sends text to every whitelisted chat (used by the reminder push).
func (b *Bot) Broadcast(text string) {
	for id := range b.allowed {
		if err := b.Send(id, text); err != nil {
			log.Printf("telegram: broadcast to %d failed: %v", id, err)
		}
	}
}

// call performs one Bot API method POST and returns its `result` payload.
func (b *Bot) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %s", method, b.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		// A transport error is a *url.Error whose text embeds the request URL —
		// which contains the bot token. Redact before it can reach a log.
		return nil, fmt.Errorf("telegram %s: %s", method, b.redact(err.Error()))
	}
	defer resp.Body.Close()

	var tr tgResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	if !tr.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, tr.Description)
	}
	return tr.Result, nil
}

// redact strips the bot token from a string so it can't leak into logs.
func (b *Bot) redact(s string) string {
	if b.token == "" {
		return s
	}
	return strings.ReplaceAll(s, b.token, "***")
}

// splitMessage breaks s into chunks no longer than max runes, preferring to
// break on a newline near the limit for readability.
func splitMessage(s string, max int) []string {
	if s == "" {
		return []string{""}
	}
	var chunks []string
	runes := []rune(s)
	for len(runes) > max {
		cut := max
		// Prefer a newline break within the last quarter of the window.
		if idx := lastIndexRune(runes[:max], '\n'); idx > max*3/4 {
			cut = idx + 1
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	chunks = append(chunks, string(runes))
	return chunks
}

func lastIndexRune(rs []rune, target rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == target {
			return i
		}
	}
	return -1
}

const helpText = "Hi! I'm your home-server assistant.\n\n" +
	"You can chat with me in plain language and I can act on your intranet:\n" +
	"• Calendar: \"add dentist Thursday 3pm\", \"what's on this weekend?\", \"move the meeting to 4pm\"\n" +
	"• Todos: \"add milk to my todos\", \"what's on my list?\", \"mark the laundry done\"\n" +
	"• Projects: \"track a project called Gauntlet capstone\", \"log an update on project 2: 60% done, fixed 1 bug\"\n" +
	"• Check-ins: \"check in — mood 4, energy 3, focus 5, shipped the login flow\"\n" +
	"• Goals: \"add a goal to finish the capstone by Aug 1\"\n" +
	"• Server: \"how's the server doing?\" for CPU, memory, disk and temperature\n\n" +
	"Commands:\n" +
	"/ask <question> — answer using your LM Studio MCP tools\n" +
	"/projects · /checkins · /goals — list yours\n" +
	"/digest on|off — daily morning digest (events, todos, countdowns, weather)\n" +
	"/weather — list your weather locations\n" +
	"/weather add <label> <lat> <lon> — save a location (e.g. /weather add Home 51.51 -0.13)\n" +
	"/weather remove <label> — delete a location\n" +
	"/clear — reset our conversation context\n" +
	"/help — show this message"

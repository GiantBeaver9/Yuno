// Package config centralizes runtime configuration. Values come from environment
// variables; for local dev a .env file is loaded automatically (in production
// the systemd unit supplies them via EnvironmentFile).
package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port   string
	DBPath string
	// LLMBaseURL is the OpenAI-compatible base URL of the local LLM (e.g. LM
	// Studio at http://localhost:1234/v1). Empty disables the LLM proxy.
	LLMBaseURL string
	// LLMModel is passed to the tool-calling agent. Empty lets the server use
	// whatever model is currently loaded (LM Studio behaviour).
	LLMModel string
	// MCP via LM Studio's native /api/v1/chat (0.4.0+). MCPServers are mcp.json
	// server ids (e.g. "mcp/playwright"); empty disables the bot's /ask command.
	MCPServers   []string
	MCPNativeURL string // LM Studio host base; derived from LLMBaseURL if unset
	LLMAPIToken  string // Bearer token for the native endpoint
	// AppPassword enables single-password cookie auth. Empty disables auth
	// (VPN-only mode).
	AppPassword string
	// TelegramBotToken enables the Telegram bot. Empty disables it.
	TelegramBotToken string
	// TelegramAllowedChatIDs whitelists chat ids allowed to use the bot. Empty
	// puts the bot in discovery mode (it only reveals a caller's chat id).
	TelegramAllowedChatIDs []int64
	// ReminderLeadMinutes is how far ahead of an event's start to push its
	// reminder. Defaults to 30.
	ReminderLeadMinutes int
	// DigestEnabled/DigestHour/DigestMinute schedule a daily Telegram digest at a
	// local wall-clock time. Set DIGEST_TIME=HH:MM to enable; empty disables it.
	DigestEnabled bool
	DigestHour    int
	DigestMinute  int
	// Digest tuning.
	DigestTodoAgingDays int     // open todos older than this are flagged "aging"
	DiskAlertPercent    float64 // digest flags disk usage at/above this percent
	TempAlertC          float64 // digest flags CPU temp at/above this many °C
	WeatherUnit         string  // "celsius" or "fahrenheit"
	DigestLLM           bool    // route the digest through the LLM narration hook
}

// Load reads configuration, applying a best-effort .env load first.
func Load() Config {
	_ = godotenv.Load() // ignored if no .env file is present

	digestOK, digestH, digestM := parseDigestTime(os.Getenv("DIGEST_TIME"))

	baseURL := os.Getenv("LLM_BASE_URL")
	nativeURL := os.Getenv("LLM_NATIVE_URL")
	if nativeURL == "" && baseURL != "" {
		// http://host:1234/v1 -> http://host:1234 for the /api/v1/chat endpoint.
		nativeURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	}

	return Config{
		Port:                   getenv("PORT", "8080"),
		DBPath:                 getenv("DB_PATH", "./data.db"),
		LLMBaseURL:             baseURL,
		LLMModel:               os.Getenv("LLM_MODEL"),
		MCPServers:             splitCSV(os.Getenv("MCP_SERVERS")),
		MCPNativeURL:           nativeURL,
		LLMAPIToken:            os.Getenv("LLM_API_TOKEN"),
		AppPassword:            os.Getenv("APP_PASSWORD"),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAllowedChatIDs: parseChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS")),
		ReminderLeadMinutes:    getenvInt("REMINDER_LEAD_MINUTES", 30),
		DigestEnabled:          digestOK,
		DigestHour:             digestH,
		DigestMinute:           digestM,
		DigestTodoAgingDays:    getenvInt("DIGEST_TODO_AGING_DAYS", 7),
		DiskAlertPercent:       getenvFloat("DISK_ALERT_PERCENT", 90),
		TempAlertC:             getenvFloat("TEMP_ALERT_C", 75),
		WeatherUnit:            weatherUnit(os.Getenv("WEATHER_UNIT")),
		DigestLLM:              getenvBool("DIGEST_LLM", false),
	}
}

// weatherUnit normalizes the temperature-unit env to "celsius"/"fahrenheit".
func weatherUnit(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "f", "fahrenheit":
		return "fahrenheit"
	default:
		return "celsius"
	}
}

func getenvFloat(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("config: invalid %s=%q, using %g", k, v, def)
		return def
	}
	return f
}

func getenvBool(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// parseDigestTime parses "HH:MM" (24-hour, local time). Empty or invalid input
// disables the digest (ok=false).
func parseDigestTime(raw string) (ok bool, hour, min int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, 0, 0
	}
	h, m, found := strings.Cut(raw, ":")
	if !found {
		log.Printf("config: invalid DIGEST_TIME=%q, want HH:MM — digest disabled", raw)
		return false, 0, 0
	}
	hour, err1 := strconv.Atoi(strings.TrimSpace(h))
	min, err2 := strconv.Atoi(strings.TrimSpace(m))
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
		log.Printf("config: invalid DIGEST_TIME=%q, want HH:MM 00:00–23:59 — digest disabled", raw)
		return false, 0, 0
	}
	return true, hour, min
}

// parseChatIDs parses a comma-separated int64 list, skipping (and logging)
// blank or invalid entries.
func parseChatIDs(raw string) []int64 {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Printf("config: skipping invalid TELEGRAM_ALLOWED_CHAT_IDS entry %q", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// getenvInt reads an integer env var, falling back to def when unset or invalid.
func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: invalid %s=%q, using %d", k, v, def)
		return def
	}
	return n
}

// splitCSV parses a comma-separated list, trimming and dropping blanks.
func splitCSV(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

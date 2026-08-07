// Package config centralizes runtime configuration from environment variables;
// a .env file is loaded automatically for local dev. Ported from the pi-server
// config helpers, retargeted to Postgres + Yuno's needs.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port string
	// DatabaseURL is the Postgres connection string (pgx format).
	DatabaseURL string
	// SecretKey is the 32-byte (hex or base64) key used to encrypt provider keys
	// and the GitHub PAT at rest. Required for secret storage.
	SecretKey string
	// GoosePath is the path to the goose binary the runner shells out to.
	GoosePath string
	// WorkspacesDir is where per-build repo clones are checked out.
	WorkspacesDir string
	// AgentsDir is where the Factory writes agent recipe files.
	AgentsDir string
	// TelegramBotToken enables the Telegram transport. Empty disables it.
	TelegramBotToken string
	// TelegramAllowedChatIDs whitelists chat ids. Empty = discovery mode.
	TelegramAllowedChatIDs []int64
	// BuildMaxCost caps total spend per build before parking needs_human.
	BuildMaxCost float64
}

// Load reads configuration, applying a best-effort .env load first.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		Port:                   getenv("PORT", "8080"),
		DatabaseURL:            getenv("DATABASE_URL", "postgres://yuno:yuno@localhost:5432/yuno?sslmode=disable"),
		SecretKey:              os.Getenv("SECRET_KEY"),
		GoosePath:              getenv("GOOSE_PATH", "goose"),
		WorkspacesDir:          getenv("WORKSPACES_DIR", "./workspaces"),
		AgentsDir:              getenv("AGENTS_DIR", "./agents"),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAllowedChatIDs: parseChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS")),
		BuildMaxCost:           getenvFloat("BUILD_MAX_COST", 5.0),
	}
}

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

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

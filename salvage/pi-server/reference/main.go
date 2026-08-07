package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/api"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/config"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/llm"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/telegram"
	"github.com/giantbeaver9/pi-home-server-page/backend/web"
)

func main() {
	cfg := config.Load()

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	mux := http.NewServeMux()
	api.Register(mux, store, cfg.LLMBaseURL, cfg.AppPassword, cfg.LLMModel) // /api/* JSON endpoints
	web.Register(mux)                                                       // embedded Vue SPA + index.html fallback

	// bgCtx ties the Telegram bot and reminder scheduler to process lifetime so
	// they exit cleanly on shutdown.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	if cfg.TelegramBotToken != "" {
		// Share a single stateless agent with the HTTP endpoint's copy; nil when
		// the LLM is unconfigured (the bot still runs reminders and reports the
		// LLM as unavailable for chat).
		var agent *llm.Agent
		if cfg.LLMBaseURL != "" {
			agent = llm.NewAgent(cfg.LLMBaseURL, cfg.LLMModel, store)
		}
		bot := telegram.New(cfg.TelegramBotToken, cfg.TelegramAllowedChatIDs, agent, store)
		bot.ConfigureDigest(telegram.DigestOptions{
			AgingDays:    cfg.DigestTodoAgingDays,
			DiskAlertPct: cfg.DiskAlertPercent,
			TempAlertC:   cfg.TempAlertC,
			WeatherUnit:  cfg.WeatherUnit,
			LLMNarrate:   cfg.DigestLLM,
		})
		// Optional /ask command via LM Studio's native MCP endpoint.
		if len(cfg.MCPServers) > 0 && cfg.MCPNativeURL != "" {
			bot.ConfigureMCP(llm.NewMCPClient(cfg.MCPNativeURL, cfg.LLMAPIToken, cfg.LLMModel, cfg.MCPServers))
			log.Printf("telegram /ask enabled via MCP (%s, servers=%v)", cfg.MCPNativeURL, cfg.MCPServers)
		}
		go bot.Run(bgCtx)
		go bot.RunReminders(bgCtx, time.Duration(cfg.ReminderLeadMinutes)*time.Minute)

		mode := "discovery mode (no allowlist — will reveal chat ids)"
		if len(cfg.TelegramAllowedChatIDs) > 0 {
			mode = "allowlist active"
		}
		log.Printf("telegram bot started (%s, reminder lead=%dm)", mode, cfg.ReminderLeadMinutes)

		if cfg.DigestEnabled {
			go bot.RunDailyDigest(bgCtx, cfg.DigestHour, cfg.DigestMinute)
			log.Printf("daily digest scheduled for %02d:%02d local time", cfg.DigestHour, cfg.DigestMinute)
		}
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Logging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		llm := "disabled"
		if cfg.LLMBaseURL != "" {
			llm = cfg.LLMBaseURL
		}
		auth := "disabled"
		if cfg.AppPassword != "" {
			auth = "enabled"
		}
		log.Printf("pi-dashboard listening on :%s (db=%s, llm=%s, auth=%s)", cfg.Port, cfg.DBPath, llm, auth)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	bgCancel() // stop the Telegram bot and reminder goroutines

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

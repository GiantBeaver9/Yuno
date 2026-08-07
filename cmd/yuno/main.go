// Command yuno is the platform backend and composition root. It connects to
// Postgres, applies the schema, seeds the workflow templates, starts the
// orchestrator worker loop and the schedule ticker, serves the REST + SSE API
// and the embedded React SPA, and (when configured) runs the Telegram transport.
//
// Everything is a message on the bus: the REST API, Telegram, and the scheduler
// are all just producers of `message` rows; the orchestrator is the single
// consumer that drives agent turns. Real agent execution shells out to `goose`
// (set GOOSE_PATH; install goose in the runtime) — the orchestration itself is
// proven by the test suite via a fake runner.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/api"
	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/config"
	"github.com/giantbeaver9/yuno/internal/factory"
	"github.com/giantbeaver9/yuno/internal/orchestrator"
	"github.com/giantbeaver9/yuno/internal/runner"
	"github.com/giantbeaver9/yuno/internal/schedule"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/seed"
	"github.com/giantbeaver9/yuno/internal/sse"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/telegram"
	"github.com/giantbeaver9/yuno/internal/workflow"
	"github.com/giantbeaver9/yuno/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	box, err := secretbox.NewFromHex(cfg.SecretKey)
	if err != nil {
		log.Fatalf("SECRET_KEY invalid (need 64 hex chars = 32 bytes): %v", err)
	}

	// Subsystem stores.
	ag := agents.New(st.Pool, box)
	wf := workflow.New(st.Pool)
	fac := factory.New(ag, cfg.AgentsDir, box)
	fac.MCPBinDir = cfg.MCPBinDir // wire custom MCP servers into generated recipes

	// Seed the workflow templates + their agents (idempotent).
	if err := seed.Seed(ctx, ag, wf); err != nil {
		log.Printf("seed templates: %v", err)
	}

	// The orchestrator worker drains the bus and drives agent turns.
	gooseRunner := runner.NewGooseRunner(cfg.GoosePath)
	orch := orchestrator.New(st.Pool, bus.New(st.Pool), wf, ag, gooseRunner, 30*time.Second)
	go orch.Run(ctx)

	// Schedule ticker: due schedules → wake messages on the bus.
	sched := schedule.New(st.Pool, bus.New(st.Pool))
	go sched.Run(ctx)

	// Live monitor.
	stream := sse.NewHandler(st.Pool)

	// HTTP: REST command API + SSE stream + embedded SPA.
	app := api.NewApp(st.Pool, ag, wf, fac, stream)
	mux := http.NewServeMux()
	app.Routes(mux)
	web.Register(mux)

	// Optional Telegram transport (a peer on the bus). Best-effort: never fatal.
	if cfg.TelegramBotToken != "" {
		startTelegram(ctx, cfg, st.Pool, wf)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Logging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		tg := "disabled"
		if cfg.TelegramBotToken != "" {
			tg = "enabled"
		}
		log.Printf("yuno listening on :%s (worker on, telegram=%s, goose=%s)", cfg.Port, tg, cfg.GoosePath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancel() // stop worker, scheduler, telegram

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// startTelegram binds the bot's inbound messages to a run on the first seeded
// template workflow, then long-polls in a goroutine. Best-effort: any setup
// failure is logged and the rest of the platform runs normally. Inbound DMs
// become bus rows (human:telegram:<chatid>) that the orchestrator drives.
func startTelegram(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, wf *workflow.Store) {
	var wfID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM workflow WHERE is_template ORDER BY id LIMIT 1`).Scan(&wfID); err != nil {
		log.Printf("telegram: no template workflow to bind to: %v — telegram disabled", err)
		return
	}
	entry, err := wf.EntryNode(ctx, wfID)
	if err != nil {
		log.Printf("telegram: entry node lookup failed: %v — telegram disabled", err)
		return
	}

	guid := newGuid()
	if _, err := pool.Exec(ctx,
		`INSERT INTO run (guid, workflow_id, status, max_iterations) VALUES ($1, $2, 'running', 20)`,
		guid, wfID); err != nil {
		log.Printf("telegram: create bound run failed: %v — telegram disabled", err)
		return
	}

	client := telegram.NewClient(cfg.TelegramBotToken, &http.Client{Timeout: 65 * time.Second})
	bot := telegram.NewBot(client, bus.New(pool), cfg.TelegramAllowedChatIDs, entry.AgentID, guid)

	go func() {
		mode := "discovery mode (no allowlist)"
		if len(cfg.TelegramAllowedChatIDs) > 0 {
			mode = "allowlist active"
		}
		log.Printf("telegram bot polling (%s, bound run=%s agent=%d)", mode, guid, entry.AgentID)
		for ctx.Err() == nil {
			if err := bot.Poll(ctx); err != nil && ctx.Err() == nil {
				time.Sleep(2 * time.Second) // transient long-poll error; back off
			}
		}
	}()
}

// newGuid returns a random 128-bit hex id for a run.
func newGuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; fall back to a timestamp-ish id.
		return "run-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

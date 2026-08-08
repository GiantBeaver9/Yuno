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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

// startTelegram runs a single long-poll loop that dispatches each Telegram chat
// to its OWN isolated run (run-per-chat), so multiple people can each hold a
// separate agent conversation with the same bot. Only one poller may consume
// getUpdates for a token, so this is the sole reader; it fans out per chat.
// Best-effort: any setup failure is logged and the rest of the platform runs.
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

	client := telegram.NewClient(cfg.TelegramBotToken, &http.Client{Timeout: 65 * time.Second})
	runs := &chatRuns{pool: pool, client: client, wfID: wfID, entryAgentID: entry.AgentID, byChat: map[int64]string{}}

	mode := "discovery mode (no allowlist — anyone can DM)"
	if len(cfg.TelegramAllowedChatIDs) > 0 {
		mode = fmt.Sprintf("allowlist active (%d chat id(s))", len(cfg.TelegramAllowedChatIDs))
	}
	log.Printf("telegram bot polling (%s, per-chat runs on workflow %d, entry agent %d)", mode, wfID, entry.AgentID)

	go func() {
		var offset int64
		for ctx.Err() == nil {
			updates, err := client.GetUpdates(ctx, offset)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				time.Sleep(2 * time.Second) // transient long-poll error; back off
				continue
			}
			for _, u := range updates {
				offset = u.UpdateID + 1
				if !u.IsPrivate || strings.TrimSpace(u.Text) == "" {
					continue // ignore groups/channels and empty/non-text updates
				}
				if !telegram.Allowed(u.ChatID, cfg.TelegramAllowedChatIDs) {
					continue // not on the allowlist
				}
				guid, created := runs.ensure(ctx, u.ChatID)
				if guid == "" {
					continue
				}
				if created {
					_ = client.SendMessage(ctx, u.ChatID, "👋 On it — spinning up your agents. Their replies land here.")
				}
				if _, err := bus.New(pool).Enqueue(ctx, bus.EnqueueParams{
					RunID:   guid,
					FromRef: "human:telegram:" + strconv.FormatInt(u.ChatID, 10),
					ToRef:   "agent:" + strconv.FormatInt(runs.entryAgentID, 10),
					Content: u.Text,
				}); err != nil && ctx.Err() == nil {
					log.Printf("telegram: enqueue inbound for chat %d failed: %v", u.ChatID, err)
				}
			}
		}
	}()
}

// chatRuns maps each Telegram chat to its own run so users get isolated agent
// conversations. Only the single poll loop calls ensure, but the mutex keeps it
// safe against future callers. Runs are in-memory per process (a restart starts
// fresh runs); the runs and their trails persist in the DB.
type chatRuns struct {
	pool         *pgxpool.Pool
	client       *telegram.Client
	wfID         int64
	entryAgentID int64

	mu     sync.Mutex
	byChat map[int64]string
}

// ensure returns the run guid for a chat, creating the run and its outbound
// responder on first contact. created is true only on the creating call.
func (c *chatRuns) ensure(ctx context.Context, chatID int64) (guid string, created bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, ok := c.byChat[chatID]; ok {
		return g, false
	}
	g := newGuid()
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO run (guid, workflow_id, status, max_iterations) VALUES ($1, $2, 'running', 20)`,
		g, c.wfID); err != nil {
		log.Printf("telegram: create run for chat %d failed: %v", chatID, err)
		return "", false
	}
	c.byChat[chatID] = g
	go relayAgentActivity(ctx, c.pool, c.client, g, chatID)
	return g, true
}

// relayAgentActivity forwards new agent messages on runGuid to a specific chat —
// the read side of the bus for one Telegram conversation.
func relayAgentActivity(ctx context.Context, pool *pgxpool.Pool, client *telegram.Client, runGuid string, chatID int64) {
	var lastID int64
	_ = pool.QueryRow(ctx, `SELECT COALESCE(MAX(id),0) FROM message WHERE run_id=$1`, runGuid).Scan(&lastID)

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		rows, err := pool.Query(ctx,
			`SELECT id, decision, summary, content FROM message
			 WHERE run_id=$1 AND id>$2 AND from_ref LIKE 'agent:%' ORDER BY id`, runGuid, lastID)
		if err != nil {
			continue
		}
		type item struct {
			id                      int64
			decision, summary, cont string
		}
		var batch []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.decision, &it.summary, &it.cont); err == nil {
				batch = append(batch, it)
			}
		}
		rows.Close()

		for _, it := range batch {
			text := it.summary
			if text == "" {
				text = it.cont
			}
			if it.decision != "" {
				text = "[" + it.decision + "] " + text
			}
			if text == "" {
				text = "(no content)"
			}
			if err := client.SendMessage(ctx, chatID, "🤖 "+text); err != nil && ctx.Err() == nil {
				log.Printf("telegram: relay send to chat %d failed: %v", chatID, err)
			}
			lastID = it.id
		}
	}
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

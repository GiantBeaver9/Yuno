// Command yuno is the platform backend: it connects to Postgres, applies the
// schema, serves the REST + SSE API and the embedded React SPA, and (as
// subsystems land) starts the bus worker, scheduler ticker, and Telegram
// transport. Graceful-shutdown template ported from pi-server.
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

	"github.com/giantbeaver9/yuno/internal/api"
	"github.com/giantbeaver9/yuno/internal/config"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/web"
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

	mux := http.NewServeMux()
	api.Register(mux, st)
	web.Register(mux)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Logging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("yuno listening on :%s (db configured)", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancel() // stop background workers

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

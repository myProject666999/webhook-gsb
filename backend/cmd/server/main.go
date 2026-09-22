package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webhook/internal/apiserver"
	"webhook/internal/config"
	"webhook/internal/db"
	"webhook/internal/demo"
	"webhook/internal/worker"
)

//go:embed all:web/dist
var webAssets embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "receiver" {
		demo.RunReceiver(os.Args[2:])
		return
	}

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer st.Close()
	if err := waitForDB(ctx, st); err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	w := worker.New(st, cfg)
	go w.Run(ctx)

	sub, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		log.Fatalf("embedded ui: %v", err)
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apiserver.New(st, cfg, w.Wake, sub).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("http server listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func waitForDB(ctx context.Context, st *db.Store) error {
	var last error
	for i := 0; i < 30; i++ {
		if last = st.Pool().Ping(ctx); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return last
}

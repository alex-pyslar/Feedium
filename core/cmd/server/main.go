package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/app"
	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/logger"
	"github.com/alex-pyslar/Feedium/internal/postgres"
	"github.com/alex-pyslar/Feedium/internal/rss"
	"github.com/alex-pyslar/Feedium/internal/scheduler"
	"github.com/alex-pyslar/Feedium/internal/scorer"
	"github.com/alex-pyslar/Feedium/internal/telegram"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	must(err, "load config")

	log, err := logger.New(cfg.Log)
	must(err, "build logger")
	defer log.Sync() //nolint:errcheck

	log.Info("starting feedium core")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.Database)
	must(err, "connect postgres")
	must(postgres.Migrate(pool), "run migrations")
	log.Info("migrations applied")

	store := postgres.New(pool, log.Named("postgres"))
	must(store.Ping(ctx), "postgres ping")
	must(store.UpsertFeedsFromConfig(ctx, cfg.Feeds), "upsert feeds")
	log.Info("postgres connected")

	scorerClient := scorer.New(
		cfg.Scoring.ScorerURL,
		cfg.Scoring.RecencyHalfLifeHours,
		log.Named("scorer"),
	)
	if scorerClient.Healthy(ctx) {
		log.Info("scorer API connected", zap.String("url", cfg.Scoring.ScorerURL))
	} else {
		log.Warn("scorer API unavailable, using fallback heuristic",
			zap.String("url", cfg.Scoring.ScorerURL))
	}

	fetcher := rss.NewFetcher(log.Named("rss"))
	bot, err := telegram.NewBot(cfg.Telegram, log.Named("telegram"))
	must(err, "init telegram bot")

	fetchSvc := app.NewFetchService(
		store, store,
		fetcher, scorerClient, bot,
		cfg, log.Named("fetch"),
	)

	reactionSvc := app.NewReactionService(
		store, store,
		bot, log.Named("reaction"),
	)

	sched := scheduler.New(cfg, fetchSvc, reactionSvc, log.Named("scheduler"))
	must(sched.Start(ctx), "start scheduler")

	startAdminServer(ctx, envOr("ADMIN_ADDR", ":8081"), fetchSvc, log)

	<-ctx.Done()
	log.Info("shutdown signal received")
	sched.Stop()
	store.Close()
	log.Info("shutdown complete")
}

func startAdminServer(ctx context.Context, addr string, fetchSvc *app.FetchService, log *zap.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/fetch", func(w http.ResponseWriter, r *http.Request) {
		go fetchSvc.Run(ctx)
		log.Info("admin: manual fetch triggered")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "fetch started")
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Info("admin server listening", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("admin server", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

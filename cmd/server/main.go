package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alex-pyslar/Feedium/internal/analytics"
	"github.com/alex-pyslar/Feedium/internal/app"
	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/logger"
	"github.com/alex-pyslar/Feedium/internal/media"
	"github.com/alex-pyslar/Feedium/internal/postgres"
	"github.com/alex-pyslar/Feedium/internal/rss"
	"github.com/alex-pyslar/Feedium/internal/scheduler"
	"github.com/alex-pyslar/Feedium/internal/scorer"
	"github.com/alex-pyslar/Feedium/internal/search"
	"github.com/alex-pyslar/Feedium/internal/summarizer"
	"github.com/alex-pyslar/Feedium/internal/telegram"
	"go.uber.org/zap"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	must(err, "load config")

	log, err := logger.New(cfg.Log)
	must(err, "build logger")
	defer log.Sync() //nolint:errcheck

	log.Info("starting feedium")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ---- PostgreSQL: операционные данные ----
	pool, err := postgres.NewPool(ctx, cfg.Database)
	must(err, "connect postgres")

	store := postgres.New(pool, log.Named("postgres"))
	must(store.Ping(ctx), "postgres ping")
	must(store.UpsertFeedsFromConfig(ctx, cfg.Feeds), "upsert feeds")
	log.Info("postgres connected")

	// ---- Elasticsearch: поиск и BM25-релевантность ----
	var searchClient *search.Client
	if cfg.Elasticsearch.Enabled {
		searchClient, err = search.New(cfg.Elasticsearch.Addr, log.Named("search"))
		if err != nil {
			log.Warn("elasticsearch unavailable, BM25 scoring disabled", zap.Error(err))
		} else {
			log.Info("elasticsearch connected", zap.String("addr", cfg.Elasticsearch.Addr))
		}
	}

	// ---- ClickHouse: аналитика событий ----
	var analyticsClient *analytics.Client
	if cfg.ClickHouse.Enabled {
		analyticsClient, err = analytics.New(ctx, cfg.ClickHouse.DSN, log.Named("analytics"))
		if err != nil {
			log.Warn("clickhouse unavailable, analytics disabled", zap.Error(err))
		} else {
			log.Info("clickhouse connected")
		}
	}

	// ---- Суммаризатор ----
	var sum *summarizer.Summarizer
	if cfg.Summarizer.Enabled {
		if s, sumErr := summarizer.New(cfg.Summarizer, log.Named("summarizer")); sumErr != nil {
			log.Warn("summarizer init failed, posts will use raw description", zap.Error(sumErr))
		} else {
			sum = s
		}
	}

	// ---- MinIO: хранилище изображений ----
	var mediaClient *media.Client
	if cfg.Media.Enabled && cfg.Media.Endpoint != "" {
		mediaClient, err = media.New(ctx, cfg.Media, log.Named("media"))
		if err != nil {
			log.Warn("minio unavailable, images disabled", zap.Error(err))
		} else {
			log.Info("minio connected", zap.String("endpoint", cfg.Media.Endpoint))
		}
	}

	// ---- Базовые компоненты ----
	fetcher := rss.NewFetcher(log.Named("rss"))
	sc := scorer.New(cfg.Scoring, log.Named("scorer"))

	bot, err := telegram.NewBot(cfg.Telegram, log.Named("telegram"))
	must(err, "init telegram bot")

	// ---- Сервисы прикладного уровня ----
	// store реализует все пять доменных репозиториев одновременно.
	fetchSvc := app.NewFetchService(
		store, store, store,
		fetcher, sc,
		searchClient, analyticsClient,
		sum, mediaClient, bot,
		cfg, log.Named("fetch"),
	)

	reactionSvc := app.NewReactionService(
		store, store, store,
		sc, searchClient, analyticsClient, bot, log.Named("reaction"),
	)

	var retrainSvc *app.RetrainService
	if analyticsClient != nil {
		retrainSvc = app.NewRetrainService(store, analyticsClient, cfg.Scoring, log.Named("retrain"))
	}

	// ---- Планировщик (только cron-оркестрация) ----
	sched := scheduler.New(cfg, fetchSvc, reactionSvc, retrainSvc, log.Named("scheduler"))
	must(sched.Start(ctx), "start scheduler")

	<-ctx.Done()
	log.Info("shutdown signal received")
	sched.Stop()
	store.Close()
	if analyticsClient != nil {
		_ = analyticsClient.Close()
	}
	log.Info("shutdown complete")
}

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

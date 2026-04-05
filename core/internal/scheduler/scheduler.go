package scheduler

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/app"
	"github.com/alex-pyslar/Feedium/internal/config"
)

type Scheduler struct {
	cfg      *config.Config
	fetch    *app.FetchService
	reaction *app.ReactionService
	log      *zap.Logger
	cron     *cron.Cron
}

func New(
	cfg *config.Config,
	fetch *app.FetchService,
	reaction *app.ReactionService,
	log *zap.Logger,
) *Scheduler {
	return &Scheduler{cfg: cfg, fetch: fetch, reaction: reaction, log: log}
}

func (s *Scheduler) Start(ctx context.Context) error {
	loc, err := time.LoadLocation(s.cfg.Scheduler.Timezone)
	if err != nil {
		return err
	}

	s.cron = cron.New(
		cron.WithLocation(loc),
		cron.WithChain(
			cron.SkipIfStillRunning(cron.DiscardLogger),
			cron.Recover(cron.DiscardLogger),
		),
	)

	if _, err := s.cron.AddFunc(s.cfg.Scheduler.FetchCron, func() {
		s.fetch.Run(ctx)
	}); err != nil {
		return err
	}

	if _, err := s.cron.AddFunc(s.cfg.Scheduler.ReactionCron, func() {
		s.reaction.Harvest(ctx)
	}); err != nil {
		return err
	}

	s.reaction.StartPolling(ctx)
	s.cron.Start()
	s.log.Info("scheduler started")
	go s.fetch.Run(ctx)
	return nil
}

func (s *Scheduler) Stop() {
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
	s.log.Info("scheduler stopped")
}

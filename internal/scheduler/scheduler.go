// Package scheduler — тонкий оркестратор cron-заданий.
//
// Не содержит бизнес-логики: только регистрирует крон-задания и делегирует
// вызовы сервисам прикладного уровня (app.*Service).
//
// Три задания:
//  1. FetchCron     — запускает app.FetchService.Run (fetch → score → publish).
//  2. ReactionCron  — запускает app.ReactionService.Harvest (reconciliation реакций).
//  3. BatchCron     — запускает app.RetrainService.Run (батч-переобучение весов).
//
// Плюс запуск long-polling через app.ReactionService.StartPolling.
package scheduler

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/app"
	"github.com/alex-pyslar/Feedium/internal/config"
)

// Scheduler регистрирует крон-задания и управляет их жизненным циклом.
type Scheduler struct {
	cfg      *config.Config
	fetch    *app.FetchService
	reaction *app.ReactionService
	retrain  *app.RetrainService // nil если ClickHouse отключён
	log      *zap.Logger
	cron     *cron.Cron
}

// New создаёт Scheduler.
func New(
	cfg *config.Config,
	fetch *app.FetchService,
	reaction *app.ReactionService,
	retrain *app.RetrainService,
	log *zap.Logger,
) *Scheduler {
	return &Scheduler{
		cfg:      cfg,
		fetch:    fetch,
		reaction: reaction,
		retrain:  retrain,
		log:      log,
	}
}

// Start регистрирует задания, запускает long-polling и выполняет первый fetch сразу.
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

	if s.retrain != nil && s.cfg.ClickHouse.BatchCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.ClickHouse.BatchCron, func() {
			if err := s.retrain.Run(ctx, s.cfg.ClickHouse.BatchWindowDays); err != nil {
				s.log.Error("batch retrain", zap.Error(err))
			}
		}); err != nil {
			return err
		}
		s.log.Info("batch retrainer scheduled", zap.String("cron", s.cfg.ClickHouse.BatchCron))
	}

	s.reaction.StartPolling(ctx)
	s.cron.Start()
	s.log.Info("scheduler started")
	go s.fetch.Run(ctx) // первый fetch сразу, не дожидаясь крона
	return nil
}

// Stop мягко завершает крон.
func (s *Scheduler) Stop() {
	if s.cron == nil {
		return
	}
	<-s.cron.Stop().Done()
	s.log.Info("scheduler stopped")
}

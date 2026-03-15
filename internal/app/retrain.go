package app

import (
	"context"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/analytics"
	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
)

// RetrainService выполняет ночное батч-переобучение весов ключевых слов.
//
// Архитектура (аналог Spark job):
//
//  1. EXTRACT  — читает KeywordStats из ClickHouse за окно N дней.
//
//  2. TRANSFORM — map-фаза: параллельные «партиции» вычисляют weight_delta.
//                reduce-фаза: объединяет результаты партиций.
//                Формула: delta = tanh(total_signal / sqrt(event_count))
//                tanh держит delta в (-1, +1) и сглаживает выбросы.
//
//  3. LOAD     — батч-UPDATE весов в PostgreSQL.
//
// Отличие от онлайн-обучения (ReactionService):
//   - Онлайн — обновляет при каждой реакции, быстрый отклик.
//   - Батч   — раз в сутки, учитывает ВСЮ историю окна — стабильные веса.
type RetrainService struct {
	keywords  domain.KeywordRepository
	analytics *analytics.Client
	cfg       config.ScoringConfig
	log       *zap.Logger
}

// NewRetrainService создаёт RetrainService.
func NewRetrainService(
	keywords domain.KeywordRepository,
	analyticsClient *analytics.Client,
	cfg config.ScoringConfig,
	log *zap.Logger,
) *RetrainService {
	return &RetrainService{
		keywords:  keywords,
		analytics: analyticsClient,
		cfg:       cfg,
		log:       log,
	}
}

// Run выполняет один батч-джоб.
func (s *RetrainService) Run(ctx context.Context, windowDays int) error {
	start := time.Now()
	s.log.Info("batch retrainer started", zap.Int("window_days", windowDays))

	// EXTRACT
	stats, err := s.analytics.GetKeywordStats(ctx, windowDays)
	if err != nil {
		return err
	}
	if len(stats) == 0 {
		s.log.Info("no reaction data in window, skipping")
		return nil
	}

	// TRANSFORM: параллельные партиции (map)
	partitions := splitPartitions(stats, 4)
	resultCh := make(chan map[string]float64, len(partitions))
	var wg sync.WaitGroup

	for _, part := range partitions {
		wg.Add(1)
		go func(p []analytics.KeywordStats) {
			defer wg.Done()
			resultCh <- mapPartition(p)
		}(part)
	}
	wg.Wait()
	close(resultCh)

	// TRANSFORM: reduce — объединяем партиции
	merged := make(map[string]float64)
	for partial := range resultCh {
		for word, delta := range partial {
			merged[word] += delta
		}
	}

	// LOAD: текущие веса из Postgres
	allKeywords, err := s.keywords.GetAllKeywords(ctx)
	if err != nil {
		return err
	}
	kwByWord := make(map[string]domain.Keyword, len(allKeywords))
	for _, kw := range allKeywords {
		kwByWord[kw.Word] = kw
	}

	updates := make(map[int]float64)
	for word, delta := range merged {
		kw, ok := kwByWord[word]
		if !ok {
			continue
		}
		newWeight := clampWeight(
			kw.Weight+s.cfg.LearningRate*delta,
			s.cfg.MinKeywordWeight,
			s.cfg.MaxKeywordWeight,
		)
		if math.Abs(newWeight-kw.Weight) > 0.001 {
			updates[kw.ID] = newWeight
		}
	}

	if len(updates) == 0 {
		s.log.Info("batch retrainer: no weight changes needed")
		return nil
	}

	if err := s.keywords.UpdateKeywordWeights(ctx, updates); err != nil {
		return err
	}

	s.log.Info("batch retrainer done",
		zap.Int("keywords_updated", len(updates)),
		zap.Int("stats_processed", len(stats)),
		zap.Duration("elapsed", time.Since(start)),
	)
	return nil
}

// mapPartition — MAP-фаза: delta = tanh(signal / sqrt(events)).
func mapPartition(stats []analytics.KeywordStats) map[string]float64 {
	result := make(map[string]float64, len(stats))
	for _, s := range stats {
		if s.EventCount == 0 {
			continue
		}
		normalized := s.TotalSignal / math.Sqrt(float64(s.EventCount))
		result[s.Keyword] = math.Tanh(normalized)
	}
	return result
}

func splitPartitions[T any](s []T, n int) [][]T {
	if n <= 0 || len(s) == 0 {
		return [][]T{s}
	}
	size := (len(s) + n - 1) / n
	var parts [][]T
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		parts = append(parts, s[i:end])
	}
	return parts
}

func clampWeight(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

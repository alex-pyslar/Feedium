package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/analytics"
	"github.com/alex-pyslar/Feedium/internal/domain"
	"github.com/alex-pyslar/Feedium/internal/scorer"
	"github.com/alex-pyslar/Feedium/internal/search"
	"github.com/alex-pyslar/Feedium/internal/telegram"
)

// ReactionService обрабатывает реакции пользователей на Telegram-посты.
//
// Два режима работы:
//  1. Real-time — горутина long-polling принимает события из канала и обновляет
//     веса ключевых слов сразу (онлайн-обучение Перцептрона).
//  2. Harvest — периодическая reconciliation: обновляет счётчики для постов,
//     у которых давно не собирались реакции (не реже 1 раза в 5 минут).
type ReactionService struct {
	reactions domain.ReactionRepository
	keywords  domain.KeywordRepository
	state     domain.StateRepository
	scorer    *scorer.Scorer
	search    *search.Client    // nil если ES отключён
	analytics *analytics.Client // nil если CH отключён
	bot       *telegram.Bot
	log       *zap.Logger
}

// NewReactionService создаёт ReactionService.
func NewReactionService(
	reactions domain.ReactionRepository,
	keywords domain.KeywordRepository,
	state domain.StateRepository,
	sc *scorer.Scorer,
	searchClient *search.Client,
	analyticsClient *analytics.Client,
	bot *telegram.Bot,
	log *zap.Logger,
) *ReactionService {
	return &ReactionService{
		reactions: reactions,
		keywords:  keywords,
		state:     state,
		scorer:    sc,
		search:    searchClient,
		analytics: analyticsClient,
		bot:       bot,
		log:       log,
	}
}

// Harvest reconciles reaction counts для сообщений с устаревшими данными.
// Вызывается планировщиком по расписанию (reaction_cron).
func (s *ReactionService) Harvest(ctx context.Context) {
	msgs, err := s.reactions.GetMessagesForHarvest(ctx, 10*time.Minute, 48*time.Hour)
	if err != nil {
		s.log.Error("get messages for harvest", zap.Error(err))
		return
	}
	for _, pm := range msgs {
		if err := s.reactions.UpdateReactionCounts(ctx, pm.ID, pm.PositiveReactions, pm.NegativeReactions); err != nil {
			s.log.Warn("update reaction counts", zap.Error(err))
		}
	}
}

// StartPolling запускает Telegram long-polling и горутину обработки реакций.
// Вызывается однократно при старте планировщика.
func (s *ReactionService) StartPolling(ctx context.Context) {
	ch := make(chan telegram.ReactionEvent, 256)
	go s.bot.PollReactions(ctx, ch, s.state.GetTelegramOffset, s.state.SetTelegramOffset)
	go s.consume(ctx, ch)
}

func (s *ReactionService) consume(ctx context.Context, ch <-chan telegram.ReactionEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			s.handle(ctx, event)
		}
	}
}

func (s *ReactionService) handle(ctx context.Context, event telegram.ReactionEvent) {
	pm, err := s.reactions.GetPostedMessageByTelegramID(ctx, event.ChatID, event.TelegramMsgID)
	if err != nil {
		s.log.Warn("get posted message", zap.Error(err))
		return
	}
	if pm == nil {
		return
	}

	oldPos, oldNeg := scorer.ClassifyReactions(event.OldEmojis)
	newPos, newNeg := scorer.ClassifyReactions(event.NewEmojis)
	addedPos := newPos - oldPos
	addedNeg := newNeg - oldNeg

	netSignal := s.scorer.NetSignal(addedPos, addedNeg)
	if netSignal == 0 {
		return
	}

	kws, err := s.keywords.GetKeywordsForArticle(ctx, pm.ArticleID)
	if err != nil {
		s.log.Warn("get keywords for article", zap.Error(err))
		return
	}

	// Онлайн-обновление весов (правило Перцептрона)
	if updates := s.scorer.ComputeWeightUpdates(kws, netSignal); len(updates) > 0 {
		if err := s.keywords.UpdateKeywordWeights(ctx, updates); err != nil {
			s.log.Error("update keyword weights", zap.Error(err))
			return
		}
	}

	// Запись событий реакций в ClickHouse
	if s.analytics != nil {
		evType := analytics.EventReactedPositive
		if netSignal < 0 {
			evType = analytics.EventReactedNegative
		}
		events := make([]analytics.Event, 0, len(kws))
		now := time.Now()
		for _, kw := range kws {
			events = append(events, analytics.Event{
				EventType: evType,
				ArticleID: pm.ArticleID,
				Keyword:   kw.Word,
				Weight:    kw.Weight,
				Signal:    netSignal,
				CreatedAt: now,
			})
		}
		if err := s.analytics.WriteBatch(ctx, events); err != nil {
			s.log.Warn("analytics write reaction events", zap.Error(err))
		}
	}

	// Помечаем в ES как понравившуюся
	if s.search != nil && addedPos > 0 {
		if err := s.search.MarkLiked(ctx, pm.ArticleID); err != nil {
			s.log.Warn("es mark liked", zap.Error(err))
		}
	}

	totalPos := maxInt(0, pm.PositiveReactions+addedPos)
	totalNeg := maxInt(0, pm.NegativeReactions+addedNeg)
	if err := s.reactions.UpdateReactionCounts(ctx, pm.ID, totalPos, totalNeg); err != nil {
		s.log.Warn("update reaction counts", zap.Error(err))
	}

	s.log.Info("reaction handled",
		zap.Int("msg_id", event.TelegramMsgID),
		zap.Float64("signal", netSignal),
		zap.Int("keywords_updated", len(kws)),
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

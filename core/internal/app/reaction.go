package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
	"github.com/alex-pyslar/Feedium/internal/telegram"
)

// positiveEmoji и negativeEmoji используются для подсчёта реакций.
// Данные хранятся в БД как обучающие метки для trainer-сервиса.
var positiveEmoji = map[string]bool{
	"👍": true, "❤": true, "❤️": true, "🔥": true,
	"🎉": true, "🏆": true, "✅": true, "💯": true,
}
var negativeEmoji = map[string]bool{
	"👎": true, "💩": true, "🤮": true, "❌": true, "😴": true,
}

// ReactionService собирает реакции из Telegram и сохраняет в БД.
// Реакции — обучающие метки для trainer-сервиса.
type ReactionService struct {
	reactions domain.ReactionRepository
	state     domain.StateRepository
	bot       *telegram.Bot
	log       *zap.Logger
}

func NewReactionService(
	reactions domain.ReactionRepository,
	state domain.StateRepository,
	bot *telegram.Bot,
	log *zap.Logger,
) *ReactionService {
	return &ReactionService{
		reactions: reactions,
		state:     state,
		bot:       bot,
		log:       log,
	}
}

// Harvest reconciles reaction counts для устаревших сообщений.
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

// StartPolling запускает Telegram long-polling реакций.
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

	oldPos, oldNeg := classify(event.OldEmojis)
	newPos, newNeg := classify(event.NewEmojis)
	addedPos := newPos - oldPos
	addedNeg := newNeg - oldNeg

	if addedPos == 0 && addedNeg == 0 {
		return
	}

	totalPos := max(0, pm.PositiveReactions+addedPos)
	totalNeg := max(0, pm.NegativeReactions+addedNeg)

	if err := s.reactions.UpdateReactionCounts(ctx, pm.ID, totalPos, totalNeg); err != nil {
		s.log.Warn("update reaction counts", zap.Error(err))
		return
	}

	s.log.Info("reaction saved",
		zap.Int("msg_id", event.TelegramMsgID),
		zap.Int("pos", totalPos),
		zap.Int("neg", totalNeg),
	)
}

func classify(emojis []string) (pos, neg int) {
	for _, e := range emojis {
		if positiveEmoji[e] {
			pos++
		} else if negativeEmoji[e] {
			neg++
		}
	}
	return
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

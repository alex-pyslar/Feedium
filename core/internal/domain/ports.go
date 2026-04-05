package domain

import (
	"context"
	"time"
)

// FeedRepository управляет RSS-лентами.
type FeedRepository interface {
	GetActiveFeeds(ctx context.Context) ([]Feed, error)
	UpdateFeedFetchedAt(ctx context.Context, feedIDs []int, at time.Time) error
}

// ArticleRepository управляет статьями, скорами и состоянием публикации.
type ArticleRepository interface {
	UpsertArticles(ctx context.Context, articles []Article) ([]int64, error)
	GetArticlesByIDs(ctx context.Context, ids []int64) ([]Article, error)
	GetTopUnposted(ctx context.Context, limit int, minScore float64) ([]Article, error)
	SaveScores(ctx context.Context, scores map[int64]float64) error
	MarkPosted(ctx context.Context, articleID int64, msgID int, chatID int64) error
}

// ReactionRepository управляет Telegram-сообщениями и счётчиками реакций.
// Реакции — это обучающие метки для trainer-сервиса.
type ReactionRepository interface {
	GetMessagesForHarvest(ctx context.Context, staleSince, maxAge time.Duration) ([]PostedMessage, error)
	GetPostedMessageByTelegramID(ctx context.Context, chatID int64, msgID int) (*PostedMessage, error)
	UpdateReactionCounts(ctx context.Context, pmID int64, pos, neg int) error
}

// StateRepository сохраняет cursor long-polling (Telegram update offset).
type StateRepository interface {
	GetTelegramOffset(ctx context.Context) (int, error)
	SetTelegramOffset(ctx context.Context, offset int) error
}

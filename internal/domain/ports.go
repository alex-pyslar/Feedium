// Package domain — чистые доменные типы и порты (интерфейсы) без зависимостей от инфраструктуры.
//
// Порты определяют что нужно приложению, но не как это реализовано.
// Реализации (адаптеры) живут в отдельных пакетах: postgres/, search/, analytics/ и т.д.
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
	SaveScores(ctx context.Context, scored []ScoredArticle) error
	UpdateArticleMedia(ctx context.Context, id int64, imageKey, summary string) error
	MarkPosted(ctx context.Context, articleID int64, msgID int, chatID int64) error
}

// KeywordRepository управляет обучаемыми весами слов — параметрами модели.
type KeywordRepository interface {
	GetAllKeywords(ctx context.Context) ([]Keyword, error)
	GetKeywordsForArticle(ctx context.Context, articleID int64) ([]Keyword, error)
	EnsureKeywords(ctx context.Context, words []string) ([]Keyword, error)
	UpdateKeywordWeights(ctx context.Context, updates map[int]float64) error
}

// ReactionRepository управляет Telegram-сообщениями и счётчиками реакций.
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

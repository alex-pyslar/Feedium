// Package domain содержит чистые доменные модели без зависимостей от БД.
// Все остальные пакеты (storage, search, analytics, scorer) импортируют именно отсюда.
package domain

import "time"

// Feed — RSS-лента.
type Feed struct {
	ID            int
	Name          string
	URL           string
	Weight        float64
	IsActive      bool
	LastFetchedAt *time.Time
}

// Article — статья из RSS-ленты.
type Article struct {
	ID              int64
	FeedID          int
	FeedWeight      float64    // transient — заполняется при загрузке из БД
	GUID            string
	Title           string
	Description     string
	Content         string     // полный текст из RSS (item.Content), для суммаризации
	Link            string
	ImageURL        string     // оригинальный URL изображения из RSS
	ImageKey        string     // ключ объекта в MinIO (пусто если нет изображения)
	Summary         string     // Telegram-пост, сгенерированный Claude
	PublishedAt     *time.Time
	FetchedAt       time.Time
	RelevanceScore  float64
	PopularityScore float64
	FinalScore      float64
	IsPosted        bool
}

// Keyword — ключевое слово с обучаемым весом (параметр мини-нейросети).
type Keyword struct {
	ID        int
	Word      string
	Weight    float64
	HitCount  int64
	UpdatedAt time.Time
}

// PostedMessage — опубликованное в Telegram сообщение со счётчиками реакций.
type PostedMessage struct {
	ID                      int64
	ArticleID               int64
	TelegramMsgID           int
	ChatID                  int64
	PostedAt                time.Time
	PositiveReactions       int
	NegativeReactions       int
	LastReactionHarvestedAt *time.Time
}

// ScoredArticle — статья после скоринга + ключевые слова для обратного распространения реакций.
type ScoredArticle struct {
	Article         Article
	MatchedKeywords []Keyword
}

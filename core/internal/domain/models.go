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
	ID          int64
	FeedID      int
	FeedWeight  float64    // transient — заполняется join'ом с feeds
	GUID        string
	Title       string
	Description string
	Link        string
	PublishedAt *time.Time
	FetchedAt   time.Time
	FinalScore  float64
	IsPosted    bool
}

// PostedMessage — опубликованное в Telegram сообщение со счётчиками реакций.
// Реакции хранятся в БД как обучающие данные для trainer.
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

package rss

import (
	"context"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

const (
	maxConcurrent = 5
	fetchTimeout  = 30 * time.Second
)

// Fetcher парсит RSS-ленты параллельно.
type Fetcher struct {
	log *zap.Logger
	sem *semaphore.Weighted
}

func NewFetcher(log *zap.Logger) *Fetcher {
	return &Fetcher{
		log: log,
		sem: semaphore.NewWeighted(maxConcurrent),
	}
}

// FetchResult — результат парсинга одной ленты.
type FetchResult struct {
	Feed      domain.Feed
	Articles  []domain.Article
	FetchedAt time.Time
	Error     error
}

// FetchAll параллельно скачивает и парсит все ленты.
func (f *Fetcher) FetchAll(ctx context.Context, feeds []domain.Feed) []FetchResult {
	results := make([]FetchResult, len(feeds))
	done := make(chan struct{})

	for i, feed := range feeds {
		i, feed := i, feed
		go func() {
			defer func() { done <- struct{}{} }()

			if err := f.sem.Acquire(ctx, 1); err != nil {
				results[i] = FetchResult{Feed: feed, Error: err}
				return
			}
			defer f.sem.Release(1)

			results[i] = f.fetch(ctx, feed)
		}()
	}

	for range feeds {
		<-done
	}
	return results
}

func (f *Fetcher) fetch(ctx context.Context, feed domain.Feed) FetchResult {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	parser := gofeed.NewParser()
	parsed, err := parser.ParseURLWithContext(feed.URL, ctx)
	if err != nil {
		f.log.Warn("fetch feed failed", zap.String("url", feed.URL), zap.Error(err))
		return FetchResult{Feed: feed, Error: err, FetchedAt: time.Now()}
	}

	var articles []domain.Article
	for _, item := range parsed.Items {
		a := domain.Article{
			FeedID:      feed.ID,
			FeedWeight:  feed.Weight,
			GUID:        guid(item),
			Title:       item.Title,
			Description: stripHTML(item.Description),
			Content:     stripHTML(item.Content), // полный текст статьи если есть в RSS
			Link:        item.Link,
			ImageURL:    extractImageURL(item),
		}
		if item.PublishedParsed != nil {
			t := *item.PublishedParsed
			a.PublishedAt = &t
		}
		articles = append(articles, a)
	}

	f.log.Info("fetched feed",
		zap.String("name", feed.Name),
		zap.Int("items", len(articles)),
	)
	return FetchResult{Feed: feed, Articles: articles, FetchedAt: time.Now()}
}

// extractImageURL пытается найти URL изображения в RSS-элементе.
// Проверяет enclosure, media:content, item.Image в порядке приоритета.
func extractImageURL(item *gofeed.Item) string {
	// 1. Enclosure (podcast/media feeds)
	for _, enc := range item.Enclosures {
		if strings.HasPrefix(enc.Type, "image/") {
			return enc.URL
		}
	}
	// 2. media:content (Yahoo Media RSS Extension)
	if media, ok := item.Extensions["media"]; ok {
		if contents, ok := media["content"]; ok {
			for _, ext := range contents {
				if url := ext.Attrs["url"]; url != "" {
					if mt := ext.Attrs["medium"]; mt == "image" || mt == "" {
						return url
					}
				}
			}
		}
	}
	// 3. item.Image (RSS 2.0 image tag)
	if item.Image != nil && item.Image.URL != "" {
		return item.Image.URL
	}
	return ""
}

// guid возвращает GUID элемента, используя Link как fallback.
func guid(item *gofeed.Item) string {
	if item.GUID != "" {
		return item.GUID
	}
	return item.Link
}

// stripHTML удаляет HTML-теги из строки (простая реализация).
func stripHTML(s string) string {
	var out []byte
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>':
			inTag = false
		case !inTag:
			out = append(out, s[i])
		}
	}
	return string(out)
}

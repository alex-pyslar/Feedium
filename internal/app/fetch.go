// Package app содержит сервисы прикладного уровня (use cases).
//
// Каждый сервис инкапсулирует один бизнес-сценарий и зависит только от
// доменных интерфейсов (domain.*Repository) и конкретных адаптеров.
// Вся оркестрация (cron, goroutines) вынесена в пакет scheduler.
package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/analytics"
	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
	"github.com/alex-pyslar/Feedium/internal/media"
	"github.com/alex-pyslar/Feedium/internal/rss"
	"github.com/alex-pyslar/Feedium/internal/scorer"
	"github.com/alex-pyslar/Feedium/internal/search"
	"github.com/alex-pyslar/Feedium/internal/summarizer"
	"github.com/alex-pyslar/Feedium/internal/telegram"
)

// FetchService реализует сценарий fetch → score → publish:
//  1. Загрузить RSS-ленты.
//  2. Сохранить новые статьи в PostgreSQL, проиндексировать в ES.
//  3. Оценить (score) каждую статью через keyword-модель + ES BM25.
//  4. Опубликовать топ-N в Telegram с summary и изображением.
type FetchService struct {
	feeds     domain.FeedRepository
	articles  domain.ArticleRepository
	keywords  domain.KeywordRepository
	fetcher   *rss.Fetcher
	scorer    *scorer.Scorer
	search    *search.Client       // nil если ES отключён
	analytics *analytics.Client   // nil если CH отключён
	sum       *summarizer.Summarizer // nil если суммаризатор отключён
	media     *media.Client        // nil если MinIO отключён
	bot       *telegram.Bot
	cfg       *config.Config
	log       *zap.Logger
}

// NewFetchService создаёт FetchService со всеми зависимостями.
func NewFetchService(
	feeds domain.FeedRepository,
	articles domain.ArticleRepository,
	keywords domain.KeywordRepository,
	fetcher *rss.Fetcher,
	sc *scorer.Scorer,
	searchClient *search.Client,
	analyticsClient *analytics.Client,
	sum *summarizer.Summarizer,
	mediaClient *media.Client,
	bot *telegram.Bot,
	cfg *config.Config,
	log *zap.Logger,
) *FetchService {
	return &FetchService{
		feeds:     feeds,
		articles:  articles,
		keywords:  keywords,
		fetcher:   fetcher,
		scorer:    sc,
		search:    searchClient,
		analytics: analyticsClient,
		sum:       sum,
		media:     mediaClient,
		bot:       bot,
		cfg:       cfg,
		log:       log,
	}
}

// Run выполняет один цикл fetch → score → publish.
// Вызывается планировщиком по расписанию и сразу при старте.
func (s *FetchService) Run(ctx context.Context) {
	start := time.Now()
	s.log.Info("fetch cycle started")

	feeds, err := s.feeds.GetActiveFeeds(ctx)
	if err != nil {
		s.log.Error("get active feeds", zap.Error(err))
		return
	}
	if len(feeds) == 0 {
		s.log.Warn("no active feeds configured")
		return
	}

	results := s.fetcher.FetchAll(ctx, feeds)

	var allArticles []domain.Article
	var fetchedFeedIDs []int
	for _, r := range results {
		if r.Error != nil {
			continue
		}
		allArticles = append(allArticles, r.Articles...)
		fetchedFeedIDs = append(fetchedFeedIDs, r.Feed.ID)
	}
	if len(allArticles) == 0 {
		s.log.Info("no new items from feeds")
		return
	}

	newIDs, err := s.articles.UpsertArticles(ctx, allArticles)
	if err != nil {
		s.log.Error("upsert articles", zap.Error(err))
		return
	}
	if len(newIDs) == 0 {
		return
	}

	arts, err := s.articles.GetArticlesByIDs(ctx, newIDs)
	if err != nil {
		s.log.Error("get articles by ids", zap.Error(err))
		return
	}

	kws, err := s.keywords.GetAllKeywords(ctx)
	if err != nil {
		s.log.Error("get keywords", zap.Error(err))
		return
	}

	s.indexInES(ctx, arts)
	esScores := s.scoreFromES(ctx, arts, kws)
	scored := s.scorer.ScoreAll(arts, kws, esScores)

	// Новые слова → keywords
	kwMap := make(map[string]domain.Keyword, len(kws))
	for _, kw := range kws {
		kwMap[kw.Word] = kw
	}
	var newWords []string
	for _, a := range arts {
		newWords = append(newWords, scorer.ExtractNewWords(a.Title+" "+a.Description, kwMap)...)
	}
	if len(newWords) > 0 {
		if _, err := s.keywords.EnsureKeywords(ctx, dedup(newWords)); err != nil {
			s.log.Warn("ensure keywords", zap.Error(err))
		}
	}

	if err := s.articles.SaveScores(ctx, scored); err != nil {
		s.log.Error("save scores", zap.Error(err))
		return
	}

	s.writeScoredEvents(ctx, scored)

	toPost, err := s.articles.GetTopUnposted(ctx,
		s.cfg.Telegram.MaxMessagesPerRun,
		s.cfg.Scoring.MinScoreToPost,
	)
	if err != nil {
		s.log.Error("get top unposted", zap.Error(err))
		return
	}

	for _, a := range toPost {
		s.publish(ctx, a, kws)
	}

	if len(fetchedFeedIDs) > 0 {
		_ = s.feeds.UpdateFeedFetchedAt(ctx, fetchedFeedIDs, time.Now())
	}

	s.log.Info("fetch cycle done",
		zap.Int("fetched", len(allArticles)),
		zap.Int("new", len(newIDs)),
		zap.Int("posted", len(toPost)),
		zap.Duration("elapsed", time.Since(start)),
	)
}

// publish генерирует summary, сохраняет изображение и публикует статью в Telegram.
func (s *FetchService) publish(ctx context.Context, a domain.Article, kws []domain.Keyword) {
	// Суммаризация
	if s.sum != nil && a.Summary == "" {
		if text, err := s.sum.Summarize(ctx, a, kws); err != nil {
			s.log.Warn("summarize article", zap.Int64("id", a.ID), zap.Error(err))
		} else {
			a.Summary = text
		}
	}

	// Сохранение изображения в MinIO
	if s.media != nil && a.ImageURL != "" && a.ImageKey == "" {
		if key, err := s.media.StoreFromURL(ctx, a.ImageURL, a.ID); err != nil {
			s.log.Warn("store image", zap.Int64("id", a.ID), zap.Error(err))
		} else {
			a.ImageKey = key
		}
	}

	// Сохраняем обогащения в Postgres
	if a.Summary != "" || a.ImageKey != "" {
		if err := s.articles.UpdateArticleMedia(ctx, a.ID, a.ImageKey, a.Summary); err != nil {
			s.log.Warn("save article media", zap.Error(err))
		}
	}

	// Получаем байты изображения
	var imageData []byte
	if s.media != nil && a.ImageKey != "" {
		if data, err := s.media.GetBytes(ctx, a.ImageKey); err != nil {
			s.log.Warn("get image bytes", zap.String("key", a.ImageKey), zap.Error(err))
		} else {
			imageData = data
		}
	}

	// Публикация в Telegram
	msgID, err := s.bot.PostArticle(ctx, a, imageData)
	if err != nil {
		s.log.Error("post article", zap.String("title", a.Title), zap.Error(err))
		return
	}

	if err := s.articles.MarkPosted(ctx, a.ID, msgID, s.cfg.Telegram.ChannelID); err != nil {
		s.log.Error("mark posted", zap.Int64("article_id", a.ID), zap.Error(err))
	}
	s.writeEvent(ctx, analytics.Event{
		EventType:  analytics.EventPosted,
		ArticleID:  a.ID,
		FinalScore: a.FinalScore,
		CreatedAt:  time.Now(),
	})
}

func (s *FetchService) indexInES(ctx context.Context, articles []domain.Article) {
	if s.search == nil {
		return
	}
	for _, a := range articles {
		if err := s.search.IndexArticle(ctx, a); err != nil {
			s.log.Warn("es index article", zap.Int64("id", a.ID), zap.Error(err))
		}
	}
}

func (s *FetchService) scoreFromES(ctx context.Context, articles []domain.Article, keywords []domain.Keyword) map[int64]float64 {
	if s.search == nil {
		return nil
	}
	scores := make(map[int64]float64, len(articles))
	for _, a := range articles {
		score, err := s.search.Score(ctx, a, keywords)
		if err != nil {
			s.log.Warn("es score", zap.Int64("id", a.ID), zap.Error(err))
			scores[a.ID] = 0.5
			continue
		}
		scores[a.ID] = score
	}
	return scores
}

func (s *FetchService) writeScoredEvents(ctx context.Context, scored []domain.ScoredArticle) {
	if s.analytics == nil {
		return
	}
	events := make([]analytics.Event, 0, len(scored)*3)
	now := time.Now()
	for _, sa := range scored {
		for _, kw := range sa.MatchedKeywords {
			events = append(events, analytics.Event{
				EventType:  analytics.EventScored,
				ArticleID:  sa.Article.ID,
				Keyword:    kw.Word,
				Weight:     kw.Weight,
				FinalScore: sa.Article.FinalScore,
				CreatedAt:  now,
			})
		}
	}
	if err := s.analytics.WriteBatch(ctx, events); err != nil {
		s.log.Warn("analytics write batch", zap.Error(err))
	}
}

func (s *FetchService) writeEvent(ctx context.Context, e analytics.Event) {
	if s.analytics == nil {
		return
	}
	if err := s.analytics.WriteEvent(ctx, e); err != nil {
		s.log.Warn("analytics write event", zap.Error(err))
	}
}

func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

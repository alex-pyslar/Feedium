package app

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
	"github.com/alex-pyslar/Feedium/internal/rss"
	"github.com/alex-pyslar/Feedium/internal/scorer"
	"github.com/alex-pyslar/Feedium/internal/telegram"
)

// FetchService реализует цикл: RSS → score → publish.
type FetchService struct {
	feeds    domain.FeedRepository
	articles domain.ArticleRepository
	fetcher  *rss.Fetcher
	scorer   *scorer.Client
	bot      *telegram.Bot
	cfg      *config.Config
	log      *zap.Logger
}

func NewFetchService(
	feeds domain.FeedRepository,
	articles domain.ArticleRepository,
	fetcher *rss.Fetcher,
	sc *scorer.Client,
	bot *telegram.Bot,
	cfg *config.Config,
	log *zap.Logger,
) *FetchService {
	return &FetchService{
		feeds:    feeds,
		articles: articles,
		fetcher:  fetcher,
		scorer:   sc,
		bot:      bot,
		cfg:      cfg,
		log:      log,
	}
}

// Run выполняет один цикл fetch → score → publish.
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

	// Запрашиваем оценки у scorer-сервиса (или fallback если недоступен)
	scores := s.scorer.ScoreAll(ctx, arts)
	if err := s.articles.SaveScores(ctx, scores); err != nil {
		s.log.Error("save scores", zap.Error(err))
		return
	}

	toPost, err := s.articles.GetTopUnposted(ctx,
		s.cfg.Telegram.MaxMessagesPerRun,
		s.cfg.Scoring.MinScoreToPost,
	)
	if err != nil {
		s.log.Error("get top unposted", zap.Error(err))
		return
	}

	for _, a := range toPost {
		msgID, err := s.bot.PostArticle(ctx, a, nil)
		if err != nil {
			s.log.Error("post article", zap.String("title", a.Title), zap.Error(err))
			continue
		}
		if err := s.articles.MarkPosted(ctx, a.ID, msgID, s.cfg.Telegram.ChannelID); err != nil {
			s.log.Error("mark posted", zap.Int64("article_id", a.ID), zap.Error(err))
		}
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

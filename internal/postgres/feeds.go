package postgres

import (
	"context"
	"time"

	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
)

// UpsertFeedsFromConfig синхронизирует список лент из config.yaml с БД.
// Вызывается при старте приложения из main.go.
func (s *Store) UpsertFeedsFromConfig(ctx context.Context, feeds []config.FeedConfig) error {
	for _, f := range feeds {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO feeds (name, url, weight)
			VALUES ($1, $2, $3)
			ON CONFLICT (url) DO UPDATE SET name = EXCLUDED.name, weight = EXCLUDED.weight
		`, f.Name, f.URL, f.Weight)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetActiveFeeds возвращает все активные ленты.
func (s *Store) GetActiveFeeds(ctx context.Context) ([]domain.Feed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, url, weight, is_active, last_fetched_at
		FROM feeds WHERE is_active = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var feeds []domain.Feed
	for rows.Next() {
		var f domain.Feed
		if err := rows.Scan(&f.ID, &f.Name, &f.URL, &f.Weight, &f.IsActive, &f.LastFetchedAt); err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// UpdateFeedFetchedAt проставляет last_fetched_at для лент.
func (s *Store) UpdateFeedFetchedAt(ctx context.Context, feedIDs []int, t time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE feeds SET last_fetched_at = $1 WHERE id = ANY($2)`,
		t, feedIDs,
	)
	return err
}

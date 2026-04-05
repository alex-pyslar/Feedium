package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

func (s *Store) UpsertArticles(ctx context.Context, articles []domain.Article) ([]int64, error) {
	var ids []int64
	for _, a := range articles {
		var id int64
		err := s.pool.QueryRow(ctx, `
			INSERT INTO articles (feed_id, guid, title, description, link, published_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (feed_id, guid) DO NOTHING
			RETURNING id
		`, a.FeedID, a.GUID, a.Title, a.Description, a.Link, a.PublishedAt).Scan(&id)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			s.log.Warn("upsert article", zap.String("guid", a.GUID), zap.Error(err))
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) GetArticlesByIDs(ctx context.Context, ids []int64) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.feed_id, f.weight, a.guid, a.title,
		       COALESCE(a.description,''), a.link,
		       a.published_at, a.fetched_at, a.final_score, a.is_posted
		FROM articles a
		JOIN feeds f ON f.id = a.feed_id
		WHERE a.id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArticles(rows)
}

// SaveScores обновляет final_score для набора статей.
func (s *Store) SaveScores(ctx context.Context, scores map[int64]float64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for id, score := range scores {
		if _, err := tx.Exec(ctx,
			`UPDATE articles SET final_score = $2 WHERE id = $1`,
			id, score,
		); err != nil {
			return fmt.Errorf("update score for %d: %w", id, err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) GetTopUnposted(ctx context.Context, limit int, minScore float64) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.feed_id, f.weight, a.guid, a.title,
		       COALESCE(a.description,''), a.link,
		       a.published_at, a.fetched_at, a.final_score, a.is_posted
		FROM articles a
		JOIN feeds f ON f.id = a.feed_id
		WHERE a.is_posted = false AND a.final_score >= $1
		ORDER BY a.final_score DESC
		LIMIT $2
	`, minScore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArticles(rows)
}

func (s *Store) MarkPosted(ctx context.Context, articleID int64, msgID int, chatID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err = tx.Exec(ctx, `UPDATE articles SET is_posted = true WHERE id = $1`, articleID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO posted_messages (article_id, telegram_msg_id, chat_id)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING
	`, articleID, msgID, chatID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanArticles(rows pgx.Rows) ([]domain.Article, error) {
	var arts []domain.Article
	for rows.Next() {
		var a domain.Article
		if err := rows.Scan(
			&a.ID, &a.FeedID, &a.FeedWeight, &a.GUID, &a.Title,
			&a.Description, &a.Link,
			&a.PublishedAt, &a.FetchedAt, &a.FinalScore, &a.IsPosted,
		); err != nil {
			return nil, err
		}
		arts = append(arts, a)
	}
	return arts, rows.Err()
}

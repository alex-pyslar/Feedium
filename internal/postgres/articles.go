package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// UpsertArticles вставляет новые статьи, возвращает только вставленные ID.
func (s *Store) UpsertArticles(ctx context.Context, articles []domain.Article) ([]int64, error) {
	var ids []int64
	for _, a := range articles {
		var id int64
		err := s.pool.QueryRow(ctx, `
			INSERT INTO articles (feed_id, guid, title, description, content, link, image_url, published_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (feed_id, guid) DO NOTHING
			RETURNING id
		`, a.FeedID, a.GUID, a.Title, a.Description, a.Content, a.Link, a.ImageURL, a.PublishedAt).Scan(&id)
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

// GetArticlesByIDs загружает статьи вместе с весом ленты.
func (s *Store) GetArticlesByIDs(ctx context.Context, ids []int64) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.feed_id, f.weight, a.guid, a.title,
		       COALESCE(a.description,''), COALESCE(a.content,''), a.link,
		       COALESCE(a.image_url,''), COALESCE(a.image_key,''), COALESCE(a.summary,''),
		       a.published_at, a.fetched_at,
		       a.relevance_score, a.popularity_score, a.final_score, a.is_posted
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

// SaveScores сохраняет скоры и связи article_keywords в одной транзакции.
func (s *Store) SaveScores(ctx context.Context, scored []domain.ScoredArticle) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, sa := range scored {
		a := sa.Article
		_, err := tx.Exec(ctx, `
			UPDATE articles
			SET relevance_score = $2, popularity_score = $3, final_score = $4
			WHERE id = $1
		`, a.ID, a.RelevanceScore, a.PopularityScore, a.FinalScore)
		if err != nil {
			return fmt.Errorf("update scores for %d: %w", a.ID, err)
		}
		for _, kw := range sa.MatchedKeywords {
			_, err := tx.Exec(ctx, `
				INSERT INTO article_keywords (article_id, keyword_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING
			`, a.ID, kw.ID)
			if err != nil {
				return fmt.Errorf("insert article_keyword: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}

// GetTopUnposted возвращает топ-N неопубликованных статей выше порога.
func (s *Store) GetTopUnposted(ctx context.Context, limit int, minScore float64) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.feed_id, f.weight, a.guid, a.title,
		       COALESCE(a.description,''), COALESCE(a.content,''), a.link,
		       COALESCE(a.image_url,''), COALESCE(a.image_key,''), COALESCE(a.summary,''),
		       a.published_at, a.fetched_at,
		       a.relevance_score, a.popularity_score, a.final_score, a.is_posted
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

// MarkPosted помечает статью опубликованной и создаёт запись в posted_messages.
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

// UpdateArticleMedia сохраняет image_key и summary после обработки статьи.
func (s *Store) UpdateArticleMedia(ctx context.Context, id int64, imageKey, summary string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE articles SET image_key = $2, summary = $3 WHERE id = $1
	`, id, imageKey, summary)
	return err
}

// scanArticles — вспомогательная функция сканирования строк.
func scanArticles(rows pgx.Rows) ([]domain.Article, error) {
	var arts []domain.Article
	for rows.Next() {
		var a domain.Article
		if err := rows.Scan(
			&a.ID, &a.FeedID, &a.FeedWeight, &a.GUID, &a.Title,
			&a.Description, &a.Content, &a.Link,
			&a.ImageURL, &a.ImageKey, &a.Summary,
			&a.PublishedAt, &a.FetchedAt,
			&a.RelevanceScore, &a.PopularityScore, &a.FinalScore, &a.IsPosted,
		); err != nil {
			return nil, err
		}
		arts = append(arts, a)
	}
	return arts, rows.Err()
}

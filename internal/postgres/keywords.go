package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// GetAllKeywords загружает все ключевые слова с весами.
func (s *Store) GetAllKeywords(ctx context.Context) ([]domain.Keyword, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, word, weight, hit_count, updated_at FROM keywords
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeywords(rows)
}

// EnsureKeywords вставляет новые слова с weight=1.0, пропуская существующие.
func (s *Store) EnsureKeywords(ctx context.Context, words []string) ([]domain.Keyword, error) {
	if len(words) == 0 {
		return nil, nil
	}
	batch := &pgx.Batch{}
	for _, w := range words {
		batch.Queue(`
			INSERT INTO keywords (word) VALUES ($1)
			ON CONFLICT (word) DO NOTHING
		`, w)
	}
	br := s.pool.SendBatch(ctx, batch)
	if err := br.Close(); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, word, weight, hit_count, updated_at FROM keywords WHERE word = ANY($1)`,
		words,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeywords(rows)
}

// UpdateKeywordWeights обновляет веса ключевых слов (батч).
func (s *Store) UpdateKeywordWeights(ctx context.Context, updates map[int]float64) error {
	batch := &pgx.Batch{}
	for id, w := range updates {
		batch.Queue(`
			UPDATE keywords SET weight = $2, updated_at = NOW() WHERE id = $1
		`, id, w)
	}
	br := s.pool.SendBatch(ctx, batch)
	return br.Close()
}

// GetKeywordsForArticle возвращает ключевые слова, связанные со статьёй.
func (s *Store) GetKeywordsForArticle(ctx context.Context, articleID int64) ([]domain.Keyword, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT k.id, k.word, k.weight, k.hit_count, k.updated_at
		FROM keywords k
		JOIN article_keywords ak ON ak.keyword_id = k.id
		WHERE ak.article_id = $1
	`, articleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeywords(rows)
}

func scanKeywords(rows pgx.Rows) ([]domain.Keyword, error) {
	var kws []domain.Keyword
	for rows.Next() {
		var k domain.Keyword
		if err := rows.Scan(&k.ID, &k.Word, &k.Weight, &k.HitCount, &k.UpdatedAt); err != nil {
			return nil, err
		}
		kws = append(kws, k)
	}
	return kws, rows.Err()
}

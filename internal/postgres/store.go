// Package postgres реализует репозитории PostgreSQL (адаптеры доменных портов).
//
// Роль PostgreSQL в системе: операционные данные (OLTP).
//   - feeds            — список RSS-лент и их веса
//   - articles         — дедупликация, скоры, флаг публикации
//   - keywords         — обучаемые веса слов (параметры мини-нейросети)
//   - posted_messages  — связь Telegram-сообщений со статьями + счётчики реакций
//   - scheduler_state  — персистентный offset для Telegram long-polling
//
// Store реализует все пять доменных интерфейсов-репозиториев:
// domain.FeedRepository, domain.ArticleRepository, domain.KeywordRepository,
// domain.ReactionRepository, domain.StateRepository.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
)

// Store — единая точка доступа к PostgreSQL.
// Методы разнесены по файлам: feeds.go, articles.go, keywords.go, reactions.go, state.go.
type Store struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// New создаёт Store.
func New(pool *pgxpool.Pool, log *zap.Logger) *Store {
	return &Store{pool: pool, log: log}
}

// NewPool создаёт pgxpool по конфигу.
func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// Ping проверяет соединение.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close закрывает пул соединений.
func (s *Store) Close() {
	s.pool.Close()
}

// scanTime хелпер для nullable time.
func scanTime(t *time.Time) *time.Time { return t }

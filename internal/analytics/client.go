// Package analytics пишет аналитические события в ClickHouse.
//
// Роль ClickHouse в системе: хранение всей истории событий для анализа и переобучения.
//
// Три типа событий:
//   - scored          — статья прошла скоринг (keyword + ES скор)
//   - posted          — статья опубликована в Telegram
//   - reacted_positive / reacted_negative — пользователь поставил реакцию
//
// Данные используются:
//  1. Мониторинг — GetScoreTimeseries показывает тренд качества скоринга.
//  2. Батч-переобучение — GetKeywordStats агрегирует сигналы реакций
//     за скользящее окно (7 дней по умолчанию) для batch/retrainer.
//
// TTL 90 дней, партиции по месяцу — эффективные агрегации по времени.
package analytics

import (
	"context"
	"fmt"
	"time"

	clickhousego "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.uber.org/zap"
)

// EventType — тип аналитического события.
type EventType string

const (
	EventScored          EventType = "scored"
	EventPosted          EventType = "posted"
	EventReactedPositive EventType = "reacted_positive"
	EventReactedNegative EventType = "reacted_negative"
)

// Event — одна запись аналитики.
type Event struct {
	EventType  EventType
	ArticleID  int64
	Keyword    string
	Weight     float64
	Signal     float64 // +1 / -1 / 0
	FinalScore float64
	CreatedAt  time.Time
}

// KeywordStats — агрегат реакций по ключевому слову за период.
// Используется батч-переобучателем.
type KeywordStats struct {
	Keyword     string
	TotalSignal float64
	EventCount  int64
	AvgScore    float64
}

// Client управляет соединением с ClickHouse.
type Client struct {
	conn driver.Conn
	log  *zap.Logger
}

// New подключается к ClickHouse и создаёт таблицу если нужно.
func New(ctx context.Context, dsn string, log *zap.Logger) (*Client, error) {
	opts, err := clickhousego.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	conn, err := clickhousego.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	c := &Client{conn: conn, log: log}
	if err := c.ensureTable(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Close закрывает соединение.
func (c *Client) Close() error {
	return c.conn.Close()
}

// WriteEvent записывает одно событие.
func (c *Client) WriteEvent(ctx context.Context, e Event) error {
	return c.conn.Exec(ctx, `
		INSERT INTO article_events
		(event_type, article_id, keyword, weight, signal, final_score, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, string(e.EventType), e.ArticleID, e.Keyword,
		e.Weight, e.Signal, e.FinalScore, e.CreatedAt,
	)
}

// WriteBatch пишет пачку событий одним батчем (эффективно для scored-событий).
func (c *Client) WriteBatch(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, `
		INSERT INTO article_events
		(event_type, article_id, keyword, weight, signal, final_score, created_at)
	`)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, e := range events {
		if err := batch.Append(
			string(e.EventType), e.ArticleID, e.Keyword,
			e.Weight, e.Signal, e.FinalScore, e.CreatedAt,
		); err != nil {
			return fmt.Errorf("append event: %w", err)
		}
	}
	return batch.Send()
}

// GetKeywordStats возвращает агрегированную статистику реакций по ключевым словам
// за последние windowDays дней. Результат используется батч-переобучателем.
func (c *Client) GetKeywordStats(ctx context.Context, windowDays int) ([]KeywordStats, error) {
	rows, err := c.conn.Query(ctx, `
		SELECT
			keyword,
			sum(signal)      AS total_signal,
			count()          AS event_count,
			avg(final_score) AS avg_score
		FROM article_events
		WHERE event_type IN ('reacted_positive', 'reacted_negative')
		  AND created_at >= now() - INTERVAL ? DAY
		  AND keyword != ''
		GROUP BY keyword
		ORDER BY abs(total_signal) DESC
	`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("query keyword stats: %w", err)
	}
	defer rows.Close()

	var stats []KeywordStats
	for rows.Next() {
		var s KeywordStats
		if err := rows.Scan(&s.Keyword, &s.TotalSignal, &s.EventCount, &s.AvgScore); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// GetScoreTimeseries возвращает средний скор по дням за последние days дней.
// Полезно для мониторинга качества скоринга.
func (c *Client) GetScoreTimeseries(ctx context.Context, days int) (map[string]float64, error) {
	rows, err := c.conn.Query(ctx, `
		SELECT
			formatDateTime(created_at, '%Y-%m-%d') AS day,
			avg(final_score)                        AS avg_score
		FROM article_events
		WHERE event_type = 'scored'
		  AND created_at >= now() - INTERVAL ? DAY
		GROUP BY day
		ORDER BY day
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var day string
		var avg float64
		if err := rows.Scan(&day, &avg); err != nil {
			return nil, err
		}
		result[day] = avg
	}
	return result, rows.Err()
}

// ensureTable создаёт таблицу если её нет.
func (c *Client) ensureTable(ctx context.Context) error {
	err := c.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS article_events (
			event_type  LowCardinality(String),
			article_id  Int64,
			keyword     String,
			weight      Float64,
			signal      Float64,
			final_score Float64,
			created_at  DateTime
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(created_at)
		ORDER BY (created_at, event_type, article_id)
		TTL created_at + INTERVAL 90 DAY
		SETTINGS index_granularity = 8192
	`)
	if err != nil {
		return fmt.Errorf("ensure clickhouse table: %w", err)
	}
	c.log.Info("clickhouse table ready")
	return nil
}

// Package config загружает конфигурацию из двух источников:
//
//   - config.toml — логика приложения (RSS-ленты, scoring, cron, log)
//   - .env / переменные окружения — подключения и секреты
//
// Правило: строки подключения (DSN, токены, адреса) никогда не попадают
// в config.toml. Это позволяет хранить config.toml в репозитории без риска
// засветить секреты.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config — корневая конфигурация приложения.
type Config struct {
	Database      DatabaseConfig      `mapstructure:"database"`
	Elasticsearch ElasticsearchConfig `mapstructure:"elasticsearch"`
	ClickHouse    ClickHouseConfig    `mapstructure:"clickhouse"`
	Telegram      TelegramConfig      `mapstructure:"telegram"`
	Scoring       ScoringConfig       `mapstructure:"scoring"`
	Scheduler     SchedulerConfig     `mapstructure:"scheduler"`
	Summarizer    SummarizerConfig    `mapstructure:"summarizer"`
	Media         MediaConfig         `mapstructure:"media"`
	Feeds         []FeedConfig        `mapstructure:"feeds"`
	Log           LogConfig           `mapstructure:"log"`
}

// SummarizerConfig — настройки суммаризатора.
//
//	provider = "local"  — встроенный, нет внешних зависимостей (по умолчанию)
//	provider = "openai" — OpenAI-совместимый API (Ollama, LM Studio, vLLM и др.)
type SummarizerConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Provider  string `mapstructure:"provider"`   // local | openai
	Model     string `mapstructure:"model"`      // для openai: название модели
	MaxTokens int    `mapstructure:"max_tokens"` // для openai: лимит токенов
	APIURL    string `mapstructure:"-"`          // только из env: SUMMARIZER_API_URL
	APIKey    string `mapstructure:"-"`          // только из env: SUMMARIZER_API_KEY (необязательно)
}

// MediaConfig — настройки MinIO для хранения изображений.
type MediaConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Endpoint  string `mapstructure:"-"` // только из env: MINIO_ENDPOINT
	AccessKey string `mapstructure:"-"` // только из env: MINIO_ACCESS_KEY
	SecretKey string `mapstructure:"-"` // только из env: MINIO_SECRET_KEY
	Bucket    string `mapstructure:"bucket"`
	UseSSL    bool   `mapstructure:"use_ssl"`
}

// DatabaseConfig — только env (DATABASE_DSN).
type DatabaseConfig struct {
	DSN             string        `mapstructure:"-"` // только из env
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
}

// ElasticsearchConfig — addr только из env (ELASTICSEARCH_ADDR).
type ElasticsearchConfig struct {
	Addr    string `mapstructure:"-"` // только из env
	Enabled bool   `mapstructure:"enabled"`
}

// ClickHouseConfig — DSN только из env (CLICKHOUSE_DSN).
type ClickHouseConfig struct {
	DSN             string `mapstructure:"-"` // только из env
	Enabled         bool   `mapstructure:"enabled"`
	BatchCron       string `mapstructure:"batch_cron"`
	BatchWindowDays int    `mapstructure:"batch_window_days"`
}

// TelegramConfig — token и channel_id только из env.
type TelegramConfig struct {
	Token             string `mapstructure:"-"` // только из env: TELEGRAM_TOKEN
	ChannelID         int64  `mapstructure:"-"` // только из env: TELEGRAM_CHANNEL_ID
	UpdateTimeoutSecs int    `mapstructure:"update_timeout_seconds"`
	MaxMessagesPerRun int    `mapstructure:"max_messages_per_run"`
}

// ScoringConfig описывает параметры линейной модели (мини-нейросети).
//
// Формула итогового скора:
//
//	relevance = 0.6 * keyword_score + 0.4 * es_similarity
//	final = α * relevance + β * popularity
//
// Обновление весов при реакции:
//
//	w_i = clamp(w_i + η * signal, min, max)
type ScoringConfig struct {
	RecencyHalfLifeHours float64 `mapstructure:"recency_half_life_hours"`
	RelevanceWeight      float64 `mapstructure:"relevance_weight"`
	PopularityWeight     float64 `mapstructure:"popularity_weight"`
	LearningRate         float64 `mapstructure:"learning_rate"`
	PositiveRewardDelta  float64 `mapstructure:"positive_reward_delta"`
	NegativeRewardDelta  float64 `mapstructure:"negative_reward_delta"`
	MinKeywordWeight     float64 `mapstructure:"min_keyword_weight"`
	MaxKeywordWeight     float64 `mapstructure:"max_keyword_weight"`
	MinScoreToPost       float64 `mapstructure:"min_score_to_post"`
}

type SchedulerConfig struct {
	FetchCron    string `mapstructure:"fetch_cron"`
	ReactionCron string `mapstructure:"reaction_cron"`
	Timezone     string `mapstructure:"timezone"`
}

type FeedConfig struct {
	Name   string  `mapstructure:"name"`
	URL    string  `mapstructure:"url"`
	Weight float64 `mapstructure:"weight"`
}

// LogConfig — настройки логирования.
//
// Формат вывода:
//
//	format = "json"    — структурированный JSON (по умолчанию в production)
//	format = "console" — читаемый текст с цветами (по умолчанию при development=true)
//
// Ротация файлов (работает только если file != ""):
//
//	max_size_mb  — максимальный размер файла до ротации (МБ, default: 100)
//	max_backups  — количество хранимых ротированных файлов (default: 5)
//	max_age_days — максимальный возраст файла в днях (default: 30)
//	compress     — gzip-сжатие старых файлов (default: true)
type LogConfig struct {
	Level       string `mapstructure:"level"`       // debug | info | warn | error
	Development bool   `mapstructure:"development"` // цветной вывод, panic при DPanic
	Format      string `mapstructure:"format"`      // json | console (пусто = авто)
	File        string `mapstructure:"file"`        // путь к файлу; пусто = только stdout
	MaxSizeMB   int    `mapstructure:"max_size_mb"` // default 100
	MaxBackups  int    `mapstructure:"max_backups"` // default 5
	MaxAgeDays  int    `mapstructure:"max_age_days"` // default 30
	Compress    bool   `mapstructure:"compress"`    // gzip ротированных файлов
	Sampling    bool   `mapstructure:"sampling"`    // семплирование в production
}

// Load читает config.yaml (путь path) и накладывает env-переменные.
//
// Переменные окружения для подключений (обязательны если сервис включён):
//
//	DATABASE_DSN          — строка подключения к PostgreSQL
//	TELEGRAM_TOKEN        — токен Telegram-бота
//	TELEGRAM_CHANNEL_ID   — ID канала (число, например -1001234567890)
//	ELASTICSEARCH_ADDR    — адрес ES (например http://es:9200)
//	CLICKHOUSE_DSN        — строка подключения к ClickHouse
//	ANTHROPIC_API_KEY     — API-ключ Claude (суммаризатор)
//	MINIO_ENDPOINT        — хост:порт MinIO (например minio:9000)
//	MINIO_ACCESS_KEY      — MinIO access key
//	MINIO_SECRET_KEY      — MinIO secret key
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Дефолты для полей из YAML
	v.SetDefault("database.max_conns", 10)
	v.SetDefault("database.min_conns", 2)
	v.SetDefault("database.max_conn_lifetime", time.Hour)
	v.SetDefault("telegram.update_timeout_seconds", 30)
	v.SetDefault("telegram.max_messages_per_run", 5)
	v.SetDefault("elasticsearch.enabled", true)
	v.SetDefault("clickhouse.enabled", true)
	v.SetDefault("clickhouse.batch_cron", "0 3 * * *")
	v.SetDefault("clickhouse.batch_window_days", 7)
	v.SetDefault("scoring.recency_half_life_hours", 24.0)
	v.SetDefault("scoring.relevance_weight", 0.7)
	v.SetDefault("scoring.popularity_weight", 0.3)
	v.SetDefault("scoring.learning_rate", 0.05)
	v.SetDefault("scoring.positive_reward_delta", 1.0)
	v.SetDefault("scoring.negative_reward_delta", -1.0)
	v.SetDefault("scoring.min_keyword_weight", 0.1)
	v.SetDefault("scoring.max_keyword_weight", 10.0)
	v.SetDefault("scoring.min_score_to_post", 0.3)
	v.SetDefault("summarizer.enabled", true)
	v.SetDefault("summarizer.provider", "local")
	v.SetDefault("summarizer.model", "llama3.2") // используется только для openai-провайдера
	v.SetDefault("summarizer.max_tokens", 400)
	v.SetDefault("media.enabled", true)
	v.SetDefault("media.bucket", "article-images")
	v.SetDefault("media.use_ssl", false)
	v.SetDefault("scheduler.fetch_cron", "*/30 * * * *")
	v.SetDefault("scheduler.reaction_cron", "*/5 * * * *")
	v.SetDefault("scheduler.timezone", "UTC")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.max_size_mb", 100)
	v.SetDefault("log.max_backups", 5)
	v.SetDefault("log.max_age_days", 30)
	v.SetDefault("log.compress", true)
	v.SetDefault("log.sampling", true)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// ---- Подключения из env (никогда не из YAML) ----
	cfg.Database.DSN = requireEnv("DATABASE_DSN")
	cfg.Telegram.Token = requireEnv("TELEGRAM_TOKEN")
	cfg.Telegram.ChannelID = parseInt64Env("TELEGRAM_CHANNEL_ID")
	cfg.Elasticsearch.Addr = envOr("ELASTICSEARCH_ADDR", "http://localhost:9200")
	cfg.ClickHouse.DSN = envOr("CLICKHOUSE_DSN", "clickhouse://localhost:9000/default")
	cfg.Summarizer.APIURL = os.Getenv("SUMMARIZER_API_URL") // пусто → local провайдер
	cfg.Summarizer.APIKey = os.Getenv("SUMMARIZER_API_KEY") // необязательно
	cfg.Media.Endpoint = envOr("MINIO_ENDPOINT", "localhost:9000")
	cfg.Media.AccessKey = envOr("MINIO_ACCESS_KEY", "minioadmin")
	cfg.Media.SecretKey = envOr("MINIO_SECRET_KEY", "minioadmin")

	// Валидация обязательных полей
	if cfg.Database.DSN == "" {
		return nil, fmt.Errorf("DATABASE_DSN env is required")
	}
	if cfg.Telegram.Token == "" {
		return nil, fmt.Errorf("TELEGRAM_TOKEN env is required")
	}
	if cfg.Telegram.ChannelID == 0 {
		return nil, fmt.Errorf("TELEGRAM_CHANNEL_ID env is required")
	}

	return &cfg, nil
}

func requireEnv(key string) string {
	return os.Getenv(key)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseInt64Env(key string) int64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	var n int64
	fmt.Sscanf(v, "%d", &n)
	return n
}

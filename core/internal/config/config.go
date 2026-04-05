package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config — корневая конфигурация core-сервиса.
type Config struct {
	Database  DatabaseConfig  `mapstructure:"database"`
	Telegram  TelegramConfig  `mapstructure:"telegram"`
	Scoring   ScoringConfig   `mapstructure:"scoring"`
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
	Feeds     []FeedConfig    `mapstructure:"feeds"`
	Log       LogConfig       `mapstructure:"log"`
}

// DatabaseConfig — только env (DATABASE_DSN).
type DatabaseConfig struct {
	DSN             string        `mapstructure:"-"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
}

// TelegramConfig — token и channel_id только из env.
type TelegramConfig struct {
	Token             string `mapstructure:"-"`
	ChannelID         int64  `mapstructure:"-"`
	UpdateTimeoutSecs int    `mapstructure:"update_timeout_seconds"`
	MaxMessagesPerRun int    `mapstructure:"max_messages_per_run"`
}

// ScoringConfig — параметры скоринга и адрес scorer-сервиса.
type ScoringConfig struct {
	ScorerURL      string  `mapstructure:"-"`         // только из env: SCORER_URL
	MinScoreToPost float64 `mapstructure:"min_score_to_post"`
	// Fallback параметры (когда scorer недоступен):
	RecencyHalfLifeHours float64 `mapstructure:"recency_half_life_hours"`
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
type LogConfig struct {
	Level       string `mapstructure:"level"`
	Development bool   `mapstructure:"development"`
	Format      string `mapstructure:"format"`
	File        string `mapstructure:"file"`
	MaxSizeMB   int    `mapstructure:"max_size_mb"`
	MaxBackups  int    `mapstructure:"max_backups"`
	MaxAgeDays  int    `mapstructure:"max_age_days"`
	Compress    bool   `mapstructure:"compress"`
	Sampling    bool   `mapstructure:"sampling"`
}

// Load читает config.toml и накладывает env-переменные.
//
// Обязательные переменные окружения:
//
//	DATABASE_DSN        — PostgreSQL DSN
//	TELEGRAM_TOKEN      — токен бота
//	TELEGRAM_CHANNEL_ID — ID канала
//
// Опциональные:
//
//	SCORER_URL — адрес scorer API (default: http://scorer:8000)
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetDefault("database.max_conns", 10)
	v.SetDefault("database.min_conns", 2)
	v.SetDefault("database.max_conn_lifetime", time.Hour)
	v.SetDefault("telegram.update_timeout_seconds", 30)
	v.SetDefault("telegram.max_messages_per_run", 5)
	v.SetDefault("scoring.min_score_to_post", 0.3)
	v.SetDefault("scoring.recency_half_life_hours", 24.0)
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

	cfg.Database.DSN = os.Getenv("DATABASE_DSN")
	cfg.Telegram.Token = os.Getenv("TELEGRAM_TOKEN")
	cfg.Telegram.ChannelID = parseInt64Env("TELEGRAM_CHANNEL_ID")
	cfg.Scoring.ScorerURL = envOr("SCORER_URL", "http://scorer:8000")

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

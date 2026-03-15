// Package logger предоставляет фабрику zap-логгера с поддержкой:
//   - JSON или консольного кодирования (по умолчанию: JSON в prod, консоль в dev)
//   - Записи в файл с ротацией через lumberjack (опционально)
//   - Дублирования вывода: stdout + файл (tee)
//   - Семплирования высокочастотных событий в production
//   - Автоматического добавления caller (file:line)
//   - Стектрейса для Error и выше
package logger

import (
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/alex-pyslar/Feedium/internal/config"
)

// New строит *zap.Logger по конфигурации.
// Возвращает готовый логгер; вызывающая сторона должна вызвать log.Sync() при завершении.
func New(cfg config.LogConfig) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	enc := buildEncoder(cfg)
	out := buildOutput(cfg)

	var core zapcore.Core
	base := zapcore.NewCore(enc, out, level)

	// Семплирование в production: не более 100 одинаковых сообщений/сек,
	// после чего каждое 10-е (prevents log storms).
	if cfg.Sampling && !cfg.Development {
		core = zapcore.NewSamplerWithOptions(base, time.Second, 100, 10)
	} else {
		core = base
	}

	opts := []zap.Option{
		zap.AddStacktrace(zapcore.ErrorLevel),
		zap.AddCaller(),
	}
	if cfg.Development {
		opts = append(opts, zap.Development())
	}

	return zap.New(core, opts...), nil
}

// buildEncoder возвращает encoder в зависимости от cfg.Format и cfg.Development.
func buildEncoder(cfg config.LogConfig) zapcore.Encoder {
	ecfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.RFC3339TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	if cfg.Development {
		ecfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		ecfg.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05.000")
	}

	format := cfg.Format
	if format == "" {
		if cfg.Development {
			format = "console"
		} else {
			format = "json"
		}
	}

	if format == "console" {
		return zapcore.NewConsoleEncoder(ecfg)
	}
	return zapcore.NewJSONEncoder(ecfg)
}

// buildOutput возвращает WriteSyncer: stdout или stdout+файл (tee).
func buildOutput(cfg config.LogConfig) zapcore.WriteSyncer {
	stdout := zapcore.AddSync(os.Stdout)

	if cfg.File == "" {
		return stdout
	}

	maxSize := cfg.MaxSizeMB
	if maxSize <= 0 {
		maxSize = 100
	}
	maxBackups := cfg.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 5
	}
	maxAge := cfg.MaxAgeDays
	if maxAge <= 0 {
		maxAge = 30
	}

	rotator := zapcore.AddSync(&lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   cfg.Compress,
		LocalTime:  true,
	})

	return zapcore.NewMultiWriteSyncer(stdout, rotator)
}

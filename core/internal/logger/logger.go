package logger

import (
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/alex-pyslar/Feedium/internal/config"
)

func New(cfg config.LogConfig) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	enc := buildEncoder(cfg)
	out := buildOutput(cfg)

	var core zapcore.Core
	base := zapcore.NewCore(enc, out, level)
	if cfg.Sampling && !cfg.Development {
		core = zapcore.NewSamplerWithOptions(base, time.Second, 100, 10)
	} else {
		core = base
	}

	opts := []zap.Option{zap.AddStacktrace(zapcore.ErrorLevel), zap.AddCaller()}
	if cfg.Development {
		opts = append(opts, zap.Development())
	}
	return zap.New(core, opts...), nil
}

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

func buildOutput(cfg config.LogConfig) zapcore.WriteSyncer {
	stdout := zapcore.AddSync(os.Stdout)
	if cfg.File == "" {
		return stdout
	}
	maxSize := cfg.MaxSizeMB
	if maxSize <= 0 {
		maxSize = 100
	}
	return zapcore.NewMultiWriteSyncer(stdout, zapcore.AddSync(&lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    maxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
		LocalTime:  true,
	}))
}

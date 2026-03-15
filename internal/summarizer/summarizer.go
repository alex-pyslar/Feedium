// Package summarizer генерирует краткие Telegram-посты из статей.
//
// Провайдеры (задаётся через summarizer.provider в config.yaml):
//
//	local  — встроенная экстрактивная суммаризация (Go, без внешних вызовов).
//	         Использует веса ключевых слов из Postgres — часть той же «нейросети».
//	         Работает офлайн, нулевая стоимость.
//
//	openai — OpenAI-совместимый API: Ollama, LM Studio, vLLM, llama.cpp и др.
//	         Параметры: SUMMARIZER_API_URL, SUMMARIZER_API_KEY, summarizer.model.
//
// По умолчанию используется local.
package summarizer

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
)

const maxSummaryLen = 900 // лимит Telegram photo caption

// Summarizer — обёртка над Provider с логированием.
type Summarizer struct {
	provider Provider
	log      *zap.Logger
}

// New создаёт Summarizer по конфигу.
// provider = "local"  → встроенная экстрактивная суммаризация
// provider = "openai" → OpenAI-совместимый API (Ollama и т.д.)
func New(cfg config.SummarizerConfig, log *zap.Logger) (*Summarizer, error) {
	var p Provider

	switch cfg.Provider {
	case "openai":
		if cfg.APIURL == "" {
			return nil, fmt.Errorf("summarizer.provider=openai requires SUMMARIZER_API_URL")
		}
		p = NewOpenAI(cfg.APIURL, cfg.APIKey, cfg.Model, cfg.MaxTokens, log)
		log.Info("summarizer: openai-compatible provider",
			zap.String("url", cfg.APIURL),
			zap.String("model", cfg.Model),
		)

	default: // "local" и всё остальное
		p = NewLocal()
		log.Info("summarizer: built-in extractive provider")
	}

	return &Summarizer{provider: p, log: log}, nil
}

// Summarize генерирует пост из статьи.
// keywords используются встроенным провайдером для ранжирования предложений.
func (s *Summarizer) Summarize(ctx context.Context, a domain.Article, keywords []domain.Keyword) (string, error) {
	text, err := s.provider.Summarize(ctx, a, keywords)
	if err != nil {
		return "", fmt.Errorf("summarizer[%s]: %w", s.provider.Name(), err)
	}
	return text, nil
}

// ---- общие helpers (используются обоими провайдерами) --------------------

// pickContent выбирает лучший источник текста для суммаризации.
func pickContent(a domain.Article) string {
	src := a.Content
	if src == "" {
		src = a.Description
	}
	if src == "" {
		src = a.Title
	}
	const maxLen = 3000
	runes := []rune(src)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return src
}

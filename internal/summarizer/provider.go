package summarizer

import (
	"context"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// Provider — интерфейс суммаризатора.
// Реализации: LocalProvider (встроенный), OpenAIProvider (локальный/удалённый LLM).
type Provider interface {
	Summarize(ctx context.Context, a domain.Article, keywords []domain.Keyword) (string, error)
	Name() string
}

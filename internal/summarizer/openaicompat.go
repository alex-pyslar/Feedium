// OpenAI-совместимый провайдер — работает с любым локальным LLM.
//
// Совместимые серверы (достаточно выставить SUMMARIZER_API_URL):
//
//	Ollama:     http://localhost:11434/v1    (модель: llama3.2, qwen2.5 и т.д.)
//	LM Studio:  http://localhost:1234/v1
//	vLLM:       http://localhost:8000/v1
//	llama.cpp:  http://localhost:8080/v1
//	OpenAI:     https://api.openai.com/v1   (если нужен внешний)
//
// Никаких внешних SDK — только стандартный net/http.
package summarizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// OpenAIProvider вызывает OpenAI-совместимый /v1/chat/completions.
type OpenAIProvider struct {
	baseURL string
	apiKey  string
	model   string
	maxTok  int
	http    *http.Client
	log     *zap.Logger
}

func NewOpenAI(baseURL, apiKey, model string, maxTokens int, log *zap.Logger) *OpenAIProvider {
	return &OpenAIProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		maxTok:  maxTokens,
		http:    &http.Client{Timeout: 60 * time.Second},
		log:     log,
	}
}

func (p *OpenAIProvider) Name() string { return fmt.Sprintf("openai(%s)", p.model) }

func (p *OpenAIProvider) Summarize(ctx context.Context, a domain.Article, _ []domain.Keyword) (string, error) {
	content := pickContent(a)

	reqBody := map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "user", "content": buildLLMPrompt(a.Title, content)},
		},
		"max_tokens": p.maxTok,
		"stream":     false,
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, raw)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices from llm")
	}

	text := strings.TrimSpace(result.Choices[0].Message.Content)

	// Ограничиваем длину для Telegram caption
	runes := []rune(text)
	if len(runes) > maxSummaryLen {
		text = string(runes[:maxSummaryLen-1]) + "…"
	}

	p.log.Debug("article summarized via llm",
		zap.Int64("article_id", a.ID),
		zap.String("model", p.model),
		zap.Int("len", len(text)),
	)
	return text, nil
}

func buildLLMPrompt(title, content string) string {
	return fmt.Sprintf(`You are an editor of a tech news Telegram channel.

Write a concise post (3-5 sentences) summarizing the article below.

Requirements:
- Only the most important facts
- Use Telegram HTML: <b>key term</b> for 1-2 key phrases only
- Detect the language of the article and write in the SAME language
- No hashtags, no links, no greetings

Article title: %s

Content:
%s`, title, content)
}

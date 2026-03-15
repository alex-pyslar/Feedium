// Встроенная экстрактивная суммаризация — без внешних зависимостей и без LLM.
//
// Алгоритм:
//  1. Разбиваем текст на предложения.
//  2. Каждое предложение получает скор:
//     keyword_score  = сумма весов слов из Postgres keywords / sqrt(длина предложения)
//     position_score = убывает с позицией (первые предложения важнее)
//     sentence_score = 0.7 * keyword_score + 0.3 * position_score
//  3. Берём топ-3 предложения (в оригинальном порядке).
//  4. Форматируем как Telegram HTML-пост.
//
// Связь с «нейросетью»:
// Keyword weights обновляются при каждой реакции пользователя → экстрактор
// автоматически выделяет те предложения, которые пользователям интереснее всего.
// Чем больше лайков получают статьи про AI — тем выше AI-предложения в выжимке.
package summarizer

import (
	"context"
	"fmt"
	"html"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

var (
	sentenceSplit = regexp.MustCompile(`[.!?…]+\s+`)
	nonAlpha      = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
)

// LocalProvider — встроенная суммаризация без внешних вызовов.
type LocalProvider struct{}

func NewLocal() *LocalProvider { return &LocalProvider{} }

func (p *LocalProvider) Name() string { return "local" }

func (p *LocalProvider) Summarize(_ context.Context, a domain.Article, keywords []domain.Keyword) (string, error) {
	text := pickContent(a)
	if text == "" {
		return fallbackPost(a), nil
	}

	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return fallbackPost(a), nil
	}

	kwWeights := buildKWIndex(keywords)
	scored := scoreSentences(sentences, kwWeights)

	top := topN(scored, 3)
	if len(top) == 0 {
		return fallbackPost(a), nil
	}

	// Собираем пост
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>\n\n", html.EscapeString(a.Title)))
	for _, s := range top {
		sb.WriteString(html.EscapeString(strings.TrimSpace(s.text)))
		sb.WriteString(" ")
	}

	result := strings.TrimSpace(sb.String())
	// Ограничиваем длину
	runes := []rune(result)
	if len(runes) > maxSummaryLen {
		result = string(runes[:maxSummaryLen-1]) + "…"
	}
	return result, nil
}

// ---- sentence scoring ---------------------------------------------------

type scoredSentence struct {
	text  string
	score float64
	idx   int
}

func scoreSentences(sentences []string, kwWeights map[string]float64) []scoredSentence {
	result := make([]scoredSentence, len(sentences))

	for i, s := range sentences {
		tokens := tokenizeSentence(s)
		if len(tokens) < 3 {
			result[i] = scoredSentence{text: s, score: 0, idx: i}
			continue
		}

		// keyword_score: сумма весов совпавших слов / sqrt(длина)
		var kwSum float64
		for _, t := range tokens {
			if w, ok := kwWeights[t]; ok {
				kwSum += w
			}
		}
		keywordScore := kwSum / math.Sqrt(float64(len(tokens)))

		// position_score: первые предложения важнее
		// 1.0 → 1/(1+0.4*i), убывает мягко
		positionScore := 1.0 / (1.0 + 0.4*float64(i))

		result[i] = scoredSentence{
			text:  s,
			score: 0.7*keywordScore + 0.3*positionScore,
			idx:   i,
		}
	}
	return result
}

// topN выбирает n предложений с наибольшим скором,
// возвращает их в оригинальном порядке.
func topN(sentences []scoredSentence, n int) []scoredSentence {
	if len(sentences) <= n {
		return sentences
	}

	// Копируем и сортируем по скору
	ranked := make([]scoredSentence, len(sentences))
	copy(ranked, sentences)
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	// Берём топ-n
	top := ranked[:n]

	// Восстанавливаем исходный порядок
	sort.Slice(top, func(i, j int) bool {
		return top[i].idx < top[j].idx
	})
	return top
}

// ---- helpers -------------------------------------------------------------

func buildKWIndex(keywords []domain.Keyword) map[string]float64 {
	m := make(map[string]float64, len(keywords))
	for _, kw := range keywords {
		m[strings.ToLower(kw.Word)] = kw.Weight
	}
	return m
}

func splitSentences(text string) []string {
	// Разбиваем по знакам завершения предложения
	parts := sentenceSplit.Split(text, -1)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// Фильтруем слишком короткие (< 20 символов) — обычно мусор
		if len([]rune(p)) >= 20 {
			result = append(result, p)
		}
	}
	return result
}

func tokenizeSentence(s string) []string {
	s = strings.ToLower(s)
	s = nonAlpha.ReplaceAllString(s, " ")
	var tokens []string
	for _, w := range strings.Fields(s) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(w) >= 3 {
			tokens = append(tokens, w)
		}
	}
	return tokens
}

func fallbackPost(a domain.Article) string {
	desc := a.Description
	runes := []rune(desc)
	if len(runes) > 300 {
		desc = string(runes[:300]) + "…"
	}
	return fmt.Sprintf("<b>%s</b>\n\n%s", html.EscapeString(a.Title), html.EscapeString(desc))
}

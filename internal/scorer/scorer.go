// Package scorer реализует линейную модель оценки статей с онлайн-обучением.
//
// Архитектура «мини-нейросети»:
//
//	Параметры модели: веса ключевых слов (хранятся в PostgreSQL).
//
//	Скор статьи:
//	  keyword_score = sum(matched_weights) / (1 + len(tokens))    ← из Postgres весов
//	  es_score      = 0.6 * ES_BM25 + 0.4 * liked_similarity     ← из Elasticsearch
//	  relevance     = 0.5 * keyword_score + 0.5 * es_score
//	  popularity    = exp(-ln2 * age_h / half_life) * feed_weight
//	  final         = α * relevance + β * popularity
//
//	Онлайн-обучение (при каждой реакции):
//	  w_i = clamp(w_i + η * signal, min, max)    ← правило Перцептрона
//
//	Батч-обучение (раз в сутки, см. batch/retrainer.go):
//	  delta = tanh(total_signal / sqrt(events))   ← сглаженный сигнал за 7 дней
package scorer

import (
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
	"github.com/alex-pyslar/Feedium/internal/domain"
)

var (
	nonAlpha   = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	whitespace = regexp.MustCompile(`\s+`)
)

// Positive/negative emoji-реакции Telegram.
var positiveEmoji = map[string]bool{
	"👍": true, "❤": true, "❤️": true, "🔥": true,
	"🎉": true, "🏆": true, "✅": true, "💯": true,
}
var negativeEmoji = map[string]bool{
	"👎": true, "💩": true, "🤮": true, "❌": true, "😴": true,
}

// Scorer хранит логику скоринга и обновления весов.
type Scorer struct {
	log *zap.Logger
	cfg config.ScoringConfig
}

func New(cfg config.ScoringConfig, log *zap.Logger) *Scorer {
	return &Scorer{cfg: cfg, log: log}
}

// ScoreAll вычисляет скоры для набора статей.
//
//   - keywords    — ключевые слова с весами из PostgreSQL (один раз за цикл).
//   - esScores    — карта article_id → ES-скор [0,1] из search.Client.Score;
//     nil если ES недоступен (scorer использует нейтральный 0.5).
func (s *Scorer) ScoreAll(
	articles []domain.Article,
	keywords []domain.Keyword,
	esScores map[int64]float64,
) []domain.ScoredArticle {
	kwIndex := make(map[string]domain.Keyword, len(keywords))
	for _, kw := range keywords {
		kwIndex[kw.Word] = kw
	}

	scored := make([]domain.ScoredArticle, 0, len(articles))
	for _, a := range articles {
		esScore := 0.5 // нейтральный при недоступном ES
		if esScores != nil {
			if v, ok := esScores[a.ID]; ok {
				esScore = v
			}
		}
		scored = append(scored, s.scoreOne(a, kwIndex, esScore))
	}
	return scored
}

// scoreOne вычисляет скор одной статьи.
func (s *Scorer) scoreOne(a domain.Article, kwIndex map[string]domain.Keyword, esScore float64) domain.ScoredArticle {
	tokens := tokenize(a.Title + " " + a.Description)
	tokenSet := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = true
	}

	var matched []domain.Keyword
	var weightSum float64
	for word := range tokenSet {
		if kw, ok := kwIndex[word]; ok {
			matched = append(matched, kw)
			weightSum += kw.Weight
		}
	}

	keywordScore := 0.0
	if len(tokenSet) > 0 {
		keywordScore = weightSum / float64(1+len(tokenSet))
	}

	relevance := 0.5*keywordScore + 0.5*esScore
	popularity := s.popularityScore(a)
	final := s.cfg.RelevanceWeight*relevance + s.cfg.PopularityWeight*popularity

	a.RelevanceScore = relevance
	a.PopularityScore = popularity
	a.FinalScore = final

	return domain.ScoredArticle{Article: a, MatchedKeywords: matched}
}

// popularityScore = exp(-ln2 * age_hours / half_life) * feed_weight
func (s *Scorer) popularityScore(a domain.Article) float64 {
	if a.PublishedAt == nil {
		return 0
	}
	age := time.Since(*a.PublishedAt).Hours()
	if age < 0 {
		age = 0
	}
	recency := math.Exp(-math.Log(2) * age / s.cfg.RecencyHalfLifeHours)
	return recency * a.FeedWeight
}

// ComputeWeightUpdates вычисляет обновлённые веса ключевых слов (онлайн-обучение).
func (s *Scorer) ComputeWeightUpdates(keywords []domain.Keyword, netSignal float64) map[int]float64 {
	if netSignal == 0 || len(keywords) == 0 {
		return nil
	}
	updates := make(map[int]float64, len(keywords))
	for _, kw := range keywords {
		newW := clamp(
			kw.Weight+s.cfg.LearningRate*netSignal,
			s.cfg.MinKeywordWeight,
			s.cfg.MaxKeywordWeight,
		)
		if newW != kw.Weight {
			updates[kw.ID] = newW
		}
	}
	return updates
}

// ClassifyReactions разбивает список emoji на позитивные/негативные.
func ClassifyReactions(emojis []string) (positive, negative int) {
	for _, e := range emojis {
		switch {
		case positiveEmoji[e]:
			positive++
		case negativeEmoji[e]:
			negative++
		}
	}
	return
}

// NetSignal вычисляет суммарный сигнал из добавленных реакций.
func (s *Scorer) NetSignal(addedPos, addedNeg int) float64 {
	return float64(addedPos)*s.cfg.PositiveRewardDelta +
		float64(addedNeg)*s.cfg.NegativeRewardDelta
}

// ExtractNewWords возвращает слова из текста, которых нет в known.
func ExtractNewWords(text string, known map[string]domain.Keyword) []string {
	tokens := tokenize(text)
	seen := make(map[string]bool)
	var novel []string
	for _, t := range tokens {
		if !seen[t] && !isStopWord(t) {
			if _, exists := known[t]; !exists {
				novel = append(novel, t)
			}
			seen[t] = true
		}
	}
	return novel
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	text = nonAlpha.ReplaceAllString(text, " ")
	text = whitespace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var tokens []string
	for _, w := range strings.Fields(text) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(w) >= 3 && !isStopWord(w) {
			tokens = append(tokens, w)
		}
	}
	return tokens
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func isStopWord(w string) bool { return stopWords[w] }

var stopWords = map[string]bool{
	// EN
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "has": true,
	"have": true, "was": true, "that": true, "this": true, "with": true,
	"from": true, "they": true, "will": true, "been": true, "its": true,
	"more": true, "also": true, "into": true, "than": true, "then": true,
	"some": true, "when": true, "what": true, "which": true, "about": true,
	"would": true, "there": true, "their": true, "were": true, "your": true,
	// RU
	"это": true, "как": true, "для": true, "что": true, "или": true,
	"его": true, "так": true, "уже": true, "еще": true, "ещё": true,
	"при": true, "все": true, "она": true, "они": true, "был": true,
	"нет": true, "где": true, "там": true, "тут": true,
	"тоже": true, "когда": true, "если": true, "того": true, "этого": true,
}

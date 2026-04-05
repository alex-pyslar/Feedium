// Package scorer — HTTP-клиент для scorer-сервиса (Python FastAPI).
// Если scorer недоступен, возвращает fallback-скор на основе свежести и веса ленты.
package scorer

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// Client вызывает scorer API для оценки релевантности статей.
type Client struct {
	url      string
	http     *http.Client
	log      *zap.Logger
	halfLife float64 // часов, для fallback
}

func New(url string, halfLifeHours float64, log *zap.Logger) *Client {
	return &Client{
		url:      url,
		http:     &http.Client{Timeout: 5 * time.Second},
		log:      log,
		halfLife: halfLifeHours,
	}
}

type scoreReq struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	FeedWeight  float64 `json:"feed_weight"`
	AgeHours    float64 `json:"age_hours"`
}

type scoreResp struct {
	Score float64 `json:"score"`
}

// Score запрашивает оценку одной статьи.
// При недоступности scorer API возвращает fallback: exp(-ln2 × age / halfLife) × feedWeight.
func (c *Client) Score(ctx context.Context, a domain.Article) float64 {
	age := 0.0
	if a.PublishedAt != nil {
		age = time.Since(*a.PublishedAt).Hours()
		if age < 0 {
			age = 0
		}
	}

	body, _ := json.Marshal(scoreReq{
		Title:       a.Title,
		Description: a.Description,
		FeedWeight:  a.FeedWeight,
		AgeHours:    age,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/score", bytes.NewReader(body))
	if err != nil {
		return c.fallback(age, a.FeedWeight)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Debug("scorer unavailable, using fallback", zap.Error(err))
		return c.fallback(age, a.FeedWeight)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Warn("scorer returned non-200", zap.Int("status", resp.StatusCode))
		return c.fallback(age, a.FeedWeight)
	}

	var r scoreResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return c.fallback(age, a.FeedWeight)
	}
	return r.Score
}

// ScoreAll оценивает список статей и возвращает map[articleID]score.
func (c *Client) ScoreAll(ctx context.Context, articles []domain.Article) map[int64]float64 {
	scores := make(map[int64]float64, len(articles))
	for _, a := range articles {
		scores[a.ID] = c.Score(ctx, a)
	}
	return scores
}

// Healthy проверяет доступность scorer API.
func (c *Client) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// fallback: popularity-based score используется когда scorer API недоступен.
func (c *Client) fallback(ageHours, feedWeight float64) float64 {
	hl := c.halfLife
	if hl <= 0 {
		hl = 24.0
	}
	score := math.Exp(-math.Log(2)*ageHours/hl) * feedWeight
	if score > 1.0 {
		return 1.0
	}
	if score < 0 {
		return 0
	}
	return score
}

// Package search управляет индексом статей в Elasticsearch.
//
// Роль ES в системе: полнотекстовый поиск и BM25-релевантность.
//
// Три вида операций:
//
//  1. Индексирование — каждая новая статья попадает в ES со всем текстом.
//     Это позволяет искать статьи по любому слову через SearchArticles.
//
//  2. Скоринг — для каждой новой статьи вычисляется комбинированный скор [0,1]:
//     - BM25 против текущего списка ключевых слов из Postgres (60%)
//     - more_like_this против статей, которые пользователи отметили лайком (40%)
//     Этот скор передаётся в scorer и заменяет ручной TF-IDF.
//
//  3. Поиск — SearchArticles позволяет находить статьи по произвольному запросу.
//     Можно использовать для будущего API или дебаггинга.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

const indexName = "articles"

// SearchResult — результат полнотекстового поиска.
type SearchResult struct {
	ArticleID int64   `json:"article_id"`
	Title     string  `json:"title"`
	Link      string  `json:"link"`
	Score     float64 `json:"score"`
}

// Client — клиент Elasticsearch.
type Client struct {
	es  *elasticsearch.Client
	log *zap.Logger
}

// New создаёт клиент и убеждается что индекс существует.
func New(addr string, log *zap.Logger) (*Client, error) {
	cfg := elasticsearch.Config{Addresses: []string{addr}}
	es, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create es client: %w", err)
	}
	c := &Client{es: es, log: log}
	if err := c.ensureIndex(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

// IndexArticle добавляет статью в индекс. Вызывается при появлении новой статьи.
func (c *Client) IndexArticle(ctx context.Context, a domain.Article) error {
	doc := map[string]any{
		"article_id":   a.ID,
		"title":        a.Title,
		"description":  a.Description,
		"link":         a.Link,
		"feed_id":      a.FeedID,
		"final_score":  a.FinalScore,
		"liked":        false,
		"published_at": publishedAtStr(a),
	}
	body, _ := json.Marshal(doc)

	res, err := c.es.Index(indexName,
		bytes.NewReader(body),
		c.es.Index.WithDocumentID(fmt.Sprintf("%d", a.ID)),
		c.es.Index.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("es index: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("es index: %s", res.String())
	}
	return nil
}

// MarkLiked помечает статью понравившейся — она войдёт в профиль more_like_this.
// Вызывается при позитивной реакции пользователя.
func (c *Client) MarkLiked(ctx context.Context, articleID int64) error {
	body, _ := json.Marshal(map[string]any{
		"doc": map[string]any{"liked": true},
	})
	res, err := c.es.Update(indexName, fmt.Sprintf("%d", articleID),
		bytes.NewReader(body),
		c.es.Update.WithContext(ctx),
	)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("es mark liked: %s", res.String())
	}
	return nil
}

// Score возвращает комбинированный ES-скор статьи [0, 1]:
//   - 60% BM25 статьи против списка ключевых слов (прямой поиск по тексту)
//   - 40% сходство с понравившимися статьями (more_like_this)
//
// Используется scorer-ом вместо ручного TF-IDF.
func (c *Client) Score(ctx context.Context, a domain.Article, keywords []domain.Keyword) (float64, error) {
	bm25, err := c.bm25Score(ctx, a.ID, extractWords(keywords))
	if err != nil {
		c.log.Warn("es bm25 score", zap.Int64("id", a.ID), zap.Error(err))
		bm25 = 0.5
	}

	liked, err := c.likedSimilarity(ctx, a)
	if err != nil {
		c.log.Warn("es liked similarity", zap.Int64("id", a.ID), zap.Error(err))
		liked = 0.5
	}

	return 0.6*bm25 + 0.4*liked, nil
}

// SearchArticles выполняет полнотекстовый поиск по статьям.
// Возвращает до size результатов, отсортированных по релевантности.
func (c *Client) SearchArticles(ctx context.Context, query string, size int) ([]SearchResult, error) {
	q := map[string]any{
		"query": map[string]any{
			"multi_match": map[string]any{
				"query":  query,
				"fields": []string{"title^3", "description"},
				"type":   "best_fields",
			},
		},
		"size": size,
		"_source": []string{"article_id", "title", "link"},
	}
	body, _ := json.Marshal(q)

	res, err := c.es.Search(
		c.es.Search.WithContext(ctx),
		c.es.Search.WithIndex(indexName),
		c.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("es search: %s", res.String())
	}

	var raw struct {
		Hits struct {
			Hits []struct {
				Score  float64 `json:"_score"`
				Source struct {
					ArticleID int64  `json:"article_id"`
					Title     string `json:"title"`
					Link      string `json:"link"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(raw.Hits.Hits))
	for _, h := range raw.Hits.Hits {
		results = append(results, SearchResult{
			ArticleID: h.Source.ArticleID,
			Title:     h.Source.Title,
			Link:      h.Source.Link,
			Score:     h.Score,
		})
	}
	return results, nil
}

// DeleteOldArticles удаляет статьи старше daysOld дней (TTL-очистка).
func (c *Client) DeleteOldArticles(ctx context.Context, daysOld int) error {
	q := map[string]any{
		"query": map[string]any{
			"range": map[string]any{
				"published_at": map[string]any{
					"lt": fmt.Sprintf("now-%dd/d", daysOld),
				},
			},
		},
	}
	body, _ := json.Marshal(q)
	res, err := c.es.DeleteByQuery(
		[]string{indexName},
		bytes.NewReader(body),
		c.es.DeleteByQuery.WithContext(ctx),
	)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("es delete old: %s", res.String())
	}
	return nil
}

// ---- внутренние методы ---------------------------------------------------

// bm25Score возвращает нормализованный BM25-скор статьи против списка слов.
// ES сам вычисляет TF-IDF/BM25 — точнее нашего ручного расчёта.
func (c *Client) bm25Score(ctx context.Context, articleID int64, words []string) (float64, error) {
	if len(words) == 0 {
		return 0.5, nil
	}
	q := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				// filter не влияет на скор, фильтрует только по ID
				"filter": []map[string]any{
					{"ids": map[string]any{"values": []string{fmt.Sprintf("%d", articleID)}}},
				},
				// must влияет на скор — ES считает BM25 для этого документа
				"must": map[string]any{
					"multi_match": map[string]any{
						"query":  strings.Join(words, " "),
						"fields": []string{"title^2", "description"},
						"type":   "best_fields",
					},
				},
			},
		},
		"size":    1,
		"_source": false,
	}
	return c.singleDocScore(ctx, q)
}

// likedSimilarity возвращает more_like_this скор статьи против liked=true документов.
func (c *Client) likedSimilarity(ctx context.Context, a domain.Article) (float64, error) {
	q := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"must": map[string]any{
					"more_like_this": map[string]any{
						"fields":               []string{"title", "description"},
						"like":                 a.Title + " " + a.Description,
						"min_term_freq":        1,
						"min_doc_freq":         1,
						"max_query_terms":      25,
						"minimum_should_match": "20%",
					},
				},
				"filter": []map[string]any{
					{"term": map[string]any{"liked": true}},
				},
			},
		},
		"size":    1,
		"_source": false,
	}
	score, err := c.singleDocScore(ctx, q)
	if err != nil {
		return 0.5, err
	}
	// Нет liked-статей → холодный старт → нейтральный скор
	return score, nil
}

// singleDocScore выполняет запрос и возвращает нормализованный скор первого документа.
func (c *Client) singleDocScore(ctx context.Context, q map[string]any) (float64, error) {
	body, _ := json.Marshal(q)
	res, err := c.es.Search(
		c.es.Search.WithContext(ctx),
		c.es.Search.WithIndex(indexName),
		c.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return 0.5, err
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return 0.5, nil
	}
	if res.IsError() {
		return 0.5, fmt.Errorf("es query: %s", res.String())
	}

	var result struct {
		Hits struct {
			Total struct{ Value int } `json:"total"`
			Hits  []struct {
				Score float64 `json:"_score"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return 0.5, err
	}
	if len(result.Hits.Hits) == 0 {
		return 0, nil
	}
	return sigmoidNorm(result.Hits.Hits[0].Score), nil
}

// ensureIndex создаёт индекс с маппингом, если его нет.
func (c *Client) ensureIndex(ctx context.Context) error {
	res, err := c.es.Indices.Exists([]string{indexName}, c.es.Indices.Exists.WithContext(ctx))
	if err != nil {
		return err
	}
	io.Copy(io.Discard, res.Body) //nolint:errcheck
	res.Body.Close()
	if res.StatusCode == 200 {
		return nil
	}

	mapping := `{
	  "mappings": {
	    "properties": {
	      "article_id":   {"type": "long"},
	      "title":        {"type": "text", "analyzer": "standard"},
	      "description":  {"type": "text", "analyzer": "standard"},
	      "link":         {"type": "keyword"},
	      "published_at": {"type": "date"},
	      "feed_id":      {"type": "integer"},
	      "final_score":  {"type": "float"},
	      "liked":        {"type": "boolean"}
	    }
	  }
	}`
	res, err = c.es.Indices.Create(indexName,
		c.es.Indices.Create.WithBody(strings.NewReader(mapping)),
		c.es.Indices.Create.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("create index: %s", res.String())
	}
	c.log.Info("elasticsearch index created", zap.String("index", indexName))
	return nil
}

// ---- helpers -------------------------------------------------------------

func extractWords(keywords []domain.Keyword) []string {
	words := make([]string, len(keywords))
	for i, kw := range keywords {
		words[i] = kw.Word
	}
	return words
}

func publishedAtStr(a domain.Article) string {
	if a.PublishedAt != nil {
		return a.PublishedAt.Format("2006-01-02T15:04:05Z")
	}
	return ""
}

// sigmoidNorm нормализует произвольный ES score в [0, 1] через sigmoid.
func sigmoidNorm(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x/5.0))
}

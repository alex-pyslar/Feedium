// Package media управляет хранением изображений статей в MinIO.
//
// Роль MinIO в системе: объектное хранилище для изображений из RSS.
//
// Поток:
//  1. RSS-fetcher извлекает image_url из enclosure/media:content статьи.
//  2. media.Client скачивает изображение по URL и сохраняет в MinIO.
//  3. При публикации в Telegram scheduler достаёт байты из MinIO
//     и отправляет как фото (sendPhoto) вместо текстового сообщения.
//
// Структура объектов в MinIO:
//
//	bucket: article-images
//	key:    articles/{article_id}.{ext}   (jpg/png/webp)
package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"

	"github.com/alex-pyslar/Feedium/internal/config"
)

// Client управляет MinIO.
type Client struct {
	mc     *minio.Client
	bucket string
	http   *http.Client
	log    *zap.Logger
}

// New создаёт клиент MinIO и убеждается что бакет существует.
func New(ctx context.Context, cfg config.MediaConfig, log *zap.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	c := &Client{
		mc:     mc,
		bucket: cfg.Bucket,
		http:   &http.Client{Timeout: 15 * time.Second},
		log:    log,
	}
	if err := c.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// StoreFromURL скачивает изображение по imageURL и сохраняет в MinIO.
// Возвращает ключ объекта (например "articles/42.jpg").
// Если изображение недоступно или не является картинкой — возвращает "".
func (c *Client) StoreFromURL(ctx context.Context, imageURL string, articleID int64) (string, error) {
	if imageURL == "" {
		return "", nil
	}

	// Скачиваем изображение
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image download status %d", resp.StatusCode)
	}

	// Проверяем Content-Type — только изображения
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("not an image: %s", ct)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // max 10 MB
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}

	ext := extFromContentType(ct)
	key := fmt.Sprintf("articles/%d%s", articleID, ext)

	_, err = c.mc.PutObject(ctx, c.bucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: ct},
	)
	if err != nil {
		return "", fmt.Errorf("minio put: %w", err)
	}

	c.log.Debug("image stored",
		zap.Int64("article_id", articleID),
		zap.String("key", key),
		zap.Int("bytes", len(data)),
	)
	return key, nil
}

// GetBytes возвращает байты изображения из MinIO по ключу.
// Используется при отправке фото в Telegram.
func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("minio get: %w", err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return data, nil
}

// DeleteObject удаляет объект из MinIO (при очистке старых статей).
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	return c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}

// ensureBucket создаёт бакет если его нет.
func (c *Client) ensureBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("create bucket %s: %w", c.bucket, err)
	}
	c.log.Info("minio bucket created", zap.String("bucket", c.bucket))
	return nil
}

func extFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return filepath.Ext(ct) // fallback
	}
}

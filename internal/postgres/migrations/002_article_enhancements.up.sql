-- Добавляем поля для суммаризации и медиа-контента

ALTER TABLE articles
    ADD COLUMN IF NOT EXISTS content   TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS image_url TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS image_key TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS summary   TEXT    NOT NULL DEFAULT '';

COMMENT ON COLUMN articles.content   IS 'Полный текст статьи из RSS item.Content, используется для суммаризации';
COMMENT ON COLUMN articles.image_url IS 'Оригинальный URL изображения из RSS (enclosure/media:content)';
COMMENT ON COLUMN articles.image_key IS 'Ключ объекта в MinIO (articles/{id}.jpg), пусто если нет изображения';
COMMENT ON COLUMN articles.summary   IS 'Telegram-пост, сгенерированный Claude. Используется вместо description при публикации';

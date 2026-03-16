BEGIN;

-- RSS-ленты
CREATE TABLE IF NOT EXISTS feeds (
    id                      SERIAL PRIMARY KEY,
    name                    TEXT        NOT NULL,
    url                     TEXT        NOT NULL UNIQUE,
    weight                  FLOAT8      NOT NULL DEFAULT 1.0,
    is_active               BOOLEAN     NOT NULL DEFAULT TRUE,
    last_fetched_at         TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Статьи из лент
CREATE TABLE IF NOT EXISTS articles (
    id               BIGSERIAL PRIMARY KEY,
    feed_id          INT         NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    guid             TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    description      TEXT,
    link             TEXT        NOT NULL,
    published_at     TIMESTAMPTZ,
    fetched_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    relevance_score  FLOAT8      NOT NULL DEFAULT 0.0,
    popularity_score FLOAT8      NOT NULL DEFAULT 0.0,
    final_score      FLOAT8      NOT NULL DEFAULT 0.0,
    is_posted        BOOLEAN     NOT NULL DEFAULT FALSE,
    UNIQUE (feed_id, guid)
);

CREATE INDEX IF NOT EXISTS idx_articles_final_score   ON articles (final_score DESC);
CREATE INDEX IF NOT EXISTS idx_articles_published_at  ON articles (published_at DESC);
CREATE INDEX IF NOT EXISTS idx_articles_is_posted     ON articles (is_posted);
CREATE INDEX IF NOT EXISTS idx_articles_feed_id       ON articles (feed_id);

-- Обучаемые веса ключевых слов (мини-нейросеть — линейная модель)
CREATE TABLE IF NOT EXISTS keywords (
    id          SERIAL PRIMARY KEY,
    word        TEXT        NOT NULL UNIQUE,
    weight      FLOAT8      NOT NULL DEFAULT 1.0,
    hit_count   BIGINT      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_keywords_word ON keywords (word);

-- Связь статья → ключевые слова (для обратного прохода при реакции)
CREATE TABLE IF NOT EXISTS article_keywords (
    article_id  BIGINT  NOT NULL REFERENCES articles(id)  ON DELETE CASCADE,
    keyword_id  INT     NOT NULL REFERENCES keywords(id)  ON DELETE CASCADE,
    PRIMARY KEY (article_id, keyword_id)
);

-- Опубликованные в Telegram сообщения
CREATE TABLE IF NOT EXISTS posted_messages (
    id                          BIGSERIAL PRIMARY KEY,
    article_id                  BIGINT      NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    telegram_msg_id             INT         NOT NULL,
    chat_id                     BIGINT      NOT NULL,
    posted_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    positive_reactions          INT         NOT NULL DEFAULT 0,
    negative_reactions          INT         NOT NULL DEFAULT 0,
    last_reaction_harvested_at  TIMESTAMPTZ,
    UNIQUE (chat_id, telegram_msg_id)
);

CREATE INDEX IF NOT EXISTS idx_posted_messages_msg_id ON posted_messages (telegram_msg_id);
CREATE INDEX IF NOT EXISTS idx_posted_messages_harvest ON posted_messages (last_reaction_harvested_at);

-- Персистентное состояние планировщика (offset для long-polling Telegram)
CREATE TABLE IF NOT EXISTS scheduler_state (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL
);

INSERT INTO scheduler_state (key, value) VALUES ('telegram_update_offset', '0')
ON CONFLICT (key) DO NOTHING;

COMMIT;

# Feedium

RSS-агрегатор с публикацией в Telegram и адаптивной моделью релевантности.

Feedium парсит RSS-ленты, оценивает статьи по релевантности и популярности, публикует лучшие в Telegram-канал и обучается на основе emoji-реакций подписчиков.

## Возможности

- **Парсинг RSS** — поддержка любых лент, настраиваемый вес каждой ленты
- **Скоринг статей** — линейная модель: ключевые слова (PostgreSQL) + BM25 (Elasticsearch)
- **Онлайн-обучение** — веса ключевых слов обновляются в реальном времени при каждой emoji-реакции (правило Перцептрона)
- **Батч-переобучение** — ночной пересчёт весов по истории из ClickHouse (map-reduce, формула `tanh`)
- **Summarizer** — краткое изложение статьи для Telegram-поста:
  - `local` — встроенный экстрактивный алгоритм, без внешних зависимостей
  - `openai` — OpenAI-совместимый API: Ollama, LM Studio, vLLM
- **Изображения** — скачивает и хранит картинки из RSS в MinIO, прикрепляет к постам
- **Полностью offline** — работает без внешних API при использовании локального LLM

## Стек

| Компонент | Роль |
|---|---|
| PostgreSQL | Операционные данные: ленты, статьи, ключевые слова, реакции |
| Elasticsearch | BM25-поиск, профиль «понравившихся» статей |
| ClickHouse | Аналитика событий, история реакций для батч-переобучения |
| MinIO | Хранилище изображений статей |
| Telegram Bot API | Публикация постов, сбор emoji-реакций |

## Архитектура

```
cmd/server/
└── main.go           # Composition root: создаёт все сервисы, wire

internal/
├── domain/           # Доменные модели и интерфейсы-репозитории (порты)
├── app/              # Прикладной слой (use cases)
│   ├── fetch.go      # FetchService: RSS → score → publish
│   ├── reaction.go   # ReactionService: реакции + harvest
│   └── retrain.go    # RetrainService: батч map-reduce переобучение
├── postgres/         # Адаптер PostgreSQL (реализует domain.*Repository)
├── analytics/        # Адаптер ClickHouse
├── search/           # Адаптер Elasticsearch
├── media/            # Адаптер MinIO
├── telegram/         # Telegram Bot API
├── rss/              # RSS-парсер (gofeed)
├── scorer/           # Скоринговая модель + онлайн-обучение
├── summarizer/       # Суммаризатор: local / openai-compatible
├── config/           # Загрузка config.toml + env
└── scheduler/        # Тонкий cron-оркестратор
```

## Быстрый старт

### 1. Зависимости

Поднимите базы данных. Пример с Docker:

```bash
# PostgreSQL
docker run -d --name pg -e POSTGRES_PASSWORD=pass -p 5432:5432 postgres:16

# Elasticsearch
docker run -d --name es -e "discovery.type=single-node" -p 9200:9200 elasticsearch:8.13.0

# ClickHouse
docker run -d --name ch -p 9000:9000 clickhouse/clickhouse-server:24

# MinIO
docker run -d --name minio -e MINIO_ROOT_USER=admin -e MINIO_ROOT_PASSWORD=password \
  -p 9000:9000 -p 9001:9001 minio/minio server /data --console-address ":9001"
```

### 2. Переменные окружения

Скопируйте `.env.example` в `.env` и заполните:

```bash
cp .env.example .env
```

```env
DATABASE_DSN=postgres://user:pass@localhost:5432/feedium?sslmode=disable
TELEGRAM_TOKEN=your_bot_token
TELEGRAM_CHANNEL_ID=-1001234567890
ELASTICSEARCH_ADDR=http://localhost:9200
CLICKHOUSE_DSN=clickhouse://user:pass@localhost:9000/default
MINIO_ENDPOINT=localhost:9000
MINIO_ACCESS_KEY=admin
MINIO_SECRET_KEY=password

# Только если summarizer.provider = "openai":
SUMMARIZER_API_URL=http://localhost:11434/v1   # Ollama
SUMMARIZER_API_KEY=                            # пусто для Ollama
```

### 3. Миграции

```bash
psql $DATABASE_DSN -f migrations/001_init.sql
psql $DATABASE_DSN -f migrations/002_article_enhancements.sql
```

### 4. Запуск

```bash
go run ./cmd/server
# или с кастомным конфигом:
go run ./cmd/server -config /path/to/config.toml
```

### Docker

```bash
docker build -t feedium .
docker run --env-file .env feedium
```

## Конфигурация

Бизнес-логика приложения описывается в `config.toml`. Подключения и секреты — **только** в `.env`.

### Ленты

```toml
[[feeds]]
name   = "Hacker News"
url    = "https://news.ycombinator.com/rss"
weight = 1.2   # вес ленты влияет на popularity score

[[feeds]]
name   = "Habr"
url    = "https://habr.com/ru/rss/articles/"
weight = 1.0
```

### Скоринг

```toml
[scoring]
relevance_weight      = 0.7   # вес релевантности (ключевые слова + ES BM25)
popularity_weight     = 0.3   # вес популярности (новизна + вес ленты)
min_score_to_post     = 0.3   # порог публикации
learning_rate         = 0.05  # скорость обучения весов
recency_half_life_hours = 24  # период полураспада popularity
```

### Суммаризатор

```toml
[summarizer]
provider = "local"   # встроенный, без внешних сервисов
# provider = "openai"  # для Ollama / LM Studio / vLLM
model      = "llama3.2"
max_tokens = 400
```

При `provider = "openai"` укажите `SUMMARIZER_API_URL` в `.env`.

### Планировщик

```toml
[scheduler]
fetch_cron    = "*/30 * * * *"  # загрузка RSS каждые 30 мин
reaction_cron = "*/5 * * * *"   # reconciliation реакций каждые 5 мин

[clickhouse]
batch_cron        = "0 3 * * *"  # батч-переобучение каждую ночь
batch_window_days = 7            # история реакций для пересчёта
```

## Как работает модель

```
keyword_score = Σ(matched_weights) / (1 + len(tokens))    ← веса из PostgreSQL
es_score      = 0.6 × BM25 + 0.4 × liked_similarity       ← Elasticsearch
relevance     = 0.5 × keyword_score + 0.5 × es_score
popularity    = exp(−ln2 × age_h / half_life) × feed_weight
final_score   = α × relevance + β × popularity
```

**Онлайн-обучение** (при каждой реакции):
```
w_i = clamp(w_i + η × signal, min, max)    # Perceptron rule
```

**Батч-переобучение** (ночью, по ClickHouse):
```
delta = tanh(total_signal / √event_count)   # сглаженный сигнал за 7 дней
```

## Лицензия

MIT

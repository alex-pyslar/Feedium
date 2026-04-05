# Feedium

RSS-агрегатор с ML-системой ранжирования и публикацией в Telegram.

Feedium парсит RSS-ленты, оценивает статьи с помощью LightGBM LambdaRank поверх bge-m3 эмбеддингов, публикует лучшие в Telegram-канал и дообучает модель на основе emoji-реакций подписчиков.

## Архитектура

```
┌─────────────────────────────────────────────────────────┐
│  core (Go)                                              │
│  RSS → PostgreSQL → scorer → Telegram                  │
│                         ↑                               │
│              POST /score (per article)                  │
└─────────────────────────────────────────────────────────┘
                          │
              ┌───────────┴───────────┐
              │  scorer (Python)      │
              │  bge-m3 → FAISS       │
              │  → LightGBM predict   │
              └───────────────────────┘
                          ↑
              POST /reload │ (после обучения)
                          │
              ┌───────────┴───────────┐
              │  trainer (Python)     │
              │  PostgreSQL → embed   │
              │  → LambdaRank train   │
              │  → FAISS build        │
              └───────────────────────┘
```

### ML pipeline

| Компонент | Роль |
|-----------|------|
| **BAAI/bge-m3** | Мультиязычные sentence embeddings 1024-dim (фиксированный, pre-trained) |
| **LightGBM LambdaRank** | Ранжировщик, оптимизирующий NDCG напрямую |
| **FAISS IndexFlatIP** | Поиск похожих статей для similarity features |
| **BART zero-shot** | Quality evaluator для pseudo-labels (опционально) |
| **Optuna** | Байесовский HPO (фаза learning/mature) |

### Feature vector (1032 признака)

```
[0:1024]  bge-m3 embedding (L2-нормированный)
[1024]    feed_weight
[1025]    age_hours / 168    (до 1 недели)
[1026]    age_hours / 720    (до 1 месяца)
[1027]    title_len / 200
[1028]    desc_len / 500
[1029]    title_words / 30
[1030]    avg cosine sim к liked-статьям  (FAISS top-5)
[1031]    avg cosine sim к disliked-статьям (FAISS top-5)
```

### Фазы обучения

| Фаза | Реакций | Алгоритм |
|------|---------|---------|
| cold | < 10 | Recency heuristic: `exp(-ln2 × age/24h) × feed_weight` |
| bootstrap | 10–200 | LightGBM LambdaRank (дефолтные параметры) |
| learning | 200–2000 | + Optuna HPO (30 trials) |
| mature | 2000+ | + Optuna HPO + incremental FAISS / LightGBM updates |

## Стек

| Сервис | Технологии |
|--------|-----------|
| core | Go 1.25, pgx/v5, gofeed, robfig/cron, Bot API 7.0+ |
| scorer | Python 3.12, FastAPI, sentence-transformers, lightgbm, faiss-cpu |
| trainer | Python 3.12, sentence-transformers, lightgbm, faiss-cpu, optuna, asyncpg |
| БД | PostgreSQL 16 |

## Быстрый старт

### 1. Настройка

Создайте `.env` в корне:

```env
POSTGRES_PASSWORD=feedium
TELEGRAM_TOKEN=your_bot_token
TELEGRAM_CHANNEL_ID=-1001234567890
```

### 2. Конфигурация лент (`core/config.toml`)

```toml
[telegram]
max_messages_per_run   = 5
update_timeout_seconds = 30

[scoring]
scorer_url            = "http://scorer:8000"
min_score_to_post     = 0.3
recency_half_life_hours = 24.0

[scheduler]
fetch_cron    = "*/30 * * * *"
reaction_cron = "*/5 * * * *"

[[feeds]]
name   = "Hacker News"
url    = "https://news.ycombinator.com/rss"
weight = 1.2

[[feeds]]
name   = "Habr"
url    = "https://habr.com/ru/rss/articles/"
weight = 1.0
```

### 3. Запуск

```bash
# Собрать и запустить всё
docker compose up -d

# Проверить статус
docker compose ps
curl http://localhost:8000/health   # scorer
curl http://localhost:8081/         # core admin

# Запустить обучение вручную (после накопления реакций)
docker compose run --rm trainer

# Плановое обучение каждые 12ч
docker compose run -d trainer python main.py --schedule --interval 12
```

## Структура проекта

```
Feedium/
├── core/                          # Go сервис
│   ├── cmd/server/main.go         # Точка входа
│   ├── config.toml                # Конфигурация
│   └── internal/
│       ├── domain/                # Модели и интерфейсы
│       ├── app/                   # Use cases (fetch, reaction)
│       ├── postgres/              # БД адаптер
│       ├── rss/                   # RSS парсер
│       ├── scorer/                # HTTP клиент к scorer
│       ├── telegram/              # Telegram бот
│       ├── scheduler/             # Cron
│       └── config/                # Конфиг
│
├── scorer/                        # Python FastAPI
│   ├── main.py                    # FastAPI app
│   ├── pipeline.py                # Inference pipeline
│   ├── features.py                # Feature vector (должен совпадать с trainer!)
│   └── faiss_store.py             # FAISS wrapper
│
├── trainer/                       # Python ML
│   ├── main.py                    # Оркестрация обучения
│   ├── config.py                  # Настройки
│   ├── data/
│   │   ├── loader.py              # asyncpg загрузчик
│   │   └── quality.py             # BART quality evaluator
│   ├── models/
│   │   ├── bge.py                 # bge-m3 с numpy кэшем
│   │   ├── faiss_index.py         # FAISS IndexFlatIP
│   │   └── ranker.py              # LightGBM LambdaRank
│   ├── pipeline/
│   │   ├── embed.py               # Batch encoding
│   │   ├── features.py            # Feature matrix [N, 1032]
│   │   ├── labels.py              # Метки + query groups
│   │   ├── train.py               # Optuna HPO + финальное обучение
│   │   └── evaluate.py            # NDCG@K, MAP, MRR
│   └── online/
│       └── updater.py             # Incremental updates
│
└── docker-compose.yml
```

## Важные замечания

**Синхронизация features.py** — файлы `scorer/features.py` и `trainer/pipeline/features.py` должны производить идентичный feature vector. Изменение одного требует изменения другого.

**FAISS индексы** — создаются trainer'ом и читаются scorer'ом через shared volume `/models/`. После первого обучения scorer автоматически получает similarity features.

**Cold start** — пока реакций меньше 10, scorer возвращает recency heuristic. Система работает без обучения с первого запуска.

**Reload без перезапуска** — trainer вызывает `POST /reload` после обучения. Scorer hot-reloads модель без downtime.

## Лицензия

MIT

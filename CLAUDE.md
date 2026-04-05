# CLAUDE.md — github.com/alex-pyslar/Feedium

> Индекс проекта для Claude. Обновляй при изменении архитектуры.

## Архитектура (3 сервиса)

```
core/      — Go: RSS парсер → PostgreSQL → Telegram
scorer/    — Python FastAPI: инференс LightGBM + bge-m3
trainer/   — Python ML: обучение ранжировщика на данных из БД
```

Все сервисы оркестрирует `docker-compose.yml` в корне.
Shared volume `models_data` (`/models/`) — trainer записывает, scorer читает.

---

## core/ (Go 1.25)

**Entry:** `cmd/server/main.go`
**Config:** `config.toml` (логика) + env vars (секреты)

### Packages

| Пакет | Роль |
|-------|------|
| `cmd/server` | Composition root — DI wiring, HTTP admin (:8081) |
| `internal/domain` | Модели + интерфейсы (нет внешних зависимостей) |
| `internal/config` | TOML + env overlay |
| `internal/postgres` | Реализует все domain-репозитории одним `Store` |
| `internal/rss` | RSS/Atom парсер (gofeed), параллельный (семафор 5 лент) |
| `internal/scorer` | HTTP клиент к scorer API + fallback heuristic |
| `internal/telegram` | Публикация + long-polling реакций (Bot API 7.0+) |
| `internal/app` | Use cases: `FetchService`, `ReactionService` |
| `internal/scheduler` | Cron-оркестрация (robfig/cron) |
| `internal/logger` | zap + lumberjack |

### Ключевые типы (`internal/domain/models.go`)

| Тип | Ключевые поля |
|-----|--------------|
| `Feed` | ID, URL, Weight, IsActive, LastFetchedAt |
| `Article` | ID, FeedID, FeedWeight, GUID, Title, Description, Link, FinalScore, IsPosted |
| `PostedMessage` | ArticleID, TelegramMsgID, ChatID, PositiveReactions, NegativeReactions |

### Переменные окружения (core)

| Переменная | Обяз. | По умолчанию |
|-----------|-------|-------------|
| `DATABASE_DSN` | ✅ | — |
| `TELEGRAM_TOKEN` | ✅ | — |
| `TELEGRAM_CHANNEL_ID` | ✅ | — |
| `SCORER_URL` | — | `http://scorer:8000` |
| `ADMIN_ADDR` | — | `:8081` |

### Admin HTTP (`:8081`)

| Endpoint | Действие |
|----------|---------|
| `POST /admin/fetch` | Ручной запуск RSS → score → publish |

---

## scorer/ (Python FastAPI)

**Entry:** `main.py` → FastAPI app
**Pipeline:** `pipeline.py` → `ScoringPipeline`

### Компоненты инференса

```
text → bge-m3 embed (1024-dim)
     → FAISS liked/disliked similarity features
     → feature vector [1032]
     → LightGBM predict
     → sigmoid → score ∈ [0, 1]
```

Если `ranker.lgbm` не существует — fallback: `exp(-ln2 × age/24h) × feed_weight`

### Файлы моделей (`/models/`)

| Файл | Содержимое |
|------|-----------|
| `ranker.lgbm` | LightGBM LambdaRank модель |
| `liked.faiss` | FAISS индекс liked-статей (IndexFlatIP) |
| `disliked.faiss` | FAISS индекс disliked-статей |
| `embed_cache.npz` | Кэш bge-m3 эмбеддингов (trainer) |
| `phase.txt` | Текущая фаза обучения |

### Endpoints

| Endpoint | Метод | Описание |
|----------|-------|---------|
| `GET /health` | GET | `{status, model_loaded}` |
| `POST /score` | POST | `{title, description, feed_weight, age_hours}` → `{score, source}` |
| `POST /reload` | POST | Перезагрузить модель с диска |

### Переменные окружения (scorer)

| Переменная | По умолчанию |
|-----------|-------------|
| `MODELS_DIR` | `/models` |
| `EMBED_MODEL` | `BAAI/bge-m3` |

---

## trainer/ (Python ML)

**Entry:** `main.py`
**Команда:** `python main.py` (однократно) или `--schedule --interval 12`

### Архитектура ML pipeline

```
PostgreSQL
    ↓ load_articles()           data/loader.py
ArticleRow list
    ↓ BGEEncoder.encode()       models/bge.py  ← кэш в embed_cache.npz
embeddings [N, 1024]
    ↓ build_faiss_indices()     online/updater.py
liked.faiss + disliked.faiss
    ↓ build_feature_matrix()    pipeline/features.py
X [N, 1032]
    ↓ build_labels_and_groups() pipeline/labels.py
y [N] (0/1/2) + groups (query sizes)
    ↓ train()                   pipeline/train.py
LGBMRanker → ranker.lgbm
    ↓ POST /reload
Scorer hot-reload
```

### Feature vector [1032]

| Индекс | Признак |
|--------|---------|
| `0:1024` | bge-m3 embedding (L2-норм.) |
| `1024` | feed_weight |
| `1025` | age_hours / 168 |
| `1026` | age_hours / 720 |
| `1027` | len(title) / 200 |
| `1028` | len(desc) / 500 |
| `1029` | word_count(title) / 30 |
| `1030` | FAISS sim_liked (avg top-5) |
| `1031` | FAISS sim_disliked (avg top-5) |

### Фазы обучения

| Фаза | Реакций | Действие |
|------|---------|---------|
| `cold` | < 10 | Пропуск — scorer использует recency heuristic |
| `bootstrap` | 10–200 | LightGBM дефолтные параметры |
| `learning` | 200–2000 | + Optuna HPO (30 trials) |
| `mature` | 2000+ | + Optuna HPO + incremental updates |

### Метки для LambdaRank (0/1/2)

- С реакциями: `reaction_signal × confidence → 0/1/2`
  - `signal = (pos-neg)/(total+1)`, `confidence = min(1, total/10)`
- Без реакций: quality evaluator (BART zero-shot) → pseudo-label

### Переменные окружения (trainer)

| Переменная | По умолчанию | Описание |
|-----------|-------------|---------|
| `DATABASE_DSN` | — | PostgreSQL DSN (обязательно) |
| `MODELS_DIR` | `/models` | Директория моделей |
| `EMBED_MODEL` | `BAAI/bge-m3` | Embedding модель |
| `QUALITY_MODEL` | `facebook/bart-large-mnli` | Quality evaluator (`""` — откл.) |
| `SCORER_URL` | `http://scorer:8000` | Для POST /reload |
| `OPTUNA_TRIALS` | `30` | Кол-во trials Optuna |
| `MIN_REACTIONS` | `10` | Порог cold start |
| `MAX_ARTICLES` | `50000` | Максимум статей из БД |

---

## Схема БД (core/internal/postgres/migrations/)

| Таблица | Назначение |
|---------|-----------|
| `feeds` | RSS ленты с весами |
| `articles` | Статьи: title, description, link, final_score |
| `posted_messages` | Опубликованные сообщения + счётчики реакций |
| `scheduler_state` | Telegram offset и прочее состояние |

---

## Граф зависимостей (core)

```
cmd/server
  ├── config, logger, postgres
  ├── rss, scorer (HTTP client), telegram
  ├── app (FetchService, ReactionService)
  └── scheduler

app/fetch    → domain, rss, scorer, telegram, config
app/reaction → domain, telegram
scorer       → domain, config
postgres     → domain
telegram     → config
domain       → (только stdlib)
```

---

## Запуск

```bash
# Полный стек
docker compose up -d

# Только core + scorer (без обучения)
docker compose up -d postgres scorer core

# Запустить обучение вручную
docker compose run --rm trainer

# Плановое обучение каждые 12ч
docker compose run -d trainer python main.py --schedule --interval 12
```

```bash
# Локальная разработка core
cd core && go build ./... && go vet ./... && go test ./...
```

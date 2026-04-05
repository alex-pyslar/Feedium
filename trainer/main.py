"""
Trainer — обучает LightGBM LambdaRank на данных из PostgreSQL.

Архитектура:
  1. BGE-M3 (BAAI/bge-m3, pre-trained, не обучается):
       Мультиязычные sentence embeddings 1024-dim.
       Результаты кэшируются в /models/embed_cache.npz.

  2. Quality Evaluator (facebook/bart-large-mnli, опционально):
       Zero-shot классификация для pseudo-labels статей без реакций.
       Отключается через QUALITY_MODEL="" или "none".

  3. RelevanceRanker (LightGBM LambdaRank, обучается):
       MLP поверх bge-m3 + tabular features + FAISS similarity.
       Оптимизирует NDCG напрямую.
       Сохраняет /models/ranker.lgbm

  4. FAISS indices (/models/liked.faiss, /models/disliked.faiss):
       Базы векторов понравившихся/не понравившихся статей.
       Используются как similarity features.

Фазы обучения:
  cold      (<10 реакций)  — пропускаем, scorer использует heuristic
  bootstrap (10-200)       — базовое LightGBM обучение
  learning  (200-2000)     — + Optuna HPO
  mature    (2000+)        — + Optuna HPO + incremental updates

Запуск:
  python main.py                  # однократный прогон
  python main.py --schedule       # каждые 12ч
  python main.py --online         # инкрементальный режим
"""
from __future__ import annotations

import argparse
import asyncio
import logging
import time
from pathlib import Path

import httpx

from config import settings
from data import load_articles
from data.quality import build_quality_evaluator
from models.bge import BGEEncoder
from online.updater import build_faiss_indices
from pipeline.embed import compute_embeddings
from pipeline.features import build_feature_matrix
from pipeline.labels import build_labels_and_groups
from pipeline.train import determine_phase, train

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s — %(message)s",
)
logger = logging.getLogger("trainer")


# ── Основной цикл обучения ────────────────────────────────────────────────────

async def train_once() -> None:
    settings.ensure_models_dir()

    # 1. Загружаем данные
    rows = await load_articles(settings.database_dsn, settings.max_articles)
    if not rows:
        logger.warning("No articles found — skipping")
        return

    total_reactions = sum(1 for r in rows if r.has_reactions)
    phase = determine_phase(total_reactions)
    logger.info(
        "Dataset: %d articles, %d with reactions → phase=%s",
        len(rows), total_reactions, phase,
    )

    # 2. Сохраняем текущую фазу
    settings.phase_path.write_text(phase)

    if phase == "cold":
        logger.info("Cold start — scorer will use recency heuristic")
        return

    # 3. Quality evaluator (pseudo-labels для статей без реакций)
    quality_eval = build_quality_evaluator(settings.quality_model)

    # 4. Эмбеддинги (с кэшем)
    embeddings = compute_embeddings(
        rows,
        model_name=settings.embed_model,
        cache_path=settings.embed_cache_path,
        batch_size=settings.embed_batch_size,
    )

    # 5. FAISS индексы (liked / disliked)
    encoder = BGEEncoder(settings.embed_model, settings.embed_cache_path, settings.embed_batch_size)
    liked_index, disliked_index = build_faiss_indices(
        rows, embeddings, encoder.embedding_dim,
        settings.liked_faiss_path,
        settings.disliked_faiss_path,
    )

    # 6. Метки и группы
    y, groups = build_labels_and_groups(rows, quality_eval)

    # 7. Feature matrix [N, 1032]
    X = build_feature_matrix(rows, embeddings, liked_index, disliked_index)
    logger.info("Feature matrix: %s", X.shape)

    # 8. Обучение
    ranker = train(
        X, y, groups,
        n_reactions=total_reactions,
        optuna_trials=settings.optuna_trials,
        early_stopping=settings.lgbm_early_stopping,
    )

    if ranker is None:
        logger.info("Training skipped (cold start)")
        return

    # 9. Сохраняем модель
    ranker.save(settings.ranker_path)
    logger.info("Model saved: %s", settings.ranker_path)

    # 10. Уведомляем scorer перезагрузить модель
    _reload_scorer()


def _reload_scorer() -> None:
    try:
        resp = httpx.post(f"{settings.scorer_url}/reload", timeout=10)
        if resp.status_code == 200:
            logger.info("Scorer reloaded successfully")
        else:
            logger.warning("Scorer reload returned %d", resp.status_code)
    except Exception as exc:
        logger.warning("Could not notify scorer: %s", exc)


# ── Entrypoint ────────────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(description="Feedium Trainer")
    parser.add_argument(
        "--schedule", action="store_true",
        help="Запускать обучение каждые N часов",
    )
    parser.add_argument(
        "--interval", type=int, default=12,
        help="Интервал между запусками в часах (default: 12)",
    )
    args = parser.parse_args()

    if args.schedule:
        logger.info("Scheduled mode: interval=%dh", args.interval)
        while True:
            try:
                asyncio.run(train_once())
            except Exception as exc:
                logger.error("Training failed: %s", exc, exc_info=True)
            logger.info("Next run in %dh", args.interval)
            time.sleep(args.interval * 3600)
    else:
        asyncio.run(train_once())


if __name__ == "__main__":
    main()

"""
Inference pipeline — полный цикл оценки статьи.

Этапы:
  1. bge-m3 embedding
  2. FAISS similarity features
  3. Feature vector [1032]
  4. LightGBM predict
  5. Нормировка в [0, 1]

Если модель не загружена — fallback (recency × feed_weight).
"""
from __future__ import annotations

import logging
import math
import os
from pathlib import Path

import numpy as np

from faiss_store import FaissStore
from features import build_feature_vector

logger = logging.getLogger(__name__)

MODELS_DIR      = Path(os.getenv("MODELS_DIR", "/models"))
EMBED_MODEL     = os.getenv("EMBED_MODEL", "BAAI/bge-m3")
EMBED_DIM       = 1024


class ScoringPipeline:
    """Загружает все компоненты и выполняет инференс."""

    def __init__(self) -> None:
        self._embedder    = None
        self._ranker      = None
        self._faiss       = FaissStore()
        self._model_ready = False

    # ── Инициализация ─────────────────────────────────────────────────────────

    def load_embedder(self) -> None:
        """Загружает bge-m3 (вызывается один раз на старте)."""
        from sentence_transformers import SentenceTransformer
        logger.info("Loading embedding model: %s", EMBED_MODEL)
        self._embedder = SentenceTransformer(EMBED_MODEL)
        logger.info("Embedder ready, dim=%d", self._embedder.get_sentence_embedding_dimension())

    def reload_ranker(self) -> bool:
        """
        Перезагружает LightGBM + FAISS с диска.
        Вызывается при старте и по POST /reload.

        Returns:
            True если модель успешно загружена
        """
        ranker_path = MODELS_DIR / "ranker.lgbm"
        if not ranker_path.exists():
            logger.info("No ranker model at %s — using fallback heuristic", ranker_path)
            self._ranker = None
            self._model_ready = False
            return False

        try:
            import lightgbm as lgb
            self._ranker = lgb.Booster(model_file=str(ranker_path))
            logger.info("Loaded LightGBM ranker from %s", ranker_path)
        except Exception as exc:
            logger.error("Failed to load ranker: %s", exc)
            self._ranker = None
            self._model_ready = False
            return False

        # FAISS (опционально — не ошибка если нет)
        self._faiss.load(
            MODELS_DIR / "liked.faiss",
            MODELS_DIR / "disliked.faiss",
        )

        self._model_ready = True
        return True

    # ── Инференс ──────────────────────────────────────────────────────────────

    def score(
        self,
        title: str,
        description: str,
        feed_weight: float,
        age_hours: float,
    ) -> tuple[float, str]:
        """
        Оценивает релевантность статьи.

        Returns:
            (score ∈ [0, 1], source: "model" | "fallback")
        """
        if not self._model_ready or self._embedder is None:
            return _fallback(age_hours, feed_weight), "fallback"

        # Эмбеддинг
        text = f"{title} {description}".strip()
        embedding = self._embedder.encode(
            [text],
            normalize_embeddings=True,
            convert_to_numpy=True,
        )[0].astype(np.float32)  # [1024]

        # FAISS similarity features
        sim_liked    = self._faiss.sim_liked(embedding)
        sim_disliked = self._faiss.sim_disliked(embedding)

        # Feature vector
        x = build_feature_vector(
            embedding, feed_weight, age_hours,
            title, description,
            sim_liked, sim_disliked,
        )

        # LightGBM predict
        raw = float(self._ranker.predict(x.reshape(1, -1))[0])
        # LambdaRank scores не ограничены [0,1] — нормируем через sigmoid
        score = float(1.0 / (1.0 + math.exp(-raw)))
        return score, "model"

    @property
    def model_loaded(self) -> bool:
        return self._model_ready


def _fallback(age_hours: float, feed_weight: float) -> float:
    """Recency heuristic: exp(-ln2 * age / 24h) × feed_weight."""
    score = math.exp(-math.log(2) * age_hours / 24.0) * feed_weight
    return max(0.0, min(1.0, score))

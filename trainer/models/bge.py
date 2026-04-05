"""
BGE-M3 энкодер — SOTA мультиязычные эмбеддинги (1024-dim).

BAAI/bge-m3 поддерживает русский и английский языки, выдаёт
L2-нормированные векторы (cosine similarity = inner product).

Кэширует вычисленные эмбеддинги в .npz файл, чтобы не
пересчитывать их при повторных запусках обучения.
"""
from __future__ import annotations

import logging
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


class BGEEncoder:
    """Обёртка над sentence-transformers с numpy-кэшем."""

    def __init__(self, model_name: str, cache_path: Path, batch_size: int = 64):
        self.model_name = model_name
        self.cache_path = cache_path
        self.batch_size = batch_size
        self._model = None
        # cache: article_id (int) → embedding vector (1024,)
        self._cache: dict[int, np.ndarray] = {}
        self._load_cache()

    # ── Инициализация ─────────────────────────────────────────────────────────

    def _ensure_model(self) -> None:
        if self._model is not None:
            return
        from sentence_transformers import SentenceTransformer
        logger.info("Loading embedding model: %s", self.model_name)
        self._model = SentenceTransformer(self.model_name)
        logger.info("Model loaded, dim=%d", self.embedding_dim)

    def _load_cache(self) -> None:
        if not self.cache_path.exists():
            return
        try:
            data = np.load(self.cache_path, allow_pickle=False)
            ids = data["ids"]
            embs = data["embeddings"]
            self._cache = {int(k): embs[i] for i, k in enumerate(ids)}
            logger.info("Loaded %d cached embeddings", len(self._cache))
        except Exception as exc:
            logger.warning("Could not load embedding cache: %s", exc)
            self._cache = {}

    def save_cache(self) -> None:
        if not self._cache:
            return
        self.cache_path.parent.mkdir(parents=True, exist_ok=True)
        ids = np.array(list(self._cache.keys()), dtype=np.int64)
        embs = np.stack(list(self._cache.values()), axis=0)
        np.savez_compressed(self.cache_path, ids=ids, embeddings=embs)
        logger.info("Saved %d embeddings to cache", len(self._cache))

    # ── Кодирование ───────────────────────────────────────────────────────────

    @property
    def embedding_dim(self) -> int:
        self._ensure_model()
        return self._model.get_sentence_embedding_dimension()

    def encode(
        self,
        article_ids: list[int],
        texts: list[str],
    ) -> np.ndarray:
        """
        Кодирует тексты, используя кэш для уже вычисленных.

        Args:
            article_ids: идентификаторы статей (ключи кэша)
            texts: соответствующие тексты

        Returns:
            np.ndarray shape [N, 1024], float32, L2-нормированные
        """
        assert len(article_ids) == len(texts)
        n = len(article_ids)

        # Определяем что нужно вычислить
        missing_idx = [i for i, aid in enumerate(article_ids) if aid not in self._cache]
        if missing_idx:
            self._ensure_model()
            missing_texts = [texts[i] for i in missing_idx]
            logger.info("Computing embeddings for %d/%d articles…", len(missing_idx), n)
            vecs = self._model.encode(
                missing_texts,
                batch_size=self.batch_size,
                show_progress_bar=True,
                normalize_embeddings=True,
                convert_to_numpy=True,
            )
            for i, idx in enumerate(missing_idx):
                aid = article_ids[idx]
                self._cache[aid] = vecs[i].astype(np.float32)
            self.save_cache()

        result = np.stack([self._cache[aid] for aid in article_ids], axis=0)
        return result.astype(np.float32)

"""
FAISS IndexFlatIP — индекс сходства для liked/disliked статей.

Использует inner product (= cosine similarity для L2-нормированных векторов).
Поддерживает инкрементальное добавление векторов.
"""
from __future__ import annotations

import logging
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


class FaissIndex:
    """Обёртка над faiss.IndexFlatIP с поддержкой save/load."""

    def __init__(self, dim: int, path: Path):
        import faiss
        self.dim = dim
        self.path = path
        self._index = faiss.IndexFlatIP(dim)
        self._count = 0

    @classmethod
    def load(cls, dim: int, path: Path) -> "FaissIndex":
        """Загружает индекс из файла или создаёт пустой."""
        obj = cls.__new__(cls)
        obj.dim = dim
        obj.path = path
        if path.exists():
            import faiss
            try:
                obj._index = faiss.read_index(str(path))
                obj._count = obj._index.ntotal
                logger.info("Loaded FAISS index from %s (%d vectors)", path, obj._count)
            except Exception as exc:
                logger.warning("Could not load FAISS index %s: %s — creating empty", path, exc)
                import faiss as _faiss
                obj._index = _faiss.IndexFlatIP(dim)
                obj._count = 0
        else:
            import faiss
            obj._index = faiss.IndexFlatIP(dim)
            obj._count = 0
        return obj

    def add(self, vectors: np.ndarray) -> None:
        """Добавляет L2-нормированные векторы."""
        vecs = np.ascontiguousarray(vectors, dtype=np.float32)
        self._index.add(vecs)
        self._count += len(vecs)

    def search(self, query: np.ndarray, k: int = 5) -> tuple[np.ndarray, np.ndarray]:
        """
        Ищет k ближайших соседей.

        Returns:
            (similarities, indices) — shape [N, k]
        """
        if self._count == 0:
            n = len(query)
            return np.zeros((n, k), dtype=np.float32), np.full((n, k), -1, dtype=np.int64)
        q = np.ascontiguousarray(query, dtype=np.float32)
        k_actual = min(k, self._count)
        sims, idxs = self._index.search(q, k_actual)
        if k_actual < k:
            pad_s = np.zeros((len(query), k - k_actual), dtype=np.float32)
            pad_i = np.full((len(query), k - k_actual), -1, dtype=np.int64)
            sims = np.hstack([sims, pad_s])
            idxs = np.hstack([idxs, pad_i])
        return sims, idxs

    def mean_similarity(self, query: np.ndarray, k: int = 5) -> np.ndarray:
        """Среднее сходство с k ближайшими соседями. Shape [N]."""
        sims, _ = self.search(query, k)
        return sims.mean(axis=1)

    def save(self) -> None:
        import faiss
        self.path.parent.mkdir(parents=True, exist_ok=True)
        faiss.write_index(self._index, str(self.path))
        logger.info("Saved FAISS index to %s (%d vectors)", self.path, self._count)

    def reset(self) -> None:
        import faiss
        self._index = faiss.IndexFlatIP(self.dim)
        self._count = 0

    @property
    def size(self) -> int:
        return self._count

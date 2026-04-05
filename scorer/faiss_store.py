"""FAISS индексы для similarity features в scorer."""
from __future__ import annotations

import logging
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


class FaissStore:
    """Загружает и хранит два FAISS индекса: liked и disliked."""

    def __init__(self) -> None:
        self._liked    = None
        self._disliked = None

    def load(self, liked_path: Path, disliked_path: Path) -> None:
        self._liked    = _load_index(liked_path,    "liked")
        self._disliked = _load_index(disliked_path, "disliked")

    def sim_liked(self, embedding: np.ndarray, k: int = 5) -> float:
        """Среднее cosine similarity с k ближайшими liked-статьями."""
        return _mean_sim(self._liked, embedding, k)

    def sim_disliked(self, embedding: np.ndarray, k: int = 5) -> float:
        """Среднее cosine similarity с k ближайшими disliked-статьями."""
        return _mean_sim(self._disliked, embedding, k)

    @property
    def available(self) -> bool:
        return self._liked is not None or self._disliked is not None


def _load_index(path: Path, name: str):
    if not path.exists():
        logger.info("No %s FAISS index at %s", name, path)
        return None
    try:
        import faiss
        idx = faiss.read_index(str(path))
        logger.info("Loaded %s FAISS index: %d vectors", name, idx.ntotal)
        return idx
    except Exception as exc:
        logger.warning("Could not load %s FAISS index: %s", name, exc)
        return None


def _mean_sim(index, embedding: np.ndarray, k: int) -> float:
    if index is None or index.ntotal == 0:
        return 0.0
    q = np.ascontiguousarray(embedding.reshape(1, -1), dtype=np.float32)
    k_actual = min(k, index.ntotal)
    sims, _ = index.search(q, k_actual)
    return float(sims[0].mean())

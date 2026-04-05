"""Пакетное вычисление эмбеддингов для всех статей."""
from __future__ import annotations

import logging
from pathlib import Path

import numpy as np

from data.loader import ArticleRow
from models.bge import BGEEncoder

logger = logging.getLogger(__name__)


def compute_embeddings(
    rows: list[ArticleRow],
    model_name: str,
    cache_path: Path,
    batch_size: int = 64,
) -> np.ndarray:
    """
    Возвращает эмбеддинги shape [N, 1024].

    Уже вычисленные эмбеддинги берёт из кэша (cache_path).
    """
    encoder = BGEEncoder(model_name, cache_path, batch_size)
    ids   = [r.article_id for r in rows]
    texts = [r.text       for r in rows]
    embeddings = encoder.encode(ids, texts)
    logger.info("Embeddings ready: %s", embeddings.shape)
    return embeddings

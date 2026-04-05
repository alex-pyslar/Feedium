"""
Feature engineering для LightGBM LambdaRank.

Итоговый feature vector (1032 признака):

  [0:1024]  bge-m3 embedding (L2-нормированный, float32)
  [1024]    feed_weight
  [1025]    age_hours_norm  (age_hours / 168, 1 неделя = 1.0)
  [1026]    age_days_norm   (age_hours / 720, 1 месяц  = 1.0)
  [1027]    title_len_norm  (len(title) / 200)
  [1028]    desc_len_norm   (len(desc) / 500)
  [1029]    title_words_norm (word count / 30)
  [1030]    sim_liked       (avg cosine sim с liked FAISS, 0 если нет индекса)
  [1031]    sim_disliked    (avg cosine sim с disliked FAISS, 0 если нет индекса)
"""
from __future__ import annotations

import numpy as np

from data.loader import ArticleRow
from models.faiss_index import FaissIndex

FEATURE_DIM = 1032
EMBED_DIM   = 1024


def build_feature_matrix(
    rows: list[ArticleRow],
    embeddings: np.ndarray,
    liked_index: FaissIndex | None,
    disliked_index: FaissIndex | None,
) -> np.ndarray:
    """
    Строит матрицу признаков [N, 1032].

    Args:
        rows:            список статей
        embeddings:      bge-m3 эмбеддинги [N, 1024]
        liked_index:     FAISS индекс понравившихся статей (может быть None)
        disliked_index:  FAISS индекс не понравившихся статей (может быть None)

    Returns:
        X: np.ndarray shape [N, 1032], dtype float32
    """
    n = len(rows)
    X = np.zeros((n, FEATURE_DIM), dtype=np.float32)

    # ── bge-m3 эмбеддинги ────────────────────────────────────────────────────
    X[:, :EMBED_DIM] = embeddings

    # ── Табличные признаки ───────────────────────────────────────────────────
    for i, r in enumerate(rows):
        ah = r.age_hours
        X[i, EMBED_DIM + 0] = float(r.feed_weight)
        X[i, EMBED_DIM + 1] = min(ah / 168.0, 5.0)       # до 5 недель → clamp
        X[i, EMBED_DIM + 2] = min(ah / 720.0, 3.0)       # до 3 месяцев → clamp
        X[i, EMBED_DIM + 3] = min(len(r.title) / 200.0, 1.0)
        X[i, EMBED_DIM + 4] = min(len(r.description) / 500.0, 1.0)
        X[i, EMBED_DIM + 5] = min(len(r.title.split()) / 30.0, 1.0)

    # ── FAISS similarity features ─────────────────────────────────────────────
    if liked_index is not None and liked_index.size > 0:
        X[:, EMBED_DIM + 6] = liked_index.mean_similarity(embeddings, k=5)

    if disliked_index is not None and disliked_index.size > 0:
        X[:, EMBED_DIM + 7] = disliked_index.mean_similarity(embeddings, k=5)

    return X


def feature_names() -> list[str]:
    names = [f"emb_{i}" for i in range(EMBED_DIM)]
    names += [
        "feed_weight",
        "age_hours_norm",
        "age_days_norm",
        "title_len_norm",
        "desc_len_norm",
        "title_words_norm",
        "sim_liked",
        "sim_disliked",
    ]
    return names

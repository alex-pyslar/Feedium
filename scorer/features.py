"""
Real-time feature computation для scorer.

Должна производить ТОЧНО такой же feature vector, как trainer/pipeline/features.py.
Изменения в одном файле требуют синхронного изменения в другом.

Feature vector (1032):
  [0:1024]  bge-m3 embedding
  [1024]    feed_weight
  [1025]    age_hours_norm  (/ 168)
  [1026]    age_days_norm   (/ 720)
  [1027]    title_len_norm  (/ 200)
  [1028]    desc_len_norm   (/ 500)
  [1029]    title_words_norm (/ 30)
  [1030]    sim_liked
  [1031]    sim_disliked
"""
from __future__ import annotations

import numpy as np

FEATURE_DIM = 1032
EMBED_DIM   = 1024


def build_feature_vector(
    embedding: np.ndarray,
    feed_weight: float,
    age_hours: float,
    title: str,
    description: str,
    sim_liked: float,
    sim_disliked: float,
) -> np.ndarray:
    """Строит feature vector [1032] для одной статьи."""
    x = np.zeros(FEATURE_DIM, dtype=np.float32)
    x[:EMBED_DIM] = embedding

    i = EMBED_DIM
    x[i + 0] = float(feed_weight)
    x[i + 1] = min(age_hours / 168.0, 5.0)
    x[i + 2] = min(age_hours / 720.0, 3.0)
    x[i + 3] = min(len(title) / 200.0, 1.0)
    x[i + 4] = min(len(description) / 500.0, 1.0)
    x[i + 5] = min(len(title.split()) / 30.0, 1.0)
    x[i + 6] = float(sim_liked)
    x[i + 7] = float(sim_disliked)

    return x

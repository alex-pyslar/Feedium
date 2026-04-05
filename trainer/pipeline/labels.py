"""
Генерация меток и групп для LightGBM LambdaRank.

Метки (0/1/2):
  - Статьи С реакциями:   reaction_signal × confidence → 0/1/2
  - Статьи БЕЗ реакций:   quality_evaluator → pseudo-label 0/1/2

Группы (query_id):
  - Все статьи из одного дня публикации = один query
  - Группы отсортированы по времени для корректного time-based CV
"""
from __future__ import annotations

import logging
from typing import Callable

import numpy as np

from data.loader import ArticleRow
from data.quality import score_to_label

logger = logging.getLogger(__name__)


def build_labels_and_groups(
    rows: list[ArticleRow],
    quality_evaluator: Callable[[str], float] | None = None,
) -> tuple[np.ndarray, list[int]]:
    """
    Строит вектор меток y и список размеров групп.

    Args:
        rows:               статьи (отсортированные по published_at DESC)
        quality_evaluator:  callable(text) → float или None

    Returns:
        y:      np.ndarray[int32] shape [N]
        groups: list[int] — размеры групп (сумма = N)
    """
    n = len(rows)
    y = np.zeros(n, dtype=np.int32)

    # Метки
    no_reaction_count = 0
    pseudo_label_count = 0

    for i, r in enumerate(rows):
        if r.has_reactions:
            y[i] = r.relevance_label()
        else:
            no_reaction_count += 1
            if quality_evaluator is not None:
                score = quality_evaluator(r.text)
                y[i] = score_to_label(score)
                pseudo_label_count += 1
            else:
                y[i] = 1  # нейтрально — модель не получит сигнала, но не испортит

    logger.info(
        "Labels: %d reaction-based, %d pseudo-labels, %d neutral",
        n - no_reaction_count, pseudo_label_count, no_reaction_count - pseudo_label_count,
    )

    # LambdaRank требует: все метки ≥ 0, и в каждой группе хотя бы один non-zero
    # Группируем по дню публикации
    groups = _compute_groups(rows)
    return y, groups


def _compute_groups(rows: list[ArticleRow]) -> list[int]:
    """Группирует статьи по дню публикации → список размеров групп."""
    from collections import Counter
    # Порядок групп должен соответствовать порядку строк в rows
    day_buckets: list[int] = [r.day_bucket() for r in rows]
    # Подсчёт непрерывных серий одного bucket (статьи уже отсортированы по времени)
    groups: list[int] = []
    current_bucket = day_buckets[0]
    count = 1
    for b in day_buckets[1:]:
        if b == current_bucket:
            count += 1
        else:
            groups.append(count)
            current_bucket = b
            count = 1
    groups.append(count)
    assert sum(groups) == len(rows), "Groups size mismatch"
    return groups


def time_based_split(
    X: np.ndarray,
    y: np.ndarray,
    groups: list[int],
    val_frac: float = 0.15,
) -> tuple[np.ndarray, np.ndarray, list[int], np.ndarray, np.ndarray, list[int]]:
    """
    Time-based split: первые (1-val_frac) групп — train, последние — val.

    Предотвращает temporal data leakage (нельзя делать random split по времени).
    """
    n_groups = len(groups)
    n_val_groups = max(1, int(n_groups * val_frac))
    n_train_groups = n_groups - n_val_groups

    sizes = np.array(groups)
    cumsum = np.concatenate([[0], np.cumsum(sizes)])

    train_end = int(cumsum[n_train_groups])

    X_tr,  X_val  = X[:train_end],   X[train_end:]
    y_tr,  y_val  = y[:train_end],   y[train_end:]
    g_tr   = groups[:n_train_groups]
    g_val  = groups[n_train_groups:]

    logger.info(
        "Split: train=%d samples in %d groups, val=%d samples in %d groups",
        len(y_tr), len(g_tr), len(y_val), len(g_val),
    )
    return X_tr, X_val, g_tr, g_val, y_tr, y_val

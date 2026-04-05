"""
Инкрементальное обновление модели при накоплении новых реакций.

Обновляет:
  1. FAISS индексы liked/disliked (добавляет новые векторы)
  2. LightGBM: continue_training на новых данных
  3. Сохраняет файлы и уведомляет scorer через POST /reload

Вызывается из main.py в режиме --online.
"""
from __future__ import annotations

import logging
from pathlib import Path

import numpy as np

from models.faiss_index import FaissIndex
from models.ranker import LGBMRanker
from data.loader import ArticleRow

logger = logging.getLogger(__name__)


def build_faiss_indices(
    rows: list[ArticleRow],
    embeddings: np.ndarray,
    dim: int,
    liked_path: Path,
    disliked_path: Path,
) -> tuple[FaissIndex, FaissIndex]:
    """
    Строит FAISS индексы для liked / disliked статей.

    Статья «liked»:   pos > neg и confidence > 0.3
    Статья «disliked»: neg > pos и confidence > 0.3
    """
    liked_index    = FaissIndex(dim, liked_path)
    disliked_index = FaissIndex(dim, disliked_path)

    liked_vecs:    list[np.ndarray] = []
    disliked_vecs: list[np.ndarray] = []

    for i, r in enumerate(rows):
        if not r.has_reactions:
            continue
        signal     = r.reaction_signal()
        confidence = r.reaction_confidence()
        if confidence < 0.3:
            continue
        if signal > 0.1:
            liked_vecs.append(embeddings[i])
        elif signal < -0.1:
            disliked_vecs.append(embeddings[i])

    if liked_vecs:
        liked_index.add(np.stack(liked_vecs))
        liked_index.save()
    if disliked_vecs:
        disliked_index.add(np.stack(disliked_vecs))
        disliked_index.save()

    logger.info(
        "FAISS indices: %d liked, %d disliked",
        liked_index.size, disliked_index.size,
    )
    return liked_index, disliked_index


def incremental_update(
    ranker: LGBMRanker,
    new_rows: list[ArticleRow],
    new_embeddings: np.ndarray,
    liked_index: FaissIndex,
    disliked_index: FaissIndex,
    ranker_path: Path,
    liked_path: Path,
    disliked_path: Path,
) -> None:
    """
    Дообучает модель на новых данных и обновляет FAISS индексы.

    Args:
        ranker:          текущая модель
        new_rows:        новые статьи С реакциями
        new_embeddings:  их эмбеддинги [M, 1024]
        liked_index:     текущий FAISS liked
        disliked_index:  текущий FAISS disliked
        *_path:          пути для сохранения
    """
    from pipeline.features import build_feature_matrix
    from pipeline.labels import build_labels_and_groups

    if len(new_rows) == 0:
        logger.info("No new data for incremental update")
        return

    logger.info("Incremental update: %d new samples", len(new_rows))

    # 1. Обновляем FAISS
    new_liked:    list[np.ndarray] = []
    new_disliked: list[np.ndarray] = []
    for i, r in enumerate(new_rows):
        sig  = r.reaction_signal()
        conf = r.reaction_confidence()
        if conf < 0.3:
            continue
        if sig > 0.1:
            new_liked.append(new_embeddings[i])
        elif sig < -0.1:
            new_disliked.append(new_embeddings[i])

    if new_liked:
        liked_index.add(np.stack(new_liked))
        liked_index.save()
    if new_disliked:
        disliked_index.add(np.stack(new_disliked))
        disliked_index.save()

    # 2. Continue training
    X_new = build_feature_matrix(new_rows, new_embeddings, liked_index, disliked_index)
    y_new, groups_new = build_labels_and_groups(new_rows)
    ranker.continue_training(X_new, y_new, groups_new, n_estimators=100)
    ranker.save(ranker_path)
    logger.info("Incremental update complete")

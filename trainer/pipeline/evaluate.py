"""
Метрики ранжирования: NDCG@K, MAP, MRR.

Все метрики реализованы без внешних зависимостей (только numpy).
"""
from __future__ import annotations

import numpy as np


def dcg_at_k(relevances: np.ndarray, k: int) -> float:
    """Discounted Cumulative Gain @K."""
    r = relevances[:k]
    if len(r) == 0:
        return 0.0
    gains = (2.0 ** r - 1.0) / np.log2(np.arange(2, len(r) + 2))
    return float(gains.sum())


def ndcg_at_k(relevances: np.ndarray, k: int) -> float:
    """Normalised DCG @K."""
    ideal = np.sort(relevances)[::-1]
    idcg = dcg_at_k(ideal, k)
    if idcg == 0:
        return 0.0
    return dcg_at_k(relevances, k) / idcg


def average_precision(relevances: np.ndarray) -> float:
    """Average Precision для бинарных меток."""
    hits = (relevances > 0).astype(float)
    if hits.sum() == 0:
        return 0.0
    precision_at_k = np.cumsum(hits) / (np.arange(len(hits)) + 1)
    return float((precision_at_k * hits).sum() / hits.sum())


def reciprocal_rank(relevances: np.ndarray) -> float:
    """Mean Reciprocal Rank."""
    for i, r in enumerate(relevances):
        if r > 0:
            return 1.0 / (i + 1)
    return 0.0


def evaluate_ranking(
    y_true: np.ndarray,
    y_pred: np.ndarray,
    groups: list[int],
    k_list: tuple[int, ...] = (5, 10),
) -> dict[str, float]:
    """
    Вычисляет метрики ранжирования по всем группам (queries).

    Args:
        y_true:  истинные метки релевантности [N]
        y_pred:  предсказанные скоры [N]
        groups:  размеры групп (сумма = N)
        k_list:  значения K для NDCG

    Returns:
        dict с усреднёнными метриками по всем queries
    """
    ndcg: dict[int, list[float]] = {k: [] for k in k_list}
    aps: list[float] = []
    rrs: list[float] = []

    offset = 0
    for g in groups:
        true_g  = y_true[offset: offset + g]
        pred_g  = y_pred[offset: offset + g]
        # Сортируем по убыванию предсказанного скора
        order = np.argsort(pred_g)[::-1]
        sorted_true = true_g[order]

        for k in k_list:
            ndcg[k].append(ndcg_at_k(sorted_true, k))
        aps.append(average_precision(sorted_true))
        rrs.append(reciprocal_rank(sorted_true))
        offset += g

    results: dict[str, float] = {}
    for k in k_list:
        results[f"ndcg@{k}"] = float(np.mean(ndcg[k]))
    results["map"]  = float(np.mean(aps))
    results["mrr"]  = float(np.mean(rrs))
    return results

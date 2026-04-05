"""
Обучение LightGBM LambdaRank с Optuna HPO.

Режимы обучения в зависимости от фазы:
  cold     (<10 реакций)   — возвращаем None, scorer использует heuristic
  bootstrap (10-200)       — базовое обучение с дефолтными параметрами
  learning  (200-2000)     — Optuna HPO (30 trials)
  mature    (2000+)        — Optuna HPO + инкрементальные обновления
"""
from __future__ import annotations

import logging
from typing import Any

import numpy as np

from models.ranker import LGBMRanker
from pipeline.evaluate import evaluate_ranking
from pipeline.labels import time_based_split

logger = logging.getLogger(__name__)

# Пространство поиска Optuna
_OPTUNA_SPACE = {
    "learning_rate":      (0.01,  0.15),
    "num_leaves":         (31,    127),
    "min_child_samples":  (5,     30),
    "subsample":          (0.6,   1.0),
    "colsample_bytree":   (0.5,   1.0),
    "reg_alpha":          (1e-4,  1.0),
    "reg_lambda":         (1e-4,  1.0),
}


def determine_phase(n_reactions: int) -> str:
    if n_reactions < 10:
        return "cold"
    if n_reactions < 200:
        return "bootstrap"
    if n_reactions < 2000:
        return "learning"
    return "mature"


def _train_with_params(
    params: dict[str, Any],
    X_tr: np.ndarray,
    y_tr: np.ndarray,
    g_tr: list[int],
    X_val: np.ndarray,
    y_val: np.ndarray,
    g_val: list[int],
    n_estimators: int,
    early_stopping: int,
) -> tuple[LGBMRanker, dict]:
    ranker = LGBMRanker(params)
    history = ranker.train(
        X_tr, y_tr, g_tr,
        X_val, y_val, g_val,
        n_estimators=n_estimators,
        early_stopping=early_stopping,
    )
    return ranker, history


def _optuna_search(
    X_tr: np.ndarray,
    y_tr: np.ndarray,
    g_tr: list[int],
    X_val: np.ndarray,
    y_val: np.ndarray,
    g_val: list[int],
    n_trials: int,
    early_stopping: int,
) -> dict[str, Any]:
    """Байесовский поиск гиперпараметров через Optuna."""
    import optuna
    optuna.logging.set_verbosity(optuna.logging.WARNING)

    def objective(trial: optuna.Trial) -> float:
        params = {
            "learning_rate":     trial.suggest_float("learning_rate", *_OPTUNA_SPACE["learning_rate"], log=True),
            "num_leaves":        trial.suggest_int("num_leaves", *_OPTUNA_SPACE["num_leaves"]),
            "min_child_samples": trial.suggest_int("min_child_samples", *_OPTUNA_SPACE["min_child_samples"]),
            "subsample":         trial.suggest_float("subsample", *_OPTUNA_SPACE["subsample"]),
            "colsample_bytree":  trial.suggest_float("colsample_bytree", *_OPTUNA_SPACE["colsample_bytree"]),
            "reg_alpha":         trial.suggest_float("reg_alpha", *_OPTUNA_SPACE["reg_alpha"], log=True),
            "reg_lambda":        trial.suggest_float("reg_lambda", *_OPTUNA_SPACE["reg_lambda"], log=True),
        }
        ranker = LGBMRanker(params)
        ranker.train(
            X_tr, y_tr, g_tr,
            X_val, y_val, g_val,
            n_estimators=300,
            early_stopping=early_stopping,
        )
        metrics = evaluate_ranking(y_val, ranker.predict(X_val), g_val, k_list=(5, 10))
        return metrics["ndcg@5"]

    study = optuna.create_study(direction="maximize")
    study.optimize(objective, n_trials=n_trials, show_progress_bar=False)
    logger.info("Optuna best NDCG@5=%.4f  params=%s", study.best_value, study.best_params)
    return study.best_params


def train(
    X: np.ndarray,
    y: np.ndarray,
    groups: list[int],
    n_reactions: int,
    optuna_trials: int = 30,
    early_stopping: int = 50,
) -> LGBMRanker | None:
    """
    Полный цикл обучения.

    Returns:
        обученный LGBMRanker или None если слишком мало данных
    """
    phase = determine_phase(n_reactions)
    logger.info("Training phase: %s (reactions=%d)", phase, n_reactions)

    if phase == "cold":
        logger.info("Cold start — skipping training, scorer will use heuristic fallback")
        return None

    # Time-based split
    X_tr, X_val, g_tr, g_val, y_tr, y_val = time_based_split(X, y, groups)

    # Подбор гиперпараметров
    if phase in ("learning", "mature") and optuna_trials > 0:
        logger.info("Running Optuna HPO (%d trials)…", optuna_trials)
        best_params = _optuna_search(X_tr, y_tr, g_tr, X_val, y_val, g_val, optuna_trials, early_stopping)
        n_estimators = 1000
    else:
        best_params = {}
        n_estimators = 500

    # Финальное обучение
    ranker, _ = _train_with_params(
        best_params, X_tr, y_tr, g_tr, X_val, y_val, g_val,
        n_estimators=n_estimators,
        early_stopping=early_stopping,
    )

    # Оценка
    metrics = evaluate_ranking(y_val, ranker.predict(X_val), g_val, k_list=(5, 10))
    logger.info(
        "Val metrics: NDCG@5=%.4f  NDCG@10=%.4f  MAP=%.4f  MRR=%.4f",
        metrics["ndcg@5"], metrics["ndcg@10"], metrics["map"], metrics["mrr"],
    )
    return ranker

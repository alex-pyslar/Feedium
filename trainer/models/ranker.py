"""
LightGBM LambdaRank — обучаемый ранжировщик статей.

LambdaRank напрямую оптимизирует NDCG (listwise ranking).
Значительно лучше MLP на sparse interaction data (<50K примеров):
- Нет проблемы vanishing gradient
- Встроенная работа с mixed features (float + categorical)
- Быстрое обучение и инференс
- Continue training для incremental updates
"""
from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

import numpy as np

logger = logging.getLogger(__name__)

# LightGBM параметры по умолчанию для LambdaRank
_DEFAULT_PARAMS: dict[str, Any] = {
    "objective": "lambdarank",
    "metric": "ndcg",
    "ndcg_eval_at": [5, 10],
    "learning_rate": 0.05,
    "num_leaves": 63,
    "min_child_samples": 5,
    "subsample": 0.8,
    "colsample_bytree": 0.7,
    "reg_alpha": 0.1,
    "reg_lambda": 0.1,
    "n_jobs": -1,
    "verbose": -1,
    "random_state": 42,
}


class LGBMRanker:
    """Обёртка над lightgbm для задачи ранжирования."""

    def __init__(self, params: dict[str, Any] | None = None):
        self.params = {**_DEFAULT_PARAMS, **(params or {})}
        self._model = None

    # ── Обучение ──────────────────────────────────────────────────────────────

    def train(
        self,
        X_train: np.ndarray,
        y_train: np.ndarray,
        groups_train: list[int],
        X_val: np.ndarray | None = None,
        y_val: np.ndarray | None = None,
        groups_val: list[int] | None = None,
        n_estimators: int = 500,
        early_stopping: int = 50,
    ) -> dict[str, list[float]]:
        """
        Обучает ранжировщик.

        Args:
            X_train, y_train, groups_train: обучающая выборка
            X_val, y_val, groups_val: валидационная выборка (опционально)
            n_estimators: максимум деревьев
            early_stopping: стоп при отсутствии улучшений на val

        Returns:
            history: словарь с метриками по эпохам
        """
        import lightgbm as lgb

        train_ds = lgb.Dataset(X_train, label=y_train, group=groups_train)
        valid_sets = [train_ds]
        valid_names = ["train"]

        if X_val is not None and y_val is not None and groups_val is not None:
            val_ds = lgb.Dataset(X_val, label=y_val, group=groups_val, reference=train_ds)
            valid_sets.append(val_ds)
            valid_names.append("val")

        callbacks = [lgb.log_evaluation(period=50)]
        if X_val is not None:
            callbacks.append(lgb.early_stopping(early_stopping, verbose=True))

        evals_result: dict = {}
        self._model = lgb.train(
            self.params,
            train_ds,
            num_boost_round=n_estimators,
            valid_sets=valid_sets,
            valid_names=valid_names,
            callbacks=callbacks,
            evals_result=evals_result,
        )
        logger.info("Training complete. Trees: %d", self._model.num_trees())
        return evals_result

    def continue_training(
        self,
        X_new: np.ndarray,
        y_new: np.ndarray,
        groups_new: list[int],
        n_estimators: int = 100,
    ) -> None:
        """Инкрементальное дообучение на новых данных (continue_training)."""
        if self._model is None:
            raise RuntimeError("Model not trained yet")
        import lightgbm as lgb

        ds = lgb.Dataset(X_new, label=y_new, group=groups_new)
        self._model = lgb.train(
            self.params,
            ds,
            num_boost_round=n_estimators,
            init_model=self._model,
            callbacks=[lgb.log_evaluation(period=20)],
        )
        logger.info("Continue training done. Trees: %d", self._model.num_trees())

    # ── Инференс ──────────────────────────────────────────────────────────────

    def predict(self, X: np.ndarray) -> np.ndarray:
        """Предсказывает relevance scores. Shape [N]."""
        if self._model is None:
            raise RuntimeError("Model not trained/loaded")
        return self._model.predict(X, num_iteration=self._model.best_iteration or -1)

    # ── Персистентность ───────────────────────────────────────────────────────

    def save(self, path: Path) -> None:
        if self._model is None:
            raise RuntimeError("Nothing to save")
        path.parent.mkdir(parents=True, exist_ok=True)
        self._model.save_model(str(path))
        logger.info("Saved LightGBM model to %s", path)

    def load(self, path: Path) -> None:
        import lightgbm as lgb
        self._model = lgb.Booster(model_file=str(path))
        logger.info("Loaded LightGBM model from %s", path)

    @classmethod
    def from_file(cls, path: Path, params: dict | None = None) -> "LGBMRanker":
        obj = cls(params)
        obj.load(path)
        return obj

    @property
    def is_trained(self) -> bool:
        return self._model is not None

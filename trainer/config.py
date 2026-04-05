"""Конфигурация trainer-сервиса из переменных окружения."""
from __future__ import annotations
import os
from pathlib import Path


class Settings:
    # ── Обязательные ──────────────────────────────────────────────────────────
    database_dsn: str = os.environ["DATABASE_DSN"]

    # ── Хранилище моделей (shared volume с scorer) ───────────────────────────
    models_dir: str = os.getenv("MODELS_DIR", "/models")

    # ── Модели ────────────────────────────────────────────────────────────────
    # bge-m3: мультиязычные эмбеддинги 1024-dim (SOTA 2024)
    embed_model: str = os.getenv("EMBED_MODEL", "BAAI/bge-m3")
    # Quality evaluator: BART zero-shot для pseudo-labels; "" — отключить
    quality_model: str = os.getenv("QUALITY_MODEL", "facebook/bart-large-mnli")
    embed_batch_size: int = int(os.getenv("EMBED_BATCH_SIZE", "64"))

    # ── Scorer API ────────────────────────────────────────────────────────────
    scorer_url: str = os.getenv("SCORER_URL", "http://scorer:8000")

    # ── Данные ────────────────────────────────────────────────────────────────
    max_articles: int = int(os.getenv("MAX_ARTICLES", "50000"))
    # Минимум реакций для перехода в supervised-режим
    min_reactions: int = int(os.getenv("MIN_REACTIONS", "10"))

    # ── Обучение ──────────────────────────────────────────────────────────────
    optuna_trials: int = int(os.getenv("OPTUNA_TRIALS", "30"))
    lgbm_early_stopping: int = int(os.getenv("LGBM_EARLY_STOPPING", "50"))

    # ── Пути к файлам (дериваты от models_dir) ───────────────────────────────
    @property
    def ranker_path(self) -> Path:
        return Path(self.models_dir) / "ranker.lgbm"

    @property
    def liked_faiss_path(self) -> Path:
        return Path(self.models_dir) / "liked.faiss"

    @property
    def disliked_faiss_path(self) -> Path:
        return Path(self.models_dir) / "disliked.faiss"

    @property
    def embed_cache_path(self) -> Path:
        return Path(self.models_dir) / "embed_cache.npz"

    @property
    def phase_path(self) -> Path:
        return Path(self.models_dir) / "phase.txt"

    def ensure_models_dir(self) -> None:
        Path(self.models_dir).mkdir(parents=True, exist_ok=True)


settings = Settings()

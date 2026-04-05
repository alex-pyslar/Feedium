"""
Quality Evaluator — вторая нейросеть для pseudo-labels.

Использует facebook/bart-large-mnli (MNLI zero-shot) для оценки
"важности" статьи без реакций пользователей.

Отключается если QUALITY_MODEL="" или "none".
"""
from __future__ import annotations

import logging
from typing import Callable

logger = logging.getLogger(__name__)

# Кандидаты для zero-shot классификации
_POS_LABEL = "важная новость"
_NEG_LABEL  = "неинтересный контент"
_HYPOTHESIS = "Этот текст является {}."


def build_quality_evaluator(model_name: str) -> Callable[[str], float] | None:
    """
    Возвращает callable(text) → float ∈ [0, 1], или None если отключено.

    Args:
        model_name: имя HuggingFace модели, "" / "none" — отключить

    Returns:
        функция оценки или None
    """
    if not model_name or model_name.lower() in ("none", "off", "false"):
        logger.info("Quality evaluator disabled")
        return None

    try:
        from transformers import pipeline as hf_pipeline
        logger.info("Loading quality evaluator: %s", model_name)
        clf = hf_pipeline(
            "zero-shot-classification",
            model=model_name,
            device=-1,  # CPU
        )
        logger.info("Quality evaluator ready")
    except Exception as exc:
        logger.warning("Failed to load quality evaluator (%s): %s — pseudo-labels disabled", model_name, exc)
        return None

    def evaluate(text: str) -> float:
        """Zero-shot классификация: вероятность 'важной новости'."""
        try:
            result = clf(
                text[:512],
                candidate_labels=[_POS_LABEL, _NEG_LABEL],
                hypothesis_template=_HYPOTHESIS,
            )
            idx = result["labels"].index(_POS_LABEL)
            return float(result["scores"][idx])
        except Exception as exc:
            logger.debug("Quality eval failed: %s", exc)
            return 0.5  # нейтральный fallback

    return evaluate


def score_to_label(score: float) -> int:
    """Конвертирует float качества (0-1) в дискретный ярлык (0/1/2)."""
    if score >= 0.65:
        return 2
    if score >= 0.35:
        return 1
    return 0

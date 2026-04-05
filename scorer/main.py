"""
Scorer API — FastAPI сервис оценки релевантности статей.

Архитектура инференса:
  1. BAAI/bge-m3 (1024-dim, pre-trained) — эмбеддинги текста
  2. FAISS IndexFlatIP — similarity features к liked/disliked статьям
  3. LightGBM LambdaRank — основная модель ранжирования

Если обученная модель отсутствует — fallback: recency × feed_weight.
Trainer уведомляет POST /reload после завершения обучения.

ENV:
  MODELS_DIR   — директория с моделями (default: /models)
  EMBED_MODEL  — имя sentence-transformers модели (default: BAAI/bge-m3)
"""
import logging

from fastapi import FastAPI
from pydantic import BaseModel

from pipeline import ScoringPipeline

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s — %(message)s",
)
logger = logging.getLogger("scorer")

app = FastAPI(title="Feedium Scorer API", version="2.0")
pipeline = ScoringPipeline()


@app.on_event("startup")
def startup() -> None:
    pipeline.load_embedder()
    pipeline.reload_ranker()


# ── Схемы ─────────────────────────────────────────────────────────────────────

class ScoreRequest(BaseModel):
    title: str
    description: str  = ""
    feed_weight: float = 1.0
    age_hours: float   = 0.0


class ScoreResponse(BaseModel):
    score: float
    source: str  # "model" | "fallback"


class HealthResponse(BaseModel):
    status: str
    model_loaded: bool


# ── Endpoints ─────────────────────────────────────────────────────────────────

@app.get("/health", response_model=HealthResponse)
def health() -> HealthResponse:
    return HealthResponse(status="ok", model_loaded=pipeline.model_loaded)


@app.post("/reload")
def reload() -> dict:
    """Перезагрузить модель с диска (POST после завершения обучения)."""
    ok = pipeline.reload_ranker()
    return {"model_loaded": ok}


@app.post("/score", response_model=ScoreResponse)
def score(req: ScoreRequest) -> ScoreResponse:
    s, source = pipeline.score(
        req.title, req.description, req.feed_weight, req.age_hours,
    )
    return ScoreResponse(score=s, source=source)

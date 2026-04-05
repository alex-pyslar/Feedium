"""Загрузка обучающих данных из PostgreSQL."""
from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass
from datetime import datetime, timezone

import asyncpg

logger = logging.getLogger(__name__)


@dataclass
class ArticleRow:
    article_id: int
    feed_id: int
    title: str
    description: str
    feed_weight: float
    published_at: datetime | None
    pos: int
    neg: int

    # ── Производные свойства ─────────────────────────────────────────────────

    @property
    def text(self) -> str:
        return f"{self.title} {self.description}".strip()

    @property
    def age_hours(self) -> float:
        if self.published_at is None:
            return 48.0
        now = datetime.now(timezone.utc)
        pub = self.published_at
        if pub.tzinfo is None:
            pub = pub.replace(tzinfo=timezone.utc)
        return max(0.0, (now - pub).total_seconds() / 3600.0)

    @property
    def has_reactions(self) -> bool:
        return (self.pos + self.neg) > 0

    def reaction_signal(self) -> float:
        """Сигнал реакций: (pos-neg)/(total+1) ∈ [-1, 1]."""
        total = self.pos + self.neg
        return (self.pos - self.neg) / (total + 1)

    def reaction_confidence(self) -> float:
        """Уверенность: растёт с количеством реакций, насыщается при 10+."""
        return min(1.0, (self.pos + self.neg) / 10.0)

    def relevance_label(self) -> int:
        """
        Дискретный ярлык релевантности для LambdaRank (0/1/2).

        2 — явно релевантная (сильные позитивные реакции)
        1 — умеренно релевантная
        0 — нерелевантная
        """
        if not self.has_reactions:
            return 1  # нейтральный — будет заменён pseudo-label
        signal = self.reaction_signal()
        confidence = self.reaction_confidence()
        weighted = signal * confidence
        if weighted > 0.3:
            return 2
        if weighted > -0.2:
            return 1
        return 0

    def day_bucket(self) -> int:
        """День с unix epoch — используется для формирования LambdaRank query."""
        if self.published_at is None:
            return 0
        pub = self.published_at
        if pub.tzinfo is None:
            pub = pub.replace(tzinfo=timezone.utc)
        return int(pub.timestamp() // 86400)


async def load_articles(dsn: str, limit: int = 50_000) -> list[ArticleRow]:
    """Загружает статьи с реакциями из PostgreSQL."""
    conn = await asyncpg.connect(dsn)
    try:
        rows = await conn.fetch(
            """
            SELECT
                a.id,
                a.feed_id,
                a.title,
                COALESCE(a.description, '')    AS description,
                f.weight                        AS feed_weight,
                a.published_at,
                COALESCE(pm.positive_reactions, 0) AS pos,
                COALESCE(pm.negative_reactions, 0) AS neg
            FROM   articles a
            JOIN   feeds    f  ON f.id = a.feed_id
            LEFT JOIN posted_messages pm ON pm.article_id = a.id
            ORDER  BY a.published_at DESC NULLS LAST
            LIMIT  $1
            """,
            limit,
        )
    finally:
        await conn.close()

    logger.info("Loaded %d articles from DB", len(rows))
    return [
        ArticleRow(
            article_id=int(r["id"]),
            feed_id=int(r["feed_id"]),
            title=r["title"],
            description=r["description"],
            feed_weight=float(r["feed_weight"]),
            published_at=r["published_at"],
            pos=int(r["pos"]),
            neg=int(r["neg"]),
        )
        for r in rows
    ]

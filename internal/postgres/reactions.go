package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alex-pyslar/Feedium/internal/domain"
)

// GetPostedMessageByTelegramID ищет запись по Telegram message_id + chat_id.
func (s *Store) GetPostedMessageByTelegramID(ctx context.Context, chatID int64, msgID int) (*domain.PostedMessage, error) {
	var pm domain.PostedMessage
	err := s.pool.QueryRow(ctx, `
		SELECT id, article_id, telegram_msg_id, chat_id, posted_at,
		       positive_reactions, negative_reactions, last_reaction_harvested_at
		FROM posted_messages
		WHERE chat_id = $1 AND telegram_msg_id = $2
	`, chatID, msgID).Scan(
		&pm.ID, &pm.ArticleID, &pm.TelegramMsgID, &pm.ChatID, &pm.PostedAt,
		&pm.PositiveReactions, &pm.NegativeReactions, &pm.LastReactionHarvestedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// UpdateReactionCounts обновляет счётчики реакций и время последнего сбора.
func (s *Store) UpdateReactionCounts(ctx context.Context, pmID int64, pos, neg int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE posted_messages
		SET positive_reactions = $2, negative_reactions = $3,
		    last_reaction_harvested_at = NOW()
		WHERE id = $1
	`, pmID, pos, neg)
	return err
}

// GetMessagesForHarvest возвращает сообщения, у которых давно не собирались реакции.
func (s *Store) GetMessagesForHarvest(ctx context.Context, staleSince, maxAge time.Duration) ([]domain.PostedMessage, error) {
	cutoff := time.Now().Add(-staleSince)
	oldest := time.Now().Add(-maxAge)

	rows, err := s.pool.Query(ctx, `
		SELECT id, article_id, telegram_msg_id, chat_id, posted_at,
		       positive_reactions, negative_reactions, last_reaction_harvested_at
		FROM posted_messages
		WHERE (last_reaction_harvested_at IS NULL OR last_reaction_harvested_at < $1)
		  AND posted_at > $2
		ORDER BY posted_at DESC
	`, cutoff, oldest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []domain.PostedMessage
	for rows.Next() {
		var pm domain.PostedMessage
		if err := rows.Scan(
			&pm.ID, &pm.ArticleID, &pm.TelegramMsgID, &pm.ChatID, &pm.PostedAt,
			&pm.PositiveReactions, &pm.NegativeReactions, &pm.LastReactionHarvestedAt,
		); err != nil {
			return nil, err
		}
		msgs = append(msgs, pm)
	}
	return msgs, rows.Err()
}

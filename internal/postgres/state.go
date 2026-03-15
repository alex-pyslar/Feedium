package postgres

import (
	"context"
	"strconv"
)

// GetTelegramOffset читает персистентный offset для Telegram long-polling.
func (s *Store) GetTelegramOffset(ctx context.Context) (int, error) {
	var val string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM scheduler_state WHERE key = 'telegram_update_offset'`,
	).Scan(&val)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(val)
}

// SetTelegramOffset сохраняет offset.
func (s *Store) SetTelegramOffset(ctx context.Context, offset int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE scheduler_state SET value = $1 WHERE key = 'telegram_update_offset'`,
		strconv.Itoa(offset),
	)
	return err
}

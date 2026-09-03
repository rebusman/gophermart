package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул подключений к PostgreSQL и проверяет доступность базы
func NewPool(ctx context.Context, dsn string, connectTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор строки подключения к базе данных: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(connectCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("создание пула подключений: %w", err)
	}

	if err = pool.Ping(connectCtx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("проверка доступности базы данных: %w", err)
	}

	return pool, nil
}

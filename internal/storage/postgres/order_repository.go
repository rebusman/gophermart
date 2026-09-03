package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/domain"
)

// SQL-запросы репозитория заказов.
const (
	// insertOrderQuery создаёт заказ и сообщает об исходе самим фактом
	insertOrderQuery = `
		INSERT INTO orders (number, user_id, status)
		VALUES ($1, $2, $3)
		ON CONFLICT (number) DO NOTHING
		RETURNING number`

	// selectOrderOwnerQuery определяет владельца занятого номера.
	selectOrderOwnerQuery = `
		SELECT user_id
		FROM orders
		WHERE number = $1`

	// selectOrdersByUserQuery выбирает заказы пользователя от новых к старым.
	selectOrdersByUserQuery = `
		SELECT number, status, accrual, uploaded_at
		FROM orders
		WHERE user_id = $1
		ORDER BY uploaded_at DESC, number DESC`
)

// OrderRepository хранит заказы пользователей в PostgreSQL.
type OrderRepository struct {
	pool *pgxpool.Pool
}

// NewOrderRepository создаёт репозиторий поверх пула подключений.
func NewOrderRepository(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

// CreateOrder создаёт заказ и сообщает, был ли он создан этим вызовом.
func (r *OrderRepository) CreateOrder(ctx context.Context, order domain.Order) (bool, error) {
	var inserted string

	err := r.pool.QueryRow(ctx, insertOrderQuery,
		order.Number.String(),
		UUIDFromGoogle(uuid.UUID(order.UserID)),
		order.Status.String(),
	).Scan(&inserted)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("создание заказа: %w", err)
	}

	return true, nil
}

// OrderOwner возвращает владельца заказа с указанным номером.
func (r *OrderRepository) OrderOwner(ctx context.Context, number domain.OrderNumber) (domain.UserID, error) {
	var owner pgtype.UUID

	err := r.pool.QueryRow(ctx, selectOrderOwnerQuery, number.String()).Scan(&owner)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.UserID{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.UserID{}, fmt.Errorf("чтение владельца заказа: %w", err)
	}

	parsed, err := GoogleFromUUID(owner)
	if err != nil {
		return domain.UserID{}, fmt.Errorf("чтение идентификатора владельца заказа: %w", err)
	}

	return domain.UserID(parsed), nil
}

// OrdersByUser возвращает заказы пользователя от самых новых к самым старым.
func (r *OrderRepository) OrdersByUser(ctx context.Context, userID domain.UserID) ([]domain.Order, error) {
	rows, err := r.pool.Query(ctx, selectOrdersByUserQuery, UUIDFromGoogle(uuid.UUID(userID)))
	if err != nil {
		return nil, fmt.Errorf("чтение заказов пользователя: %w", err)
	}

	defer rows.Close()

	var orders []domain.Order

	for rows.Next() {
		order, scanErr := scanOrder(rows, userID)
		if scanErr != nil {
			return nil, scanErr
		}

		orders = append(orders, order)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("обход заказов пользователя: %w", err)
	}

	return orders, nil
}

// scanOrder собирает заказ из текущей строки результата.
func scanOrder(rows pgx.Rows, userID domain.UserID) (domain.Order, error) {
	var (
		number     string
		status     string
		accrual    pgtype.Numeric
		uploadedAt pgtype.Timestamptz
	)

	if err := rows.Scan(&number, &status, &accrual, &uploadedAt); err != nil {
		return domain.Order{}, fmt.Errorf("чтение заказа: %w", err)
	}

	parsedNumber, err := domain.ParseOrderNumber(number)
	if err != nil {
		return domain.Order{}, fmt.Errorf("чтение номера заказа: %w", err)
	}

	parsedStatus, err := domain.ParseOrderStatus(status)
	if err != nil {
		return domain.Order{}, fmt.Errorf("чтение состояния расчёта заказа: %w", err)
	}

	parsedAccrual, err := DecimalPtrFromNumeric(accrual)
	if err != nil {
		return domain.Order{}, fmt.Errorf("чтение начисления по заказу: %w", err)
	}

	return domain.Order{
		Number:     parsedNumber,
		UserID:     userID,
		Status:     parsedStatus,
		Accrual:    parsedAccrual,
		UploadedAt: uploadedAt.Time.UTC(),
	}, nil
}

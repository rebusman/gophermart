package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"gophermart/internal/domain"
)

// loginUniqueConstraint — имя ограничения уникальности логина.
const loginUniqueConstraint = "users_login_unique"

// SQL-запросы репозитория пользователей.
const (
	insertUserQuery = `
		INSERT INTO users (id, login, password_hash, created_at)
		VALUES ($1, $2, $3, $4)`

	insertBalanceQuery = `
		INSERT INTO balances (user_id, current, withdrawn_total, updated_at)
		VALUES ($1, 0, 0, $2)`

	selectUserByLoginQuery = `
		SELECT id, login, password_hash, created_at
		FROM users
		WHERE login = $1`

	selectUserByIDQuery = `
		SELECT id, login, password_hash, created_at
		FROM users
		WHERE id = $1`
)

// UserRepository хранит учётные записи пользователей и их счета лояльности в
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository создаёт репозиторий поверх пула подключений.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// CreateUser создаёт учётную запись и её счёт лояльности с нулевыми суммами в
func (r *UserRepository) CreateUser(ctx context.Context, user domain.User) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции создания пользователя: %w", err)
	}

	defer func() {
		// Откат уже зафиксированной транзакции безвреден и возвращает
		_ = tx.Rollback(ctx)
	}()

	id := UUIDFromGoogle(uuid.UUID(user.ID))

	if _, err = tx.Exec(ctx, insertUserQuery, id, user.Login, user.PasswordHash, user.CreatedAt); err != nil {
		return translateInsertUserError(err)
	}

	if _, err = tx.Exec(ctx, insertBalanceQuery, id, user.CreatedAt); err != nil {
		return fmt.Errorf("создание счёта лояльности: %w", err)
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("фиксация транзакции создания пользователя: %w", err)
	}

	return nil
}

// UserByLogin возвращает учётную запись по логину.
func (r *UserRepository) UserByLogin(ctx context.Context, login string) (domain.User, error) {
	return r.queryUser(ctx, selectUserByLoginQuery, login)
}

// UserByID возвращает учётную запись по идентификатору.
func (r *UserRepository) UserByID(ctx context.Context, id domain.UserID) (domain.User, error) {
	return r.queryUser(ctx, selectUserByIDQuery, UUIDFromGoogle(uuid.UUID(id)))
}

// queryUser выполняет запрос, возвращающий не более одной учётной записи, и
func (r *UserRepository) queryUser(ctx context.Context, query string, arg any) (domain.User, error) {
	var (
		id        pgtype.UUID
		user      domain.User
		createdAt pgtype.Timestamptz
	)

	err := r.pool.QueryRow(ctx, query, arg).Scan(&id, &user.Login, &user.PasswordHash, &createdAt)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.User{}, domain.ErrUserNotFound
	case err != nil:
		return domain.User{}, fmt.Errorf("чтение учётной записи: %w", err)
	}

	parsed, err := GoogleFromUUID(id)
	if err != nil {
		return domain.User{}, fmt.Errorf("чтение идентификатора пользователя: %w", err)
	}

	user.ID = domain.UserID(parsed)
	user.CreatedAt = createdAt.Time.UTC()

	return user, nil
}

// translateInsertUserError переводит ошибку вставки пользователя в доменную.
func translateInsertUserError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) &&
		pgErr.Code == pgerrcode.UniqueViolation &&
		pgErr.ConstraintName == loginUniqueConstraint {
		return domain.ErrLoginTaken
	}

	return fmt.Errorf("создание учётной записи: %w", err)
}

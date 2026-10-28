package repository

import (
	"context"

	"gophermart/internal/domain"
)

// UserRepository хранит учётные записи пользователей и их счета лояльности.
type UserRepository interface {
	// CreateUser создаёт учётную запись и её счёт лояльности с нулевыми
	CreateUser(ctx context.Context, user domain.User) error

	// UserByLogin возвращает учётную запись по логину.
	UserByLogin(ctx context.Context, login string) (domain.User, error)

	// UserByID возвращает учётную запись по идентификатору.
	UserByID(ctx context.Context, id domain.UserID) (domain.User, error)
}

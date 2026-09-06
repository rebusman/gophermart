package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/shopspring/decimal"
)

// UserID — идентификатор пользователя.
type UserID uuid.UUID

// NewUserID генерирует идентификатор нового пользователя.
func NewUserID() UserID {
	return UserID(uuid.New())
}

// ParseUserID разбирает идентификатор пользователя из строкового
func ParseUserID(raw string) (UserID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, ErrInvalidUserID
	}

	return UserID(parsed), nil
}

// String возвращает каноническое строковое представление идентификатора.
func (id UserID) String() string {
	return uuid.UUID(id).String()
}

// IsZero сообщает, что идентификатор не задан.
func (id UserID) IsZero() bool {
	return uuid.UUID(id) == uuid.Nil
}

// User — учётная запись пользователя системы лояльности.
type User struct {
	// ID — идентификатор пользователя, уникальный в пределах системы.
	ID UserID

	// Login — логин пользователя. Уникален глобально, сравнивается с учётом
	Login string

	// PasswordHash — адаптивный хеш пароля вместе с солью и параметрами
	PasswordHash string

	// CreatedAt — момент создания учётной записи в UTC.
	CreatedAt time.Time
}

// Balance — счёт лояльности пользователя.
type Balance struct {
	// UserID — владелец счёта.
	UserID UserID

	// Current — текущая сумма доступных баллов. Не может быть отрицательной.
	Current decimal.Decimal

	// Withdrawn — сумма баллов, списанных за весь период существования счёта.
	Withdrawn decimal.Decimal

	// UpdatedAt — момент последнего изменения счёта в UTC.
	UpdatedAt time.Time
}

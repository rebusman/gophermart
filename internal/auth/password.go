package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"gophermart/internal/domain"
)

// Параметры хеширования паролей.
const (
	// MaxPasswordBytes — предельная длина пароля в байтах.
	MaxPasswordBytes = 72

	// MinCost — минимально допустимый параметр стоимости хеширования.
	MinCost = bcrypt.MinCost

	// MaxCost — максимально допустимый параметр стоимости хеширования.
	MaxCost = bcrypt.MaxCost

	dummyPassword = "dummy-password-for-constant-time-comparison"
)

// ErrInvalidCost возвращается, когда параметр стоимости хеширования выходит за
var ErrInvalidCost = errors.New("недопустимая стоимость хеширования пароля")

// Hasher хеширует пароли и сравнивает их с сохранёнными хешами.
type Hasher struct {
	cost      int
	dummyHash string
}

// NewHasher создаёт хешер с заданной стоимостью.
func NewHasher(cost int) (*Hasher, error) {
	if cost < MinCost || cost > MaxCost {
		return nil, fmt.Errorf("%w: %d, ожидается от %d до %d", ErrInvalidCost, cost, MinCost, MaxCost)
	}

	dummy, err := bcrypt.GenerateFromPassword([]byte(dummyPassword), cost)
	if err != nil {
		return nil, fmt.Errorf("вычисление фиктивного хеша: %w", err)
	}

	return &Hasher{cost: cost, dummyHash: string(dummy)}, nil
}

// Cost возвращает стоимость хеширования, с которой создан хешер.
func (h *Hasher) Cost() int {
	return h.cost
}

// Hash возвращает адаптивный хеш пароля вместе с солью и параметрами
func (h *Hasher) Hash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", fmt.Errorf("хеширование пароля: %w", err)
	}

	return string(hash), nil
}

// Compare сравнивает пароль с сохранённым хешем.
func (h *Hasher) Compare(hash, password string) error {
	if len(password) > MaxPasswordBytes {
		return domain.ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return domain.ErrInvalidCredentials
	}

	return nil
}

// CompareDummy выполняет сравнение с фиктивным хешем.
func (h *Hasher) CompareDummy() error {
	_ = bcrypt.CompareHashAndPassword([]byte(h.dummyHash), []byte(dummyPassword))

	return domain.ErrInvalidCredentials
}

// ValidatePassword проверяет пароль на пригодность к хешированию.
func ValidatePassword(password string) error {
	if password == "" {
		return domain.ErrEmptyPassword
	}

	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("%w: %d байт при пределе %d", domain.ErrPasswordTooLong, len(password), MaxPasswordBytes)
	}

	return nil
}

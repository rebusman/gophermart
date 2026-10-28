package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gophermart/internal/domain"
	"gophermart/internal/repository"
)

// PasswordHasher хеширует пароли и сравнивает их с сохранёнными хешами.
type PasswordHasher interface {
	// Hash возвращает адаптивный хеш пароля.
	Hash(password string) (string, error)

	// Compare сравнивает пароль с сохранённым хешем и возвращает
	Compare(hash, password string) error

	// CompareDummy выполняет сравнение с фиктивным хешем и всегда возвращает
	CompareDummy() error
}

// TokenIssuer выпускает и проверяет токены доступа.
type TokenIssuer interface {
	// Issue выпускает новый токен доступа для указанного пользователя.
	Issue(userID domain.UserID) (string, error)

	// Parse проверяет токен и возвращает идентификатор его владельца.
	Parse(token string) (domain.UserID, error)
}

// Auth реализует сценарии регистрации, входа и проверки токена доступа.
type Auth struct {
	users  repository.UserRepository
	hasher PasswordHasher
	tokens TokenIssuer
}

// NewAuth создаёт сервис аутентификации.
func NewAuth(users repository.UserRepository, hasher PasswordHasher, tokens TokenIssuer) *Auth {
	return &Auth{users: users, hasher: hasher, tokens: tokens}
}

// Register создаёт учётную запись и сразу выпускает для неё токен доступа.
func (a *Auth) Register(ctx context.Context, login, password string) (string, error) {
	if strings.TrimSpace(login) == "" {
		return "", domain.ErrEmptyLogin
	}

	hash, err := a.hasher.Hash(password)
	if err != nil {
		return "", fmt.Errorf("подготовка пароля: %w", err)
	}

	user := domain.User{
		ID:           domain.NewUserID(),
		Login:        login,
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC(),
	}

	if err = a.users.CreateUser(ctx, user); err != nil {
		return "", fmt.Errorf("создание учётной записи: %w", err)
	}

	token, err := a.tokens.Issue(user.ID)
	if err != nil {
		return "", fmt.Errorf("выпуск токена доступа: %w", err)
	}

	return token, nil
}

// Login проверяет пару логин/пароль и выпускает новый токен доступа.
func (a *Auth) Login(ctx context.Context, login, password string) (string, error) {
	if strings.TrimSpace(login) == "" {
		return "", domain.ErrEmptyLogin
	}

	if password == "" {
		return "", domain.ErrEmptyPassword
	}

	user, err := a.users.UserByLogin(ctx, login)

	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		return "", fmt.Errorf("вход по логину %q: %w", login, a.hasher.CompareDummy())
	case err != nil:
		return "", fmt.Errorf("поиск учётной записи: %w", err)
	}

	if err = a.hasher.Compare(user.PasswordHash, password); err != nil {
		return "", fmt.Errorf("проверка пароля: %w", err)
	}

	token, err := a.tokens.Issue(user.ID)
	if err != nil {
		return "", fmt.Errorf("выпуск токена доступа: %w", err)
	}

	return token, nil
}

// Authenticate проверяет токен доступа и возвращает идентификатор его
func (a *Auth) Authenticate(ctx context.Context, token string) (domain.UserID, error) {
	userID, err := a.tokens.Parse(token)
	if err != nil {
		return domain.UserID{}, fmt.Errorf("проверка токена доступа: %w", err)
	}

	user, err := a.users.UserByID(ctx, userID)

	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		return domain.UserID{}, fmt.Errorf("владелец токена не найден: %w", domain.ErrUnauthenticated)
	case err != nil:
		return domain.UserID{}, fmt.Errorf("поиск владельца токена: %w", err)
	}

	return user.ID, nil
}

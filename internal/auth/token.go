package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"gophermart/internal/domain"
)

// signingMethod — алгоритм подписи токенов доступа.
const signingMethod = "HS256"

// ErrEmptySecret возвращается, когда секрет подписи токенов пуст.
var ErrEmptySecret = errors.New("секрет подписи токенов не задан")

// TokenIssuer выпускает и проверяет токены доступа.
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer создаёт издателя токенов с заданными секретом подписи и
func NewTokenIssuer(secret string, ttl time.Duration) (*TokenIssuer, error) {
	if secret == "" {
		return nil, ErrEmptySecret
	}

	return &TokenIssuer{secret: []byte(secret), ttl: ttl}, nil
}

// TTL возвращает срок действия выпускаемых токенов.
func (i *TokenIssuer) TTL() time.Duration {
	return i.ttl
}

// Issue выпускает токен доступа для указанного пользователя.
func (i *TokenIssuer) Issue(userID domain.UserID) (string, error) {
	now := time.Now()

	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("подпись токена доступа: %w", err)
	}

	return signed, nil
}

// Parse проверяет токен и возвращает идентификатор его владельца.
func (i *TokenIssuer) Parse(token string) (domain.UserID, error) {
	claims := &jwt.RegisteredClaims{}

	parsed, err := jwt.ParseWithClaims(token, claims, i.keyFunc,
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return domain.UserID{}, domain.ErrUnauthenticated
	}

	userID, err := domain.ParseUserID(claims.Subject)
	if err != nil {
		return domain.UserID{}, domain.ErrUnauthenticated
	}

	return userID, nil
}

// keyFunc возвращает ключ проверки подписи.
func (i *TokenIssuer) keyFunc(*jwt.Token) (any, error) {
	return i.secret, nil
}

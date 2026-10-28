package middleware

import (
	"context"
	"net/http"
	"strings"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
)

// Имена и значения, которыми переносится токен доступа.
const (
	// HeaderAuthorization переносит токен доступа в запросе и в ответе.
	HeaderAuthorization = "Authorization"

	// CookieAuthToken — имя cookie, в которой дублируется токен доступа.
	CookieAuthToken = "auth_token"

	// BearerScheme — схема аутентификации в заголовке Authorization.
	BearerScheme = "Bearer"
)

// userIDContextKey — тип ключа, под которым идентификатор пользователя
type userIDContextKey struct{}

// Authenticator проверяет токен доступа и сообщает, кому он принадлежит.
type Authenticator interface {
	// Authenticate возвращает идентификатор владельца токена либо
	Authenticate(ctx context.Context, token string) (domain.UserID, error)
}

// Auth возвращает обработчик, допускающий к нижележащей цепочке только запросы
func Auth(authenticator Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := logging.FromContext(ctx)

			token, ok := tokenFromRequest(r)
			if !ok {
				logger.DebugContext(ctx, "запрос не предъявил токен доступа")
				writeStatus(w, http.StatusUnauthorized)

				return
			}

			userID, err := authenticator.Authenticate(ctx, token)
			if err != nil {
				logger.DebugContext(ctx, "токен доступа отклонён", logging.ErrorAttr(err))
				writeStatus(w, http.StatusUnauthorized)

				return
			}

			next.ServeHTTP(w, r.WithContext(ContextWithUserID(ctx, userID)))
		})
	}
}

// ContextWithUserID возвращает контекст, в котором сохранён идентификатор
func ContextWithUserID(ctx context.Context, id domain.UserID) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, id)
}

// UserIDFromContext извлекает идентификатор пользователя из контекста запроса.
func UserIDFromContext(ctx context.Context) (domain.UserID, bool) {
	id, ok := ctx.Value(userIDContextKey{}).(domain.UserID)

	return id, ok
}

// tokenFromRequest извлекает токен доступа из запроса.
func tokenFromRequest(r *http.Request) (string, bool) {
	if header := r.Header.Get(HeaderAuthorization); header != "" {
		return parseBearer(header)
	}

	cookie, err := r.Cookie(CookieAuthToken)
	if err != nil || cookie.Value == "" {
		return "", false
	}

	return cookie.Value, true
}

// parseBearer разбирает значение заголовка Authorization формы «Bearer
func parseBearer(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, BearerScheme) {
		return "", false
	}

	token = strings.TrimSpace(token)

	return token, token != ""
}

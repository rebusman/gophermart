package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// requestIDByteLen — длина генерируемого идентификатора запроса в байтах.
const requestIDByteLen = 16

// requestIDContextKey — тип ключа, под которым идентификатор запроса хранится
type requestIDContextKey struct{}

// RequestID возвращает обработчик, сопоставляющий каждому запросу
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = newRequestID()
		}

		w.Header().Set(HeaderRequestID, id)

		next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
	})
}

// ContextWithRequestID возвращает контекст, в котором сохранён идентификатор
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext извлекает идентификатор запроса из контекста.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return id
	}

	return ""
}

// newRequestID генерирует случайный идентификатор запроса.
func newRequestID() string {
	buf := make([]byte, requestIDByteLen)
	_, _ = rand.Read(buf)

	return hex.EncodeToString(buf)
}

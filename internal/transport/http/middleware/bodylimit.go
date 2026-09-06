package middleware

import (
	"errors"
	"net/http"
)

// BodyLimit возвращает обработчик, ограничивающий размер тела запроса
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				writeStatus(w, http.StatusRequestEntityTooLarge)

				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, limit)

			next.ServeHTTP(w, r)
		})
	}
}

// IsBodyTooLarge сообщает, что ошибка чтения тела вызвана превышением лимита,
func IsBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError

	return errors.As(err, &maxBytesErr)
}

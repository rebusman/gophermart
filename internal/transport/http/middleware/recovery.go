package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recovery возвращает обработчик, перехватывающий panic в нижележащей цепочке.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				logger.ErrorContext(r.Context(), "panic при обработке запроса",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)

				writeStatus(w, http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

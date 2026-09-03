package middleware

import (
	"net/http"
	"strconv"
)

// Имена HTTP-заголовков, используемых сквозными обработчиками.
const (
	HeaderRequestID = "X-Request-Id"
)

// writeStatus отправляет клиенту ответ, состоящий только из кода состояния и
func writeStatus(w http.ResponseWriter, status int) {
	body := http.StatusText(status)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	_, _ = w.Write([]byte(body))
}

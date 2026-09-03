package handlers

import (
	"net/http"
	"strconv"
)

// writeStatus отправляет клиенту ответ, состоящий только из кода состояния и
func writeStatus(w http.ResponseWriter, status int) {
	body := http.StatusText(status)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	//nolint:gosec // G705: body — стандартное описание кода состояния http.StatusText, а не данные пользователя.
	_, _ = w.Write([]byte(body))
}

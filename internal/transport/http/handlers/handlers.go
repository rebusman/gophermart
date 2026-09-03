package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
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

// writeError отображает ошибку прикладного сценария в код ответа.
func writeError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	status := statusForError(err)
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	if status == http.StatusInternalServerError {
		logger.ErrorContext(ctx, operation+" не выполнена", logging.ErrorAttr(err))
	} else {
		logger.DebugContext(ctx, operation+" отклонена", logging.ErrorAttr(err))
	}

	writeStatus(w, status)
}

// statusForError сопоставляет доменной ошибке код ответа.
func statusForError(err error) int {
	switch {
	case errors.Is(err, domain.ErrLoginTaken), errors.Is(err, domain.ErrOrderBelongsToAnotherUser):
		return http.StatusConflict
	case errors.Is(err, domain.ErrInvalidCredentials), errors.Is(err, domain.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, domain.ErrInvalidOrderNumber):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrEmptyLogin),
		errors.Is(err, domain.ErrEmptyPassword),
		errors.Is(err, domain.ErrPasswordTooLong):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

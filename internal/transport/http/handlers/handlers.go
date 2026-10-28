package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
)

// writeJSON отправляет клиенту тело ответа в формате JSON с кодом 200.
func writeJSON(w http.ResponseWriter, r *http.Request, operation string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, r, operation, err)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: body — результат json.Marshal с экранированием, отдаётся как application/json с nosniff.
	if _, err = w.Write(body); err != nil {
		ctx := r.Context()
		logging.FromContext(ctx).DebugContext(ctx, "тело ответа не отправлено", logging.ErrorAttr(err))
	}
}

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
	case errors.Is(err, domain.ErrInsufficientFunds):
		return http.StatusPaymentRequired
	case errors.Is(err, domain.ErrInvalidOrderNumber):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrEmptyLogin),
		errors.Is(err, domain.ErrEmptyPassword),
		errors.Is(err, domain.ErrPasswordTooLong),
		errors.Is(err, domain.ErrNonPositiveWithdrawalSum),
		errors.Is(err, domain.ErrWithdrawalSumTooPrecise):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

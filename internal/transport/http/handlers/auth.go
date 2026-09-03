package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
	"gophermart/internal/transport/http/dto"
	"gophermart/internal/transport/http/middleware"
)

// AuthService реализует прикладные сценарии регистрации и входа.
type AuthService interface {
	// Register создаёт учётную запись и возвращает токен доступа для неё.
	Register(ctx context.Context, login, password string) (string, error)

	// Login проверяет пару логин/пароль и возвращает новый токен доступа.
	Login(ctx context.Context, login, password string) (string, error)
}

// Auth обслуживает маршруты регистрации и аутентификации пользователя.
type Auth struct {
	service  AuthService
	tokenTTL time.Duration
}

// NewAuth создаёт обработчик маршрутов аутентификации.
func NewAuth(service AuthService, tokenTTL time.Duration) *Auth {
	return &Auth{service: service, tokenTTL: tokenTTL}
}

// Register обслуживает POST /api/user/register.
func (h *Auth) Register(w http.ResponseWriter, r *http.Request) {
	credentials, ok := decodeCredentials(w, r)
	if !ok {
		return
	}

	token, err := h.service.Register(r.Context(), credentials.Login, credentials.Password)
	if err != nil {
		writeAuthError(w, r, "регистрация пользователя", err)

		return
	}

	h.writeToken(w, token)
}

// Login обслуживает POST /api/user/login.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	credentials, ok := decodeCredentials(w, r)
	if !ok {
		return
	}

	token, err := h.service.Login(r.Context(), credentials.Login, credentials.Password)
	if err != nil {
		writeAuthError(w, r, "аутентификация пользователя", err)

		return
	}

	h.writeToken(w, token)
}

// writeToken отдаёт токен доступа клиенту двумя способами одновременно и
func (h *Auth) writeToken(w http.ResponseWriter, token string) {
	w.Header().Set(middleware.HeaderAuthorization, middleware.BearerScheme+" "+token)

	http.SetCookie(w, &http.Cookie{
		Name:     middleware.CookieAuthToken,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.tokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusOK)
}

// decodeCredentials разбирает тело запроса как пару логин/пароль.
func decodeCredentials(w http.ResponseWriter, r *http.Request) (dto.Credentials, bool) {
	var credentials dto.Credentials

	if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeStatus(w, http.StatusRequestEntityTooLarge)

			return dto.Credentials{}, false
		}

		logging.FromContext(r.Context()).DebugContext(r.Context(),
			"тело запроса не разобрано", logging.ErrorAttr(err))
		writeStatus(w, http.StatusBadRequest)

		return dto.Credentials{}, false
	}

	return credentials, true
}

// writeAuthError отображает ошибку прикладного сценария в код ответа.
func writeAuthError(w http.ResponseWriter, r *http.Request, operation string, err error) {
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
	case errors.Is(err, domain.ErrLoginTaken):
		return http.StatusConflict
	case errors.Is(err, domain.ErrInvalidCredentials), errors.Is(err, domain.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, domain.ErrEmptyLogin),
		errors.Is(err, domain.ErrEmptyPassword),
		errors.Is(err, domain.ErrPasswordTooLong):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

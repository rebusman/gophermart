package httptransport

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
)

// RouterConfig содержит параметры сборки маршрутизатора.
type RouterConfig struct {
	Logger *slog.Logger

	// MaxRequestBodyBytes — предельный размер тела запроса в байтах.
	MaxRequestBodyBytes int64

	Auth *handlers.Auth

	Orders *handlers.Orders

	// Authenticator — проверка токена доступа для группы защищённых
	Authenticator middleware.Authenticator
}

// Router — HTTP-маршрутизатор сервиса.
type Router struct {
	chi.Router

	handler   http.Handler
	protected chi.Router
}

// NewRouter собирает маршрутизатор со сквозными обработчиками.
func NewRouter(cfg RouterConfig) *Router {
	mux := chi.NewRouter()
	mux.NotFound(notFoundHandler)
	mux.MethodNotAllowed(methodNotAllowedHandler)

	mux.Post("/api/user/register", cfg.Auth.Register)
	mux.Post("/api/user/login", cfg.Auth.Login)

	protected := mux.With(middleware.Auth(cfg.Authenticator))
	protected.Post("/api/user/orders", cfg.Orders.Upload)
	protected.Get("/api/user/orders", cfg.Orders.List)

	var handler http.Handler = mux

	handler = middleware.BodyLimit(cfg.MaxRequestBodyBytes)(handler)
	handler = middleware.Gzip(handler)
	handler = middleware.Logging(cfg.Logger)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Recovery(cfg.Logger)(handler)

	return &Router{
		Router:    mux,
		handler:   handler,
		protected: protected,
	}
}

// Protected возвращает маршрутизатор группы защищённых маршрутов.
func (r *Router) Protected() chi.Router {
	return r.protected
}

// ServeHTTP пропускает запрос через цепочку сквозных обработчиков и передаёт
func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.handler.ServeHTTP(w, request)
}

// notFoundHandler отвечает на запрос к незарегистрированному маршруту.
func notFoundHandler(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusNotFound)
}

// methodNotAllowedHandler отвечает на запрос к зарегистрированному маршруту
func methodNotAllowedHandler(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusMethodNotAllowed)
}

// writeStatus отправляет ответ, состоящий только из кода состояния и его
func writeStatus(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

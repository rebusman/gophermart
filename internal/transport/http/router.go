package httptransport

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"gophermart/internal/transport/http/handlers"
	"gophermart/internal/transport/http/middleware"
)

// ErrMissingRouterConfig возвращается, когда обязательное поле RouterConfig
var ErrMissingRouterConfig = errors.New("обязательное поле конфигурации маршрутизатора не задано")

// RouterConfig содержит параметры сборки маршрутизатора.
type RouterConfig struct {
	Logger *slog.Logger

	// MaxRequestBodyBytes — предельный размер тела запроса в байтах.
	MaxRequestBodyBytes int64

	Auth *handlers.Auth

	Orders *handlers.Orders

	Balance *handlers.Balance

	// Authenticator — проверка токена доступа для защищённых маршрутов.
	Authenticator middleware.Authenticator
}

// Router — HTTP-маршрутизатор сервиса.
type Router struct {
	mux          *http.ServeMux
	handler      http.Handler
	authenticate func(http.Handler) http.Handler
}

// NewRouter собирает маршрутизатор со сквозными обработчиками.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if err := validateRouterConfig(cfg); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	router := &Router{
		mux:          mux,
		authenticate: middleware.Auth(cfg.Authenticator),
	}

	router.HandleFunc("POST /api/user/register", cfg.Auth.Register)
	router.HandleFunc("POST /api/user/login", cfg.Auth.Login)

	router.HandleProtected("POST /api/user/orders", cfg.Orders.Upload)
	router.HandleProtected("GET /api/user/orders", cfg.Orders.List)
	router.HandleProtected("GET /api/user/balance", cfg.Balance.Get)
	router.HandleProtected("POST /api/user/balance/withdraw", cfg.Balance.Withdraw)
	router.HandleProtected("GET /api/user/withdrawals", cfg.Balance.Withdrawals)

	var handler http.Handler = mux

	handler = middleware.BodyLimit(cfg.MaxRequestBodyBytes)(handler)
	handler = middleware.Gzip(handler)
	handler = middleware.Logging(cfg.Logger)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Recovery(cfg.Logger)(handler)

	router.handler = handler

	return router, nil
}

// validateRouterConfig проверяет обязательные зависимости и параметры
func validateRouterConfig(cfg RouterConfig) error {
	var errs []error

	if cfg.Logger == nil {
		errs = append(errs, fmt.Errorf("%w: RouterConfig.Logger", ErrMissingRouterConfig))
	}

	if cfg.Auth == nil {
		errs = append(errs, fmt.Errorf("%w: RouterConfig.Auth", ErrMissingRouterConfig))
	}

	if cfg.Orders == nil {
		errs = append(errs, fmt.Errorf("%w: RouterConfig.Orders", ErrMissingRouterConfig))
	}

	if cfg.Balance == nil {
		errs = append(errs, fmt.Errorf("%w: RouterConfig.Balance", ErrMissingRouterConfig))
	}

	if cfg.Authenticator == nil {
		errs = append(errs, fmt.Errorf("%w: RouterConfig.Authenticator", ErrMissingRouterConfig))
	}

	if cfg.MaxRequestBodyBytes <= 0 {
		errs = append(errs, fmt.Errorf(
			"%w: RouterConfig.MaxRequestBodyBytes, ожидается положительное значение",
			ErrMissingRouterConfig,
		))
	}

	return errors.Join(errs...)
}

// Handle регистрирует публичный маршрут.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
}

// HandleFunc регистрирует публичный маршрут с обработчиком-функцией на тех же
func (r *Router) HandleFunc(pattern string, handler http.HandlerFunc) {
	r.mux.Handle(pattern, handler)
}

// HandleProtected регистрирует маршрут, доступный только с действительным
func (r *Router) HandleProtected(pattern string, handler http.HandlerFunc) {
	r.mux.Handle(pattern, r.authenticate(handler))
}

// ServeHTTP пропускает запрос через цепочку сквозных обработчиков и передаёт
func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.handler.ServeHTTP(w, request)
}

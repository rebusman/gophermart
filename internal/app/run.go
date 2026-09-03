package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"gophermart/internal/accrual"
	"gophermart/internal/auth"
	"gophermart/internal/logging"
	"gophermart/internal/service"
	"gophermart/internal/storage/postgres"
	httptransport "gophermart/internal/transport/http"
	"gophermart/internal/transport/http/handlers"
	"gophermart/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Main разбирает конфигурацию, создаёт логгер и выполняет жизненный цикл
func Main(ctx context.Context, args []string, lookupEnv func(string) (string, bool), out io.Writer) error {
	cfg, err := LoadConfig(args, lookupEnv)
	if err != nil {
		return err
	}

	return Run(ctx, cfg, logging.New(out, cfg.LogLevel, cfg.LogFormat))
}

// Run выполняет полный жизненный цикл сервиса: подключается к PostgreSQL,
func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if cfg.JWTSecretGenerated {
		logger.WarnContext(ctx,
			"переменная "+EnvJWTSecret+" не задана: секрет подписи сгенерирован на время работы процесса, "+
				"выпущенные токены станут недействительными после перезапуска")
	}

	logger.InfoContext(ctx, "конфигурация загружена", slog.Any("config", cfg))

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURI, cfg.DatabaseConnectTimeout)
	if err != nil {
		return fmt.Errorf("подключение к базе данных: %w", err)
	}

	defer pool.Close()

	if err = postgres.Migrate(ctx, cfg.DatabaseURI, migrations.FS); err != nil {
		return fmt.Errorf("применение миграций: %w", err)
	}

	logger.InfoContext(ctx, "схема базы данных актуальна")

	router, err := newRouter(cfg, logger, pool)
	if err != nil {
		return fmt.Errorf("сборка маршрутизатора: %w", err)
	}

	worker, err := newAccrualWorker(cfg, logger, pool)
	if err != nil {
		return fmt.Errorf("сборка фонового расчёта начислений: %w", err)
	}

	application, err := New(ctx, cfg, logger, router)
	if err != nil {
		return fmt.Errorf("инициализация приложения: %w", err)
	}

	application.AddBackgroundTask(BackgroundTask{
		Name: accrual.BackgroundTaskName,
		Run:  worker.Run,
	})

	if err = application.Serve(ctx); err != nil {
		return fmt.Errorf("работа приложения: %w", err)
	}

	logger.InfoContext(ctx, "сервис остановлен")

	return nil
}

// newAccrualWorker собирает фоновый воркер расчёта начислений вместе с его
func newAccrualWorker(cfg Config, logger *slog.Logger, pool *pgxpool.Pool) (*accrual.Worker, error) {
	client, err := accrual.NewClient(cfg.AccrualSystemAddress, cfg.AccrualRequestTimeout, cfg.AccrualRetryAfter)
	if err != nil {
		return nil, fmt.Errorf("инициализация клиента системы расчёта: %w", err)
	}

	service := service.NewAccruals(postgres.NewOrderRepository(pool), client, service.AccrualsConfig{
		BackoffBase:  cfg.AccrualBackoffBase,
		BackoffCap:   cfg.AccrualBackoffCap,
		PollInterval: cfg.AccrualPollInterval,
	})

	workerCfg := accrual.WorkerConfig{
		PollInterval:  cfg.AccrualPollInterval,
		BatchSize:     cfg.AccrualBatchSize,
		LeaseDuration: cfg.AccrualLeaseDuration,
	}

	if err = workerCfg.Validate(); err != nil {
		return nil, err
	}

	return accrual.NewWorker(service, logger, workerCfg), nil
}

// newRouter собирает маршрутизатор вместе со всеми зависимостями обработчиков.
func newRouter(cfg Config, logger *slog.Logger, pool *pgxpool.Pool) (*httptransport.Router, error) {
	hasher, err := auth.NewHasher(cfg.PasswordHashCost)
	if err != nil {
		return nil, fmt.Errorf("инициализация хеширования паролей: %w", err)
	}

	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	if err != nil {
		return nil, fmt.Errorf("инициализация выпуска токенов: %w", err)
	}

	authService := service.NewAuth(postgres.NewUserRepository(pool), hasher, tokens)
	orderService := service.NewOrders(postgres.NewOrderRepository(pool))
	balanceService := service.NewBalances(postgres.NewBalanceRepository(pool))

	return httptransport.NewRouter(httptransport.RouterConfig{
		Logger:              logger,
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
		Auth:                handlers.NewAuth(authService, cfg.TokenTTL),
		Orders:              handlers.NewOrders(orderService),
		Balance:             handlers.NewBalance(balanceService),
		Authenticator:       authService,
	})
}

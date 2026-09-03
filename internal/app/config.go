package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"gophermart/internal/auth"
	"gophermart/internal/logging"
)

// Значения конфигурации по умолчанию.
const (
	// DefaultRunAddress — адрес прослушивания HTTP-сервера, если он не задан
	DefaultRunAddress = ":8080"

	DefaultShutdownTimeout = 10 * time.Second

	DefaultReadHeaderTimeout = 5 * time.Second

	DefaultReadTimeout = 15 * time.Second

	DefaultWriteTimeout = 30 * time.Second

	DefaultIdleTimeout = 60 * time.Second

	DefaultMaxRequestBodyBytes int64 = 1 << 20

	DefaultDatabaseConnectTimeout = 10 * time.Second

	// DefaultPasswordHashCost — стоимость хеширования паролей по умолчанию.
	DefaultPasswordHashCost = 10

	DefaultTokenTTL = 24 * time.Hour

	// DefaultLogLevel — уровень журналирования по умолчанию.
	DefaultLogLevel = slog.LevelInfo

	// DefaultLogFormat — формат журналирования по умолчанию.
	DefaultLogFormat = logging.FormatJSON
)

// Имена переменных окружения, из которых читается конфигурация.
const (
	// EnvRunAddress задаёт адрес прослушивания HTTP-сервера.
	EnvRunAddress = "RUN_ADDRESS"

	// EnvDatabaseURI задаёт строку подключения к PostgreSQL.
	EnvDatabaseURI = "DATABASE_URI"

	// EnvAccrualSystemAddress задаёт базовый адрес системы расчёта начислений.
	EnvAccrualSystemAddress = "ACCRUAL_SYSTEM_ADDRESS"

	// EnvJWTSecret задаёт секрет подписи токенов аутентификации.
	EnvJWTSecret = "JWT_SECRET"

	// EnvPasswordHashCost задаёт стоимость хеширования паролей.
	EnvPasswordHashCost = "PASSWORD_HASH_COST"

	// EnvTokenTTL задаёт срок действия токена доступа.
	EnvTokenTTL = "TOKEN_TTL"

	// EnvLogLevel задаёт уровень журналирования.
	EnvLogLevel = "LOG_LEVEL"

	// EnvLogFormat задаёт формат журналирования.
	EnvLogFormat = "LOG_FORMAT"
)

// secretByteLen — длина случайно сгенерированного секрета подписи в байтах.
const secretByteLen = 32

// redactedPlaceholder подставляется вместо скрытых значений при журналировании.
const redactedPlaceholder = "REDACTED"

// ErrMissingConfig возвращается, когда обязательный параметр конфигурации не
var ErrMissingConfig = errors.New("обязательный параметр конфигурации не задан")

// Config содержит полный набор параметров запуска сервиса.
type Config struct {
	// RunAddress — адрес и порт прослушивания HTTP-сервера, флаг -a или
	RunAddress string

	// DatabaseURI — строка подключения к PostgreSQL, флаг -d или переменная
	DatabaseURI string

	// AccrualSystemAddress — базовый адрес системы расчёта начислений, флаг -r
	AccrualSystemAddress string

	// JWTSecret — секрет подписи токенов аутентификации. Читается из
	//nolint:gosec // Config не сериализуется наружу, а LogValue скрывает значение.
	JWTSecret string

	// JWTSecretGenerated сообщает, что секрет подписи не был задан извне и
	JWTSecretGenerated bool

	// LogLevel — минимальный уровень записей журнала.
	LogLevel slog.Level

	// LogFormat — формат вывода журнала, logging.FormatJSON или
	LogFormat string

	// ShutdownTimeout — время, отведённое активным запросам на завершение при
	ShutdownTimeout time.Duration

	// ReadHeaderTimeout — предельное время чтения заголовков запроса.
	ReadHeaderTimeout time.Duration

	// ReadTimeout — предельное время чтения запроса целиком.
	ReadTimeout time.Duration

	// WriteTimeout — предельное время записи ответа.
	WriteTimeout time.Duration

	// IdleTimeout — предельное время простоя keep-alive соединения.
	IdleTimeout time.Duration

	// MaxRequestBodyBytes — предельный размер тела запроса в байтах.
	MaxRequestBodyBytes int64

	// DatabaseConnectTimeout — предельное время проверки доступности
	DatabaseConnectTimeout time.Duration

	// PasswordHashCost — стоимость адаптивного хеширования паролей,
	PasswordHashCost int

	// TokenTTL — срок действия выпускаемых токенов доступа, переменная
	TokenTTL time.Duration
}

// LoadConfig собирает конфигурацию из аргументов командной строки и переменных
func LoadConfig(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	env := newEnvReader(lookupEnv)

	cfg := Config{
		RunAddress:             env.String(EnvRunAddress, DefaultRunAddress),
		DatabaseURI:            env.String(EnvDatabaseURI, ""),
		AccrualSystemAddress:   env.String(EnvAccrualSystemAddress, ""),
		JWTSecret:              env.String(EnvJWTSecret, ""),
		LogFormat:              env.String(EnvLogFormat, DefaultLogFormat),
		LogLevel:               DefaultLogLevel,
		ShutdownTimeout:        DefaultShutdownTimeout,
		ReadHeaderTimeout:      DefaultReadHeaderTimeout,
		ReadTimeout:            DefaultReadTimeout,
		WriteTimeout:           DefaultWriteTimeout,
		IdleTimeout:            DefaultIdleTimeout,
		MaxRequestBodyBytes:    DefaultMaxRequestBodyBytes,
		DatabaseConnectTimeout: DefaultDatabaseConnectTimeout,
		PasswordHashCost:       DefaultPasswordHashCost,
		TokenTTL:               DefaultTokenTTL,
	}

	if raw, ok := env.Lookup(EnvLogLevel); ok {
		level, err := parseLogLevel(raw)
		if err != nil {
			return Config{}, err
		}

		cfg.LogLevel = level
	}

	if raw, ok := env.Lookup(EnvPasswordHashCost); ok {
		cost, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("недопустимая стоимость хеширования %q: %w", raw, err)
		}

		cfg.PasswordHashCost = cost
	}

	if raw, ok := env.Lookup(EnvTokenTTL); ok {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("недопустимый срок действия токена %q: %w", raw, err)
		}

		cfg.TokenTTL = ttl
	}

	if err := applyFlags(&cfg, args); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	if cfg.JWTSecret == "" {
		secret, err := generateSecret()
		if err != nil {
			return Config{}, err
		}

		cfg.JWTSecret = secret
		cfg.JWTSecretGenerated = true
	}

	return cfg, nil
}

// LogValue возвращает представление конфигурации, безопасное для записи в
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("run_address", c.RunAddress),
		slog.String("database_uri", RedactDSN(c.DatabaseURI)),
		slog.String("accrual_system_address", c.AccrualSystemAddress),
		slog.String("jwt_secret", redactedPlaceholder),
		slog.Bool("jwt_secret_generated", c.JWTSecretGenerated),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.Int("password_hash_cost", c.PasswordHashCost),
		slog.Duration("token_ttl", c.TokenTTL),
	)
}

// validate проверяет, что обязательные параметры заданы, а значения
func (c Config) validate() error {
	if c.DatabaseURI == "" {
		return fmt.Errorf(
			"%w: адрес подключения к базе данных, флаг -d или переменная %s",
			ErrMissingConfig, EnvDatabaseURI,
		)
	}

	if c.AccrualSystemAddress == "" {
		return fmt.Errorf(
			"%w: адрес системы расчёта начислений, флаг -r или переменная %s",
			ErrMissingConfig, EnvAccrualSystemAddress,
		)
	}

	if c.PasswordHashCost < auth.MinCost || c.PasswordHashCost > auth.MaxCost {
		return fmt.Errorf(
			"недопустимая стоимость хеширования %d: ожидается от %d до %d",
			c.PasswordHashCost, auth.MinCost, auth.MaxCost,
		)
	}

	if c.TokenTTL <= 0 {
		return fmt.Errorf("недопустимый срок действия токена %s: ожидается положительное значение", c.TokenTTL)
	}

	if c.LogFormat != logging.FormatJSON && c.LogFormat != logging.FormatText {
		return fmt.Errorf(
			"недопустимый формат журнала %q: ожидается %q или %q",
			c.LogFormat, logging.FormatJSON, logging.FormatText,
		)
	}

	return nil
}

// RedactDSN скрывает пароль в строке подключения к базе данных, сохраняя
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return redactedPlaceholder
	}

	if parsed.User == nil {
		return parsed.String()
	}

	if _, hasPassword := parsed.User.Password(); !hasPassword {
		return parsed.String()
	}

	parsed.User = url.UserPassword(parsed.User.Username(), redactedPlaceholder)

	return parsed.String()
}

// applyFlags разбирает CLI-флаги поверх значений, уже прочитанных из окружения.
func applyFlags(cfg *Config, args []string) error {
	fs := flag.NewFlagSet("gophermart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.RunAddress, "a", cfg.RunAddress, "адрес и порт запуска HTTP-сервера")
	fs.StringVar(&cfg.DatabaseURI, "d", cfg.DatabaseURI, "строка подключения к PostgreSQL")
	fs.StringVar(&cfg.AccrualSystemAddress, "r", cfg.AccrualSystemAddress, "адрес системы расчёта начислений")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("разбор аргументов командной строки: %w", err)
	}

	return nil
}

// generateSecret создаёт случайный секрет подписи токенов на время работы
func generateSecret() (string, error) {
	buf := make([]byte, secretByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("генерация секрета подписи: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

// parseLogLevel разбирает уровень журналирования, заданный строкой.
func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("недопустимый уровень журнала %q: %w", raw, err)
	}

	return level, nil
}

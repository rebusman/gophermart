package app_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"gophermart/internal/app"
)

// envOf возвращает функцию поиска переменных окружения по фиксированной карте.
// Это делает разбор конфигурации независимым от окружения теста.
func envOf(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]

		return value, ok
	}
}

// requiredEnv содержит обязательные параметры, без которых разбор завершается
// ошибкой. Тесты, проверяющие другие свойства, используют его как основу.
func requiredEnv() map[string]string {
	return map[string]string{
		app.EnvDatabaseURI:          "postgres://user:db-secret-value@localhost:5432/gophermart",
		app.EnvAccrualSystemAddress: "http://localhost:8081",
		app.EnvJWTSecret:            "секрет",
	}
}

func TestRunAddressFromEnvironment(t *testing.T) {
	env := requiredEnv()
	env[app.EnvRunAddress] = ":9090"

	cfg, err := app.LoadConfig(nil, envOf(env))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if cfg.RunAddress != ":9090" {
		t.Errorf("адрес не взят из окружения: got %s, want :9090", cfg.RunAddress)
	}
}

func TestRunAddressFlagOverridesEnvironment(t *testing.T) {
	env := requiredEnv()
	env[app.EnvRunAddress] = ":9090"

	cfg, err := app.LoadConfig([]string{"-a", ":8081"}, envOf(env))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if cfg.RunAddress != ":8081" {
		t.Errorf("флаг не победил переменную окружения: got %s, want :8081", cfg.RunAddress)
	}
}

func TestRunAddressDefault(t *testing.T) {
	cfg, err := app.LoadConfig(nil, envOf(requiredEnv()))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if cfg.RunAddress != app.DefaultRunAddress {
		t.Errorf("не применено значение по умолчанию: got %s, want %s", cfg.RunAddress, app.DefaultRunAddress)
	}
}

func TestDatabaseURIAndAccrualAddressFromFlags(t *testing.T) {
	args := []string{
		"-d", "postgres://localhost:5432/db",
		"-r", "http://accrual:8080",
	}

	cfg, err := app.LoadConfig(args, envOf(nil))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if cfg.DatabaseURI != "postgres://localhost:5432/db" {
		t.Errorf("строка подключения не взята из флага: got %s", cfg.DatabaseURI)
	}

	if cfg.AccrualSystemAddress != "http://accrual:8080" {
		t.Errorf("адрес системы начислений не взят из флага: got %s", cfg.AccrualSystemAddress)
	}
}

func TestMissingRequiredParameters(t *testing.T) {
	tests := map[string]struct {
		env      map[string]string
		mentions string
	}{
		"нет строки подключения": {
			env:      map[string]string{app.EnvAccrualSystemAddress: "http://localhost:8081"},
			mentions: app.EnvDatabaseURI,
		},
		"нет адреса системы начислений": {
			env:      map[string]string{app.EnvDatabaseURI: "postgres://localhost:5432/db"},
			mentions: app.EnvAccrualSystemAddress,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := app.LoadConfig(nil, envOf(test.env))
			if err == nil {
				t.Fatal("ожидалась ошибка отсутствующего параметра")
			}

			if !errors.Is(err, app.ErrMissingConfig) {
				t.Errorf("неожиданный тип ошибки: %v", err)
			}

			if !strings.Contains(err.Error(), test.mentions) {
				t.Errorf("ошибка не называет параметр %s: %v", test.mentions, err)
			}
		})
	}
}

func TestSecretTakenFromEnvironment(t *testing.T) {
	cfg, err := app.LoadConfig(nil, envOf(requiredEnv()))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if cfg.JWTSecret != "секрет" {
		t.Errorf("секрет не взят из окружения: got %s", cfg.JWTSecret)
	}

	if cfg.JWTSecretGenerated {
		t.Error("заданный извне секрет помечен как сгенерированный")
	}
}

func TestSecretGeneratedWhenAbsent(t *testing.T) {
	env := requiredEnv()
	delete(env, app.EnvJWTSecret)

	first, err := app.LoadConfig(nil, envOf(env))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	second, err := app.LoadConfig(nil, envOf(env))
	if err != nil {
		t.Fatalf("повторный разбор конфигурации: %v", err)
	}

	if first.JWTSecret == "" {
		t.Fatal("сгенерированный секрет пуст")
	}

	if !first.JWTSecretGenerated {
		t.Error("не выставлен признак сгенерированного секрета")
	}

	if first.JWTSecret == second.JWTSecret {
		t.Error("секрет одинаков между запусками: генерация не случайна")
	}
}

func TestSecretGeneratedWhenEmpty(t *testing.T) {
	env := requiredEnv()
	env[app.EnvJWTSecret] = ""

	cfg, err := app.LoadConfig(nil, envOf(env))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	if !cfg.JWTSecretGenerated {
		t.Error("пустая переменная окружения должна считаться незаданной")
	}
}

func TestLogValueHidesSecrets(t *testing.T) {
	cfg, err := app.LoadConfig(nil, envOf(requiredEnv()))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	rendered := renderAttr(t, slog.Any("config", cfg))

	// Проверяются сами секретные значения, а не подстроки: имя атрибута
	// password_hash_cost содержит «pass», но секретом не является.
	for _, secret := range []string{"db-secret-value", "секрет"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("секрет %q попал в представление для журнала: %s", secret, rendered)
		}
	}

	if !strings.Contains(rendered, "localhost:5432") {
		t.Errorf("представление потеряло полезную часть строки подключения: %s", rendered)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := map[string]struct {
		dsn        string
		mustHide   string
		mustRemain string
	}{
		"с паролем": {
			dsn:        "postgres://user:s3cret@db:5432/gophermart?sslmode=disable",
			mustHide:   "s3cret",
			mustRemain: "db:5432",
		},
		"без пароля": {
			dsn:        "postgres://db:5432/gophermart",
			mustHide:   "",
			mustRemain: "db:5432",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := app.RedactDSN(test.dsn)

			if test.mustHide != "" && strings.Contains(got, test.mustHide) {
				t.Errorf("пароль не скрыт: %s", got)
			}

			if !strings.Contains(got, test.mustRemain) {
				t.Errorf("потеряна полезная часть строки: %s", got)
			}
		})
	}
}

func TestRedactDSNOfUnparsableValue(t *testing.T) {
	if got := app.RedactDSN("postgres://user:pass@[::1:5432/db"); strings.Contains(got, "pass") {
		t.Errorf("пароль из неразбираемой строки не скрыт: %s", got)
	}
}

func TestInvalidLogLevel(t *testing.T) {
	env := requiredEnv()
	env[app.EnvLogLevel] = "не уровень"

	if _, err := app.LoadConfig(nil, envOf(env)); err == nil {
		t.Error("ожидалась ошибка недопустимого уровня журнала")
	}
}

func TestInvalidLogFormat(t *testing.T) {
	env := requiredEnv()
	env[app.EnvLogFormat] = "xml"

	if _, err := app.LoadConfig(nil, envOf(env)); err == nil {
		t.Error("ожидалась ошибка недопустимого формата журнала")
	}
}

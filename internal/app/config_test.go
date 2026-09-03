package app_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

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

// TestAccrualDefaultsApplyWithoutAnyEnvironment закрепляет сценарий «Запуск без
// явных параметров расчёта»: сервис стартует, не зная ни одного параметра
// цикла, а конфигурация получает осмысленные значения по умолчанию.
func TestAccrualDefaultsApplyWithoutAnyEnvironment(t *testing.T) {
	cfg, err := app.LoadConfig(nil, envOf(requiredEnv()))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	durations := map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		"интервал опроса":    {cfg.AccrualPollInterval, app.DefaultAccrualPollInterval},
		"время обращения":    {cfg.AccrualRequestTimeout, app.DefaultAccrualRequestTimeout},
		"срок аренды":        {cfg.AccrualLeaseDuration, app.DefaultAccrualLeaseDuration},
		"база отсрочки":      {cfg.AccrualBackoffBase, app.DefaultAccrualBackoffBase},
		"потолок отсрочки":   {cfg.AccrualBackoffCap, app.DefaultAccrualBackoffCap},
		"пауза по умолчанию": {cfg.AccrualRetryAfter, app.DefaultAccrualRetryAfter},
	}

	for name, test := range durations {
		if test.got != test.want {
			t.Errorf("%s: got %s, want %s", name, test.got, test.want)
		}
	}

	if cfg.AccrualBatchSize != app.DefaultAccrualBatchSize {
		t.Errorf("размер порции: got %d, want %d", cfg.AccrualBatchSize, app.DefaultAccrualBatchSize)
	}

	// Соотношение, от которого зависит осмысленность аренды, обязано
	// выполняться уже на значениях по умолчанию.
	if cfg.AccrualLeaseDuration <= cfg.AccrualRequestTimeout {
		t.Errorf("срок аренды по умолчанию %s не превышает время обращения %s",
			cfg.AccrualLeaseDuration, cfg.AccrualRequestTimeout)
	}
}

// TestAccrualParametersFromEnvironment закрепляет чтение каждого параметра
// цикла из переменной окружения.
func TestAccrualParametersFromEnvironment(t *testing.T) {
	env := requiredEnv()
	env[app.EnvAccrualPollInterval] = "2s"
	env[app.EnvAccrualRequestTimeout] = "3s"
	env[app.EnvAccrualLeaseDuration] = "17s"
	env[app.EnvAccrualBackoffBase] = "4s"
	env[app.EnvAccrualBackoffCap] = "9m"
	env[app.EnvAccrualRetryAfter] = "45s"
	env[app.EnvAccrualBatchSize] = "7"

	cfg, err := app.LoadConfig(nil, envOf(env))
	if err != nil {
		t.Fatalf("разбор конфигурации: %v", err)
	}

	tests := map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		app.EnvAccrualPollInterval:   {cfg.AccrualPollInterval, 2 * time.Second},
		app.EnvAccrualRequestTimeout: {cfg.AccrualRequestTimeout, 3 * time.Second},
		app.EnvAccrualLeaseDuration:  {cfg.AccrualLeaseDuration, 17 * time.Second},
		app.EnvAccrualBackoffBase:    {cfg.AccrualBackoffBase, 4 * time.Second},
		app.EnvAccrualBackoffCap:     {cfg.AccrualBackoffCap, 9 * time.Minute},
		app.EnvAccrualRetryAfter:     {cfg.AccrualRetryAfter, 45 * time.Second},
	}

	for key, test := range tests {
		if test.got != test.want {
			t.Errorf("%s: got %s, want %s", key, test.got, test.want)
		}
	}

	if cfg.AccrualBatchSize != 7 {
		t.Errorf("%s: got %d, want 7", app.EnvAccrualBatchSize, cfg.AccrualBatchSize)
	}
}

// TestAccrualParametersRejectMalformedValues закрепляет отказ на значениях,
// которые не разбираются как длительность или число.
func TestAccrualParametersRejectMalformedValues(t *testing.T) {
	tests := map[string]string{
		app.EnvAccrualPollInterval: "быстро",
		app.EnvAccrualBatchSize:    "много",
	}

	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			env := requiredEnv()
			env[key] = value

			if _, err := app.LoadConfig(nil, envOf(env)); err == nil {
				t.Errorf("значение %q принято как %s", value, key)
			}
		})
	}
}

// TestAccrualLeaseMustExceedRequestTimeout закрепляет ключевое соотношение:
// аренда задания обязана переживать обращение к внешней системе, иначе тот же
// заказ возьмёт другой экземпляр, пока вызов ещё выполняется.
func TestAccrualLeaseMustExceedRequestTimeout(t *testing.T) {
	tests := []struct {
		name    string
		lease   string
		timeout string
		wantErr bool
	}{
		{name: "аренда короче обращения", lease: "3s", timeout: "5s", wantErr: true},
		{name: "аренда равна обращению", lease: "5s", timeout: "5s", wantErr: true},
		{name: "аренда длиннее обращения", lease: "6s", timeout: "5s", wantErr: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := requiredEnv()
			env[app.EnvAccrualLeaseDuration] = test.lease
			env[app.EnvAccrualRequestTimeout] = test.timeout

			_, err := app.LoadConfig(nil, envOf(env))

			if test.wantErr && err == nil {
				t.Error("нарушенное соотношение аренды и таймаута принято")
			}

			if !test.wantErr && err != nil {
				t.Errorf("корректное соотношение отвергнуто: %v", err)
			}
		})
	}
}

// TestAccrualParametersRejectNonPositiveValues закрепляет отказ на
// неположительных значениях: цикл с нулевым интервалом или нулевой порцией
// не имеет смысла.
func TestAccrualParametersRejectNonPositiveValues(t *testing.T) {
	tests := map[string]string{
		app.EnvAccrualPollInterval: "0s",
		app.EnvAccrualBackoffBase:  "-1s",
		app.EnvAccrualRetryAfter:   "0s",
		app.EnvAccrualBatchSize:    "0",
	}

	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			env := requiredEnv()
			env[key] = value

			if _, err := app.LoadConfig(nil, envOf(env)); err == nil {
				t.Errorf("неположительное значение %q принято как %s", value, key)
			}
		})
	}
}

// TestAccrualBackoffCapMustNotBeBelowBase закрепляет осмысленность отсрочки:
// потолок ниже базы означал бы, что рост отсрочки невозможен.
func TestAccrualBackoffCapMustNotBeBelowBase(t *testing.T) {
	env := requiredEnv()
	env[app.EnvAccrualBackoffBase] = "1m"
	env[app.EnvAccrualBackoffCap] = "10s"

	if _, err := app.LoadConfig(nil, envOf(env)); err == nil {
		t.Error("потолок отсрочки ниже её базы принят")
	}
}

// TestAccrualSystemAddressHasNoDefault закрепляет, что адрес внешней системы
// остаётся обязательным параметром запуска и значения по умолчанию не имеет.
func TestAccrualSystemAddressHasNoDefault(t *testing.T) {
	env := requiredEnv()
	delete(env, app.EnvAccrualSystemAddress)

	_, err := app.LoadConfig(nil, envOf(env))
	if !errors.Is(err, app.ErrMissingConfig) {
		t.Fatalf("ожидалась ошибка отсутствия обязательного параметра, получено: %v", err)
	}

	if !strings.Contains(err.Error(), app.EnvAccrualSystemAddress) {
		t.Errorf("ошибка не называет отсутствующий параметр: %v", err)
	}
}

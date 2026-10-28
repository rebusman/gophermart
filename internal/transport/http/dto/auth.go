package dto

import "log/slog"

// passwordPlaceholder подставляется вместо пароля при журналировании.
const passwordPlaceholder = "REDACTED"

// Credentials — тело запросов регистрации и аутентификации.
type Credentials struct {
	// Login — логин пользователя. Уникален, сравнивается с учётом регистра и
	Login string `json:"login"`

	// Password — пароль в открытом виде. Живёт только на время обработки
	//nolint:gosec // G117: имя поля совпадает с паттерном секрета, но это поле запроса пользователя, а не хранимый секрет.
	Password string `json:"password"`
}

// LogValue возвращает представление учётных данных, безопасное для записи в
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("login", c.Login),
		slog.String("password", passwordPlaceholder),
	)
}

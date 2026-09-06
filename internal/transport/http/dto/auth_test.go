package dto_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"gophermart/internal/transport/http/dto"
)

func TestCredentialsDecodeFromJSON(t *testing.T) {
	var credentials dto.Credentials

	body := `{"login":"gopher","password":"correct-horse-battery-staple"}`
	if err := json.Unmarshal([]byte(body), &credentials); err != nil {
		t.Fatalf("разбор тела запроса: %v", err)
	}

	if credentials.Login != "gopher" {
		t.Errorf("неожиданный логин: got %q", credentials.Login)
	}

	if credentials.Password != "correct-horse-battery-staple" {
		t.Errorf("неожиданный пароль: got %q", credentials.Password)
	}
}

func TestCredentialsIgnoreUnknownFields(t *testing.T) {
	var credentials dto.Credentials

	// Попытка передать чужой идентификатор пользователя не должна ни ломать
	// разбор, ни попадать в структуру: идентификатор берётся только из токена.
	body := `{"login":"gopher","password":"пароль","user_id":"чужой"}`
	if err := json.Unmarshal([]byte(body), &credentials); err != nil {
		t.Fatalf("разбор тела с лишним полем: %v", err)
	}

	if credentials.Login != "gopher" {
		t.Errorf("лишнее поле повлияло на разбор: got %q", credentials.Login)
	}
}

// TestCredentialsLogValueHidesPassword закрепляет требование «пароль не
// попадает в логи»: даже журналирование всей структуры целиком не раскрывает
// пароль.
func TestCredentialsLogValueHidesPassword(t *testing.T) {
	const password = "correct-horse-battery-staple"

	credentials := dto.Credentials{Login: "gopher", Password: password}

	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("проверка", slog.Any("credentials", credentials))

	rendered := buf.String()

	if strings.Contains(rendered, password) {
		t.Errorf("пароль попал в журнал: %s", rendered)
	}

	if !strings.Contains(rendered, "gopher") {
		t.Errorf("журнал потерял логин: %s", rendered)
	}
}

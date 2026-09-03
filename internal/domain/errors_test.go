package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"gophermart/internal/domain"
)

func TestDomainErrorsSurviveWrapping(t *testing.T) {
	tests := map[string]error{
		"занятый логин":              domain.ErrLoginTaken,
		"неверные учётные данные":    domain.ErrInvalidCredentials,
		"запрос не аутентифицирован": domain.ErrUnauthenticated,
		"пользователь не найден":     domain.ErrUserNotFound,
		"пустой логин":               domain.ErrEmptyLogin,
		"пустой пароль":              domain.ErrEmptyPassword,
		"слишком длинный пароль":     domain.ErrPasswordTooLong,
		"некорректный номер заказа":  domain.ErrInvalidOrderNumber,
		"чужой номер заказа":         domain.ErrOrderBelongsToAnotherUser,
		"неизвестный статус заказа":  domain.ErrUnknownOrderStatus,
	}

	for name, sentinel := range tests {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("внешний слой: %w", fmt.Errorf("внутренний слой: %w", sentinel))

			if !errors.Is(wrapped, sentinel) {
				t.Errorf("ошибка не распознаётся после оборачивания: %v", wrapped)
			}
		})
	}
}

func TestDomainErrorsAreDistinguishable(t *testing.T) {
	all := []error{
		domain.ErrLoginTaken,
		domain.ErrInvalidCredentials,
		domain.ErrUnauthenticated,
		domain.ErrUserNotFound,
		domain.ErrEmptyLogin,
		domain.ErrEmptyPassword,
		domain.ErrPasswordTooLong,
		domain.ErrInvalidOrderNumber,
		domain.ErrOrderBelongsToAnotherUser,
		domain.ErrUnknownOrderStatus,
	}

	for i, sentinel := range all {
		wrapped := fmt.Errorf("контекст: %w", sentinel)

		for j, other := range all {
			if i == j {
				continue
			}

			if errors.Is(wrapped, other) {
				t.Errorf("ошибка %v неотличима от %v", sentinel, other)
			}
		}
	}
}

func TestUserIDRoundTrip(t *testing.T) {
	id := domain.NewUserID()

	if id.IsZero() {
		t.Fatal("сгенерированный идентификатор пуст")
	}

	parsed, err := domain.ParseUserID(id.String())
	if err != nil {
		t.Fatalf("разбор идентификатора: %v", err)
	}

	if parsed != id {
		t.Errorf("идентификатор изменился при разборе: got %s, want %s", parsed, id)
	}
}

func TestParseUserIDRejectsMalformedValue(t *testing.T) {
	for _, raw := range []string{"", "не uuid", "12345"} {
		if _, err := domain.ParseUserID(raw); !errors.Is(err, domain.ErrInvalidUserID) {
			t.Errorf("значение %q принято как идентификатор: %v", raw, err)
		}
	}
}

func TestNewUserIDIsUnique(t *testing.T) {
	first := domain.NewUserID()
	second := domain.NewUserID()

	if first == second {
		t.Error("два вызова вернули один идентификатор")
	}
}

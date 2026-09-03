package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gophermart/internal/domain"
	"gophermart/internal/service"
)

func TestRegisterCreatesUserAndIssuesToken(t *testing.T) {
	users := &userRepositoryStub{}
	hasher := &hasherStub{}
	tokens := &tokenIssuerStub{}

	auth := service.NewAuth(users, hasher, tokens)

	token, err := auth.Register(t.Context(), "gopher", "пароль")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}

	if len(users.created) != 1 {
		t.Fatalf("ожидалась одна созданная учётная запись, создано %d", len(users.created))
	}

	created := users.created[0]

	if created.Login != "gopher" {
		t.Errorf("логин изменён при сохранении: got %q, want %q", created.Login, "gopher")
	}

	if created.PasswordHash == "пароль" || created.PasswordHash == "" {
		t.Errorf("пароль сохранён не в виде хеша: %q", created.PasswordHash)
	}

	if created.ID.IsZero() {
		t.Error("учётной записи не назначен идентификатор")
	}

	if created.CreatedAt.IsZero() {
		t.Error("учётной записи не проставлено время создания")
	}

	if token != "токен:"+created.ID.String() {
		t.Errorf("токен выпущен не для созданного пользователя: %q", token)
	}
}

func TestRegisterKeepsLoginAsGiven(t *testing.T) {
	users := &userRepositoryStub{}
	auth := service.NewAuth(users, &hasherStub{}, &tokenIssuerStub{})

	const login = "  Gopher  "

	if _, err := auth.Register(t.Context(), login, "пароль"); err != nil {
		t.Fatalf("регистрация: %v", err)
	}

	if users.created[0].Login != login {
		t.Errorf("логин нормализован при сохранении: got %q, want %q", users.created[0].Login, login)
	}
}

func TestRegisterRejectsEmptyFields(t *testing.T) {
	tests := map[string]struct {
		login    string
		password string
		want     error
	}{
		"пустой логин":           {login: "", password: "пароль", want: domain.ErrEmptyLogin},
		"логин из пробелов":      {login: "   \t ", password: "пароль", want: domain.ErrEmptyLogin},
		"пустой пароль":          {login: "gopher", password: "", want: domain.ErrEmptyPassword},
		"слишком длинный пароль": {login: "gopher", password: strings.Repeat("a", 73), want: domain.ErrPasswordTooLong},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			users := &userRepositoryStub{}
			hasher := &hasherStub{hash: func(password string) (string, error) {
				if password == "" {
					return "", domain.ErrEmptyPassword
				}

				if len(password) > 72 {
					return "", domain.ErrPasswordTooLong
				}

				return "хеш", nil
			}}

			auth := service.NewAuth(users, hasher, &tokenIssuerStub{})

			_, err := auth.Register(t.Context(), test.login, test.password)
			if !errors.Is(err, test.want) {
				t.Fatalf("неожиданная ошибка: got %v, want %v", err, test.want)
			}

			if len(users.created) != 0 {
				t.Error("учётная запись создана при некорректных данных")
			}
		})
	}
}

func TestRegisterReportsTakenLogin(t *testing.T) {
	users := &userRepositoryStub{createUser: func(context.Context, domain.User) error {
		return domain.ErrLoginTaken
	}}

	auth := service.NewAuth(users, &hasherStub{}, &tokenIssuerStub{})

	if _, err := auth.Register(t.Context(), "gopher", "пароль"); !errors.Is(err, domain.ErrLoginTaken) {
		t.Errorf("занятый логин не распознан: %v", err)
	}
}

func TestRegisterPropagatesRepositoryFailure(t *testing.T) {
	users := &userRepositoryStub{createUser: func(context.Context, domain.User) error {
		return errRepository
	}}

	auth := service.NewAuth(users, &hasherStub{}, &tokenIssuerStub{})

	_, err := auth.Register(t.Context(), "gopher", "пароль")
	if !errors.Is(err, errRepository) {
		t.Fatalf("сбой хранилища не пропущен наружу: %v", err)
	}

	for _, sentinel := range []error{domain.ErrLoginTaken, domain.ErrInvalidCredentials, domain.ErrEmptyLogin} {
		if errors.Is(err, sentinel) {
			t.Errorf("внутренний сбой подменён доменной ошибкой %v", sentinel)
		}
	}
}

func TestLoginIssuesTokenForValidCredentials(t *testing.T) {
	user := newUser(t, "хеш пароля")
	users := &userRepositoryStub{userByLogin: func(_ context.Context, login string) (domain.User, error) {
		if login != "gopher" {
			return domain.User{}, domain.ErrUserNotFound
		}

		return user, nil
	}}

	hasher := &hasherStub{}
	auth := service.NewAuth(users, hasher, &tokenIssuerStub{})

	token, err := auth.Login(t.Context(), "gopher", "пароль")
	if err != nil {
		t.Fatalf("вход: %v", err)
	}

	if token != "токен:"+user.ID.String() {
		t.Errorf("токен выпущен не для найденного пользователя: %q", token)
	}

	if hasher.comparedHash != user.PasswordHash || hasher.comparedPlain != "пароль" {
		t.Errorf("сравнение выполнено не с теми аргументами: hash=%q password=%q",
			hasher.comparedHash, hasher.comparedPlain)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	user := newUser(t, "хеш пароля")
	users := &userRepositoryStub{userByLogin: func(context.Context, string) (domain.User, error) {
		return user, nil
	}}

	hasher := &hasherStub{compareErr: domain.ErrInvalidCredentials}
	auth := service.NewAuth(users, hasher, &tokenIssuerStub{})

	token, err := auth.Login(t.Context(), "gopher", "неверный пароль")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("неверный пароль принят: %v", err)
	}

	if token != "" {
		t.Errorf("при неуспешном входе выдан токен: %q", token)
	}
}

// TestLoginComparesPasswordForUnknownUser закрепляет постоянное время ответа:
// путь «пользователь не найден» обязан выполнить сравнение пароля, а не
// вернуться досрочно, иначе несуществующий логин отвечал бы заметно быстрее
// неверного пароля.
func TestLoginComparesPasswordForUnknownUser(t *testing.T) {
	users := &userRepositoryStub{userByLogin: func(context.Context, string) (domain.User, error) {
		return domain.User{}, domain.ErrUserNotFound
	}}

	hasher := &hasherStub{}
	auth := service.NewAuth(users, hasher, &tokenIssuerStub{})

	_, err := auth.Login(t.Context(), "никого-нет", "пароль")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("несуществующий логин дал не тот отказ: %v", err)
	}

	if hasher.dummyCalls != 1 {
		t.Errorf("сравнение с фиктивным хешем не выполнено: вызовов %d", hasher.dummyCalls)
	}
}

// TestLoginFailuresAreIndistinguishable закрепляет требование неразличимости:
// несуществующий логин и неверный пароль обязаны давать одну и ту же ошибку.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	missing := service.NewAuth(
		&userRepositoryStub{userByLogin: func(context.Context, string) (domain.User, error) {
			return domain.User{}, domain.ErrUserNotFound
		}},
		&hasherStub{},
		&tokenIssuerStub{},
	)

	wrongPassword := service.NewAuth(
		&userRepositoryStub{userByLogin: func(context.Context, string) (domain.User, error) {
			return newUser(t, "хеш"), nil
		}},
		&hasherStub{compareErr: domain.ErrInvalidCredentials},
		&tokenIssuerStub{},
	)

	_, missingErr := missing.Login(t.Context(), "никого-нет", "пароль")
	_, wrongErr := wrongPassword.Login(t.Context(), "gopher", "не тот пароль")

	if !errors.Is(missingErr, domain.ErrInvalidCredentials) || !errors.Is(wrongErr, domain.ErrInvalidCredentials) {
		t.Fatalf("оба отказа должны быть ErrInvalidCredentials: %v, %v", missingErr, wrongErr)
	}
}

func TestLoginRejectsEmptyFields(t *testing.T) {
	auth := service.NewAuth(&userRepositoryStub{}, &hasherStub{}, &tokenIssuerStub{})

	if _, err := auth.Login(t.Context(), "  ", "пароль"); !errors.Is(err, domain.ErrEmptyLogin) {
		t.Errorf("пустой логин принят: %v", err)
	}

	if _, err := auth.Login(t.Context(), "gopher", ""); !errors.Is(err, domain.ErrEmptyPassword) {
		t.Errorf("пустой пароль принят: %v", err)
	}
}

func TestLoginPropagatesRepositoryFailure(t *testing.T) {
	users := &userRepositoryStub{userByLogin: func(context.Context, string) (domain.User, error) {
		return domain.User{}, errRepository
	}}

	auth := service.NewAuth(users, &hasherStub{}, &tokenIssuerStub{})

	_, err := auth.Login(t.Context(), "gopher", "пароль")
	if !errors.Is(err, errRepository) {
		t.Fatalf("сбой хранилища не пропущен наружу: %v", err)
	}

	if errors.Is(err, domain.ErrInvalidCredentials) {
		t.Error("внутренний сбой подменён отказом в учётных данных")
	}
}

func TestAuthenticateReturnsTokenOwner(t *testing.T) {
	user := newUser(t, "хеш")

	users := &userRepositoryStub{userByID: func(_ context.Context, id domain.UserID) (domain.User, error) {
		if id != user.ID {
			return domain.User{}, domain.ErrUserNotFound
		}

		return user, nil
	}}

	tokens := &tokenIssuerStub{parse: func(string) (domain.UserID, error) {
		return user.ID, nil
	}}

	auth := service.NewAuth(users, &hasherStub{}, tokens)

	got, err := auth.Authenticate(t.Context(), "токен")
	if err != nil {
		t.Fatalf("проверка токена: %v", err)
	}

	if got != user.ID {
		t.Errorf("неожиданный владелец токена: got %s, want %s", got, user.ID)
	}
}

// TestAuthenticateRejectsTokenOfMissingUser закрепляет решение проверять
// существование пользователя при каждом запросе: корректно подписанный токен
// удалённой учётной записи обязан давать отказ, а не пустые данные.
func TestAuthenticateRejectsTokenOfMissingUser(t *testing.T) {
	tokens := &tokenIssuerStub{parse: func(string) (domain.UserID, error) {
		return domain.NewUserID(), nil
	}}

	auth := service.NewAuth(&userRepositoryStub{}, &hasherStub{}, tokens)

	if _, err := auth.Authenticate(t.Context(), "токен"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("токен несуществующего пользователя принят: %v", err)
	}
}

func TestAuthenticateRejectsInvalidToken(t *testing.T) {
	tokens := &tokenIssuerStub{parse: func(string) (domain.UserID, error) {
		return domain.UserID{}, domain.ErrUnauthenticated
	}}

	auth := service.NewAuth(&userRepositoryStub{}, &hasherStub{}, tokens)

	if _, err := auth.Authenticate(t.Context(), "мусор"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("недействительный токен принят: %v", err)
	}
}

func TestAuthenticatePropagatesRepositoryFailure(t *testing.T) {
	users := &userRepositoryStub{userByID: func(context.Context, domain.UserID) (domain.User, error) {
		return domain.User{}, errRepository
	}}

	tokens := &tokenIssuerStub{parse: func(string) (domain.UserID, error) {
		return domain.NewUserID(), nil
	}}

	auth := service.NewAuth(users, &hasherStub{}, tokens)

	_, err := auth.Authenticate(t.Context(), "токен")
	if !errors.Is(err, errRepository) {
		t.Fatalf("сбой хранилища не пропущен наружу: %v", err)
	}

	if errors.Is(err, domain.ErrUnauthenticated) {
		t.Error("внутренний сбой подменён отказом в аутентификации")
	}
}

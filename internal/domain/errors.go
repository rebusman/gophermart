package domain

import "errors"

// Доменные ошибки предметной области.
var (
	// ErrLoginTaken возвращается, когда логин уже принадлежит другой учётной
	ErrLoginTaken = errors.New("логин уже занят")

	ErrInvalidCredentials = errors.New("неверная пара логин/пароль")

	// ErrUnauthenticated возвращается, когда запрос не предъявил действительный
	ErrUnauthenticated = errors.New("запрос не аутентифицирован")

	// ErrUserNotFound возвращается хранилищем, когда учётная запись не
	ErrUserNotFound = errors.New("пользователь не найден")

	// ErrInvalidUserID возвращается, когда строковое представление
	ErrInvalidUserID = errors.New("некорректный идентификатор пользователя")

	// ErrEmptyLogin возвращается, когда логин отсутствует, пуст или состоит
	ErrEmptyLogin = errors.New("логин не задан")

	// ErrEmptyPassword возвращается, когда пароль отсутствует или пуст.
	ErrEmptyPassword = errors.New("пароль не задан")

	// ErrPasswordTooLong возвращается, когда пароль длиннее предела,
	ErrPasswordTooLong = errors.New("пароль превышает допустимую длину")
)

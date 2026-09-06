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

	// ErrOrderNotFound возвращается хранилищем, когда заказа с указанным
	ErrOrderNotFound = errors.New("заказ не найден")

	// ErrInvalidOrderNumber возвращается, когда значение не является номером
	ErrInvalidOrderNumber = errors.New("некорректный номер заказа")

	// ErrOrderBelongsToAnotherUser возвращается, когда номер заказа уже
	ErrOrderBelongsToAnotherUser = errors.New("номер заказа принадлежит другому пользователю")

	// ErrUnknownOrderStatus возвращается, когда строковое представление
	ErrUnknownOrderStatus = errors.New("неизвестное состояние расчёта заказа")

	// ErrInsufficientFunds возвращается, когда на счёте лояльности не хватает
	ErrInsufficientFunds = errors.New("на счёте недостаточно баллов")

	// ErrNonPositiveWithdrawalSum возвращается, когда сумма списания равна
	ErrNonPositiveWithdrawalSum = errors.New("сумма списания не положительна")

	// ErrWithdrawalSumTooPrecise возвращается, когда сумма списания не
	ErrWithdrawalSumTooPrecise = errors.New("сумма списания задана точнее допустимого")

	// ErrBalanceNotFound возвращается хранилищем, когда счёта лояльности с
	ErrBalanceNotFound = errors.New("счёт лояльности не найден")

	// ErrAccrualRateLimited возвращается, когда внешняя система расчёта
	ErrAccrualRateLimited = errors.New("внешняя система расчёта отклонила обращение по лимиту запросов")

	// ErrEmptyLogin возвращается, когда логин отсутствует, пуст или состоит
	ErrEmptyLogin = errors.New("логин не задан")

	// ErrEmptyPassword возвращается, когда пароль отсутствует или пуст.
	ErrEmptyPassword = errors.New("пароль не задан")

	// ErrPasswordTooLong возвращается, когда пароль длиннее предела,
	ErrPasswordTooLong = errors.New("пароль превышает допустимую длину")
)

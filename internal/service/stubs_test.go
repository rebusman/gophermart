package service_test

import (
	"context"
	"errors"
	"testing"

	"gophermart/internal/domain"
)

// errRepository — произвольная внутренняя ошибка хранилища.
//
// Она не является доменной: сервис обязан пропустить её наружу как есть, а не
// подменить кодом отказа, понятным клиенту.
var errRepository = errors.New("сбой хранилища")

// userRepositoryStub подменяет хранилище пользователей в юнит-тестах.
//
// Поведение каждого метода задаётся функцией: тест описывает ровно тот случай,
// который проверяет, и не воспроизводит логику настоящего репозитория.
type userRepositoryStub struct {
	createUser  func(ctx context.Context, user domain.User) error
	userByLogin func(ctx context.Context, login string) (domain.User, error)
	userByID    func(ctx context.Context, id domain.UserID) (domain.User, error)

	created []domain.User
}

// hasherStub подменяет хеширование паролей и считает выполненные сравнения.
//
// Счётчики нужны проверке постоянного времени ответа: тест убеждается, что
// путь «пользователь не найден» действительно выполняет сравнение, а не
// возвращается досрочно.
type hasherStub struct {
	hash func(password string) (string, error)

	compareErr    error
	compareCalls  int
	dummyCalls    int
	comparedHash  string
	comparedPlain string
}

// tokenIssuerStub подменяет выпуск и разбор токенов доступа.
type tokenIssuerStub struct {
	issue func(userID domain.UserID) (string, error)
	parse func(token string) (domain.UserID, error)
}

// newUser собирает учётную запись с логином "gopher" и заданным хешем пароля.
func newUser(t *testing.T, hash string) domain.User {
	t.Helper()

	return domain.User{ID: domain.NewUserID(), Login: "gopher", PasswordHash: hash}
}

func (s *userRepositoryStub) CreateUser(ctx context.Context, user domain.User) error {
	s.created = append(s.created, user)

	if s.createUser == nil {
		return nil
	}

	return s.createUser(ctx, user)
}

func (s *userRepositoryStub) UserByLogin(ctx context.Context, login string) (domain.User, error) {
	if s.userByLogin == nil {
		return domain.User{}, domain.ErrUserNotFound
	}

	return s.userByLogin(ctx, login)
}

func (s *userRepositoryStub) UserByID(ctx context.Context, id domain.UserID) (domain.User, error) {
	if s.userByID == nil {
		return domain.User{}, domain.ErrUserNotFound
	}

	return s.userByID(ctx, id)
}

func (s *hasherStub) Hash(password string) (string, error) {
	if s.hash == nil {
		return "хеш:" + password, nil
	}

	return s.hash(password)
}

func (s *hasherStub) Compare(hash, password string) error {
	s.compareCalls++
	s.comparedHash = hash
	s.comparedPlain = password

	return s.compareErr
}

func (s *hasherStub) CompareDummy() error {
	s.dummyCalls++

	return domain.ErrInvalidCredentials
}

func (s *tokenIssuerStub) Issue(userID domain.UserID) (string, error) {
	if s.issue == nil {
		return "токен:" + userID.String(), nil
	}

	return s.issue(userID)
}

func (s *tokenIssuerStub) Parse(token string) (domain.UserID, error) {
	if s.parse == nil {
		return domain.UserID{}, domain.ErrUnauthenticated
	}

	return s.parse(token)
}

// orderRepositoryStub подменяет хранилище заказов в юнит-тестах.
//
// Поля описывают исход каждого обращения, а счётчики позволяют убедиться, что
// владелец занятого номера выясняется ровно тогда, когда вставка сообщила о
// конфликте, и ни разу больше.
type orderRepositoryStub struct {
	created   bool
	createErr error

	owner    domain.UserID
	ownerErr error

	orders  []domain.Order
	listErr error

	createdOrders []domain.Order
	ownerCalls    int
	listedUser    domain.UserID
}

func (s *orderRepositoryStub) CreateOrder(_ context.Context, order domain.Order) (bool, error) {
	s.createdOrders = append(s.createdOrders, order)

	if s.createErr != nil {
		return false, s.createErr
	}

	return s.created, nil
}

func (s *orderRepositoryStub) OrderOwner(_ context.Context, _ domain.OrderNumber) (domain.UserID, error) {
	s.ownerCalls++

	if s.ownerErr != nil {
		return domain.UserID{}, s.ownerErr
	}

	return s.owner, nil
}

func (s *orderRepositoryStub) OrdersByUser(_ context.Context, userID domain.UserID) ([]domain.Order, error) {
	s.listedUser = userID

	if s.listErr != nil {
		return nil, s.listErr
	}

	return s.orders, nil
}

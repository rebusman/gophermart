package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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

// balanceRepositoryStub подменяет хранилище счёта лояльности в юнит-тестах.
//
// Счётчики позволяют убедиться, что при неположительной сумме сервис вовсе не
// обращается к хранилищу, а не полагается на отказ базы данных.
type balanceRepositoryStub struct {
	balance    domain.Balance
	balanceErr error

	created     bool
	withdrawErr error

	withdrawals []domain.Withdrawal
	listErr     error

	balanceCalls  int
	withdrawCalls int
	withdrawn     []domain.Withdrawal
	listedFor     domain.UserID
	balanceFor    domain.UserID
}

func (s *balanceRepositoryStub) Balance(_ context.Context, userID domain.UserID) (domain.Balance, error) {
	s.balanceCalls++
	s.balanceFor = userID

	if s.balanceErr != nil {
		return domain.Balance{}, s.balanceErr
	}

	return s.balance, nil
}

func (s *balanceRepositoryStub) Withdraw(_ context.Context, withdrawal domain.Withdrawal) (bool, error) {
	s.withdrawCalls++
	s.withdrawn = append(s.withdrawn, withdrawal)

	if s.withdrawErr != nil {
		return false, s.withdrawErr
	}

	return s.created, nil
}

func (s *balanceRepositoryStub) WithdrawalsByUser(
	_ context.Context,
	userID domain.UserID,
) ([]domain.Withdrawal, error) {
	s.listedFor = userID

	if s.listErr != nil {
		return nil, s.listErr
	}

	return s.withdrawals, nil
}

// Ошибки, воспроизводящие недружественные исходы обращения к внешней системе.
//
// Тексты имитируют реальные причины, но сами значения объявлены здесь: сервис
// не должен зависеть от того, какие именно ошибки возвращает конкретный
// клиент, — он различает только превышение лимита запросов и всё остальное.
var (
	errOrderNotRegistered = errors.New("заказ не зарегистрирован в системе расчёта")
	errExternalFailure    = errors.New("внешняя система расчёта ответила ошибкой")
	errNetwork            = errors.New("соединение разорвано")
	errUnknownStatus      = errors.New("внешняя система расчёта вернула неизвестный статус")
)

// accrualClientStub подменяет клиента внешней системы расчёта.
type accrualClientStub struct {
	result domain.AccrualResult
	err    error

	calls      int
	gotNumbers []domain.OrderNumber
}

func (s *accrualClientStub) OrderAccrual(
	_ context.Context,
	number domain.OrderNumber,
) (domain.AccrualResult, error) {
	s.calls++
	s.gotNumbers = append(s.gotNumbers, number)

	if s.err != nil {
		return domain.AccrualResult{}, s.err
	}

	return s.result, nil
}

// appliedResult фиксирует аргументы одного применения результата расчёта.
type appliedResult struct {
	job     domain.AccrualJob
	status  domain.OrderStatus
	accrual *decimal.Decimal
}

// accrualRepositoryStub подменяет хранилище фонового расчёта в юнит-тестах.
//
// Счётчики позволяют убедиться, что каждый исход обращения приводит ровно к
// той операции над заказом, которая ему соответствует, и ни к какой другой.
type accrualRepositoryStub struct {
	jobs     []domain.AccrualJob
	claimErr error

	applied  bool
	applyErr error

	processingErr error
	rescheduleErr error
	releaseErr    error

	claimedBatch int
	claimedLease time.Duration

	appliedResults []appliedResult

	processingCalls int
	processingDelay time.Duration

	rescheduleCalls int
	rescheduleDelay time.Duration

	releaseCalls    int
	releasedNumbers []domain.OrderNumber
	releaseDelay    time.Duration
}

func (s *accrualRepositoryStub) ClaimAccrualJobs(
	_ context.Context,
	batchSize int,
	lease time.Duration,
) ([]domain.AccrualJob, error) {
	s.claimedBatch = batchSize
	s.claimedLease = lease

	if s.claimErr != nil {
		return nil, s.claimErr
	}

	return s.jobs, nil
}

func (s *accrualRepositoryStub) ApplyAccrual(
	_ context.Context,
	job domain.AccrualJob,
	status domain.OrderStatus,
	accrual *decimal.Decimal,
) (bool, error) {
	if s.applyErr != nil {
		return false, s.applyErr
	}

	s.appliedResults = append(s.appliedResults, appliedResult{job: job, status: status, accrual: accrual})

	return s.applied, nil
}

func (s *accrualRepositoryStub) MarkAccrualProcessing(
	_ context.Context,
	_ domain.OrderNumber,
	delay time.Duration,
) error {
	s.processingCalls++
	s.processingDelay = delay

	return s.processingErr
}

func (s *accrualRepositoryStub) RescheduleAccrualJob(
	_ context.Context,
	_ domain.OrderNumber,
	delay time.Duration,
) error {
	s.rescheduleCalls++
	s.rescheduleDelay = delay

	return s.rescheduleErr
}

func (s *accrualRepositoryStub) ReleaseAccrualJobs(
	_ context.Context,
	numbers []domain.OrderNumber,
	delay time.Duration,
) error {
	s.releaseCalls++
	s.releasedNumbers = numbers
	s.releaseDelay = delay

	return s.releaseErr
}

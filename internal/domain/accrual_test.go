package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gophermart/internal/domain"
)

// TestRateLimitErrorCarriesPause закрепляет перенос длительности паузы:
// решение о приостановке опроса принимает воркер, а назначает её внешняя
// система, и значение обязано доходить до него распознаваемым.
func TestRateLimitErrorCarriesPause(t *testing.T) {
	const pause = 90 * time.Second

	err := error(&domain.RateLimitError{RetryAfter: pause})

	if !errors.Is(err, domain.ErrAccrualRateLimited) {
		t.Errorf("отказ не распознаётся через errors.Is: %v", err)
	}

	wrapped := errors.Join(errors.New("внешний слой"), err)

	var rateLimit *domain.RateLimitError
	if !errors.As(wrapped, &rateLimit) {
		t.Fatalf("длительность паузы не извлекается после оборачивания: %v", wrapped)
	}

	if rateLimit.RetryAfter != pause {
		t.Errorf("неожиданная пауза: got %s, want %s", rateLimit.RetryAfter, pause)
	}

	if !strings.Contains(err.Error(), pause.String()) {
		t.Errorf("описание отказа не называет длительность паузы: %s", err)
	}
}

// TestRateLimitErrorIsDistinguishableFromOtherDomainErrors закрепляет, что
// отказ по лимиту запросов не путается с другими доменными ошибками.
func TestRateLimitErrorIsDistinguishableFromOtherDomainErrors(t *testing.T) {
	err := error(&domain.RateLimitError{RetryAfter: time.Second})

	for _, other := range []error{
		domain.ErrOrderNotFound,
		domain.ErrInsufficientFunds,
		domain.ErrBalanceNotFound,
		domain.ErrUnknownOrderStatus,
	} {
		if errors.Is(err, other) {
			t.Errorf("отказ по лимиту неотличим от %v", other)
		}
	}
}

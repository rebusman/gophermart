package domain_test

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
)

// TestValidateWithdrawalSumAcceptsRepresentableValues закрепляет, что сумма,
// точно представимая двумя знаками после запятой, принимается.
//
// Избыточные нули в дробной части приемлемы: сравнивается величина, а не её
// запись.
func TestValidateWithdrawalSumAcceptsRepresentableValues(t *testing.T) {
	for _, raw := range []string{"0.01", "1", "1.0", "1.000", "751.5", "42.42", "123456789012345.67"} {
		t.Run(raw, func(t *testing.T) {
			sum, err := decimal.NewFromString(raw)
			if err != nil {
				t.Fatalf("разбор суммы: %v", err)
			}

			if err = domain.ValidateWithdrawalSum(sum); err != nil {
				t.Errorf("сумма %s отвергнута: %v", raw, err)
			}
		})
	}
}

// TestValidateWithdrawalSumRejectsNonPositive закрепляет требование
// положительности суммы списания.
func TestValidateWithdrawalSumRejectsNonPositive(t *testing.T) {
	for _, raw := range []string{"0", "0.00", "-0.01", "-100"} {
		t.Run(raw, func(t *testing.T) {
			sum, err := decimal.NewFromString(raw)
			if err != nil {
				t.Fatalf("разбор суммы: %v", err)
			}

			if err = domain.ValidateWithdrawalSum(sum); !errors.Is(err, domain.ErrNonPositiveWithdrawalSum) {
				t.Errorf("ожидалась ошибка неположительной суммы, получено: %v", err)
			}
		})
	}
}

// TestValidateWithdrawalSumRejectsExcessivePrecision закрепляет отклонение
// суммы, которую хранилище округлило бы молча.
//
// Каждое значение подобрано под свой способ навредить: 0.001 округляется до
// нуля и нарушило бы ограничение положительности уже внутри транзакции, дав
// клиенту 500; 0.005 округляется в разные стороны в двух слагаемых одного
// оператора и рассогласовало бы остаток с суммой списаний; 1.999 молча стало
// бы суммой 2.00, отличной от отправленной.
func TestValidateWithdrawalSumRejectsExcessivePrecision(t *testing.T) {
	for _, raw := range []string{"0.001", "0.005", "1.999", "100.123", "0.0000001"} {
		t.Run(raw, func(t *testing.T) {
			sum, err := decimal.NewFromString(raw)
			if err != nil {
				t.Fatalf("разбор суммы: %v", err)
			}

			if err = domain.ValidateWithdrawalSum(sum); !errors.Is(err, domain.ErrWithdrawalSumTooPrecise) {
				t.Errorf("ожидалась ошибка избыточной точности, получено: %v", err)
			}
		})
	}
}

// TestValidateWithdrawalSumChecksSignBeforePrecision закрепляет порядок
// проверок: отрицательная переточная сумма отвергается как неположительная.
//
// Порядок существен для кода ответа только тем, что обе ошибки дают 400, но
// сообщение в журнале должно называть первую причину, а не вторую.
func TestValidateWithdrawalSumChecksSignBeforePrecision(t *testing.T) {
	sum, err := decimal.NewFromString("-0.001")
	if err != nil {
		t.Fatalf("разбор суммы: %v", err)
	}

	if err = domain.ValidateWithdrawalSum(sum); !errors.Is(err, domain.ErrNonPositiveWithdrawalSum) {
		t.Errorf("ожидалась ошибка неположительной суммы, получено: %v", err)
	}
}

// TestMoneyScaleMatchesSchema закрепляет связь доменной константы со схемой:
// значение обязано совпадать с масштабом колонок NUMERIC(18,2).
func TestMoneyScaleMatchesSchema(t *testing.T) {
	if domain.MoneyScale != 2 {
		t.Errorf("масштаб денежных значений разошёлся со схемой: got %d, want 2", domain.MoneyScale)
	}
}

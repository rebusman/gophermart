package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
	"gophermart/internal/transport/http/dto"
	"gophermart/internal/transport/http/middleware"
)

// BalanceService реализует прикладные сценарии работы со счётом лояльности.
type BalanceService interface {
	// Balance возвращает состояние счёта пользователя.
	Balance(ctx context.Context, userID domain.UserID) (domain.Balance, error)

	// Withdraw списывает сумму со счёта пользователя.
	Withdraw(ctx context.Context, number domain.OrderNumber, sum decimal.Decimal, userID domain.UserID) error

	// Withdrawals возвращает списания пользователя от самых новых к самым
	Withdrawals(ctx context.Context, userID domain.UserID) ([]domain.Withdrawal, error)
}

// Balance обслуживает маршруты состояния счёта, списания баллов и истории
type Balance struct {
	service BalanceService
}

// NewBalance создаёт обработчик маршрутов счёта лояльности.
func NewBalance(service BalanceService) *Balance {
	return &Balance{service: service}
}

// Get обслуживает GET /api/user/balance.
func (h *Balance) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	balance, err := h.service.Balance(r.Context(), userID)
	if err != nil {
		writeError(w, r, "чтение состояния счёта", err)

		return
	}

	writeJSON(w, r, "сериализация состояния счёта", dto.NewBalance(balance.Current, balance.Withdrawn))
}

// Withdraw обслуживает POST /api/user/balance/withdraw.
func (h *Balance) Withdraw(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	request, ok := decodeWithdrawRequest(w, r)
	if !ok {
		return
	}

	number, err := domain.ParseOrderNumber(request.Order)
	if err != nil {
		writeError(w, r, "списание баллов", err)

		return
	}

	if err = h.service.Withdraw(r.Context(), number, request.Sum.Decimal, userID); err != nil {
		writeError(w, r, "списание баллов", err)

		return
	}

	writeStatus(w, http.StatusOK)
}

// Withdrawals обслуживает GET /api/user/withdrawals.
func (h *Balance) Withdrawals(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	withdrawals, err := h.service.Withdrawals(r.Context(), userID)
	if err != nil {
		writeError(w, r, "выдача истории списаний", err)

		return
	}

	if len(withdrawals) == 0 {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	payload := make([]dto.Withdrawal, 0, len(withdrawals))
	for _, withdrawal := range withdrawals {
		payload = append(payload, dto.NewWithdrawal(
			withdrawal.OrderNumber.String(), withdrawal.Sum, withdrawal.ProcessedAt))
	}

	writeJSON(w, r, "сериализация истории списаний", payload)
}

// decodeWithdrawRequest разбирает тело запроса на списание и проверяет его
func decodeWithdrawRequest(w http.ResponseWriter, r *http.Request) (dto.WithdrawRequest, bool) {
	ctx := r.Context()

	var request dto.WithdrawRequest

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeStatus(w, http.StatusRequestEntityTooLarge)

			return dto.WithdrawRequest{}, false
		}

		logging.FromContext(ctx).DebugContext(ctx, "тело запроса не разобрано", logging.ErrorAttr(err))
		writeStatus(w, http.StatusBadRequest)

		return dto.WithdrawRequest{}, false
	}

	// Окружающие пробельные символы отбрасываются до разбора по той же
	request.Order = strings.TrimSpace(request.Order)

	if request.Order == "" {
		logging.FromContext(ctx).DebugContext(ctx, "запрос не содержит номера заказа")
		writeStatus(w, http.StatusBadRequest)

		return dto.WithdrawRequest{}, false
	}

	if err := domain.ValidateWithdrawalSum(request.Sum.Decimal); err != nil {
		writeError(w, r, "списание баллов", err)

		return dto.WithdrawRequest{}, false
	}

	return request, true
}

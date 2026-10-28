package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gophermart/internal/domain"
	"gophermart/internal/logging"
	"gophermart/internal/transport/http/dto"
	"gophermart/internal/transport/http/middleware"
)

// OrderService реализует прикладные сценарии работы с заказами.
type OrderService interface {
	// Upload закрепляет номер заказа за пользователем и сообщает исход.
	Upload(ctx context.Context, number domain.OrderNumber, userID domain.UserID) (domain.OrderUpload, error)

	// List возвращает заказы пользователя от самых новых к самым старым.
	List(ctx context.Context, userID domain.UserID) ([]domain.Order, error)
}

// Orders обслуживает маршруты загрузки номера заказа и списка заказов.
type Orders struct {
	service OrderService
}

// NewOrders создаёт обработчик маршрутов заказов.
func NewOrders(service OrderService) *Orders {
	return &Orders{service: service}
}

// Upload обслуживает POST /api/user/orders.
func (h *Orders) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	raw, ok := readOrderNumberBody(w, r)
	if !ok {
		return
	}

	number, err := domain.ParseOrderNumber(raw)
	if err != nil {
		writeError(w, r, "загрузка номера заказа", err)

		return
	}

	outcome, err := h.service.Upload(r.Context(), number, userID)
	if err != nil {
		writeError(w, r, "загрузка номера заказа", err)

		return
	}

	switch outcome {
	case domain.OrderUploadAccepted:
		writeStatus(w, http.StatusAccepted)
	case domain.OrderUploadDuplicate:
		writeStatus(w, http.StatusOK)
	case domain.OrderUploadUnknown:
		writeUnknownOutcome(w, r, outcome)
	default:
		writeUnknownOutcome(w, r, outcome)
	}
}

// List обслуживает GET /api/user/orders.
func (h *Orders) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	orders, err := h.service.List(r.Context(), userID)
	if err != nil {
		writeError(w, r, "выдача списка заказов", err)

		return
	}

	if len(orders) == 0 {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	payload := make([]dto.Order, 0, len(orders))
	for _, order := range orders {
		payload = append(payload, newOrderDTO(order))
	}

	writeJSON(w, r, "сериализация списка заказов", payload)
}

// newOrderDTO переводит доменный заказ в представление ответа.
func newOrderDTO(order domain.Order) dto.Order {
	var accrual *dto.Money
	if order.Accrual != nil {
		accrual = dto.MoneyPtr(*order.Accrual)
	}

	return dto.NewOrder(order.Number.String(), order.Status.String(), accrual, order.UploadedAt)
}

// userIDFromRequest извлекает идентификатор пользователя из контекста запроса.
func userIDFromRequest(w http.ResponseWriter, r *http.Request) (domain.UserID, bool) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		ctx := r.Context()
		logging.FromContext(ctx).ErrorContext(ctx,
			"защищённый маршрут вызван без проверки токена доступа")
		writeStatus(w, http.StatusInternalServerError)

		return domain.UserID{}, false
	}

	return userID, true
}

// readOrderNumberBody читает тело запроса как номер заказа.
func readOrderNumberBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeStatus(w, http.StatusRequestEntityTooLarge)

			return "", false
		}

		logging.FromContext(ctx).DebugContext(ctx, "тело запроса не прочитано", logging.ErrorAttr(err))
		writeStatus(w, http.StatusBadRequest)

		return "", false
	}

	raw := strings.TrimSpace(string(body))
	if raw == "" {
		logging.FromContext(ctx).DebugContext(ctx, "запрос не содержит номера заказа")
		writeStatus(w, http.StatusBadRequest)

		return "", false
	}

	return raw, true
}

// writeUnknownOutcome отвечает на исход, которого сервис возвращать не должен.
func writeUnknownOutcome(w http.ResponseWriter, r *http.Request, outcome domain.OrderUpload) {
	ctx := r.Context()
	logging.FromContext(ctx).ErrorContext(ctx,
		"сервис вернул неизвестный исход загрузки заказа",
		slog.String("outcome", outcome.String()))
	writeStatus(w, http.StatusInternalServerError)
}

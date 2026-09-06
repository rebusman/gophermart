package accrual

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"gophermart/internal/domain"
)

// Статусы расчёта, определённые контрактом внешней системы.
const (
	// StatusRegistered — заказ зарегистрирован в системе расчёта, но расчёт
	StatusRegistered = "REGISTERED"

	// StatusProcessing — расчёт выполняется.
	StatusProcessing = "PROCESSING"

	// StatusInvalid — система расчёта отказала в начислении.
	StatusInvalid = "INVALID"

	// StatusProcessed — расчёт завершён.
	StatusProcessed = "PROCESSED"
)

// orderPathPrefix — путь ресурса расчёта относительно базового адреса.
const orderPathPrefix = "/api/orders/"

// Ошибки обращения к внешней системе расчёта.
var (
	// ErrOrderNotRegistered возвращается, когда внешняя система не знает
	ErrOrderNotRegistered = errors.New("заказ не зарегистрирован в системе расчёта")

	// ErrServerFailure возвращается при внутренней ошибке внешней системы и
	ErrServerFailure = errors.New("внешняя система расчёта ответила ошибкой")

	// ErrUnknownStatus возвращается, когда ответ содержит статус расчёта вне
	ErrUnknownStatus = errors.New("внешняя система расчёта вернула неизвестный статус")

	// ErrInvalidBaseURL возвращается, когда базовый адрес внешней системы не
	ErrInvalidBaseURL = errors.New("некорректный адрес системы расчёта начислений")
)

// orderResponse — тело ответа внешней системы на запрос результата расчёта.
type orderResponse struct {
	Order   string           `json:"order"`
	Status  string           `json:"status"`
	Accrual *decimal.Decimal `json:"accrual"`
}

// Client обращается к внешней системе расчёта начислений по HTTP.
type Client struct {
	baseURL           *url.URL
	http              *http.Client
	timeout           time.Duration
	defaultRetryAfter time.Duration
}

// NewClient создаёт клиент внешней системы расчёта.
func NewClient(baseURL string, timeout, defaultRetryAfter time.Duration) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidBaseURL, baseURL)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: ожидается абсолютный адрес, получено %q", ErrInvalidBaseURL, baseURL)
	}

	return &Client{
		baseURL: parsed,
		http: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		timeout:           timeout,
		defaultRetryAfter: defaultRetryAfter,
	}, nil
}

// OrderAccrual возвращает результат расчёта по номеру заказа.
func (c *Client) OrderAccrual(ctx context.Context, number domain.OrderNumber) (domain.AccrualResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	target := c.baseURL.JoinPath(orderPathPrefix, number.String())

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return domain.AccrualResult{}, fmt.Errorf("создание запроса к системе расчёта: %w", err)
	}

	//nolint:gosec // G704: адрес не содержит непроверенного ввода, см. комментарий выше.
	response, err := c.http.Do(request)
	if err != nil {
		return domain.AccrualResult{}, fmt.Errorf("обращение к системе расчёта: %w", err)
	}

	defer func() {
		// Тело закрывается всегда: незакрытое тело удерживает соединение и
		_ = response.Body.Close()
	}()

	return c.result(response)
}

// result разбирает ответ внешней системы в результат либо в ошибку.
func (c *Client) result(response *http.Response) (domain.AccrualResult, error) {
	switch response.StatusCode {
	case http.StatusOK:
		return c.decode(response)
	case http.StatusNoContent:
		return domain.AccrualResult{}, ErrOrderNotRegistered
	case http.StatusTooManyRequests:
		return domain.AccrualResult{}, &domain.RateLimitError{RetryAfter: c.retryAfter(response)}
	default:
		return domain.AccrualResult{}, fmt.Errorf("%w: код %d", ErrServerFailure, response.StatusCode)
	}
}

// decode разбирает тело успешного ответа и отображает статус в состояние
func (c *Client) decode(response *http.Response) (domain.AccrualResult, error) {
	var payload orderResponse

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return domain.AccrualResult{}, fmt.Errorf("разбор ответа системы расчёта: %w", err)
	}

	status, err := ParseStatus(payload.Status)
	if err != nil {
		return domain.AccrualResult{}, err
	}

	accrual := payload.Accrual

	if status != domain.OrderStatusProcessed {
		accrual = nil
	}

	if accrual != nil && accrual.IsNegative() {
		return domain.AccrualResult{}, fmt.Errorf("%w: отрицательное начисление %s", ErrServerFailure, accrual)
	}

	return domain.AccrualResult{Status: status, Accrual: accrual}, nil
}

// retryAfter возвращает длительность паузы, назначенную внешней системой.
func (c *Client) retryAfter(response *http.Response) time.Duration {
	raw := response.Header.Get("Retry-After")
	if raw == "" {
		return c.defaultRetryAfter
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return c.defaultRetryAfter
	}

	return time.Duration(seconds) * time.Second
}

// ParseStatus отображает статус расчёта внешней системы в состояние заказа.
func ParseStatus(raw string) (domain.OrderStatus, error) {
	switch raw {
	case StatusRegistered, StatusProcessing:
		return domain.OrderStatusProcessing, nil
	case StatusInvalid:
		return domain.OrderStatusInvalid, nil
	case StatusProcessed:
		return domain.OrderStatusProcessed, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownStatus, raw)
	}
}

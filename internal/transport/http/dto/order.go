package dto

import "time"

// Order — заказ в теле ответа на запрос списка заказов.
type Order struct {
	// Number — номер заказа: последовательность десятичных цифр.
	Number string `json:"number"`

	// Status — состояние расчёта: NEW, PROCESSING, INVALID или PROCESSED.
	Status string `json:"status"`

	Accrual *Money `json:"accrual,omitempty"`

	// UploadedAt — момент загрузки номера заказа в UTC.
	UploadedAt time.Time `json:"uploaded_at"`
}

// NewOrder собирает DTO заказа из его составляющих.
func NewOrder(number, status string, accrual *Money, uploadedAt time.Time) Order {
	return Order{
		Number:     number,
		Status:     status,
		Accrual:    accrual,
		UploadedAt: uploadedAt.UTC(),
	}
}

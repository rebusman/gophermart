package domain

// OrderUpload — исход загрузки номера заказа.
type OrderUpload int

// Исходы загрузки номера заказа.
const (
	// OrderUploadUnknown — нулевое значение, не соответствующее ни одному
	OrderUploadUnknown OrderUpload = iota

	OrderUploadAccepted

	OrderUploadDuplicate
)

// String возвращает читаемое имя исхода для журнала и сообщений об ошибках.
func (u OrderUpload) String() string {
	switch u {
	case OrderUploadAccepted:
		return "принят"
	case OrderUploadDuplicate:
		return "уже загружен"
	case OrderUploadUnknown:
		return "не определён"
	default:
		return "не определён"
	}
}

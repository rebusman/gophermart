package integration_test

import (
	"bytes"
	"sync"
)

// syncBuffer накапливает журнал сервиса, работающего в отдельной goroutine, и
// допускает одновременное чтение из теста.
//
// bytes.Buffer для этого непригоден: одновременные запись логгером и чтение
// проверкой являются гонкой.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write добавляет данные в буфер.
func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

// String возвращает снимок накопленного содержимого.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

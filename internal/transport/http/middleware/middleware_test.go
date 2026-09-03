package middleware_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler отвечает кодом 200 и коротким телом.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ответ")
	})
}

// captureLogger возвращает логгер, пишущий в буфер, и сам буфер.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}

	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// gzipBytes сжимает данные для передачи в теле запроса.
func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("сжатие: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("закрытие компрессора: %v", err)
	}

	return buf.Bytes()
}

// serve пропускает запрос через обработчик и возвращает записанный ответ.
func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

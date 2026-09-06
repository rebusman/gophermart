package middleware_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gophermart/internal/transport/http/middleware"
)

func TestGzipCompressesResponseWhenAccepted(t *testing.T) {
	handler := middleware.Gzip(okHandler())

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")

	response := serve(handler, request)

	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("ответ не помечен как сжатый: got %q", got)
	}

	if got := response.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("отсутствует заголовок Vary: got %q", got)
	}

	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("тело не является корректным gzip-потоком: %v", err)
	}

	defer func() {
		_ = reader.Close()
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("распаковка тела: %v", err)
	}

	if string(decompressed) != "ответ" {
		t.Errorf("тело искажено: got %q", decompressed)
	}
}

func TestGzipLeavesResponseUncompressedWhenNotAccepted(t *testing.T) {
	handler := middleware.Gzip(okHandler())

	response := serve(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("ответ неожиданно сжат: got %q", got)
	}

	if got := response.Body.String(); got != "ответ" {
		t.Errorf("тело искажено: got %q", got)
	}
}

func TestGzipDecompressesRequest(t *testing.T) {
	const payload = "12345678903"

	var received string

	handler := middleware.Gzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}

		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))

	compressed := gzipBytes(t, []byte(payload))

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(compressed))
	request.Header.Set("Content-Encoding", "gzip")

	response := serve(handler, request)

	if response.Code != http.StatusOK {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusOK)
	}

	if received != payload {
		t.Errorf("обработчик получил искажённое тело: got %q, want %q", received, payload)
	}
}

func TestGzipRejectsCorruptedRequestBody(t *testing.T) {
	handler := middleware.Gzip(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("обработчик не должен вызываться при повреждённом теле")
	}))

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("это не gzip"))
	request.Header.Set("Content-Encoding", "gzip")

	response := serve(handler, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("неожиданный код ответа: got %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestGzipHandlesCompressedRequestAndResponseTogether(t *testing.T) {
	handler := middleware.Gzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}

		_, _ = w.Write(body)
	}))

	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(gzipBytes(t, []byte("эхо"))))
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Accept-Encoding", "gzip")

	response := serve(handler, request)

	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("ответ не является корректным gzip-потоком: %v", err)
	}

	defer func() {
		_ = reader.Close()
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("распаковка ответа: %v", err)
	}

	if string(decompressed) != "эхо" {
		t.Errorf("тело искажено: got %q", decompressed)
	}
}

func TestGzipDoesNotCompressResponsesWithoutBody(t *testing.T) {
	handler := middleware.Gzip(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")

	response := serve(handler, request)

	if got := response.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("ответ без тела помечен как сжатый: got %q", got)
	}
}

package middleware

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// Значения и имена заголовков, относящиеся к сжатию.
const (
	encodingGzip          = "gzip"
	headerAcceptEncoding  = "Accept-Encoding"
	headerContentEncoding = "Content-Encoding"
	headerContentLength   = "Content-Length"
	headerVary            = "Vary"
)

// gzipResponseWriter сжимает тело ответа и проставляет сопутствующие
type gzipResponseWriter struct {
	http.ResponseWriter

	writer      *gzip.Writer
	compressing bool
	wroteHeader bool
}

// Gzip возвращает обработчик, распаковывающий тело запроса и сжимающий тело
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acceptsEncoding(r.Header.Get(headerContentEncoding), encodingGzip) {
			decompressed, err := gzip.NewReader(r.Body)
			if err != nil {
				writeStatus(w, http.StatusBadRequest)

				return
			}

			defer func() {
				_ = decompressed.Close()
			}()

			r.Body = decompressed
			r.Header.Del(headerContentEncoding)
			r.Header.Del(headerContentLength)
			r.ContentLength = -1
		}

		w.Header().Add(headerVary, headerAcceptEncoding)

		if !acceptsEncoding(r.Header.Get(headerAcceptEncoding), encodingGzip) {
			next.ServeHTTP(w, r)

			return
		}

		compressor := gzip.NewWriter(w)
		defer func() {
			_ = compressor.Close()
		}()

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, writer: compressor}, r)
	})
}

// WriteHeader принимает решение о сжатии и проставляет заголовки ответа.
func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}

	w.wroteHeader = true
	w.compressing = bodyAllowedForStatus(status)

	if w.compressing {
		w.Header().Del(headerContentLength)
		w.Header().Set(headerContentEncoding, encodingGzip)
	}

	w.ResponseWriter.WriteHeader(status)
}

// Write направляет тело ответа в компрессор либо, если сжатие для данного кода
func (w *gzipResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	if !w.compressing {
		return w.ResponseWriter.Write(data)
	}

	return w.writer.Write(data)
}

// Flush отправляет клиенту накопленные компрессором данные.
func (w *gzipResponseWriter) Flush() {
	if w.compressing {
		_ = w.writer.Flush()
	}

	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// acceptsEncoding сообщает, перечислено ли кодирование в значении заголовка.
func acceptsEncoding(header, encoding string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, _, _ := strings.Cut(part, ";")
		if strings.EqualFold(strings.TrimSpace(name), encoding) {
			return true
		}
	}

	return false
}

// bodyAllowedForStatus сообщает, допускает ли код состояния тело ответа.
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= http.StatusContinue && status < http.StatusOK:
		return false
	case status == http.StatusNoContent:
		return false
	case status == http.StatusNotModified:
		return false
	default:
		return true
	}
}

// Проверка на этапе компиляции: gzipResponseWriter остаётся совместим с
var _ http.Flusher = (*gzipResponseWriter)(nil)

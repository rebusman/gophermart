package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"gophermart/internal/logging"
)

// statusRecorder перехватывает код ответа и объём записанного тела, оставляя
type statusRecorder struct {
	http.ResponseWriter

	status  int
	written int64
}

// Logging возвращает обработчик, записывающий по одной структурной записи на
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := RequestIDFromContext(r.Context())
			requestLogger := logger.With(slog.String(logging.AttrRequestID, requestID))

			ctx := logging.ContextWithLogger(r.Context(), requestLogger)
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()

			next.ServeHTTP(recorder, r.WithContext(ctx))

			requestLogger.InfoContext(ctx, "запрос обработан",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.Duration("duration", time.Since(started)),
				slog.Int64("bytes", recorder.written),
			)
		})
	}
}

// WriteHeader запоминает код ответа и передаёт его нижележащему writer.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write накапливает объём записанного тела и передаёт данные нижележащему
func (r *statusRecorder) Write(data []byte) (int, error) {
	written, err := r.ResponseWriter.Write(data)
	r.written += int64(written)

	return written, err
}

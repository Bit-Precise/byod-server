package byodserver

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type requestLogContextKey struct{}

// requestLogWriter records the status and response size without buffering the
// response body. Unwrap keeps ResponseController-compatible features such as
// flushing available to handlers and reverse proxies.
type requestLogWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *requestLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *requestLogWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestLogWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *requestLogWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *requestLogWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if source, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := source.ReadFrom(reader)
		w.bytes += int(n)
		return n, err
	}
	n, err := io.Copy(w.ResponseWriter, reader)
	w.bytes += int(n)
	return n, err
}

func requestIDFromContext(r *http.Request) string {
	if value, ok := r.Context().Value(requestLogContextKey{}).(string); ok {
		return value
	}
	return ""
}

func requestID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n") {
		return value
	}
	return randomToken(12)
}

func requestExamFields(path string) (examID, sessionID string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" {
		switch parts[1] {
		case "exams":
			examID = parts[2]
		case "sessions":
			sessionID = parts[2]
		}
	}
	if strings.HasSuffix(path, "/.well-known/byod-configuration") {
		prefix := strings.TrimSuffix(strings.Trim(path, "/"), "/.well-known/byod-configuration")
		prefix = strings.TrimPrefix(prefix, "/exam/")
		if prefix != "" && !strings.Contains(prefix, "/") {
			examID = prefix
		}
	}
	return examID, sessionID
}

func logHTTPRequest(r *http.Request, writer *requestLogWriter, started time.Time) {
	examID, sessionID := requestExamFields(r.URL.Path)
	attrs := []any{
		"request_id", requestIDFromContext(r),
		"method", r.Method,
		"path", r.URL.Path,
		"status", writer.status,
		"bytes", writer.bytes,
		"duration_ms", time.Since(started).Seconds() * 1000,
		"origin", r.Header.Get("Origin"),
	}
	if examID != "" {
		attrs = append(attrs, "exam_id", examID)
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID)
	}
	if writer.status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "http_request", attrs...)
	} else if writer.status >= http.StatusBadRequest {
		slog.WarnContext(r.Context(), "http_request", attrs...)
	} else {
		slog.InfoContext(r.Context(), "http_request", attrs...)
	}
}

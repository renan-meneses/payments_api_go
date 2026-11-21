package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/httpx"
)

const defaultMaxCapture = 512 * 1024

// Idempotency implements the HTTP idempotency contract:
//
//   - Idempotency-Key is required and scoped by a hash of method+path+body;
//   - concurrent duplicates get 409 + Retry-After;
//   - reusing a key with a different payload gets 422;
//   - completed responses (including 4xx) are replayed byte-for-byte;
//   - 5xx responses release the key so the client can safely retry.
//
// The guard itself is infrastructure (Redis + Postgres audit).
func Idempotency(guard usecase.IdempotencyGuard, logger *slog.Logger, maxCapture int) func(http.Handler) http.Handler {
	if maxCapture <= 0 {
		maxCapture = defaultMaxCapture
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			if key == "" {
				httpx.WriteProblem(w, http.StatusBadRequest, "idempotency_key_required",
					"Missing Idempotency-Key", "this endpoint requires an Idempotency-Key header")
				return
			}
			if !validIdempotencyKey(key) {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_idempotency_key",
					"Invalid Idempotency-Key", "use 8-128 characters from [A-Za-z0-9._:-]")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", "Invalid request body", "could not read the request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r = r.WithContext(WithIdempotencyKey(r.Context(), key))

			requestHash := hashRequest(r.Method, r.URL.Path, body)

			record, err := guard.Begin(r.Context(), key, requestHash)
			if err != nil {
				logger.ErrorContext(r.Context(), "idempotency guard unavailable",
					slog.String("request_id", RequestIDFromContext(r.Context())),
					slog.String("error", err.Error()),
				)
				// Fail closed: accepting a payment without idempotency protection
				// risks double charging.
				httpx.WriteProblem(w, http.StatusServiceUnavailable, "idempotency_unavailable",
					"Service Unavailable", "idempotency backend is unavailable, retry shortly")
				return
			}

			switch record.Outcome {
			case usecase.IdempotencyReplay:
				if record.RequestHash != requestHash {
					httpx.WriteProblem(w, http.StatusUnprocessableEntity, "idempotency_key_reuse",
						"Idempotency Key Reused", "this key was already used with a different payload")
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(record.StatusCode)
				_, _ = w.Write(record.Body)
				return

			case usecase.IdempotencyInFlight:
				w.Header().Set("Retry-After", "1")
				httpx.WriteProblem(w, http.StatusConflict, "idempotency_in_progress",
					"Request In Progress", "another request with this idempotency key is still being processed")
				return
			}

			// We own the key: execute the handler while capturing the response.
			capture := &captureWriter{header: make(http.Header), max: maxCapture}
			next.ServeHTTP(capture, r)

			status := capture.status
			if status == 0 {
				status = http.StatusOK
			}

			for name, values := range capture.header {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.WriteHeader(status)
			if !capture.overflow {
				_, _ = w.Write(capture.body.Bytes())
			}

			bg, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
			defer cancel()

			switch {
			case capture.overflow:
				logger.WarnContext(r.Context(), "idempotent response too large to cache; releasing key",
					slog.String("idempotency_key", key))
				_ = guard.Abort(bg, key)
			case status >= http.StatusInternalServerError:
				// Do not cache server errors: the retry must re-execute.
				_ = guard.Abort(bg, key)
			default:
				completion := usecase.IdempotencyCompletion{
					Key:         key,
					RequestHash: requestHash,
					Method:      r.Method,
					Path:        r.URL.Path,
					StatusCode:  status,
					Body:        capture.body.Bytes(),
				}
				if err := guard.Complete(bg, completion); err != nil {
					logger.WarnContext(bg, "failed to store idempotent response",
						slog.String("idempotency_key", key),
						slog.String("error", err.Error()),
					)
					_ = guard.Abort(bg, key)
				}
			}
		})
	}
}

// captureWriter buffers the handler response so it can be persisted and
// replayed. Overflow means the response is streamed to the client but not
// cached (and the key is released).
type captureWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	max      int
	overflow bool
}

func (w *captureWriter) Header() http.Header { return w.header }

func (w *captureWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if !w.overflow {
		if w.body.Len()+len(b) > w.max {
			w.overflow = true
			w.body.Reset()
		} else {
			w.body.Write(b)
		}
	}
	return len(b), nil
}

func hashRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("\n"))
	h.Write([]byte(path))
	h.Write([]byte("\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func validIdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ':':
		default:
			return false
		}
	}
	return true
}

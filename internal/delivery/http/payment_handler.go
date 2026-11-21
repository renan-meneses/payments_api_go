package httpdelivery

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/delivery/http/middleware"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/httpx"
)

type PaymentHandler struct {
	process *usecase.ProcessPayment
	get     *usecase.GetPayment
	logger  *slog.Logger
}

func NewPaymentHandler(process *usecase.ProcessPayment, get *usecase.GetPayment, logger *slog.Logger) *PaymentHandler {
	return &PaymentHandler{process: process, get: get, logger: logger}
}

// Create handles POST /v1/payments.
//
// The idempotency key is enforced by the middleware; it is also forwarded to
// the use case, which double-checks it against the payments table.
func (h *PaymentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createPaymentRequest
	if err := httpx.DecodeJSON(w, r, &request, 1<<20); err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_body", "Invalid Request Body", err.Error())
		return
	}

	key := middleware.IdempotencyKeyFromContext(r.Context())
	if key == "" {
		key = r.Header.Get("Idempotency-Key")
	}

	input, err := request.toInput(key)
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "validation_error", "Validation Failed", err.Error())
		return
	}

	view, err := h.process.Execute(r.Context(), input)
	if err != nil {
		writeError(w, r, h.logger, err, view)
		return
	}

	w.Header().Set("Location", "/v1/payments/"+view.Payment.ID.String())
	httpx.WriteJSON(w, http.StatusCreated, toPaymentResponse(view))
}

// Get handles GET /v1/payments/{paymentID}.
func (h *PaymentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "paymentID"))
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "validation_error", "Validation Failed", "paymentID must be a valid UUID")
		return
	}

	view, err := h.get.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err, nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPaymentResponse(view))
}

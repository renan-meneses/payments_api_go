package httpdelivery

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/httpx"
)

type DashboardHandler struct {
	summary *usecase.GetDashboardSummary
	logger  *slog.Logger
}

func NewDashboardHandler(summary *usecase.GetDashboardSummary, logger *slog.Logger) *DashboardHandler {
	return &DashboardHandler{summary: summary, logger: logger}
}

// Summary handles GET /v1/dashboard/summary?from=&to=&location_id=.
func (h *DashboardHandler) Summary(w http.ResponseWriter, r *http.Request) {
	input, err := parseDashboardInput(r)
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, "validation_error", "Validation Failed", err.Error())
		return
	}

	result, err := h.summary.Execute(r.Context(), input)
	if err != nil {
		writeError(w, r, h.logger, err, nil)
		return
	}

	if result.Cached {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	httpx.WriteJSON(w, http.StatusOK, result.Summary)
}

// brazilZone is fixed at UTC-3: Brazil abolished daylight saving in 2019, and a
// fixed zone avoids depending on tzdata being present in the container image.
var brazilZone = time.FixedZone("BRT", -3*60*60)

func parseDashboardInput(r *http.Request) (usecase.DashboardInput, error) {
	var input usecase.DashboardInput
	query := r.URL.Query()

	if raw := query.Get("from"); raw != "" {
		from, err := parseQueryTime(raw, false)
		if err != nil {
			return input, err
		}
		input.From = &from
	}

	if raw := query.Get("to"); raw != "" {
		to, err := parseQueryTime(raw, true)
		if err != nil {
			return input, err
		}
		input.To = &to
	}

	if raw := query.Get("location_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return input, errValidation("location_id must be a valid UUID")
		}
		input.LocationID = &id
	}

	return input, nil
}

// parseQueryTime accepts RFC3339 or a date (YYYY-MM-DD). A date-only `to` is
// interpreted as the *end* of that day so the filter is inclusive.
func parseQueryTime(raw string, endOfDay bool) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}

	day, err := time.ParseInLocation("2006-01-02", raw, brazilZone)
	if err != nil {
		return time.Time{}, errValidation("dates must be RFC3339 or YYYY-MM-DD")
	}
	if endOfDay {
		day = day.Add(24 * time.Hour)
	}
	return day, nil
}

type validationError string

func (e validationError) Error() string { return string(e) }

func errValidation(message string) error { return validationError(message) }

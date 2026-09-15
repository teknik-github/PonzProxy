// Package api is the control plane: the REST endpoints, the authentication in
// front of them, and the WebSocket feed the dashboard subscribes to.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// errorBody is the single error shape every endpoint returns, so the UI has
// one thing to parse rather than one per endpoint.
type errorBody struct {
	Error string `json:"error"`
	// Fields is present only for validation failures, and lets the UI mark
	// the offending inputs instead of showing a wall of text.
	Fields []domain.FieldError `json:"fields,omitempty"`
}

// writeJSON sends a value as JSON. A body that fails to encode has usually
// already had its status line written, so the error is logged rather than
// turned into a second response.
func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if body == nil || status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logger.Error("encode response body", "error", err)
	}
}

// writeError maps a domain error onto the right status code.
//
// Centralising this is what keeps the handlers readable: they return domain
// errors and never think about HTTP status codes.
func writeError(w http.ResponseWriter, logger *slog.Logger, err error) {
	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		writeJSON(w, logger, http.StatusUnprocessableEntity, errorBody{
			Error:  "the submitted values are not valid",
			Fields: validation.Fields,
		})

	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, logger, http.StatusNotFound, errorBody{Error: "not found"})

	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, logger, http.StatusConflict, errorBody{Error: err.Error()})

	case errors.Is(err, domain.ErrInUse):
		writeJSON(w, logger, http.StatusConflict, errorBody{Error: err.Error()})

	case errors.Is(err, errUnauthorized):
		writeJSON(w, logger, http.StatusUnauthorized, errorBody{Error: "authentication required"})

	case errors.Is(err, errForbidden):
		writeJSON(w, logger, http.StatusForbidden, errorBody{
			Error: "this account may not change configuration",
		})

	case errors.Is(err, errBadRequest):
		writeJSON(w, logger, http.StatusBadRequest, errorBody{Error: err.Error()})

	default:
		// Anything unclassified is a bug or an outage. The client is told
		// nothing beyond that, while the detail goes to the log.
		logger.Error("unhandled request failure", "error", err)
		writeJSON(w, logger, http.StatusInternalServerError, errorBody{
			Error: "the request could not be completed",
		})
	}
}

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
	errBadRequest   = errors.New("bad request")
)

// decodeJSON reads a request body into v, rejecting anything oversized or
// malformed before it reaches a handler.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	// 1 MiB is far more than any configuration payload, and small enough
	// that an unauthenticated request cannot be used to exhaust memory. An
	// uploaded certificate chain is a few kilobytes.
	const maxBody = 1 << 20

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	// Unknown fields are refused so a typo in a field name fails loudly
	// instead of silently doing nothing.
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return errors.Join(errBadRequest, err)
	}
	return nil
}

// contextWithTimeout bounds work that outlives a handler's own patience but
// must still stop when the client goes away.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

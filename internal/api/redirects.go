package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// RedirectOptions are the collaborators the redirect endpoints need.
type RedirectOptions struct {
	Redirects domain.RedirectRepository
	// Certs is consulted only to reject a certificate id that does not
	// exist, so the operator gets a field error instead of a foreign key
	// failure from the driver.
	Certs domain.CertificateRepository

	// ApplyConfig republishes routing to the data plane after a change,
	// exactly as it does for hosts. May be nil.
	ApplyConfig func(context.Context) error

	Logger *slog.Logger
}

// RedirectHandlers serves /api/redirects.
//
// It takes its own collaborators rather than reading Server.opts because the
// redirect feature was added alongside the server rather than inside it;
// Register mounts it on the same read and write muxes, so it inherits the
// session check and the admin-only rule by construction, just like every
// handler declared on Server.
type RedirectHandlers struct {
	opts   RedirectOptions
	logger *slog.Logger
}

// NewRedirectHandlers builds the handler group.
func NewRedirectHandlers(opts RedirectOptions) *RedirectHandlers {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &RedirectHandlers{opts: opts, logger: logger.With("component", "api")}
}

// Register adds the redirect routes. read must already be behind
// authentication and write behind the admin role check; passing the muxes in
// rather than building a third one is what keeps that true.
func (h *RedirectHandlers) Register(read, write *http.ServeMux) {
	read.HandleFunc("GET /api/redirects", h.handleList)
	read.HandleFunc("GET /api/redirects/{id}", h.handleGet)
	read.HandleFunc("GET /api/redirect-statuses", h.handleListStatuses)

	write.HandleFunc("POST /api/redirects", h.handleCreate)
	write.HandleFunc("PUT /api/redirects/{id}", h.handleUpdate)
	write.HandleFunc("DELETE /api/redirects/{id}", h.handleDelete)
}

// redirectPayload is the write shape of a redirect. As with hostPayload it is
// separate from the domain type so ids and timestamps cannot be set by a
// client.
type redirectPayload struct {
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Domains       []string `json:"domains"`
	Target        string   `json:"target"`
	StatusCode    int      `json:"statusCode"`
	PreservePath  bool     `json:"preservePath"`
	CertificateID *int64   `json:"certificateId"`
}

func (p redirectPayload) toDomain() domain.Redirect {
	return domain.Redirect{
		Name:          p.Name,
		Enabled:       p.Enabled,
		Domains:       p.Domains,
		Target:        p.Target,
		StatusCode:    p.StatusCode,
		PreservePath:  p.PreservePath,
		CertificateID: p.CertificateID,
	}
}

func (h *RedirectHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	redirects, err := h.opts.Redirects.List(r.Context())
	if err != nil {
		writeError(w, h.logger, err)
		return
	}
	writeJSON(w, h.logger, http.StatusOK, redirects)
}

func (h *RedirectHandlers) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.logger, err)
		return
	}
	redirect, err := h.opts.Redirects.Get(r.Context(), id)
	if err != nil {
		writeError(w, h.logger, err)
		return
	}
	writeJSON(w, h.logger, http.StatusOK, redirect)
}

func (h *RedirectHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var payload redirectPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, h.logger, err)
		return
	}

	redirect := payload.toDomain()
	if err := h.prepare(r, &redirect); err != nil {
		writeError(w, h.logger, err)
		return
	}
	if err := h.opts.Redirects.Create(r.Context(), &redirect); err != nil {
		writeError(w, h.logger, err)
		return
	}

	h.logger.Info("redirect created",
		"name", redirect.Name, "domains", redirect.Domains, "target", redirect.Target)
	h.applyConfig(r)
	writeJSON(w, h.logger, http.StatusCreated, redirect)
}

func (h *RedirectHandlers) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.logger, err)
		return
	}

	var payload redirectPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, h.logger, err)
		return
	}

	redirect := payload.toDomain()
	redirect.ID = id
	if err := h.prepare(r, &redirect); err != nil {
		writeError(w, h.logger, err)
		return
	}
	if err := h.opts.Redirects.Update(r.Context(), &redirect); err != nil {
		writeError(w, h.logger, err)
		return
	}

	h.logger.Info("redirect updated", "name", redirect.Name, "id", redirect.ID)
	h.applyConfig(r)
	writeJSON(w, h.logger, http.StatusOK, redirect)
}

func (h *RedirectHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.logger, err)
		return
	}
	if err := h.opts.Redirects.Delete(r.Context(), id); err != nil {
		writeError(w, h.logger, err)
		return
	}

	h.logger.Info("redirect deleted", "id", id)
	h.applyConfig(r)
	writeJSON(w, h.logger, http.StatusNoContent, nil)
}

// handleListStatuses lets the UI build the status picker from the server, so
// the allowed set is stated once. The descriptions are the part an operator
// actually has to choose between.
func (h *RedirectHandlers) handleListStatuses(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Value       int    `json:"value"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	writeJSON(w, h.logger, http.StatusOK, []entry{
		{301, "301 Moved permanently",
			"Cached by browsers and search engines. A POST may be replayed as a GET."},
		{302, "302 Found",
			"Not cached, so the redirect can be withdrawn later. A POST may be replayed as a GET."},
		{307, "307 Temporary redirect",
			"Like 302, but the method and body are kept — use it for APIs and form targets."},
		{308, "308 Permanent redirect",
			"Like 301, but the method and body are kept — use it for APIs and form targets."},
	})
}

// prepare normalises and validates a redirect and checks the cross-record
// rules the domain type cannot see, mirroring Server.prepareHost.
func (h *RedirectHandlers) prepare(r *http.Request, rd *domain.Redirect) error {
	rd.Normalize()
	if err := rd.Validate(); err != nil {
		return err
	}

	if rd.CertificateID != nil {
		if _, err := h.opts.Certs.Get(r.Context(), *rd.CertificateID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				v := &domain.ValidationError{}
				v.Add("certificateId", "no certificate has id %d", *rd.CertificateID)
				return v
			}
			return err
		}
	}
	return nil
}

func (h *RedirectHandlers) applyConfig(r *http.Request) {
	if h.opts.ApplyConfig == nil {
		return
	}
	if err := h.opts.ApplyConfig(r.Context()); err != nil {
		h.logger.Error("apply configuration to the proxy", "error", err)
	}
}

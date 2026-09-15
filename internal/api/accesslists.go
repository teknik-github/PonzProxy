package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// AccessListHandlers serves the access-list endpoints.
//
// It holds its own collaborators instead of reading them off Server, so the
// endpoints can be constructed and exercised on their own; Server.routes
// registers the methods alongside the rest of the control plane.
type AccessListHandlers struct {
	opts AccessListOptions
}

// AccessListOptions are the collaborators the access-list endpoints need.
type AccessListOptions struct {
	Lists domain.AccessListRepository

	// ApplyConfig republishes configuration to the data plane. Editing a
	// list changes who may reach a host, so it must reach the running proxy
	// the same way a host edit does.
	ApplyConfig func(context.Context) error

	// PasswordCost is the bcrypt cost for stored credentials. Zero selects
	// DefaultPasswordCost; tests lower it.
	PasswordCost int

	Logger *slog.Logger
}

// NewAccessListHandlers wires the access-list endpoints.
func NewAccessListHandlers(opts AccessListOptions) *AccessListHandlers {
	if opts.PasswordCost <= 0 {
		opts.PasswordCost = DefaultPasswordCost
	}
	opts.Logger = opts.Logger.With("component", "api")
	return &AccessListHandlers{opts: opts}
}

// accessListView adds what the UI would otherwise have to compute from data
// it does not have.
type accessListView struct {
	domain.AccessList
	// InUseByHosts reports how many hosts the list is guarding, which is
	// also what makes deletion fail.
	InUseByHosts int `json:"inUseByHosts"`
}

// accessListPayload is the write shape. It exists separately from
// domain.AccessList so ids, timestamps and — above all — password hashes
// cannot be set by a client.
type accessListPayload struct {
	Name       string              `json:"name"`
	SatisfyAny bool                `json:"satisfyAny"`
	Rules      []accessRulePayload `json:"rules"`
	BasicAuth  []basicAuthPayload  `json:"basicAuth"`
}

type accessRulePayload struct {
	Action domain.AccessAction `json:"action"`
	CIDR   string              `json:"cidr"`
}

// basicAuthPayload carries a plaintext password in, and never out. An empty
// Password on an update means "leave this user's password alone": the UI is
// never sent the hash, so an edit that only renames the list must not clear
// every credential in it.
type basicAuthPayload struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
}

// HandleList returns every access list.
func (h *AccessListHandlers) HandleList(w http.ResponseWriter, r *http.Request) {
	lists, err := h.opts.Lists.List(r.Context())
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}
	views := make([]accessListView, 0, len(lists))
	for _, l := range lists {
		views = append(views, h.viewOf(r, l))
	}
	writeJSON(w, h.opts.Logger, http.StatusOK, views)
}

// HandleGet returns one access list.
func (h *AccessListHandlers) HandleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}
	list, err := h.opts.Lists.Get(r.Context(), id)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}
	writeJSON(w, h.opts.Logger, http.StatusOK, h.viewOf(r, *list))
}

// HandleCreate stores a new access list.
func (h *AccessListHandlers) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var payload accessListPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	list, err := h.prepare(payload, nil)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}
	if err := h.opts.Lists.Create(r.Context(), list); err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	h.opts.Logger.Info("access list created", "name", list.Name,
		"rules", len(list.Rules), "users", len(list.BasicAuth))
	h.applyConfig(r)
	writeJSON(w, h.opts.Logger, http.StatusCreated, h.viewOf(r, *list))
}

// HandleUpdate replaces an access list, its rules and its users.
func (h *AccessListHandlers) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	var payload accessListPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	// The stored list is read first so a user whose password was left blank
	// keeps the hash it already had.
	existing, err := h.opts.Lists.Get(r.Context(), id)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	list, err := h.prepare(payload, existing)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}
	list.ID = id
	if err := h.opts.Lists.Update(r.Context(), list); err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	h.opts.Logger.Info("access list updated", "name", list.Name, "id", list.ID)
	h.applyConfig(r)
	writeJSON(w, h.opts.Logger, http.StatusOK, h.viewOf(r, *list))
}

// HandleDelete removes an access list that no host is using.
func (h *AccessListHandlers) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	// The foreign key would refuse this anyway, but a count makes the
	// refusal say how many hosts would have been opened up.
	if n, err := h.opts.Lists.HostsUsing(r.Context(), id); err == nil && n > 0 {
		writeError(w, h.opts.Logger, fmt.Errorf(
			"%w: %d host(s) are still protected by this access list", domain.ErrInUse, n))
		return
	}
	if err := h.opts.Lists.Delete(r.Context(), id); err != nil {
		writeError(w, h.opts.Logger, err)
		return
	}

	h.opts.Logger.Info("access list deleted", "id", id)
	h.applyConfig(r)
	writeJSON(w, h.opts.Logger, http.StatusNoContent, nil)
}

// prepare turns a payload into a normalised, validated list. existing is the
// stored version on an update, and nil on a create.
func (h *AccessListHandlers) prepare(p accessListPayload, existing *domain.AccessList) (*domain.AccessList, error) {
	list := &domain.AccessList{Name: p.Name, SatisfyAny: p.SatisfyAny}
	for _, rule := range p.Rules {
		list.Rules = append(list.Rules, domain.AccessRule{Action: rule.Action, CIDR: rule.CIDR})
	}

	kept := storedHashes(existing)
	for _, u := range p.BasicAuth {
		user := domain.BasicAuthUser{Username: u.Username}
		switch {
		case u.Password != "":
			hash, err := hashPasswordAt(u.Password, h.opts.PasswordCost)
			if err != nil {
				return nil, err
			}
			user.PasswordHash = hash
		default:
			// Empty on a user that already exists means "unchanged"; on a
			// new one it leaves the hash empty, which Validate reports as
			// a missing password.
			user.PasswordHash = kept[u.Username]
		}
		list.BasicAuth = append(list.BasicAuth, user)
	}

	list.Normalize()
	if err := list.Validate(); err != nil {
		return nil, err
	}
	return list, nil
}

// storedHashes indexes the hashes already on file by username.
func storedHashes(existing *domain.AccessList) map[string]string {
	if existing == nil {
		return nil
	}
	out := make(map[string]string, len(existing.BasicAuth))
	for _, u := range existing.BasicAuth {
		out[u.Username] = u.PasswordHash
	}
	return out
}

func (h *AccessListHandlers) viewOf(r *http.Request, l domain.AccessList) accessListView {
	view := accessListView{AccessList: l}
	if n, err := h.opts.Lists.HostsUsing(r.Context(), l.ID); err == nil {
		view.InUseByHosts = n
	}
	return view
}

// applyConfig republishes routing after a change, mirroring Server.applyConfig:
// a failure means the database and the running proxy have diverged, which is
// worth an error log but not worth failing a write that already committed.
func (h *AccessListHandlers) applyConfig(r *http.Request) {
	if h.opts.ApplyConfig == nil {
		return
	}
	if err := h.opts.ApplyConfig(r.Context()); err != nil {
		h.opts.Logger.Error("apply configuration to the proxy", "error", err)
	}
}

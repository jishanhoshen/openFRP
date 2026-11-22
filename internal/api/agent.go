package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tunnelhub/internal/auth"
	"tunnelhub/internal/store"
)

// ================================================================ agent config sync

// AgentTunnel is one tunnel the agent should expose.
type AgentTunnel struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	LocalIP       string `json:"local_ip"`
	LocalPort     int    `json:"local_port"`
	RemotePort    int    `json:"remote_port"`
	ProxyProtocol string `json:"proxy_protocol,omitempty"`
}

// AgentConfig is the configuration the control plane hands to an agent.
type AgentConfig struct {
	Revision string        `json:"revision"`
	Tunnels  []AgentTunnel `json:"tunnels"`
}

// agentLongPollMax bounds how long a config request may wait for a change.
// It stays below the server's WriteTimeout.
const agentLongPollMax = 25 * time.Second

// buildAgentConfig computes the device's tunnel list and a revision hash over it.
func (s *Server) buildAgentConfig(r *http.Request, deviceID string) (AgentConfig, error) {
	as, err := s.store.ListAllocations(r.Context(), deviceID)
	if err != nil {
		return AgentConfig{}, err
	}
	cfg := AgentConfig{Tunnels: []AgentTunnel{}}
	for _, a := range as {
		if !a.Enabled {
			continue
		}
		cfg.Tunnels = append(cfg.Tunnels, AgentTunnel{Name: a.Name, Type: a.Type, LocalIP: a.LocalIP,
			LocalPort: a.LocalPort, RemotePort: a.RemotePort, ProxyProtocol: a.ProxyProtocol})
	}
	b, _ := json.Marshal(cfg.Tunnels)
	sum := sha256.Sum256(b)
	cfg.Revision = hex.EncodeToString(sum[:8])
	return cfg, nil
}

// agentConfig is the device-token authenticated endpoint agents poll.
// GET /api/v1/agent/config?rev=<revision>&wait=<seconds>
//
// It long-polls: if the revision matches, it waits up to `wait` seconds for a
// change and answers 304 when nothing changed. Every request re-checks the
// device, so a revoked or disabled device is refused within one poll.
func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(tok, "thd_") {
		writeErr(w, http.StatusUnauthorized, "device token required")
		return
	}
	hash := auth.HashToken(tok)
	deviceID, _, err := s.store.AgentDevice(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "device token is invalid or revoked")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	// Subscribe before reading, so a change between read and wait is not missed.
	changed := s.notify.wait(deviceID)

	wait := time.Duration(0)
	if n, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && n > 0 {
		wait = min(time.Duration(n)*time.Second, agentLongPollMax)
	}
	cfg, err := s.buildAgentConfig(r, deviceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if rev := r.URL.Query().Get("rev"); rev == cfg.Revision && wait > 0 {
		select {
		case <-changed:
		case <-time.After(wait):
		case <-r.Context().Done():
			return
		}
		// Re-check the device on wake-up (it may have been disabled or revoked).
		if _, disabled, err := s.store.AgentDevice(r.Context(), hash); errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "device token is invalid or revoked")
			return
		} else if err != nil {
			s.internal(w, r, err)
			return
		} else if disabled {
			writeErr(w, http.StatusForbidden, "device is disabled")
			return
		}
		cfg, err = s.buildAgentConfig(r, deviceID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if cfg.Revision == rev {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if _, disabled, err := s.store.AgentDevice(r.Context(), hash); err == nil && disabled {
		writeErr(w, http.StatusForbidden, "device is disabled")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// ================================================================ public info / health

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	_, _ = w.Write([]byte("ok"))
}

// info is public and reveals no secrets: what the dashboard needs to show
// connection instructions.
func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":           s.cfg.Version,
		"public_host":       s.cfg.PublicHost,
		"port_range":        []int{s.cfg.Pool.Start, s.cfg.Pool.End},
		"open_registration": s.cfg.OpenRegistration,
		"max_devices":       s.cfg.MaxDevicesPerUser,
		"max_tunnels":       s.cfg.MaxTunnelsPerDevice,
		"live_statistics":   s.cfg.Live != nil,
	})
}

// ================================================================ admin

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, _ store.User) {
	o, err := s.store.Overview(r.Context(), s.cfg.Pool)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request, admin store.User) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if !validEmail(body.Email) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if msg := validPassword(body.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if body.Role == "" {
		body.Role = "user"
	}
	if body.Role != "user" && body.Role != "admin" {
		writeErr(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), body.Email, hash, body.Role)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &admin, "", "admin.user_create", u.Email, map[string]any{"role": u.Role})
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) adminListReserved(w http.ResponseWriter, r *http.Request, _ store.User) {
	rs, err := s.store.ListReservedPorts(r.Context(), nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

// reservedKey parses /{type}/{port} path values.
func reservedKey(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	typ := r.PathValue("type")
	port, err := strconv.Atoi(r.PathValue("port"))
	if (typ != "tcp" && typ != "udp") || err != nil || port < 1 || port > 65535 {
		writeErr(w, http.StatusBadRequest, "use /reserved-ports/{tcp|udp}/{port}")
		return "", 0, false
	}
	return typ, port, true
}

// adminReservePort reserves a port for a user (user_id) or for admins only (user_id null).
func (s *Server) adminReservePort(w http.ResponseWriter, r *http.Request, admin store.User) {
	typ, port, ok := reservedKey(w, r)
	if !ok {
		return
	}
	var body struct {
		UserID *int64 `json:"user_id"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Note) > 200 {
		writeErr(w, http.StatusBadRequest, "note must be at most 200 characters")
		return
	}
	err := s.store.ReservePort(r.Context(), store.ReservedPort{Type: typ, RemotePort: port, UserID: body.UserID, Note: body.Note})
	switch {
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "port is in use by another user's tunnel")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "user not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.audit(r, &admin, "", "admin.port_reserve", typ+"/"+strconv.Itoa(port), map[string]any{"user_id": body.UserID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminUnreservePort(w http.ResponseWriter, r *http.Request, admin store.User) {
	typ, port, ok := reservedKey(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteReservedPort(r.Context(), typ, port); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "reservation not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, &admin, "", "admin.port_unreserve", typ+"/"+strconv.Itoa(port), nil)
	w.WriteHeader(http.StatusNoContent)
}

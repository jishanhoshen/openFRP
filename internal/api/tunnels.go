package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"tunnelhub/internal/agentcfg"
	"tunnelhub/internal/stats"
	"tunnelhub/internal/store"
)

var (
	tunnelNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	bandwidthRe  = regexp.MustCompile(`^[1-9][0-9]{0,6}(KB|MB)$`)
)

// tunnelView is a tunnel as shown to users, with its live relay status when
// statistics are enabled.
type tunnelView struct {
	store.Allocation
	PublicAddress string      `json:"public_address"`
	Live          *stats.Live `json:"live,omitempty"`
}

func (s *Server) view(a store.Allocation) tunnelView {
	v := tunnelView{Allocation: a, PublicAddress: fmt.Sprintf("%s:%d", s.cfg.PublicHost, a.RemotePort)}
	if s.cfg.Live != nil {
		if l, ok := s.cfg.Live.Live(a.DeviceID, a.Name); ok {
			v.Live = &l
		}
	}
	return v
}

func (s *Server) views(as []store.Allocation) []tunnelView {
	out := make([]tunnelView, 0, len(as))
	for _, a := range as {
		out = append(out, s.view(a))
	}
	return out
}

// portError maps port-policy failures to HTTP responses; it returns false if
// err is not one of them.
func portError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrPortNotAllowed):
		writeErr(w, http.StatusBadRequest, "remote_port must be 0 (automatic) or within the public pool")
	case errors.Is(err, store.ErrPortReserved):
		writeErr(w, http.StatusForbidden, "that port is reserved for another user")
	case errors.Is(err, store.ErrPortCooldown):
		writeErr(w, http.StatusConflict, "that port was released recently; try again later or pick another")
	default:
		return false
	}
	return true
}

func (s *Server) listMyTunnels(w http.ResponseWriter, r *http.Request, u store.User) {
	as, err := s.store.ListUserAllocations(r.Context(), u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.views(as))
}

func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	as, err := s.store.ListAllocations(r.Context(), d.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.views(as))
}

func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	tid, ok := pathInt(r, "tid")
	if !ok {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	}
	a, err := s.store.GetAllocation(r.Context(), d.ID, tid)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(a))
}

// tunnelFields is the request body for create and update.
type tunnelFields struct {
	Name           *string `json:"name"`
	Type           *string `json:"type"`
	LocalIP        *string `json:"local_ip"`
	LocalPort      *int    `json:"local_port"`
	RemotePort     *int    `json:"remote_port"` // 0 = automatic
	Enabled        *bool   `json:"enabled"`
	ProxyProtocol  *string `json:"proxy_protocol"`  // "", "v1", "v2" (tcp only)
	BandwidthLimit *string `json:"bandwidth_limit"` // admins only
}

// validateTunnel checks the fields that are present and returns all problems.
func validateTunnel(f tunnelFields, admin bool) []string {
	var p []string
	if f.Name != nil && !tunnelNameRe.MatchString(*f.Name) {
		p = append(p, "name must match "+tunnelNameRe.String())
	}
	if f.Type != nil && *f.Type != "tcp" && *f.Type != "udp" {
		p = append(p, "type must be tcp or udp")
	}
	if f.LocalIP != nil {
		if err := agentcfg.CheckLocalTarget(*f.LocalIP, false); err != nil {
			p = append(p, err.Error())
		}
	}
	if f.LocalPort != nil && (*f.LocalPort < 1 || *f.LocalPort > 65535) {
		p = append(p, "local_port must be 1-65535")
	}
	if f.RemotePort != nil && (*f.RemotePort < 0 || *f.RemotePort > 65535) {
		p = append(p, "remote_port must be 0 (automatic) or 1-65535")
	}
	if f.ProxyProtocol != nil {
		switch *f.ProxyProtocol {
		case "", "v1", "v2":
		default:
			p = append(p, "proxy_protocol must be empty, v1 or v2")
		}
		if *f.ProxyProtocol != "" && f.Type != nil && *f.Type != "tcp" {
			p = append(p, "proxy_protocol is only supported for tcp tunnels")
		}
	}
	if f.BandwidthLimit != nil && *f.BandwidthLimit != "" {
		switch {
		case !admin:
			p = append(p, "only administrators can set bandwidth_limit")
		case !bandwidthRe.MatchString(*f.BandwidthLimit):
			p = append(p, "bandwidth_limit must look like 512KB or 10MB")
		}
	}
	return p
}

func (s *Server) createTunnel(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	var f tunnelFields
	if !decode(w, r, &f) {
		return
	}
	// Required on create.
	var problems []string
	if f.Name == nil || f.Type == nil || f.LocalPort == nil {
		problems = append(problems, "name, type and local_port are required")
	}
	problems = append(problems, validateTunnel(f, u.IsAdmin())...)
	if len(problems) > 0 {
		writeErr(w, http.StatusBadRequest, strings.Join(problems, "; "))
		return
	}
	a := store.Allocation{DeviceID: d.ID, Name: *f.Name, Type: *f.Type, LocalIP: "127.0.0.1",
		LocalPort: *f.LocalPort, Enabled: true}
	if f.LocalIP != nil {
		a.LocalIP = *f.LocalIP
	}
	if f.RemotePort != nil {
		a.RemotePort = *f.RemotePort
	}
	if f.Enabled != nil {
		a.Enabled = *f.Enabled
	}
	if f.ProxyProtocol != nil {
		a.ProxyProtocol = *f.ProxyProtocol
	}
	if f.BandwidthLimit != nil {
		a.BandwidthLimit = *f.BandwidthLimit
	}
	out, err := s.store.CreateAllocation(r.Context(), a, s.policy(), u.IsAdmin())
	switch {
	case portError(w, err):
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "tunnel name or public port already in use")
		return
	case errors.Is(err, store.ErrQuota):
		writeErr(w, http.StatusConflict, "tunnel limit reached for this device")
		return
	case errors.Is(err, store.ErrPoolExhausted):
		writeErr(w, http.StatusServiceUnavailable, "no free public ports left")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "device not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.notify.notify(d.ID)
	s.audit(r, &u, d.ID, "tunnel.create", out.Name, map[string]any{"type": out.Type, "remote_port": out.RemotePort})
	writeJSON(w, http.StatusCreated, s.view(out))
}

func (s *Server) updateTunnel(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	tid, ok := pathInt(r, "tid")
	if !ok {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	}
	var f tunnelFields
	if !decode(w, r, &f) {
		return
	}
	cur, err := s.store.GetAllocation(r.Context(), d.ID, tid)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	if f.Type != nil && *f.Type != cur.Type {
		writeErr(w, http.StatusBadRequest, "changing a tunnel's type is not supported; create a new tunnel")
		return
	}
	// Validate against the effective values (type is fixed after creation).
	f.Type = nil
	if p := validateTunnel(f, u.IsAdmin()); len(p) > 0 {
		writeErr(w, http.StatusBadRequest, strings.Join(p, "; "))
		return
	}
	if f.ProxyProtocol != nil && *f.ProxyProtocol != "" && cur.Type != "tcp" {
		writeErr(w, http.StatusBadRequest, "proxy_protocol is only supported for tcp tunnels")
		return
	}
	patch := store.AllocationPatch{Name: f.Name, LocalIP: f.LocalIP, LocalPort: f.LocalPort, Enabled: f.Enabled,
		ProxyProtocol: f.ProxyProtocol, BandwidthLimit: f.BandwidthLimit, RemotePort: f.RemotePort}
	if patch.Name == nil && patch.LocalIP == nil && patch.LocalPort == nil && patch.Enabled == nil &&
		patch.ProxyProtocol == nil && patch.BandwidthLimit == nil && patch.RemotePort == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	a, err := s.store.UpdateAllocation(r.Context(), d.ID, tid, patch, s.policy(), u.IsAdmin())
	switch {
	case portError(w, err):
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "tunnel name or public port already in use")
		return
	case errors.Is(err, store.ErrPoolExhausted):
		writeErr(w, http.StatusServiceUnavailable, "no free public ports left")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.notify.notify(d.ID)
	s.audit(r, &u, d.ID, "tunnel.update", a.Name, map[string]any{"remote_port": a.RemotePort, "enabled": a.Enabled})
	writeJSON(w, http.StatusOK, s.view(a))
}

func (s *Server) deleteTunnel(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	tid, ok := pathInt(r, "tid")
	if !ok {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	}
	if err := s.store.DeleteAllocation(r.Context(), d.ID, tid); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.notify.notify(d.ID)
	s.audit(r, &u, d.ID, "tunnel.delete", "", map[string]any{"tunnel_id": tid})
	w.WriteHeader(http.StatusNoContent)
}

// tunnelTraffic returns daily totals for one tunnel (default 30 days, max 365).
func (s *Server) tunnelTraffic(w http.ResponseWriter, r *http.Request, u store.User) {
	d, ok := s.ownedDevice(w, r, u)
	if !ok {
		return
	}
	tid, ok := pathInt(r, "tid")
	if !ok {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	}
	if _, err := s.store.GetAllocation(r.Context(), d.ID, tid); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tunnel not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	days := 30
	if q := r.URL.Query().Get("days"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 365 {
			writeErr(w, http.StatusBadRequest, "days must be 1-365")
			return
		}
		days = n
	}
	h, err := s.store.TrafficHistory(r.Context(), tid, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnel_id": tid, "days": h})
}

func (s *Server) myReservedPorts(w http.ResponseWriter, r *http.Request, u store.User) {
	rs, err := s.store.ListReservedPorts(r.Context(), &u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) myAudit(w http.ResponseWriter, r *http.Request, u store.User) {
	es, err := s.store.ListAudit(r.Context(), &u.ID, 100)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, es)
}

// Package api is the control plane's HTTP API (JSON, /api/v1).
//
// Authentication: browser sessions (HttpOnly cookie) or personal API tokens
// (Authorization: Bearer thk_...). Cross-site browser requests are rejected
// by net/http's CrossOriginProtection (Sec-Fetch-Site / Origin checks).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"tunnelhub/internal/auth"
	"tunnelhub/internal/stats"
	"tunnelhub/internal/store"
)

// RelayInfo is handed to an agent when its pairing completes.
type RelayInfo struct {
	Addr       string `json:"addr"`
	Port       int    `json:"port"`
	Protocol   string `json:"protocol"`
	Token      string `json:"token"`
	ServerName string `json:"server_name,omitempty"`
	CAPEM      string `json:"ca_pem,omitempty"`
}

type Config struct {
	OpenRegistration    bool
	SecureCookies       bool
	SessionTTL          time.Duration
	PairingTTL          time.Duration
	PublicURL           string // e.g. https://tunnels.example.com (used in claim links)
	Pool                store.PortPool
	ExtraPorts          map[int]bool  // relay-allowed ports outside the pool (premium/reserved)
	PortCooldown        time.Duration // released ports are kept from other users this long
	MaxDevicesPerUser   int
	MaxTunnelsPerDevice int
	TrustedProxies      []netip.Prefix
	Relay               RelayInfo
	// PublicHost is the hostname players connect to (default: Relay.Addr).
	PublicHost string
	// Live supplies per-tunnel relay status/traffic (nil = statistics disabled).
	Live LiveSource
	// Retention periods for the periodic cleanup (0 = keep forever).
	AuditRetention   time.Duration
	TrafficRetention time.Duration
	Version          string
	// Dashboard serves the web UI for non-API paths (nil = API only).
	Dashboard http.Handler
}

// LiveSource reports a tunnel's live relay state (implemented by stats.Collector).
type LiveSource interface {
	Live(deviceID, name string) (stats.Live, bool)
}

type Server struct {
	store  *store.Store
	cfg    Config
	log    *slog.Logger
	rl     *rateLimiter
	notify *notifier
}

// sessionCookie uses the __Host- prefix when cookies are Secure, which pins
// the cookie to this exact host and path "/".
func (s *Server) sessionCookie() string {
	if s.cfg.SecureCookies {
		return "__Host-th_session"
	}
	return "th_session"
}

func (s *Server) policy() store.PortPolicy {
	return store.PortPolicy{Pool: s.cfg.Pool, Extra: s.cfg.ExtraPorts, Cooldown: s.cfg.PortCooldown,
		MaxPerDevice: s.cfg.MaxTunnelsPerDevice}
}

func New(st *store.Store, cfg Config, log *slog.Logger) *Server {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.PairingTTL == 0 {
		cfg.PairingTTL = 10 * time.Minute
	}
	if cfg.MaxDevicesPerUser == 0 {
		cfg.MaxDevicesPerUser = 10
	}
	if cfg.MaxTunnelsPerDevice == 0 {
		cfg.MaxTunnelsPerDevice = 20
	}
	if cfg.PublicHost == "" {
		cfg.PublicHost = cfg.Relay.Addr
	}
	return &Server{store: st, cfg: cfg, log: log, rl: newRateLimiter(), notify: newNotifier()}
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/info", s.info)

	// auth
	mux.HandleFunc("POST /api/v1/auth/register", s.limit("register", s.register))
	mux.HandleFunc("POST /api/v1/auth/login", s.limit("login", s.login))
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", s.user(s.me))
	mux.HandleFunc("POST /api/v1/me/password", s.limit("login", s.user(s.changePassword)))

	// API tokens
	mux.HandleFunc("GET /api/v1/tokens", s.user(s.listTokens))
	mux.HandleFunc("POST /api/v1/tokens", s.user(s.createToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.user(s.revokeToken))

	// devices
	mux.HandleFunc("GET /api/v1/devices", s.user(s.listDevices))
	mux.HandleFunc("POST /api/v1/devices/claim", s.limit("claim", s.user(s.claimDevice)))
	mux.HandleFunc("GET /api/v1/devices/{id}", s.user(s.getDevice))
	mux.HandleFunc("PATCH /api/v1/devices/{id}", s.user(s.updateDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.user(s.revokeDevice))

	// tunnels
	mux.HandleFunc("GET /api/v1/tunnels", s.user(s.listMyTunnels))
	mux.HandleFunc("GET /api/v1/devices/{id}/tunnels", s.user(s.listTunnels))
	mux.HandleFunc("POST /api/v1/devices/{id}/tunnels", s.user(s.createTunnel))
	mux.HandleFunc("GET /api/v1/devices/{id}/tunnels/{tid}", s.user(s.getTunnel))
	mux.HandleFunc("PATCH /api/v1/devices/{id}/tunnels/{tid}", s.user(s.updateTunnel))
	mux.HandleFunc("DELETE /api/v1/devices/{id}/tunnels/{tid}", s.user(s.deleteTunnel))
	mux.HandleFunc("GET /api/v1/devices/{id}/tunnels/{tid}/traffic", s.user(s.tunnelTraffic))
	mux.HandleFunc("GET /api/v1/me/summary", s.user(s.mySummary))
	mux.HandleFunc("GET /api/v1/reserved-ports", s.user(s.myReservedPorts))
	mux.HandleFunc("GET /api/v1/audit", s.user(s.myAudit))

	// agent pairing (unauthenticated, rate limited) and config sync (device token)
	mux.HandleFunc("POST /api/v1/agent/pair/start", s.limit("pair-start", s.pairStart))
	mux.HandleFunc("POST /api/v1/agent/pair/poll", s.limit("pair-poll", s.pairPoll))
	mux.HandleFunc("GET /api/v1/agent/config", s.limit("agent", s.agentConfig))

	// admin
	mux.HandleFunc("GET /api/v1/admin/overview", s.admin(s.adminOverview))
	mux.HandleFunc("GET /api/v1/admin/users", s.admin(s.adminListUsers))
	mux.HandleFunc("POST /api/v1/admin/users", s.admin(s.adminCreateUser))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.admin(s.adminUpdateUser))
	mux.HandleFunc("GET /api/v1/admin/devices", s.admin(s.adminListDevices))
	mux.HandleFunc("DELETE /api/v1/admin/devices/{id}", s.admin(s.revokeDevice))
	mux.HandleFunc("GET /api/v1/admin/reserved-ports", s.admin(s.adminListReserved))
	mux.HandleFunc("PUT /api/v1/admin/reserved-ports/{type}/{port}", s.admin(s.adminReservePort))
	mux.HandleFunc("DELETE /api/v1/admin/reserved-ports/{type}/{port}", s.admin(s.adminUnreservePort))
	mux.HandleFunc("GET /api/v1/admin/audit", s.admin(s.adminAudit))

	// Anything else under /api is a JSON 404, not the dashboard.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Dashboard == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.cfg.Dashboard.ServeHTTP(w, r)
	})

	return s.recoverer(securityHeaders(http.NewCrossOriginProtection().Handler(mux)))
}

// securityHeaders sets conservative headers on every response. The dashboard
// uses no inline script or style, so the CSP can be strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		next.ServeHTTP(w, r)
	})
}

// RunCleanup periodically deletes expired sessions and pairings.
func (s *Server) RunCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.store.Cleanup(ctx, store.CleanupOptions{AuditRetention: s.cfg.AuditRetention,
				TrafficRetention: s.cfg.TrafficRetention, PortCooldown: s.cfg.PortCooldown}); err != nil {
				s.log.Warn("cleanup failed", "err", err)
			}
			s.rl.prune(30 * time.Minute)
		}
	}
}

// ---------------------------------------------------------------- middleware

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", fmt.Sprint(v))
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type authedFunc func(w http.ResponseWriter, r *http.Request, u store.User)

// currentUser resolves a Bearer API token or session cookie.
func (s *Server) currentUser(r *http.Request) (store.User, bool, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || !strings.HasPrefix(tok, "thk_") {
			return store.User{}, false, nil
		}
		u, err := s.store.APITokenUser(r.Context(), auth.HashToken(tok))
		if errors.Is(err, store.ErrNotFound) {
			return u, false, nil
		}
		return u, err == nil, err
	}
	c, err := r.Cookie(s.sessionCookie())
	if err != nil || !strings.HasPrefix(c.Value, "ths_") {
		return store.User{}, false, nil
	}
	u, err := s.store.SessionUser(r.Context(), auth.HashToken(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		return u, false, nil
	}
	return u, err == nil, err
}

func (s *Server) user(h authedFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok, err := s.currentUser(r)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) admin(h authedFunc) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, u store.User) {
		if !u.IsAdmin() {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		h(w, r, u)
	})
}

func (s *Server) limit(bucket string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.rl.allow(bucket, s.clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		h(w, r)
	}
}

// clientIP returns the peer address, or the right-most untrusted
// X-Forwarded-For hop when the peer is a trusted reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.trusted(host) {
		return host
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		if !s.trusted(h) {
			return h
		}
	}
	return host
}

func (s *Server) trusted(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- helpers

type errBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errBody{Error: msg})
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// decode reads a JSON body strictly (unknown fields rejected, 64 KiB max).
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathInt(r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return v, err == nil && v > 0
}

func (s *Server) audit(r *http.Request, actor *store.User, deviceID, action, target string, details map[string]any) {
	e := store.AuditEntry{ActorDeviceID: deviceID, Action: action, Target: target, IP: s.clientIP(r), Details: details}
	if actor != nil {
		e.ActorUserID = &actor.ID
	}
	// Use a fresh context: the audit write should survive a client disconnect.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.store.Audit(ctx, e); err != nil {
		s.log.Error("audit write failed", "action", action, "err", err)
	}
}

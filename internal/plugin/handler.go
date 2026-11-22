// Package plugin implements the frps HTTP server-plugin endpoint. frps calls it
// for Login, NewProxy, CloseProxy and Ping. This is the platform's security
// boundary: a device may only log in with a valid, non-revoked token and may
// only open proxies that were allocated to it.
//
// Protocol: POST <path>?version=0.1.0&op=<Op> with JSON {version, op, content};
// reply {reject, reject_reason, unchange, content}.
package plugin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	plugin "github.com/fatedier/frp/pkg/plugin/server"
)

// Device is a registered agent as the plugin sees it.
type Device struct {
	ID        string
	TokenHash string // hex sha256 of the device token
	Enabled   bool   // paired, not revoked/disabled, owner enabled
}

// Allocation is one public port granted to a device.
type Allocation struct {
	DeviceID       string
	Name           string // tunnel name as configured in the agent
	Type           string // tcp | udp
	RemotePort     int
	Enabled        bool
	BandwidthLimit string // e.g. "10MB" (frp syntax); empty = unlimited
}

// LoginInfo is recorded on successful agent login.
type LoginInfo struct {
	IP, Hostname, OS, Arch, Version string
}

// Store is the data the plugin needs (implemented by store.Store on PostgreSQL).
// Lookups return found=false for unknown rows and err only for failures.
type Store interface {
	Device(ctx context.Context, id string) (Device, bool, error)
	Allocation(ctx context.Context, deviceID, name string) (Allocation, bool, error)
	RecordLogin(ctx context.Context, deviceID string, info LoginInfo) error
	RecordSeen(ctx context.Context, deviceID string) error
}

type Handler struct {
	Store Store
	Log   *slog.Logger
}

// HashToken returns the hex sha256 used to compare device tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type rawRequest struct {
	Version string          `json:"version"`
	Op      string          `json:"op"`
	Content json.RawMessage `json:"content"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rawRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var resp plugin.Response
	switch req.Op {
	case "Login":
		resp = h.login(ctx, req.Content)
	case "NewProxy":
		resp = h.newProxy(ctx, req.Content)
	case "Ping":
		resp = h.ping(ctx, req.Content)
	case "CloseProxy":
		resp = h.closeProxy(req.Content)
	default:
		// Fail closed on ops we did not expect frps to send.
		resp = reject("unsupported operation")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func reject(reason string) plugin.Response {
	return plugin.Response{Reject: true, RejectReason: reason}
}

func allow() plugin.Response { return plugin.Response{Unchange: true} }

type authResult int

const (
	authOK authResult = iota
	authDenied
	authError // store failure: reject, but don't log it as a bad credential
)

// authenticate checks a device ID + token pair from login metadata.
func (h *Handler) authenticate(ctx context.Context, user string, metas map[string]string) (Device, authResult) {
	id := metas["device_id"]
	if id == "" || id != user {
		return Device{}, authDenied
	}
	d, found, err := h.Store.Device(ctx, id)
	if err != nil {
		h.Log.Error("device lookup failed", "device", id, "err", err)
		return Device{}, authError
	}
	if !found || !d.Enabled {
		return Device{}, authDenied
	}
	got := HashToken(metas["device_token"])
	if subtle.ConstantTimeCompare([]byte(got), []byte(d.TokenHash)) != 1 {
		return Device{}, authDenied
	}
	return d, authOK
}

func rejectFor(res authResult) plugin.Response {
	if res == authError {
		return reject("control plane temporarily unavailable")
	}
	return reject("invalid device credentials")
}

func (h *Handler) login(ctx context.Context, raw json.RawMessage) plugin.Response {
	var c plugin.LoginContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return reject("malformed login")
	}
	if _, res := h.authenticate(ctx, c.User, c.Metas); res != authOK {
		h.Log.Warn("login rejected", "user", c.User, "client_addr", c.ClientAddress)
		return rejectFor(res)
	}
	ip := c.ClientAddress
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if err := h.Store.RecordLogin(ctx, c.User, LoginInfo{
		IP: ip, Hostname: c.Hostname, OS: c.Os, Arch: c.Arch, Version: c.Version,
	}); err != nil {
		h.Log.Warn("record login failed", "device", c.User, "err", err)
	}
	h.Log.Info("login accepted", "device", c.User, "client_addr", c.ClientAddress,
		"os", c.Os, "arch", c.Arch, "version", c.Version)
	return allow()
}

// ping re-checks credentials on every heartbeat. On rejection frps answers the
// agent with an error Pong (a stock agent then drops the session immediately)
// and stops refreshing the heartbeat, so a modified agent that ignores the
// error is cut off after frps' heartbeatTimeout.
func (h *Handler) ping(ctx context.Context, raw json.RawMessage) plugin.Response {
	var c plugin.PingContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return reject("malformed ping")
	}
	if _, res := h.authenticate(ctx, c.User.User, c.User.Metas); res != authOK {
		if res == authDenied {
			h.Log.Warn("ping rejected, device revoked or disabled", "device", c.User.User)
		}
		return rejectFor(res)
	}
	if err := h.Store.RecordSeen(ctx, c.User.User); err != nil {
		h.Log.Warn("record seen failed", "device", c.User.User, "err", err)
	}
	return allow()
}

func (h *Handler) newProxy(ctx context.Context, raw json.RawMessage) plugin.Response {
	var c plugin.NewProxyContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return reject("malformed new proxy")
	}
	dev, res := h.authenticate(ctx, c.User.User, c.User.Metas)
	if res != authOK {
		return rejectFor(res)
	}

	// frpc sends "<user>.<name>"; strip our own prefix.
	name := strings.TrimPrefix(c.ProxyName, dev.ID+".")
	log := h.Log.With("device", dev.ID, "proxy", name, "type", c.ProxyType, "remote_port", c.RemotePort)

	if c.ProxyType != "tcp" && c.ProxyType != "udp" {
		log.Warn("proxy rejected: type not allowed")
		return reject("only tcp and udp tunnels are allowed")
	}
	if c.Group != "" {
		return reject("load-balancing groups are not allowed")
	}
	a, found, err := h.Store.Allocation(ctx, dev.ID, name)
	if err != nil {
		log.Error("allocation lookup failed", "err", err)
		return reject("control plane temporarily unavailable")
	}
	if !found || !a.Enabled {
		log.Warn("proxy rejected: no enabled allocation")
		return reject("tunnel is not allocated to this device")
	}
	if a.Type != c.ProxyType || a.RemotePort != c.RemotePort {
		log.Warn("proxy rejected: allocation mismatch", "allocated_type", a.Type, "allocated_port", a.RemotePort)
		return reject("tunnel type/port does not match allocation")
	}

	// Enforce server-side bandwidth limits, overriding whatever the client sent.
	out := c
	out.BandwidthLimit = a.BandwidthLimit
	out.BandwidthLimitMode = ""
	if a.BandwidthLimit != "" {
		out.BandwidthLimitMode = "server"
	}
	log.Info("proxy accepted", "bandwidth_limit", a.BandwidthLimit)
	return plugin.Response{Unchange: false, Content: out}
}

func (h *Handler) closeProxy(raw json.RawMessage) plugin.Response {
	var c plugin.CloseProxyContent
	if err := json.Unmarshal(raw, &c); err == nil {
		h.Log.Info("proxy closed", "device", c.User.User, "proxy", c.ProxyName)
	}
	return allow()
}

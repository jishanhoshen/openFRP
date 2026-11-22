package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

const goodToken = "0123456789abcdef0123456789abcdef"

type memStore struct {
	devices map[string]Device
	allocs  map[string]Allocation
	fail    bool // simulate a database outage
	logins  []string
}

func (m *memStore) Device(_ context.Context, id string) (Device, bool, error) {
	if m.fail {
		return Device{}, false, errors.New("db down")
	}
	d, ok := m.devices[id]
	return d, ok, nil
}

func (m *memStore) Allocation(_ context.Context, dev, name string) (Allocation, bool, error) {
	if m.fail {
		return Allocation{}, false, errors.New("db down")
	}
	a, ok := m.allocs[dev+"/"+name]
	return a, ok, nil
}

func (m *memStore) RecordLogin(_ context.Context, id string, _ LoginInfo) error {
	m.logins = append(m.logins, id)
	return nil
}

func (m *memStore) RecordSeen(context.Context, string) error { return nil }

func newHandler() *Handler {
	return &Handler{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: &memStore{
			devices: map[string]Device{
				"dev-a":   {ID: "dev-a", TokenHash: HashToken(goodToken), Enabled: true},
				"dev-off": {ID: "dev-off", TokenHash: HashToken(goodToken), Enabled: false},
			},
			allocs: map[string]Allocation{
				"dev-a/mc":   {DeviceID: "dev-a", Name: "mc", Type: "tcp", RemotePort: 20001, Enabled: true, BandwidthLimit: "10MB"},
				"dev-a/game": {DeviceID: "dev-a", Name: "game", Type: "udp", RemotePort: 20002, Enabled: true},
				"dev-a/old":  {DeviceID: "dev-a", Name: "old", Type: "tcp", RemotePort: 20003, Enabled: false},
			},
		},
	}
}

func call(t *testing.T, h *Handler, op string, content any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"version": "0.1.0", "op": op, "content": content})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/frps/plugin?op="+op, bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func metas(id, tok string) map[string]string {
	return map[string]string{"device_id": id, "device_token": tok}
}

func TestLogin(t *testing.T) {
	h := newHandler()
	cases := []struct {
		name, user, id, tok string
		wantReject          bool
	}{
		{"valid", "dev-a", "dev-a", goodToken, false},
		{"wrong token", "dev-a", "dev-a", "nope-nope-nope-nope-nope-nope-nope", true},
		{"unknown device", "dev-x", "dev-x", goodToken, true},
		{"disabled device", "dev-off", "dev-off", goodToken, true},
		{"user/id mismatch", "dev-b", "dev-a", goodToken, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := call(t, h, "Login", map[string]any{"user": tc.user, "metas": metas(tc.id, tc.tok)})
			if out["reject"].(bool) != tc.wantReject {
				t.Fatalf("reject=%v want %v (%v)", out["reject"], tc.wantReject, out["reject_reason"])
			}
		})
	}
}

func newProxy(name, typ string, port int) map[string]any {
	return map[string]any{
		"user":        map[string]any{"user": "dev-a", "metas": metas("dev-a", goodToken)},
		"proxy_name":  "dev-a." + name,
		"proxy_type":  typ,
		"remote_port": port,
	}
}

func TestNewProxy(t *testing.T) {
	h := newHandler()
	cases := []struct {
		name       string
		content    map[string]any
		wantReject bool
	}{
		{"allocated tcp", newProxy("mc", "tcp", 20001), false},
		{"allocated udp", newProxy("game", "udp", 20002), false},
		{"wrong port", newProxy("mc", "tcp", 20009), true},
		{"wrong type", newProxy("mc", "udp", 20001), true},
		{"disabled allocation", newProxy("old", "tcp", 20003), true},
		{"unallocated name", newProxy("other", "tcp", 20001), true},
		{"http type", newProxy("mc", "http", 0), true},
		{"stcp type", newProxy("mc", "stcp", 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := call(t, h, "NewProxy", tc.content)
			if out["reject"].(bool) != tc.wantReject {
				t.Fatalf("reject=%v want %v (%v)", out["reject"], tc.wantReject, out["reject_reason"])
			}
		})
	}
}

func TestNewProxyEnforcesBandwidthLimit(t *testing.T) {
	h := newHandler()
	c := newProxy("mc", "tcp", 20001)
	c["bandwidth_limit"] = "999MB" // client tries to raise its own limit
	out := call(t, h, "NewProxy", c)
	if out["unchange"].(bool) {
		t.Fatal("expected modified content")
	}
	content := out["content"].(map[string]any)
	if content["bandwidth_limit"] != "10MB" || content["bandwidth_limit_mode"] != "server" {
		t.Fatalf("limit not enforced: %v / %v", content["bandwidth_limit"], content["bandwidth_limit_mode"])
	}
}

func TestPingRejectsRevokedDevice(t *testing.T) {
	h := newHandler()
	out := call(t, h, "Ping", map[string]any{"user": map[string]any{"user": "dev-off", "metas": metas("dev-off", goodToken)}})
	if !out["reject"].(bool) {
		t.Fatal("disabled device ping should be rejected")
	}
}

func TestUnknownOpFailsClosed(t *testing.T) {
	out := call(t, newHandler(), "NewUserConn", map[string]any{})
	if !out["reject"].(bool) {
		t.Fatal("unknown op should be rejected")
	}
}

func TestStoreFailureFailsClosed(t *testing.T) {
	h := newHandler()
	h.Store.(*memStore).fail = true
	out := call(t, h, "Login", map[string]any{"user": "dev-a", "metas": metas("dev-a", goodToken)})
	if !out["reject"].(bool) || out["reject_reason"] != "control plane temporarily unavailable" {
		t.Fatalf("login during outage: %v", out)
	}
	out = call(t, h, "NewProxy", newProxy("mc", "tcp", 20001))
	if !out["reject"].(bool) {
		t.Fatal("new proxy during outage must be rejected")
	}
}

func TestLoginIsRecorded(t *testing.T) {
	h := newHandler()
	call(t, h, "Login", map[string]any{"user": "dev-a", "metas": metas("dev-a", goodToken), "client_address": "1.2.3.4:5555"})
	if got := h.Store.(*memStore).logins; len(got) != 1 || got[0] != "dev-a" {
		t.Fatalf("login not recorded: %v", got)
	}
}

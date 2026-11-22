package api_test

// Integration tests against a real PostgreSQL. Each test gets its own schema.
// Run via `make test` (starts a throwaway postgres) or set TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"tunnelhub/internal/api"
	"tunnelhub/internal/auth"
	"tunnelhub/internal/db"
	"tunnelhub/internal/plugin"
	"tunnelhub/internal/store"
)

type env struct {
	t     *testing.T
	ts    *httptest.Server
	store *store.Store
}

func setup(t *testing.T, mutate func(*api.Config)) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (run `make test`)")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)

	admin, err := db.Open(ctx, db.Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, db.Options{URL: url, SearchPath: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	cfg := api.Config{
		OpenRegistration:    true,
		SecureCookies:       false,
		PublicURL:           "https://tunnels.example.test",
		Pool:                store.PortPool{Start: 20000, End: 20004},
		MaxDevicesPerUser:   2,
		MaxTunnelsPerDevice: 3,
		Relay:               api.RelayInfo{Addr: "relay.example.test", Port: 7000, Protocol: "quic", Token: "relay-tok"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv := api.New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, ts: ts, store: st}
}

type client struct {
	e      *env
	hc     *http.Client
	bearer string
	header map[string]string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, hc: &http.Client{Jar: jar}}
}

// do sends a request and decodes the JSON response into a generic value.
func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &out)
	} else if len(raw) > 0 && raw[0] == '[' {
		var arr []any
		_ = json.Unmarshal(raw, &arr)
		out = map[string]any{"items": arr}
	}
	return resp.StatusCode, out
}

func (c *client) must(want int, method, path string, body any) map[string]any {
	c.e.t.Helper()
	got, out := c.do(method, path, body)
	if got != want {
		c.e.t.Fatalf("%s %s: status %d, want %d (%v)", method, path, got, want, out)
	}
	return out
}

func (e *env) user(email string) *client {
	c := e.client()
	c.must(201, "POST", "/api/v1/auth/register", map[string]string{"email": email, "password": "correct-horse-battery"})
	c.must(200, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "correct-horse-battery"})
	return c
}

func (e *env) adminUser(email string) *client {
	hash, _ := auth.HashPassword("correct-horse-battery")
	if _, err := e.store.CreateUser(context.Background(), email, hash, "admin"); err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	c.must(200, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "correct-horse-battery"})
	return c
}

// pairDevice runs the full agent pairing flow and returns (deviceID, deviceToken).
func (e *env) pairDevice(owner *client, name string) (string, string) {
	e.t.Helper()
	agent := e.client()
	st := agent.must(201, "POST", "/api/v1/agent/pair/start", map[string]string{"hostname": "pc", "os": "linux", "arch": "amd64", "version": "test"})
	owner.must(201, "POST", "/api/v1/devices/claim", map[string]string{"code": st["code"].(string), "name": name})
	res := agent.must(200, "POST", "/api/v1/agent/pair/poll", map[string]string{"poll_secret": st["poll_secret"].(string)})
	return res["device_id"].(string), res["device_token"].(string)
}

// ---------------------------------------------------------------- tests

func TestRegisterLoginLogout(t *testing.T) {
	e := setup(t, nil)
	c := e.client()
	c.must(201, "POST", "/api/v1/auth/register", map[string]string{"email": "Alice@Example.test", "password": "correct-horse-battery"})
	c.must(409, "POST", "/api/v1/auth/register", map[string]string{"email": "alice@example.test", "password": "correct-horse-battery"})
	c.must(400, "POST", "/api/v1/auth/register", map[string]string{"email": "bob@example.test", "password": "short"})
	c.must(400, "POST", "/api/v1/auth/register", map[string]string{"email": "not-an-email", "password": "correct-horse-battery"})
	c.must(400, "POST", "/api/v1/auth/register", map[string]any{"email": "x@example.test", "password": "correct-horse-battery", "role": "admin"}) // unknown field

	c.must(401, "GET", "/api/v1/me", nil)
	c.must(401, "POST", "/api/v1/auth/login", map[string]string{"email": "alice@example.test", "password": "wrong-password!"})
	c.must(401, "POST", "/api/v1/auth/login", map[string]string{"email": "nobody@example.test", "password": "correct-horse-battery"})
	c.must(200, "POST", "/api/v1/auth/login", map[string]string{"email": "alice@example.test", "password": "correct-horse-battery"})
	me := c.must(200, "GET", "/api/v1/me", nil)
	if me["role"] != "user" || me["password_hash"] != nil {
		t.Fatalf("bad /me: %v", me)
	}
	c.must(204, "POST", "/api/v1/auth/logout", nil)
	c.must(401, "GET", "/api/v1/me", nil)
}

func TestRegistrationClosed(t *testing.T) {
	e := setup(t, func(c *api.Config) { c.OpenRegistration = false })
	e.client().must(403, "POST", "/api/v1/auth/register", map[string]string{"email": "a@example.test", "password": "correct-horse-battery"})
}

func TestCrossOriginBrowserRequestRejected(t *testing.T) {
	e := setup(t, nil)
	c := e.user("alice@example.test")
	c.header = map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	c.must(403, "POST", "/api/v1/tokens", map[string]any{"name": "x"})
}

func TestAPITokens(t *testing.T) {
	e := setup(t, nil)
	c := e.user("alice@example.test")
	tok := c.must(201, "POST", "/api/v1/tokens", map[string]any{"name": "ci", "expires_in_days": 30})
	secret := tok["token"].(string)

	b := e.client()
	b.bearer = secret
	if me := b.must(200, "GET", "/api/v1/me", nil); me["email"] != "alice@example.test" {
		t.Fatalf("bearer auth returned %v", me)
	}
	list := c.must(200, "GET", "/api/v1/tokens", nil)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["token"] != nil {
		t.Fatalf("token list must not expose secrets: %v", list)
	}
	c.must(204, "DELETE", fmt.Sprintf("/api/v1/tokens/%v", tok["id"]), nil)
	b.must(401, "GET", "/api/v1/me", nil)

	b.bearer = "thk_garbage"
	b.must(401, "GET", "/api/v1/me", nil)
}

func TestPairingFlow(t *testing.T) {
	e := setup(t, nil)
	alice := e.user("alice@example.test")
	agent := e.client()

	st := agent.must(201, "POST", "/api/v1/agent/pair/start", map[string]string{"hostname": "gaming-pc", "os": "windows", "arch": "amd64", "version": "1.0"})
	code, secret := st["code"].(string), st["poll_secret"].(string)
	if st["claim_url"] != "https://tunnels.example.test/claim?code="+code {
		t.Fatalf("claim_url = %v", st["claim_url"])
	}
	agent.must(202, "POST", "/api/v1/agent/pair/poll", map[string]string{"poll_secret": secret})
	agent.must(410, "POST", "/api/v1/agent/pair/poll", map[string]string{"poll_secret": "thp_wrong"})

	alice.must(404, "POST", "/api/v1/devices/claim", map[string]string{"code": "AAAA-AAAA"})
	// lower-case with a space instead of the dash: normalised
	dev := alice.must(201, "POST", "/api/v1/devices/claim", map[string]string{"code": strings.ToLower(code[:4] + " " + code[5:])})
	if dev["name"] != "gaming-pc" || dev["os"] != "windows" || dev["paired"] != false {
		t.Fatalf("claimed device: %v", dev)
	}
	alice.must(404, "POST", "/api/v1/devices/claim", map[string]string{"code": code}) // single use

	// Not yet collected: the plugin must not accept the device.
	if d, _, _ := e.store.Device(context.Background(), dev["id"].(string)); d.Enabled {
		t.Fatal("device must not be enabled before the agent collects its token")
	}

	res := agent.must(200, "POST", "/api/v1/agent/pair/poll", map[string]string{"poll_secret": secret})
	relay := res["relay"].(map[string]any)
	if res["device_id"] != dev["id"] || relay["addr"] != "relay.example.test" || relay["token"] != "relay-tok" {
		t.Fatalf("poll result: %v", res)
	}
	agent.must(410, "POST", "/api/v1/agent/pair/poll", map[string]string{"poll_secret": secret}) // token delivered once

	d, found, err := e.store.Device(context.Background(), dev["id"].(string))
	if err != nil || !found || !d.Enabled || d.TokenHash != plugin.HashToken(res["device_token"].(string)) {
		t.Fatalf("plugin view of device: %+v found=%v err=%v", d, found, err)
	}
}

func TestDeviceQuota(t *testing.T) {
	e := setup(t, nil) // MaxDevicesPerUser = 2
	alice := e.user("alice@example.test")
	e.pairDevice(alice, "one")
	e.pairDevice(alice, "two")
	st := e.client().must(201, "POST", "/api/v1/agent/pair/start", map[string]string{})
	alice.must(409, "POST", "/api/v1/devices/claim", map[string]string{"code": st["code"].(string)})
}

func TestDeviceIsolation(t *testing.T) {
	e := setup(t, nil)
	alice, bob := e.user("alice@example.test"), e.user("bob@example.test")
	id, _ := e.pairDevice(alice, "alice-pc")
	p := "/api/v1/devices/" + id

	bob.must(404, "GET", p, nil)
	bob.must(404, "PATCH", p, map[string]any{"name": "pwned"})
	bob.must(404, "DELETE", p, nil)
	bob.must(404, "GET", p+"/tunnels", nil)
	bob.must(404, "POST", p+"/tunnels", map[string]any{"name": "x", "type": "tcp", "local_port": 22})
	if n := len(bob.must(200, "GET", "/api/v1/devices", nil)["items"].([]any)); n != 0 {
		t.Fatalf("bob sees %d devices", n)
	}
	bob.must(403, "GET", "/api/v1/admin/devices", nil)
	alice.must(200, "GET", p, nil)
}

func TestTunnelAllocation(t *testing.T) {
	e := setup(t, nil) // pool 20000-20004, 3 tunnels per device
	alice := e.user("alice@example.test")
	id, _ := e.pairDevice(alice, "pc")
	p := "/api/v1/devices/" + id + "/tunnels"

	a := alice.must(201, "POST", p, map[string]any{"name": "mc", "type": "tcp", "local_port": 25565})
	b := alice.must(201, "POST", p, map[string]any{"name": "bedrock", "type": "udp", "local_port": 19132})
	if a["remote_port"] != float64(20000) || b["remote_port"] != float64(20000) {
		t.Fatalf("auto ports: tcp=%v udp=%v (same number is fine across protocols)", a["remote_port"], b["remote_port"])
	}
	alice.must(409, "POST", p, map[string]any{"name": "other", "type": "tcp", "local_port": 80, "remote_port": 20000}) // port taken
	alice.must(409, "POST", p, map[string]any{"name": "mc", "type": "tcp", "local_port": 80})                          // name taken
	alice.must(400, "POST", p, map[string]any{"name": "x", "type": "tcp", "local_port": 80, "remote_port": 25565})     // outside pool
	alice.must(400, "POST", p, map[string]any{"name": "x", "type": "tcp", "local_port": 80, "local_ip": "8.8.8.8"})    // public target
	alice.must(400, "POST", p, map[string]any{"name": "x", "type": "http", "local_port": 80})
	alice.must(400, "POST", p, map[string]any{"name": "Bad Name", "type": "tcp", "local_port": 80})
	alice.must(400, "POST", p, map[string]any{"name": "x", "type": "tcp", "local_port": 80, "bandwidth_limit": "100MB"}) // admin only
	c := alice.must(201, "POST", p, map[string]any{"name": "ssh", "type": "tcp", "local_port": 22, "local_ip": "192.168.1.10"})
	if c["remote_port"] != float64(20001) {
		t.Fatalf("next free tcp port = %v", c["remote_port"])
	}
	alice.must(409, "POST", p, map[string]any{"name": "web", "type": "tcp", "local_port": 80}) // quota (3)

	// The plugin sees exactly what was allocated.
	pa, found, err := e.store.Allocation(context.Background(), id, "mc")
	if err != nil || !found || pa.Type != "tcp" || pa.RemotePort != 20000 || !pa.Enabled {
		t.Fatalf("plugin allocation: %+v %v %v", pa, found, err)
	}
	alice.must(200, "PATCH", fmt.Sprintf("%s/%v", p, a["id"]), map[string]any{"enabled": false})
	if pa, _, _ := e.store.Allocation(context.Background(), id, "mc"); pa.Enabled {
		t.Fatal("disabled tunnel still enabled for plugin")
	}
	alice.must(204, "DELETE", fmt.Sprintf("%s/%v", p, a["id"]), nil)
	alice.must(404, "DELETE", fmt.Sprintf("%s/%v", p, a["id"]), nil)
	d := alice.must(201, "POST", p, map[string]any{"name": "web", "type": "tcp", "local_port": 80})
	if d["remote_port"] != float64(20000) {
		t.Fatalf("freed port should be reused, got %v", d["remote_port"])
	}
}

func TestConcurrentAutoAllocationNeverCollides(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	alice := e.user("alice@example.test")
	bob := e.user("bob@example.test")
	var devs []string
	for _, owner := range []*client{alice, alice, bob, bob} {
		id, _ := e.pairDevice(owner, "pc")
		devs = append(devs, id)
	}
	pool := store.PortPool{Start: 20000, End: 20004} // 5 ports, 8 requests
	var (
		mu    sync.Mutex
		ports []int
		fails []error
		wg    sync.WaitGroup
	)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := e.store.CreateAllocation(ctx, store.Allocation{
				DeviceID: devs[i%4], Name: fmt.Sprintf("t%d", i), Type: "udp", LocalIP: "127.0.0.1", LocalPort: 1000 + i, Enabled: true,
			}, store.PortPolicy{Pool: pool, MaxPerDevice: 10}, false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err)
				return
			}
			ports = append(ports, a.RemotePort)
		}()
	}
	wg.Wait()
	sort.Ints(ports)
	if fmt.Sprint(ports) != "[20000 20001 20002 20003 20004]" {
		t.Fatalf("ports = %v (fails=%v)", ports, fails)
	}
	for _, err := range fails {
		if !errors.Is(err, store.ErrPoolExhausted) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestRevokeDevice(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	alice := e.user("alice@example.test")
	id, _ := e.pairDevice(alice, "pc")
	alice.must(201, "POST", "/api/v1/devices/"+id+"/tunnels", map[string]any{"name": "mc", "type": "tcp", "local_port": 25565})

	alice.must(200, "PATCH", "/api/v1/devices/"+id, map[string]any{"disabled": true})
	if d, _, _ := e.store.Device(ctx, id); d.Enabled {
		t.Fatal("disabled device still enabled for plugin")
	}
	alice.must(200, "PATCH", "/api/v1/devices/"+id, map[string]any{"disabled": false})
	if d, _, _ := e.store.Device(ctx, id); !d.Enabled {
		t.Fatal("re-enabled device not enabled")
	}

	alice.must(204, "DELETE", "/api/v1/devices/"+id, nil)
	d, found, _ := e.store.Device(ctx, id)
	if !found || d.Enabled || d.TokenHash != "" {
		t.Fatalf("revoked device: %+v", d)
	}
	if _, found, _ := e.store.Allocation(ctx, id, "mc"); found {
		t.Fatal("revocation must free the device's ports")
	}
	alice.must(404, "GET", "/api/v1/devices/"+id, nil)
	if n := len(alice.must(200, "GET", "/api/v1/devices", nil)["items"].([]any)); n != 0 {
		t.Fatalf("revoked device still listed (%d)", n)
	}
}

func TestAdmin(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	admin := e.adminUser("root@example.test")
	alice := e.user("alice@example.test")
	id, _ := e.pairDevice(alice, "pc")

	alice.must(403, "GET", "/api/v1/admin/users", nil)
	users := admin.must(200, "GET", "/api/v1/admin/users", nil)["items"].([]any)
	if len(users) != 2 {
		t.Fatalf("users: %v", users)
	}
	me := admin.must(200, "GET", "/api/v1/me", nil)
	admin.must(400, "PATCH", fmt.Sprintf("/api/v1/admin/users/%v", me["id"]), map[string]any{"disabled": true})
	admin.must(400, "PATCH", fmt.Sprintf("/api/v1/admin/users/%v", me["id"]), map[string]any{"role": "user"})

	// Admin can manage other users' devices (e.g. set bandwidth limits).
	tun := admin.must(201, "POST", "/api/v1/devices/"+id+"/tunnels", map[string]any{"name": "mc", "type": "tcp", "local_port": 25565, "bandwidth_limit": "2MB"})
	if tun["bandwidth_limit"] != "2MB" {
		t.Fatalf("bandwidth: %v", tun)
	}

	// Disabling a user logs them out and disables their devices for the relay.
	aliceID := alice.must(200, "GET", "/api/v1/me", nil)["id"]
	admin.must(200, "PATCH", fmt.Sprintf("/api/v1/admin/users/%v", aliceID), map[string]any{"disabled": true})
	alice.must(401, "GET", "/api/v1/me", nil)
	if d, _, _ := e.store.Device(ctx, id); d.Enabled {
		t.Fatal("device of disabled user must not authenticate")
	}
	audit := admin.must(200, "GET", "/api/v1/admin/audit", nil)["items"].([]any)
	if len(audit) < 5 {
		t.Fatalf("expected audit entries, got %d", len(audit))
	}
}

func TestPasswordChangeLogsOutOtherSessions(t *testing.T) {
	e := setup(t, nil)
	a1 := e.user("alice@example.test")
	a2 := e.client()
	a2.must(200, "POST", "/api/v1/auth/login", map[string]string{"email": "alice@example.test", "password": "correct-horse-battery"})

	a1.must(401, "POST", "/api/v1/me/password", map[string]string{"current_password": "nope-nope-nope", "new_password": "another-long-password"})
	a1.must(204, "POST", "/api/v1/me/password", map[string]string{"current_password": "correct-horse-battery", "new_password": "another-long-password"})
	a1.must(200, "GET", "/api/v1/me", nil)
	a2.must(401, "GET", "/api/v1/me", nil)
	e.client().must(200, "POST", "/api/v1/auth/login", map[string]string{"email": "alice@example.test", "password": "another-long-password"})
}

func TestLoginRateLimited(t *testing.T) {
	e := setup(t, nil)
	c := e.client()
	saw429 := false
	for range 15 {
		if code, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"email": "x@example.test", "password": "whatever-password"}); code == 429 {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Fatal("expected 429 after repeated logins")
	}
}

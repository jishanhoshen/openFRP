package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"tunnelhub/internal/api"
	"tunnelhub/internal/store"
	"tunnelhub/internal/web"
)

// tunnelID finds a tunnel's ID by name on a device.
func tunnelID(c *client, dev, name string) string {
	c.e.t.Helper()
	_, out := c.do("GET", "/api/v1/devices/"+dev+"/tunnels", nil)
	for _, x := range out["items"].([]any) {
		m := x.(map[string]any)
		if m["name"] == name {
			return fmt.Sprint(int64(m["id"].(float64)))
		}
	}
	c.e.t.Fatalf("tunnel %s not found", name)
	return ""
}

func fmtID(f float64) string { return fmt.Sprint(int64(f)) }

// Agents get their tunnel list over a device-token long-poll; changes wake them
// immediately and revocation is refused at the next request.
func TestAgentConfigLongPollSync(t *testing.T) {
	e := setup(t, func(c *api.Config) { c.Dashboard = web.Handler() })
	owner := e.user("sync@example.test")
	devID, devTok := e.pairDevice(owner, "pc")
	agent := e.client()
	agent.bearer = devTok

	st, cfg := agent.do("GET", "/api/v1/agent/config", nil)
	if st != 200 || len(cfg["tunnels"].([]any)) != 0 {
		t.Fatalf("initial config: %d %v", st, cfg)
	}
	rev := cfg["revision"].(string)

	// Unchanged revision: the long-poll times out with 304.
	if st, _ := agent.do("GET", "/api/v1/agent/config?rev="+rev+"&wait=1", nil); st != http.StatusNotModified {
		t.Fatalf("unchanged poll: got %d, want 304", st)
	}

	// A tunnel created while the agent waits wakes it up with the new list.
	done := make(chan map[string]any, 1)
	go func() {
		_, c := agent.do("GET", "/api/v1/agent/config?rev="+rev+"&wait=20", nil)
		done <- c
	}()
	time.Sleep(300 * time.Millisecond)
	owner.must(201, "POST", "/api/v1/devices/"+devID+"/tunnels", map[string]any{"name": "mc", "type": "tcp", "local_port": 25565})
	select {
	case c := <-done:
		ts := c["tunnels"].([]any)
		if len(ts) != 1 || ts[0].(map[string]any)["name"] != "mc" {
			t.Fatalf("woken config = %v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("long-poll did not wake on change")
	}

	// Revoking the device refuses the agent's token at once.
	owner.must(204, "DELETE", "/api/v1/devices/"+devID, nil)
	if st, _ := agent.do("GET", "/api/v1/agent/config", nil); st != http.StatusUnauthorized {
		t.Fatalf("revoked device config: got %d, want 401", st)
	}
}

// A disabled device gets 403 (its tunnels stop; re-enabling restores them).
func TestAgentConfigDisabledDevice(t *testing.T) {
	e := setup(t, nil)
	owner := e.user("disabled@example.test")
	devID, devTok := e.pairDevice(owner, "pc")
	agent := e.client()
	agent.bearer = devTok
	owner.must(200, "PATCH", "/api/v1/devices/"+devID, map[string]any{"disabled": true})
	if st, _ := agent.do("GET", "/api/v1/agent/config", nil); st != http.StatusForbidden {
		t.Fatalf("disabled device: got %d, want 403", st)
	}
	owner.must(200, "PATCH", "/api/v1/devices/"+devID, map[string]any{"disabled": false})
	if st, _ := agent.do("GET", "/api/v1/agent/config", nil); st != 200 {
		t.Fatalf("re-enabled device: got %d, want 200", st)
	}
}

// Port policy: pool membership, reservations, cooldown after release, extras.
func TestPortPolicy(t *testing.T) {
	e := setup(t, func(c *api.Config) { c.ExtraPorts = map[int]bool{25565: true}; c.PortCooldown = time.Hour })
	admin := e.adminUser("ports-admin@example.test")
	alice := e.user("alice-ports@example.test")
	bob := e.user("bob-ports@example.test")
	aliceDev, _ := e.pairDevice(alice, "a")
	bobDev, _ := e.pairDevice(bob, "b")
	adminDev, _ := e.pairDevice(admin, "admin-pc")
	aliceID := alice.must(200, "GET", "/api/v1/me", nil)["id"].(float64)
	bobID := bob.must(200, "GET", "/api/v1/me", nil)["id"].(float64)
	tunnels := func(dev string) string { return "/api/v1/devices/" + dev + "/tunnels" }
	tcp := func(name string, port int) map[string]any {
		return map[string]any{"name": name, "type": "tcp", "local_port": 7777, "remote_port": port}
	}

	// Outside the pool and not extra: refused.
	alice.must(400, "POST", tunnels(aliceDev), tcp("out", 30000))
	// Extra port: users may not, admins may.
	alice.must(400, "POST", tunnels(aliceDev), tcp("mc", 25565))
	admin.must(201, "POST", tunnels(adminDev), tcp("mc", 25565))

	// Explicit pool port taken by Alice: Bob is refused.
	alice.must(201, "POST", tunnels(aliceDev), tcp("web", 20000))
	bob.must(409, "POST", tunnels(bobDev), tcp("web", 20000))

	// Released port cools down: Bob refused, Alice (the previous holder) may take it back.
	alice.must(204, "DELETE", tunnels(aliceDev)+"/"+tunnelID(alice, aliceDev, "web"), nil)
	bob.must(409, "POST", tunnels(bobDev), tcp("web", 20000))
	alice.must(201, "POST", tunnels(aliceDev), tcp("web", 20000))

	// Reservations: only the holder (or an admin) may use a reserved port.
	admin.must(204, "PUT", "/api/v1/admin/reserved-ports/tcp/20003", map[string]any{"user_id": bobID, "note": "bob's server"})
	alice.must(403, "POST", tunnels(aliceDev), tcp("steal", 20003))
	bob.must(201, "POST", tunnels(bobDev), tcp("bobs", 20003))
	// Automatic allocation never hands out reserved or taken ports.
	out := alice.must(201, "POST", tunnels(aliceDev), map[string]any{"name": "auto", "type": "tcp", "local_port": 7777})
	if p := out["remote_port"].(float64); p == 20003 || p == 20000 {
		t.Fatalf("auto allocation returned %v", p)
	}

	// Per-user tunnel quota override (default in tests: 3).
	admin.must(200, "PATCH", "/api/v1/admin/users/"+fmtID(aliceID), map[string]any{"max_tunnels_per_device": 1})
	alice.must(409, "POST", tunnels(aliceDev), map[string]any{"name": "extra", "type": "udp", "local_port": 7778})
	admin.must(200, "PATCH", "/api/v1/admin/users/"+fmtID(aliceID), map[string]any{"max_tunnels_per_device": -1})
}

// Dashboard is served with a strict CSP; unknown API paths are JSON 404s.
func TestDashboardAndHeaders(t *testing.T) {
	e := setup(t, func(c *api.Config) { c.Dashboard = web.Handler() })
	c := e.client()
	req, _ := http.NewRequest("GET", e.ts.URL+"/claim?code=ABCD-EFGH", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("dashboard: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("missing CSP: %q", csp)
	}
	if st, _ := c.do("GET", "/api/v1/nope", nil); st != 404 {
		t.Fatalf("unknown api path: %d", st)
	}
	if st, _ := c.do("POST", "/", nil); st != 405 {
		t.Fatalf("POST / = %d, want 405", st)
	}
}

// The overview summary counts the user's computers and tunnels and sums traffic.
func TestUserSummary(t *testing.T) {
	e := setup(t, nil)
	owner := e.user("summary@example.test")
	devID, _ := e.pairDevice(owner, "pc")
	owner.must(201, "POST", "/api/v1/devices/"+devID+"/tunnels", map[string]any{"name": "mc", "type": "tcp", "local_port": 25565})
	if err := e.store.AddTraffic(context.Background(), []store.TrafficDelta{
		{ProxyName: devID + ".mc", DeviceID: devID, Name: "mc", In: 1000, Out: 2500, CurIn: 1000, CurOut: 2500},
	}); err != nil {
		t.Fatal(err)
	}
	s := owner.must(200, "GET", "/api/v1/me/summary", nil)
	if s["computers"].(float64) != 1 || s["tunnels"].(float64) != 1 {
		t.Fatalf("counts = %v", s)
	}
	if s["traffic_30_in"].(float64) != 1000 || s["traffic_30_out"].(float64) != 2500 {
		t.Fatalf("traffic = %v", s)
	}
	if len(s["traffic_30_days"].([]any)) != 1 {
		t.Fatalf("series = %v", s["traffic_30_days"])
	}
}

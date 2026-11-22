package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tunnelhub/internal/agentcfg"
)

type apiClient struct {
	base string
	hc   *http.Client
}

func (c *apiClient) call(method, path string, in any, want int) (map[string]any, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != want {
		return out, fmt.Errorf("%s %s: HTTP %d (want %d): %s", method, path, resp.StatusCode, want, strings.TrimSpace(string(raw)))
	}
	return out, nil
}

// waitFor polls f until it returns nil or the deadline passes.
func waitFor(what string, timeout time.Duration, f func() error) error {
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			log.Printf("PASS %s", what)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("FAIL %s: %w", what, err)
}

func runScenario(args []string) int {
	fs := flag.NewFlagSet("scenario", flag.ExitOnError)
	apiURL := fs.String("api", "http://control:8080", "control-plane API")
	relay := fs.String("relay", "frps", "relay host players connect to")
	agentConfig := fs.String("agent-config", "/data/agent.yaml", "agent config path (shared volume)")
	stability := fs.Duration("stability", 70*time.Second, "how long tunnels must stay up (must exceed relay heartbeatTimeout)")
	_ = fs.Parse(args)

	if err := scenario(*apiURL, *relay, *agentConfig, *stability); err != nil {
		log.Print(err)
		return 1
	}
	log.Print("ALL E2E CHECKS PASSED")
	return 0
}

func scenario(apiURL, relay, agentConfig string, stability time.Duration) error {
	jar, _ := cookiejar.New(nil)
	owner := &apiClient{base: apiURL, hc: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	creds := map[string]string{"email": "owner@example.test", "password": "correct-horse-battery"}

	// 1. account
	if err := waitFor("control plane reachable", 60*time.Second, func() error {
		_, err := owner.call("POST", "/api/v1/auth/register", creds, http.StatusCreated)
		return err
	}); err != nil {
		return err
	}
	if _, err := owner.call("POST", "/api/v1/auth/login", creds, http.StatusOK); err != nil {
		return err
	}

	// 2. pairing: the agent started without credentials and is showing a code
	codeFile := filepath.Join(filepath.Dir(agentConfig), "pairing-code.txt")
	var code string
	if err := waitFor("agent displayed a pairing code", 60*time.Second, func() error {
		b, err := os.ReadFile(codeFile)
		code = strings.TrimSpace(string(b))
		if err == nil && len(code) != 9 {
			err = fmt.Errorf("bad code %q", code)
		}
		return err
	}); err != nil {
		return err
	}
	dev, err := owner.call("POST", "/api/v1/devices/claim", map[string]string{"code": code, "name": "e2e-pc"}, http.StatusCreated)
	if err != nil {
		return err
	}
	deviceID := dev["id"].(string)
	log.Printf("PASS claimed pairing code %s as device %s", code, deviceID)

	if err := waitFor("agent saved its credentials", 30*time.Second, func() error {
		c, err := agentcfg.Load(agentConfig)
		if err == nil && c.Device.ID != deviceID {
			err = fmt.Errorf("config has device %q", c.Device.ID)
		}
		return err
	}); err != nil {
		return err
	}

	// 3. tunnels are created in the control plane only; the agent syncs them.
	tcp, err := owner.call("POST", "/api/v1/devices/"+deviceID+"/tunnels",
		map[string]any{"name": "tcp-echo", "type": "tcp", "local_port": 7777}, http.StatusCreated)
	if err != nil {
		return err
	}
	udp, err := owner.call("POST", "/api/v1/devices/"+deviceID+"/tunnels",
		map[string]any{"name": "udp-echo", "type": "udp", "local_port": 7778}, http.StatusCreated)
	if err != nil {
		return err
	}
	tcpPort, udpPort := int(tcp["remote_port"].(float64)), int(udp["remote_port"].(float64))
	tcpID := fmt.Sprint(tcp["id"])
	log.Printf("allocated tcp-echo=%d/tcp udp-echo=%d/udp", tcpPort, udpPort)

	tcpAddr := fmt.Sprintf("%s:%d", relay, tcpPort)
	udpAddr := fmt.Sprintf("%s:%d", relay, udpPort)
	probeBoth := func(label string) error {
		if err := waitFor(label+": tcp via relay "+tcpAddr, 60*time.Second, func() error { return tcpRoundTrip(tcpAddr) }); err != nil {
			return err
		}
		return waitFor(label+": udp via relay "+udpAddr, 30*time.Second, func() error { return udpRoundTrip(udpAddr) })
	}

	// 4. the agent picked the tunnels up through its config sync (no restart)
	if err := probeBoth("synced"); err != nil {
		return err
	}

	// 5. a tunnel deleted in the dashboard is closed on the relay
	temp, err := owner.call("POST", "/api/v1/devices/"+deviceID+"/tunnels",
		map[string]any{"name": "temp", "type": "tcp", "local_port": 7777}, http.StatusCreated)
	if err != nil {
		return err
	}
	tempAddr := fmt.Sprintf("%s:%d", relay, int(temp["remote_port"].(float64)))
	if err := waitFor("temp tunnel up", 60*time.Second, func() error { return tcpRoundTrip(tempAddr) }); err != nil {
		return err
	}
	if _, err := owner.call("DELETE", "/api/v1/devices/"+deviceID+"/tunnels/"+fmt.Sprint(temp["id"]), nil, http.StatusNoContent); err != nil {
		return err
	}
	if err := waitFor("deleted tunnel closed", 45*time.Second, func() error {
		if tcpRoundTrip(tempAddr) == nil {
			return errors.New("still reachable")
		}
		return nil
	}); err != nil {
		return err
	}

	// 6. a disabled tunnel closes; re-enabling brings it back
	if _, err := owner.call("PATCH", "/api/v1/devices/"+deviceID+"/tunnels/"+tcpID, map[string]any{"enabled": false}, http.StatusOK); err != nil {
		return err
	}
	if err := waitFor("disabled tunnel closed", 45*time.Second, func() error {
		if tcpRoundTrip(tcpAddr) == nil {
			return errors.New("still reachable")
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := owner.call("PATCH", "/api/v1/devices/"+deviceID+"/tunnels/"+tcpID, map[string]any{"enabled": true}, http.StatusOK); err != nil {
		return err
	}
	if err := probeBoth("re-enabled"); err != nil {
		return err
	}

	// 7. the session survives longer than the relay's heartbeat timeout
	log.Printf("holding for %s to check heartbeats keep the session alive...", stability)
	end := time.Now().Add(stability)
	for time.Now().Before(end) {
		if err := tcpRoundTrip(tcpAddr); err != nil {
			return fmt.Errorf("FAIL tunnel dropped during stability window: %w", err)
		}
		time.Sleep(5 * time.Second)
	}
	if err := probeBoth("after " + stability.String()); err != nil {
		return err
	}
	if d, err := owner.call("GET", "/api/v1/devices/"+deviceID, nil, http.StatusOK); err != nil {
		return err
	} else if d["last_seen_at"] == nil || d["agent_version"] == "" {
		return fmt.Errorf("FAIL device status not recorded: %v", d)
	} else if d["online"] != true {
		return fmt.Errorf("FAIL device not reported online: %v", d["online"])
	}
	log.Print("PASS device last_seen/agent_version recorded; device reported online")

	// 8. statistics: the relay's traffic counters reach the database and the live view
	if err := waitFor("traffic recorded for tcp-echo", 60*time.Second, func() error {
		out, err := owner.call("GET", "/api/v1/devices/"+deviceID+"/tunnels/"+tcpID+"/traffic?days=1", nil, http.StatusOK)
		if err != nil {
			return err
		}
		for _, d := range out["days"].([]any) {
			if d.(map[string]any)["bytes_in"].(float64) > 0 {
				return nil
			}
		}
		return errors.New("no bytes yet")
	}); err != nil {
		return err
	}
	if err := waitFor("live status shows tcp-echo online", 30*time.Second, func() error {
		t, err := owner.call("GET", "/api/v1/devices/"+deviceID+"/tunnels/"+tcpID, nil, http.StatusOK)
		if err != nil {
			return err
		}
		if l, ok := t["live"].(map[string]any); ok && l["online"] == true {
			return nil
		}
		return errors.New("not live")
	}); err != nil {
		return err
	}

	// 9. revocation cuts the live session (next heartbeat is rejected)
	start := time.Now()
	if _, err := owner.call("DELETE", "/api/v1/devices/"+deviceID, nil, http.StatusNoContent); err != nil {
		return err
	}
	if err := waitFor("revoked device disconnected", 45*time.Second, func() error {
		if err := tcpRoundTrip(tcpAddr); err == nil {
			return errors.New("tunnel still up")
		}
		return nil
	}); err != nil {
		return err
	}
	log.Printf("revocation took effect after %s", time.Since(start).Round(time.Second))
	time.Sleep(5 * time.Second) // give the agent time to try (and fail) to reconnect
	if err := tcpRoundTrip(tcpAddr); err == nil {
		return errors.New("FAIL revoked device reconnected")
	}
	log.Print("PASS revoked device cannot reconnect")
	return nil
}

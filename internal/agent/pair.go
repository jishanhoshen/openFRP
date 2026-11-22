package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tunnelhub/internal/agentcfg"
)

// PairingCodeFile is written next to agent.yaml while a pairing is pending,
// so headless installs can read the code (e.g. `cat /etc/tunnelhub/pairing-code.txt`).
const PairingCodeFile = "pairing-code.txt"

type relayInfo struct {
	Addr       string `json:"addr"`
	Port       int    `json:"port"`
	Protocol   string `json:"protocol"`
	Token      string `json:"token"`
	ServerName string `json:"server_name"`
	CAPEM      string `json:"ca_pem"`
}

type pairResult struct {
	DeviceID    string    `json:"device_id"`
	DeviceToken string    `json:"device_token"`
	Relay       relayInfo `json:"relay"`
}

type pairStart struct {
	Code         string    `json:"code"`
	PollSecret   string    `json:"poll_secret"`
	ExpiresAt    time.Time `json:"expires_at"`
	PollInterval int       `json:"poll_interval_seconds"`
	ClaimURL     string    `json:"claim_url"`
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

func postJSON(ctx context.Context, url string, in, out any) (int, error) {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return resp.StatusCode, fmt.Errorf("%s: HTTP %d %s", url, resp.StatusCode, e.Error)
	}
	return resp.StatusCode, nil
}

// Pair runs the pairing flow against the control plane and writes the device
// credentials and relay settings into configPath (keeping existing tunnels).
// It prints the code to out and blocks until the code is claimed or ctx ends.
func Pair(ctx context.Context, apiURL, configPath, version string, out io.Writer) error {
	apiURL = strings.TrimRight(apiURL, "/")
	if !strings.HasPrefix(apiURL, "https://") && !strings.HasPrefix(apiURL, "http://") {
		return fmt.Errorf("api url must start with https:// (got %q)", apiURL)
	}
	if strings.HasPrefix(apiURL, "http://") {
		fmt.Fprintln(out, "WARNING: pairing over plain HTTP; use https:// outside of testing")
	}
	cfg, err := agentcfg.LoadRaw(configPath)
	if err != nil {
		return err
	}
	codePath := filepath.Join(filepath.Dir(configPath), PairingCodeFile)
	defer os.Remove(codePath)

	hostname, _ := os.Hostname()
	for {
		var st pairStart
		if _, err := postJSON(ctx, apiURL+"/api/v1/agent/pair/start", map[string]string{
			"hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH, "version": version,
		}, &st); err != nil {
			return fmt.Errorf("start pairing: %w", err)
		}
		fmt.Fprintf(out, "\nTo link this device to your account, open:\n\n    %s\n\nor enter this code in the dashboard:  %s   (expires %s)\n\n",
			st.ClaimURL, st.Code, st.ExpiresAt.Local().Format("15:04"))
		if err := os.MkdirAll(filepath.Dir(codePath), 0o700); err == nil {
			_ = os.WriteFile(codePath, []byte(st.Code+"\n"), 0o600)
		}

		interval := time.Duration(max(st.PollInterval, 2)) * time.Second
		res, err := poll(ctx, apiURL, st.PollSecret, interval)
		if errors.Is(err, errPairingExpired) {
			fmt.Fprintln(out, "Pairing code expired; requesting a new one...")
			continue
		}
		if err != nil {
			return err
		}
		if err := applyPairing(cfg, apiURL, configPath, res); err != nil {
			return err
		}
		fmt.Fprintf(out, "Paired as device %s. Credentials saved to %s\n", res.DeviceID, configPath)
		return nil
	}
}

var errPairingExpired = errors.New("pairing expired")

func poll(ctx context.Context, apiURL, secret string, interval time.Duration) (*pairResult, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var res pairResult
		status, err := postJSON(ctx, apiURL+"/api/v1/agent/pair/poll", map[string]string{"poll_secret": secret}, &res)
		switch {
		case status == http.StatusOK:
			return &res, nil
		case status == http.StatusAccepted:
			continue
		case status == http.StatusGone:
			return nil, errPairingExpired
		case status == http.StatusTooManyRequests || status >= 500 || status == 0:
			// transient: keep polling (network blip, server restart)
			_ = err
			continue
		default:
			return nil, err
		}
	}
}

func applyPairing(cfg *agentcfg.Config, apiURL, configPath string, res *pairResult) error {
	cfg.Control.URL = apiURL
	cfg.Device = agentcfg.DeviceConfig{ID: res.DeviceID, Token: res.DeviceToken}
	cfg.Server.Addr = res.Relay.Addr
	cfg.Server.Port = res.Relay.Port
	cfg.Server.Protocol = res.Relay.Protocol
	cfg.Server.Token = res.Relay.Token
	cfg.Server.ServerName = res.Relay.ServerName
	cfg.Server.CAFile = ""
	if res.Relay.CAPEM != "" {
		caPath := filepath.Join(filepath.Dir(configPath), "relay-ca.crt")
		if err := os.WriteFile(caPath, []byte(res.Relay.CAPEM+"\n"), 0o644); err != nil {
			return fmt.Errorf("write relay CA: %w", err)
		}
		cfg.Server.CAFile = caPath
	}
	return cfg.Save(configPath)
}

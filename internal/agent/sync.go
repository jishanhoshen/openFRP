package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fatedier/frp/pkg/util/log"

	"tunnelhub/internal/agentcfg"
)

// remoteConfig mirrors the control plane's GET /api/v1/agent/config response.
type remoteConfig struct {
	Revision string         `json:"revision"`
	Tunnels  []remoteTunnel `json:"tunnels"`
}

type remoteTunnel struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	LocalIP       string `json:"local_ip"`
	LocalPort     int    `json:"local_port"`
	RemotePort    int    `json:"remote_port"`
	ProxyProtocol string `json:"proxy_protocol"`
}

func toTunnels(rs []remoteTunnel) []agentcfg.Tunnel {
	out := make([]agentcfg.Tunnel, 0, len(rs))
	for _, r := range rs {
		out = append(out, agentcfg.Tunnel{Name: r.Name, Type: r.Type, LocalIP: r.LocalIP,
			LocalPort: r.LocalPort, RemotePort: r.RemotePort, ProxyProtocol: r.ProxyProtocol})
	}
	return out
}

// synced reports whether tunnels are managed by the control plane. When it
// is, agent.yaml's tunnel list is only a cache for offline starts.
func synced(c *agentcfg.Config) bool {
	return c.Control.URL != "" && c.HasCredentials()
}

// syncLoop long-polls the control plane for this device's tunnels and applies
// every change to the running frp client. It returns when ctx ends, or after
// signalling revoked when the control plane refuses the device token.
func (r *Runner) syncLoop(ctx context.Context, cfg *agentcfg.Config, revoked chan<- struct{}) {
	client := &http.Client{Timeout: 40 * time.Second}
	base := strings.TrimRight(cfg.Control.URL, "/")
	rev := ""
	backoff := time.Second
	for ctx.Err() == nil {
		u := fmt.Sprintf("%s/api/v1/agent/config?rev=%s&wait=25", base, rev)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Device.Token)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warnf("control plane unreachable (%v); retrying in %s", err, backoff)
			sleep(ctx, backoff)
			backoff = min(2*backoff, time.Minute)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			var rc remoteConfig
			if err := json.Unmarshal(body, &rc); err != nil {
				log.Warnf("bad config from control plane: %v", err)
				sleep(ctx, backoff)
				continue
			}
			if err := r.applyRemote(cfg, toTunnels(rc.Tunnels)); err != nil {
				log.Warnf("could not apply tunnels from control plane: %v", err)
			} else {
				log.Infof("tunnels synced from control plane: %d (revision %s)", len(rc.Tunnels), rc.Revision)
				rev = rc.Revision
			}
			backoff = time.Second
		case http.StatusNotModified:
			backoff = time.Second
		case http.StatusUnauthorized:
			log.Errorf("the control plane no longer accepts this device (revoked or removed)")
			select {
			case revoked <- struct{}{}:
			default:
			}
			return
		case http.StatusForbidden:
			log.Warnf("device is disabled by its owner; waiting")
			sleep(ctx, 30*time.Second)
		default:
			log.Warnf("control plane answered HTTP %d; retrying in %s", resp.StatusCode, backoff)
			sleep(ctx, backoff)
			backoff = min(2*backoff, time.Minute)
		}
	}
}

// applyRemote applies a tunnel list to the running client and caches it in
// agent.yaml so the agent can start tunnels while the control plane is down.
// Local targets are re-checked here as defence in depth.
func (r *Runner) applyRemote(cfg *agentcfg.Config, ts []agentcfg.Tunnel) error {
	allowed := make([]agentcfg.Tunnel, 0, len(ts))
	for _, t := range ts {
		if err := agentcfg.CheckLocalTarget(t.LocalIP, cfg.AllowPublicTargets); err != nil {
			log.Warnf("skipping tunnel %s: %v", t.Name, err)
			continue
		}
		allowed = append(allowed, t)
	}
	tmp := *cfg
	tmp.Tunnels = allowed
	if err := r.reloadTunnels(&tmp); err != nil {
		return err
	}
	cfg.Tunnels = allowed
	if err := cfg.Save(r.ConfigPath); err != nil {
		log.Warnf("could not cache tunnels in %s: %v", r.ConfigPath, err)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

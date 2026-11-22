// Package agent runs the embedded frp client from an agentcfg file, keeps it
// connected, and hot-reloads tunnel changes without dropping the session.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"github.com/fatedier/frp/pkg/util/log"

	"tunnelhub/internal/agentcfg"
)

// Runner owns one frp client service built from a config file.
type Runner struct {
	ConfigPath   string
	PollInterval time.Duration // how often to check the config file for changes

	mu      sync.Mutex
	svc     *client.Service
	src     *source.ConfigSource
	common  *v1.ClientCommonConfig
	modTime time.Time
	server  string // serialized server+device section; a change requires a restart
	// synced is set when the control plane owns the tunnel list.
	synced bool
}

// Run blocks until ctx is cancelled. The frp service itself reconnects after
// network failures (loginFailExit=false); this loop additionally restarts the
// service if it ever returns, and restarts it when server/device settings change.
func (r *Runner) Run(ctx context.Context) error {
	if r.PollInterval == 0 {
		r.PollInterval = 5 * time.Second
	}
	cfg, err := agentcfg.Load(r.ConfigPath)
	if err != nil {
		return err
	}

	backoff := time.Second
	revoked := make(chan struct{}, 1)
	r.synced = synced(cfg)
	for {
		svcCtx, cancel := context.WithCancel(ctx)
		if err := r.start(cfg); err != nil {
			cancel()
			return err
		}
		restart := make(chan *agentcfg.Config, 1)
		go r.watch(svcCtx, restart)
		if r.synced {
			go r.syncLoop(svcCtx, cfg, revoked)
		}

		done := make(chan error, 1)
		go func() { done <- r.svc.Run(svcCtx) }()

		select {
		case <-ctx.Done():
			r.stop()
			cancel()
			<-done
			return nil
		case <-revoked:
			// Stay alive but disconnected: a revoked agent must not retry
			// forever. Removing the service (`uninstall`) ends the process.
			r.stop()
			cancel()
			<-done
			log.Errorf("device revoked: tunnels are closed and this agent will not reconnect; run `tunnelhub-agent uninstall`")
			<-ctx.Done()
			return nil
		case newCfg := <-restart:
			log.Infof("server/device settings changed, restarting connection")
			r.stop()
			cancel()
			<-done
			cfg = newCfg
			backoff = time.Second
		case err := <-done:
			cancel()
			log.Warnf("frp service stopped: %v; restarting in %s", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			if reloaded, lerr := agentcfg.Load(r.ConfigPath); lerr == nil {
				cfg = reloaded
			}
		}
	}
}

func (r *Runner) start(cfg *agentcfg.Config) error {
	// No ANSI colours: the agent normally runs under systemd/Windows SCM/Docker.
	log.InitLogger(cfg.Log.To, cfg.Log.Level, 7, true)
	common := cfg.FRPCommon()
	if err := common.Complete(); err != nil {
		return fmt.Errorf("complete frp common config: %w", err)
	}
	proxies := config.CompleteProxyConfigurers(cfg.FRPProxies())
	unsafe := security.NewUnsafeFeatures(nil)
	if _, err := validation.ValidateAllClientConfig(common, proxies, nil, unsafe); err != nil {
		return fmt.Errorf("frp rejected config: %w", err)
	}

	src := source.NewConfigSource()
	if err := src.ReplaceAll(proxies, nil); err != nil {
		return err
	}
	svc, err := client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(src),
		UnsafeFeatures:         unsafe,
	})
	if err != nil {
		return fmt.Errorf("create frp client: %w", err)
	}

	st, _ := os.Stat(r.ConfigPath)
	r.mu.Lock()
	r.svc, r.src, r.common = svc, src, common
	if st != nil {
		r.modTime = st.ModTime()
	}
	r.server = connectionKey(cfg)
	r.mu.Unlock()
	log.Infof("agent started: device=%s relay=%s:%d protocol=%s tunnels=%d",
		cfg.Device.ID, cfg.Server.Addr, cfg.Server.Port, cfg.Server.Protocol, len(proxies))
	return nil
}

func (r *Runner) stop() {
	r.mu.Lock()
	svc := r.svc
	r.mu.Unlock()
	if svc != nil {
		// Graceful close lets QUIC send a close frame instead of timing out.
		svc.GracefulClose(500 * time.Millisecond)
	}
}

// watch polls the config file. Tunnel-only changes are applied live; changes to
// server or device settings are sent on restart.
func (r *Runner) watch(ctx context.Context, restart chan<- *agentcfg.Config) {
	t := time.NewTicker(r.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := os.Stat(r.ConfigPath)
		if err != nil {
			continue
		}
		r.mu.Lock()
		changed := !st.ModTime().Equal(r.modTime)
		r.mu.Unlock()
		if !changed {
			continue
		}
		cfg, err := agentcfg.Load(r.ConfigPath)
		if err != nil {
			log.Warnf("config changed but is invalid, keeping current tunnels: %v", err)
			r.mu.Lock()
			r.modTime = st.ModTime() // don't spam the same error
			r.mu.Unlock()
			continue
		}
		if connectionKey(cfg) != r.currentConnectionKey() {
			restart <- cfg
			return
		}
		if r.synced {
			// Tunnels come from the control plane; local edits to the file are ignored.
			r.mu.Lock()
			r.modTime = st.ModTime()
			r.mu.Unlock()
			continue
		}
		if err := r.reloadTunnels(cfg); err != nil {
			log.Warnf("tunnel reload failed: %v", err)
		} else {
			log.Infof("tunnels reloaded (%d enabled)", len(cfg.FRPProxies()))
		}
		r.mu.Lock()
		r.modTime = st.ModTime()
		r.mu.Unlock()
	}
}

func (r *Runner) reloadTunnels(cfg *agentcfg.Config) error {
	proxies := config.CompleteProxyConfigurers(cfg.FRPProxies())
	r.mu.Lock()
	svc, common := r.svc, r.common
	r.mu.Unlock()
	if svc == nil {
		return errors.New("service not running")
	}
	if _, err := validation.ValidateAllClientConfig(common, proxies, nil, security.NewUnsafeFeatures(nil)); err != nil {
		return err
	}
	return svc.UpdateConfigSource(common, proxies, nil)
}

func (r *Runner) currentConnectionKey() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.server
}

func connectionKey(c *agentcfg.Config) string {
	return fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		c.Server.Addr, c.Server.Port, c.Server.Protocol, c.Server.Token,
		c.Server.CAFile, c.Server.ServerName, c.Device.ID, c.Device.Token, c.Log.Level, c.Log.To)
}

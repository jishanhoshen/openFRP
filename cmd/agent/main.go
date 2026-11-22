// Command tunnelhub-agent runs on a user's home computer, keeps an outbound
// connection to the TunnelHub relay, and forwards public ports to local services.
//
//	tunnelhub-agent pair      -api URL [-config PATH]  link this computer to your account
//	tunnelhub-agent run       [-config PATH] [-api URL] run in the foreground (pairs first if needed)
//	tunnelhub-agent validate  [-config PATH]            check a config file and exit
//	tunnelhub-agent install   [-config PATH] [-api URL] install as a system service (start at boot)
//	tunnelhub-agent uninstall | start | stop | restart | status
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/kardianos/service"

	"tunnelhub/internal/agent"
	"tunnelhub/internal/agentcfg"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "TunnelHub", "agent.yaml")
	}
	return "/etc/tunnelhub/agent.yaml"
}

// program adapts the Runner to kardianos/service (Windows SCM, systemd, launchd).
type program struct {
	configPath string
	apiURL     string
	cancel     context.CancelFunc
	done       chan struct{}
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := ensurePaired(ctx, p.configPath, p.apiURL); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintln(os.Stderr, "agent error:", err)
			os.Exit(1)
		}
		r := &agent.Runner{ConfigPath: p.configPath}
		if err := r.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "agent error:", err)
			os.Exit(1) // let the service manager restart us
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to agent.yaml")
	apiURL := fs.String("api", "", "control-plane URL used for pairing, e.g. https://tunnels.example.com")
	_ = fs.Parse(os.Args[2:])

	abs, err := filepath.Abs(*configPath)
	if err == nil {
		*configPath = abs
	}

	svcCfg := &service.Config{
		Name:        "tunnelhub-agent",
		DisplayName: "TunnelHub Agent",
		Description: "Forwards public relay ports to local game servers and applications.",
		Arguments:   serviceArgs(*configPath, *apiURL),
		Option: service.KeyValue{
			"Restart":                "always",  // systemd
			"OnFailure":              "restart", // windows
			"OnFailureDelayDuration": "10s",     // windows
		},
	}
	prg := &program{configPath: *configPath, apiURL: *apiURL}
	svc, err := service.New(prg, svcCfg)
	if err != nil {
		fatal(err)
	}

	switch cmd {
	case "version":
		fmt.Println("tunnelhub-agent", version)
	case "validate":
		c, err := agentcfg.Load(*configPath)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("OK: device=%s relay=%s:%d (%s), %d enabled tunnel(s)\n",
			c.Device.ID, c.Server.Addr, c.Server.Port, c.Server.Protocol, len(c.FRPProxies()))
	case "pair":
		if *apiURL == "" {
			fatal(fmt.Errorf("-api is required, e.g. -api https://tunnels.example.com"))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := agent.Pair(ctx, *apiURL, *configPath, version, os.Stdout); err != nil {
			fatal(err)
		}
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := ensurePaired(ctx, *configPath, *apiURL); err != nil {
			fatal(err)
		}
		if _, err := agentcfg.Load(*configPath); err != nil {
			fatal(err)
		}
		r := &agent.Runner{ConfigPath: *configPath}
		if err := r.Run(ctx); err != nil {
			fatal(err)
		}
	case "service": // entry point used by the service manager
		if err := svc.Run(); err != nil {
			fatal(err)
		}
	case "install":
		raw, err := agentcfg.LoadRaw(*configPath)
		if err != nil {
			fatal(err)
		}
		if raw.HasCredentials() {
			if _, err := agentcfg.Load(*configPath); err != nil {
				fatal(fmt.Errorf("refusing to install with an invalid config: %w", err))
			}
		} else if *apiURL == "" && raw.Control.URL == "" {
			fatal(fmt.Errorf("device is not paired: pass -api URL (the service will show a pairing code) or run `pair` first"))
		}
		if err := service.Control(svc, "install"); err != nil {
			fatal(err)
		}
		fmt.Println("installed; start it with: tunnelhub-agent start")
	case "uninstall", "start", "stop", "restart":
		if err := service.Control(svc, cmd); err != nil {
			fatal(err)
		}
		fmt.Println(cmd, "ok")
	case "status":
		st, err := svc.Status()
		if err != nil {
			fatal(err)
		}
		switch st {
		case service.StatusRunning:
			fmt.Println("running")
		case service.StatusStopped:
			fmt.Println("stopped")
		default:
			fmt.Println("unknown")
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tunnelhub-agent <pair|run|validate|install|uninstall|start|stop|restart|status|version> [-config PATH] [-api URL]")
}

func serviceArgs(configPath, apiURL string) []string {
	args := []string{"service", "-config", configPath}
	if apiURL != "" {
		args = append(args, "-api", apiURL)
	}
	return args
}

// ensurePaired pairs the device first if the config has no credentials yet.
func ensurePaired(ctx context.Context, configPath, apiURL string) error {
	raw, err := agentcfg.LoadRaw(configPath)
	if err != nil {
		return err
	}
	if raw.HasCredentials() {
		return nil
	}
	if apiURL == "" {
		apiURL = raw.Control.URL
	}
	if apiURL == "" {
		return fmt.Errorf("this device is not paired: run `tunnelhub-agent pair -api URL` or pass -api")
	}
	return agent.Pair(ctx, apiURL, configPath, version, os.Stdout)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// Package agentcfg defines the TunnelHub agent configuration file, validates it,
// and converts it into frp client configuration objects.
//
// Security note: frp never sends a proxy's local target (localIP/localPort) to
// the server, so the server cannot police it. Restricting local targets to
// loopback/private addresses is therefore enforced here, in the agent.
package agentcfg

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	v1 "github.com/fatedier/frp/pkg/config/v1"
	"gopkg.in/yaml.v3"
)

// Config is the on-disk agent configuration (YAML).
type Config struct {
	// Control is the TunnelHub control-plane API (used for pairing; later
	// phases also sync tunnel configuration from it).
	Control ControlConfig `yaml:"control,omitempty"`
	Server  ServerConfig  `yaml:"server"`
	Device  DeviceConfig  `yaml:"device"`
	Log     LogConfig     `yaml:"log"`
	Tunnels []Tunnel      `yaml:"tunnels"`

	// AllowPublicTargets permits tunnels whose localIP is a public address.
	// Off by default so a device cannot turn the relay into an open proxy
	// towards third-party internet hosts.
	AllowPublicTargets bool `yaml:"allowPublicTargets,omitempty"`
}

type ControlConfig struct {
	URL string `yaml:"url,omitempty"` // e.g. https://tunnels.example.com
}

type ServerConfig struct {
	Addr string `yaml:"addr"` // relay hostname or IP
	Port int    `yaml:"port"` // frps bindPort / quicBindPort (default 7000)
	// Protocol between agent and relay: "quic" (default, best for UDP games)
	// or "tcp" (fallback for networks that block outbound UDP).
	Protocol string `yaml:"protocol"`
	// Token is the relay-wide frp auth token (defence in depth; the per-device
	// token below is the real credential).
	Token string `yaml:"token"`
	// TLS: path to the relay CA certificate. When set, the relay certificate
	// is verified (strongly recommended). ServerName must match the cert.
	CAFile     string `yaml:"caFile,omitempty"`
	ServerName string `yaml:"serverName,omitempty"`
}

type DeviceConfig struct {
	ID    string `yaml:"id"`    // device ID issued by the control plane
	Token string `yaml:"token"` // per-device secret issued at pairing
}

type LogConfig struct {
	Level string `yaml:"level"` // trace|debug|info|warn|error (default info)
	To    string `yaml:"to"`    // "console" (default) or a file path
}

type Tunnel struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"` // tcp | udp
	LocalIP    string `yaml:"localIP"`
	LocalPort  int    `yaml:"localPort"`
	RemotePort int    `yaml:"remotePort"`
	Enabled    *bool  `yaml:"enabled,omitempty"`
	// ProxyProtocol: "" (off), "v1" or "v2". TCP only. Lets supporting game
	// servers (e.g. Velocity/Paper) see real player IPs.
	ProxyProtocol string `yaml:"proxyProtocol,omitempty"`
}

func (t Tunnel) IsEnabled() bool { return t.Enabled == nil || *t.Enabled }

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)
)

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// LoadRaw reads a config without defaults or validation (used when pairing
// fills in credentials). A missing file yields an empty config.
func LoadRaw(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	} else if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	return &c, nil
}

// HasCredentials reports whether the config contains a paired device.
func (c *Config) HasCredentials() bool { return c.Device.ID != "" && c.Device.Token != "" }

// Save validates the config and writes it atomically with mode 0600.
func (c *Config) Save(path string) error {
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	header := "# Managed by tunnelhub-agent. Tunnel entries may be edited; they reload live.\n"
	if _, err := tmp.WriteString(header + string(b)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Parse decodes YAML strictly (unknown keys are errors), applies defaults and validates.
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 7000
	}
	if c.Server.Protocol == "" {
		c.Server.Protocol = "quic"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.To == "" {
		c.Log.To = "console"
	}
	for i := range c.Tunnels {
		if c.Tunnels[i].LocalIP == "" {
			c.Tunnels[i].LocalIP = "127.0.0.1"
		}
	}
}

// Validate checks the whole config and returns all problems at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Server.Addr == "" {
		add("server.addr is required")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		add("server.port %d out of range", c.Server.Port)
	}
	if c.Server.Protocol != "quic" && c.Server.Protocol != "tcp" {
		add("server.protocol must be \"quic\" or \"tcp\", got %q", c.Server.Protocol)
	}
	if c.Server.CAFile != "" && c.Server.ServerName == "" {
		add("server.serverName is required when server.caFile is set")
	}
	if !idRe.MatchString(c.Device.ID) {
		add("device.id must match %s", idRe)
	}
	if len(c.Device.Token) < 32 {
		add("device.token missing or too short (need >= 32 chars)")
	}

	names := map[string]bool{}
	remotes := map[string]bool{}
	for i, t := range c.Tunnels {
		p := fmt.Sprintf("tunnels[%d] (%s)", i, t.Name)
		if !nameRe.MatchString(t.Name) {
			add("%s: name must match %s", p, nameRe)
		}
		if names[t.Name] {
			add("%s: duplicate tunnel name", p)
		}
		names[t.Name] = true

		if t.Type != "tcp" && t.Type != "udp" {
			add("%s: type must be tcp or udp", p)
		}
		if t.LocalPort < 1 || t.LocalPort > 65535 {
			add("%s: localPort %d out of range", p, t.LocalPort)
		}
		if t.RemotePort < 1 || t.RemotePort > 65535 {
			add("%s: remotePort %d out of range", p, t.RemotePort)
		}
		key := fmt.Sprintf("%s/%d", t.Type, t.RemotePort)
		if t.IsEnabled() && remotes[key] {
			add("%s: remote %s used by more than one enabled tunnel", p, key)
		}
		remotes[key] = true

		switch t.ProxyProtocol {
		case "":
		case "v1", "v2":
			if t.Type != "tcp" {
				add("%s: proxyProtocol is only supported for tcp tunnels", p)
			}
		default:
			add("%s: proxyProtocol must be empty, v1 or v2", p)
		}

		if err := CheckLocalTarget(t.LocalIP, c.AllowPublicTargets); err != nil {
			add("%s: %v", p, err)
		}
	}
	return errors.Join(errs...)
}

// CheckLocalTarget enforces that local targets are literal IPs on loopback,
// private (RFC1918 / ULA) or link-local ranges unless allowPublic is set.
// Hostnames are rejected so DNS cannot be used to point at public hosts.
func CheckLocalTarget(ip string, allowPublic bool) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("localIP %q must be a literal IP address", ip)
	}
	if addr.IsUnspecified() || addr.IsMulticast() {
		return fmt.Errorf("localIP %s is not a valid target", ip)
	}
	if allowPublic {
		return nil
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return nil
	}
	return fmt.Errorf("localIP %s is a public address; only loopback/private targets are allowed", ip)
}

// Heartbeat settings (seconds). The relay's transport.heartbeatTimeout must be
// >= HeartbeatInterval and is set to 60 in deploy/frps/frps.toml.
const (
	HeartbeatInterval = 15
	HeartbeatTimeout  = 60
)

// FRPCommon builds the frp client common config.
func (c *Config) FRPCommon() *v1.ClientCommonConfig {
	f := false
	cc := &v1.ClientCommonConfig{}
	cc.ServerAddr = c.Server.Addr
	cc.ServerPort = c.Server.Port
	// The device ID doubles as the frp "user": frps prefixes every proxy
	// name with it, which namespaces tunnels per device.
	cc.User = c.Device.ID
	cc.ClientID = c.Device.ID
	cc.LoginFailExit = &f // keep retrying forever (reconnect after outages)
	cc.Auth.Method = v1.AuthMethodToken
	cc.Auth.Token = c.Server.Token
	cc.Metadatas = map[string]string{
		"device_id":    c.Device.ID,
		"device_token": c.Device.Token,
	}
	cc.Transport.Protocol = c.Server.Protocol
	// frp disables application heartbeats when stream multiplexing is on, but
	// the relay requires them: every heartbeat is re-authorised by the control
	// plane, which is how revocation reaches connected agents.
	cc.Transport.HeartbeatInterval = HeartbeatInterval
	cc.Transport.HeartbeatTimeout = HeartbeatTimeout
	if c.Server.CAFile != "" {
		cc.Transport.TLS.TrustedCaFile = c.Server.CAFile
		cc.Transport.TLS.ServerName = c.Server.ServerName
	}
	cc.Log.Level = c.Log.Level
	cc.Log.To = c.Log.To
	cc.Log.MaxDays = 7
	return cc
}

// FRPProxies converts enabled tunnels into frp proxy configurers.
func (c *Config) FRPProxies() []v1.ProxyConfigurer {
	out := make([]v1.ProxyConfigurer, 0, len(c.Tunnels))
	for _, t := range c.Tunnels {
		if !t.IsEnabled() {
			continue
		}
		base := v1.ProxyBaseConfig{Name: t.Name, Type: t.Type}
		base.LocalIP = t.LocalIP
		base.LocalPort = t.LocalPort
		switch t.Type {
		case "tcp":
			base.Transport.ProxyProtocolVersion = t.ProxyProtocol
			out = append(out, &v1.TCPProxyConfig{ProxyBaseConfig: base, RemotePort: t.RemotePort})
		case "udp":
			out = append(out, &v1.UDPProxyConfig{ProxyBaseConfig: base, RemotePort: t.RemotePort})
		}
	}
	return out
}

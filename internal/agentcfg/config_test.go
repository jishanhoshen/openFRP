package agentcfg

import (
	"os"
	"strings"
	"testing"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

const validYAML = `
server:
  addr: relay.example.com
  token: relay-shared-token
device:
  id: dev-alice-pc
  token: 0123456789abcdef0123456789abcdef
tunnels:
  - name: minecraft
    type: tcp
    localPort: 25565
    remotePort: 20001
    proxyProtocol: v2
  - name: bedrock
    type: udp
    localPort: 19132
    remotePort: 20001
  - name: igi
    type: udp
    localIP: 192.168.1.50
    localPort: 26001
    remotePort: 20002
    enabled: false
`

func TestParseValidAppliesDefaults(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Server.Port != 7000 || c.Server.Protocol != "quic" {
		t.Errorf("defaults not applied: port=%d protocol=%s", c.Server.Port, c.Server.Protocol)
	}
	if c.Tunnels[0].LocalIP != "127.0.0.1" {
		t.Errorf("localIP default = %q", c.Tunnels[0].LocalIP)
	}
	// Same remote port number on tcp and udp is allowed.
	proxies := c.FRPProxies()
	if len(proxies) != 2 {
		t.Fatalf("want 2 enabled proxies, got %d", len(proxies))
	}
	tcp, ok := proxies[0].(*v1.TCPProxyConfig)
	if !ok || tcp.RemotePort != 20001 || tcp.Transport.ProxyProtocolVersion != "v2" {
		t.Errorf("bad tcp proxy: %#v", proxies[0])
	}
	if _, ok := proxies[1].(*v1.UDPProxyConfig); !ok {
		t.Errorf("second proxy should be udp")
	}
}

func TestFRPCommonCarriesDeviceIdentity(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	cc := c.FRPCommon()
	if cc.User != "dev-alice-pc" || cc.Metadatas["device_token"] != c.Device.Token {
		t.Errorf("device identity not propagated: user=%q metas=%v", cc.User, cc.Metadatas)
	}
	if cc.LoginFailExit == nil || *cc.LoginFailExit {
		t.Errorf("loginFailExit must be false so the agent keeps reconnecting")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ replace, with, wantErr string }{
		"unknown key":          {"  addr: relay.example.com", "  addr: relay.example.com\n  bogus: 1", "bogus"},
		"public target":        {"localIP: 192.168.1.50", "localIP: 8.8.8.8", "public address"},
		"hostname target":      {"localIP: 192.168.1.50", "localIP: example.com", "literal IP"},
		"bad type":             {"type: udp\n    localPort: 19132", "type: http\n    localPort: 19132", "type must be tcp or udp"},
		"short device token":   {"token: 0123456789abcdef0123456789abcdef", "token: short", "device.token"},
		"proxy protocol udp":   {"localPort: 19132\n", "localPort: 19132\n    proxyProtocol: v2\n", "only supported for tcp"},
		"bad protocol":         {"  addr: relay.example.com", "  addr: relay.example.com\n  protocol: kcp", "server.protocol"},
		"duplicate remote tcp": {"    enabled: false\n", "    enabled: false\n  - name: dup\n    type: tcp\n    localPort: 80\n    remotePort: 20001\n", "used by more than one"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(validYAML, tc.replace, tc.with, 1)
			if in == validYAML {
				t.Fatalf("test setup: replacement %q not found", tc.replace)
			}
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCheckLocalTarget(t *testing.T) {
	ok := []string{"127.0.0.1", "::1", "10.0.0.5", "172.16.3.4", "192.168.0.10", "fd00::1", "169.254.1.1"}
	for _, ip := range ok {
		if err := CheckLocalTarget(ip, false); err != nil {
			t.Errorf("%s should be allowed: %v", ip, err)
		}
	}
	bad := []string{"1.1.1.1", "0.0.0.0", "224.0.0.1", "2001:4860::8888", "localhost"}
	for _, ip := range bad {
		if err := CheckLocalTarget(ip, false); err == nil {
			t.Errorf("%s should be rejected", ip)
		}
	}
	if err := CheckLocalTarget("1.1.1.1", true); err != nil {
		t.Errorf("public target should pass with allowPublic: %v", err)
	}
}

func TestHeartbeatsAlwaysEnabled(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	cc := c.FRPCommon()
	if err := cc.Complete(); err != nil {
		t.Fatal(err)
	}
	// frp would default these to -1 (disabled) with tcpMux on; the relay
	// needs heartbeats to re-authorise devices.
	if cc.Transport.HeartbeatInterval != HeartbeatInterval || cc.Transport.HeartbeatTimeout != HeartbeatTimeout {
		t.Fatalf("heartbeat interval/timeout = %d/%d", cc.Transport.HeartbeatInterval, cc.Transport.HeartbeatTimeout)
	}
}

func TestSaveAndLoadRawRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sub/agent.yaml"

	raw, err := LoadRaw(path) // missing file -> empty config
	if err != nil || raw.HasCredentials() {
		t.Fatalf("LoadRaw(missing) = %+v, %v", raw, err)
	}
	c, _ := Parse([]byte(validYAML))
	c.Control.URL = "https://tunnels.example.com"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved file mode = %v, err %v", st.Mode().Perm(), err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Control.URL != c.Control.URL || back.Device != c.Device || len(back.Tunnels) != 3 || back.Tunnels[2].IsEnabled() {
		t.Fatalf("round trip mismatch: %+v", back)
	}

	bad := *c
	bad.Device.Token = "short"
	if err := bad.Save(path); err == nil {
		t.Fatal("Save must refuse an invalid config")
	}
}

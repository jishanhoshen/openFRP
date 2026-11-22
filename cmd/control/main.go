// Command tunnelhub-control is the control plane: management API + frps plugin.
//
//	tunnelhub-control serve        run API (public, behind a reverse proxy) and plugin (loopback)
//	tunnelhub-control migrate      apply database migrations and exit
//	tunnelhub-control create-user  -email EMAIL [-admin]   (password read from stdin)
//
// Every flag can also be set via an environment variable (shown in -help).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"tunnelhub/internal/api"
	"tunnelhub/internal/auth"
	"tunnelhub/internal/db"
	"tunnelhub/internal/plugin"
	"tunnelhub/internal/stats"
	"tunnelhub/internal/store"
	"tunnelhub/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:], logger)
	case "migrate":
		err = migrate(os.Args[2:], logger)
	case "create-user":
		err = createUser(os.Args[2:])
	case "version":
		fmt.Println("tunnelhub-control", version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tunnelhub-control <serve|migrate|create-user|version> [flags]")
}

// env-backed flag helpers -----------------------------------------------------

func envStr(fs *flag.FlagSet, name, env, def, help string) *string {
	if v, ok := os.LookupEnv(env); ok {
		def = v
	}
	return fs.String(name, def, help+" [$"+env+"]")
}

func envInt(fs *flag.FlagSet, name, env string, def int, help string) *int {
	if v, ok := os.LookupEnv(env); ok {
		if n, err := strconv.Atoi(v); err == nil {
			def = n
		}
	}
	return fs.Int(name, def, help+" [$"+env+"]")
}

func envBool(fs *flag.FlagSet, name, env string, def bool, help string) *bool {
	if v, ok := os.LookupEnv(env); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			def = b
		}
	}
	return fs.Bool(name, def, help+" [$"+env+"]")
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

type dbFlags struct{ url, passwordFile *string }

func addDBFlags(fs *flag.FlagSet) dbFlags {
	return dbFlags{
		url:          envStr(fs, "database-url", "TH_DATABASE_URL", "", "PostgreSQL URL (without password if -database-password-file is used)"),
		passwordFile: envStr(fs, "database-password-file", "TH_DATABASE_PASSWORD_FILE", "", "file containing the database password"),
	}
}

func (f dbFlags) open(ctx context.Context) (*store.Store, func(), error) {
	if *f.url == "" {
		return nil, nil, errors.New("-database-url is required")
	}
	pw, err := readSecret(*f.passwordFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read database password: %w", err)
	}
	var pool interface{ Close() }
	var lastErr error
	// The database may still be starting (compose); retry for ~60s.
	for i := 0; i < 30; i++ {
		p, err := db.Open(ctx, db.Options{URL: *f.url, Password: pw})
		if err == nil {
			pool = p
			if _, err := db.Migrate(ctx, p); err != nil {
				p.Close()
				return nil, nil, err
			}
			return store.New(p), pool.Close, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, nil, lastErr
}

// serve -----------------------------------------------------------------------

func serve(args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbf := addDBFlags(fs)
	apiListen := envStr(fs, "api-listen", "TH_API_LISTEN", "0.0.0.0:8080", "management API listen address")
	pluginListen := envStr(fs, "plugin-listen", "TH_PLUGIN_LISTEN", "127.0.0.1:8081", "frps plugin listen address (never expose publicly)")
	pool := envStr(fs, "pool", "TH_PORT_POOL", "20000-29999", "public port pool, inclusive")
	openReg := envBool(fs, "open-registration", "TH_OPEN_REGISTRATION", false, "allow anyone to register")
	publicURL := envStr(fs, "public-url", "TH_PUBLIC_URL", "http://localhost:8080", "public base URL of the dashboard/API")
	secureCookies := envBool(fs, "secure-cookies", "TH_SECURE_COOKIES", true, "mark session cookies Secure (disable only for plain-HTTP testing)")
	trusted := envStr(fs, "trusted-proxies", "TH_TRUSTED_PROXIES", "", "comma-separated CIDRs of reverse proxies allowed to set X-Forwarded-For")
	maxDevices := envInt(fs, "max-devices-per-user", "TH_MAX_DEVICES_PER_USER", 10, "device quota per user")
	maxTunnels := envInt(fs, "max-tunnels-per-device", "TH_MAX_TUNNELS_PER_DEVICE", 20, "tunnel quota per device (keep <= frps maxPortsPerClient)")
	relayAddr := envStr(fs, "relay-addr", "TH_RELAY_ADDR", "", "relay hostname/IP given to agents")
	relayPort := envInt(fs, "relay-port", "TH_RELAY_PORT", 7000, "relay frps port")
	relayProto := envStr(fs, "relay-protocol", "TH_RELAY_PROTOCOL", "quic", "agent transport: quic or tcp")
	relayTokenFile := envStr(fs, "relay-token-file", "TH_RELAY_TOKEN_FILE", "", "file containing the frps auth token")
	relayCAFile := envStr(fs, "relay-ca-file", "TH_RELAY_CA_FILE", "", "relay CA certificate (PEM) agents should pin")
	relayServerName := envStr(fs, "relay-server-name", "TH_RELAY_SERVER_NAME", "", "TLS server name in the relay certificate")
	publicHost := envStr(fs, "public-host", "TH_PUBLIC_HOST", "", "hostname players connect to (default: relay-addr)")
	extraPorts := envStr(fs, "extra-ports", "TH_EXTRA_PORTS", "", "comma-separated ports outside the pool that the relay also allows (admins/reservations only)")
	cooldown := envStr(fs, "port-cooldown", "TH_PORT_COOLDOWN", "24h", "how long a released port is kept from other users")
	auditRetention := envStr(fs, "audit-retention", "TH_AUDIT_RETENTION", "8760h", "audit log retention (0 = forever)")
	trafficRetention := envStr(fs, "traffic-retention", "TH_TRAFFIC_RETENTION", "8760h", "daily traffic retention (0 = forever)")
	frpsAdminURL := envStr(fs, "frps-admin-url", "TH_FRPS_ADMIN_URL", "", "frps dashboard API base URL for live status and traffic (empty = disabled)")
	frpsAdminUser := envStr(fs, "frps-admin-user", "TH_FRPS_ADMIN_USER", "admin", "frps dashboard user")
	frpsAdminPasswordFile := envStr(fs, "frps-admin-password-file", "TH_FRPS_ADMIN_PASSWORD_FILE", "", "file containing the frps dashboard password")
	statsInterval := envStr(fs, "stats-interval", "TH_STATS_INTERVAL", "30s", "how often frps statistics are polled")
	_ = fs.Parse(args)

	durs := map[string]string{"port-cooldown": *cooldown, "audit-retention": *auditRetention,
		"traffic-retention": *trafficRetention, "stats-interval": *statsInterval}
	parsed := map[string]time.Duration{}
	for name, v := range durs {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return fmt.Errorf("invalid -%s %q", name, v)
		}
		parsed[name] = d
	}
	extra := map[int]bool{}
	for _, c := range strings.Split(*extraPorts, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid -extra-ports entry %q", c)
		}
		extra[n] = true
	}

	var pr store.PortPool
	if _, err := fmt.Sscanf(*pool, "%d-%d", &pr.Start, &pr.End); err != nil || pr.Start < 1 || pr.End > 65535 || pr.Start > pr.End {
		return fmt.Errorf("invalid -pool %q", *pool)
	}
	var proxies []netip.Prefix
	for _, c := range strings.Split(*trusted, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("invalid -trusted-proxies entry %q: %w", c, err)
		}
		proxies = append(proxies, p)
	}
	if *relayAddr == "" {
		return errors.New("-relay-addr is required (agents need to know where to connect)")
	}
	relayToken, err := readSecret(*relayTokenFile)
	if err != nil {
		return fmt.Errorf("read relay token: %w", err)
	}
	caPEM, err := readSecret(*relayCAFile)
	if err != nil {
		return fmt.Errorf("read relay CA: %w", err)
	}
	if caPEM != "" && *relayServerName == "" {
		return errors.New("-relay-server-name is required when -relay-ca-file is set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, closeDB, err := dbf.open(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	var coll *stats.Collector
	var live api.LiveSource
	if *frpsAdminURL != "" {
		pw, err := readSecret(*frpsAdminPasswordFile)
		if err != nil {
			return fmt.Errorf("read frps admin password: %w", err)
		}
		if pw == "" {
			pw = os.Getenv("TH_FRPS_ADMIN_PASSWORD")
		}
		if pw == "" {
			return errors.New("frps admin password required (-frps-admin-password-file or TH_FRPS_ADMIN_PASSWORD) when -frps-admin-url is set")
		}
		coll = &stats.Collector{URL: *frpsAdminURL, User: *frpsAdminUser, Password: pw,
			Interval: parsed["stats-interval"], Sink: st, Log: logger}
		live = coll
	}
	host := *publicHost
	if host == "" {
		host = *relayAddr
	}

	apiSrv := api.New(st, api.Config{
		OpenRegistration:    *openReg,
		SecureCookies:       *secureCookies,
		PublicURL:           *publicURL,
		Pool:                pr,
		ExtraPorts:          extra,
		PortCooldown:        parsed["port-cooldown"],
		MaxDevicesPerUser:   *maxDevices,
		MaxTunnelsPerDevice: *maxTunnels,
		TrustedProxies:      proxies,
		PublicHost:          host,
		Live:                live,
		AuditRetention:      parsed["audit-retention"],
		TrafficRetention:    parsed["traffic-retention"],
		Version:             version,
		Dashboard:           web.Handler(),
		Relay: api.RelayInfo{
			Addr: *relayAddr, Port: *relayPort, Protocol: *relayProto,
			Token: relayToken, ServerName: *relayServerName, CAPEM: caPEM,
		},
	}, logger)

	pluginMux := http.NewServeMux()
	pluginMux.Handle("POST /frps/plugin", &plugin.Handler{Store: st, Log: logger})
	pluginMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	servers := []*http.Server{
		{Addr: *apiListen, Handler: apiSrv.Handler(), ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second},
		{Addr: *pluginListen, Handler: pluginMux, ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second},
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, srv := range servers {
		g.Go(func() error {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("listen %s: %w", srv.Addr, err)
			}
			return nil
		})
	}
	g.Go(func() error { apiSrv.RunCleanup(gctx, 10*time.Minute); return nil })
	if coll != nil {
		g.Go(func() error { coll.Run(gctx); return nil })
	}
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(shutdownCtx)
		}
		return nil
	})
	logger.Info("control plane started", "version", version, "api", *apiListen, "plugin", *pluginListen,
		"pool", *pool, "open_registration", *openReg, "relay", fmt.Sprintf("%s:%d/%s", *relayAddr, *relayPort, *relayProto))
	return g.Wait()
}

// migrate ---------------------------------------------------------------------

func migrate(args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbf := addDBFlags(fs)
	_ = fs.Parse(args)
	_, closeDB, err := dbf.open(context.Background()) // open() applies migrations
	if err != nil {
		return err
	}
	closeDB()
	logger.Info("migrations up to date")
	return nil
}

// create-user -----------------------------------------------------------------

func createUser(args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	dbf := addDBFlags(fs)
	email := fs.String("email", "", "user email")
	admin := fs.Bool("admin", false, "grant the admin role")
	_ = fs.Parse(args)
	if *email == "" {
		return errors.New("-email is required")
	}
	fmt.Fprintln(os.Stderr, "password (read from stdin, min 10 chars):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("read password: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if len([]rune(pw)) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, closeDB, err := dbf.open(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	role := "user"
	if *admin {
		role = "admin"
	}
	u, err := st.CreateUser(ctx, *email, hash, role)
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("a user with email %s already exists", *email)
	} else if err != nil {
		return err
	}
	_ = st.Audit(ctx, store.AuditEntry{ActorUserID: &u.ID, Action: "user.create_cli", Target: u.Email,
		Details: map[string]any{"role": role}})
	fmt.Printf("created user id=%d email=%s role=%s\n", u.ID, u.Email, u.Role)
	return nil
}

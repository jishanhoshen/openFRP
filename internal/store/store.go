// Package store is the PostgreSQL data layer for the control plane. It also
// implements plugin.Store, which the frps plugin uses to authorise agents.
package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tunnelhub/internal/plugin"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrQuota         = errors.New("quota exceeded")
	ErrPoolExhausted = errors.New("no free port in pool")
	ErrExpired       = errors.New("expired")
)

type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

func uniqueViolation(err error) (constraint string, ok bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return pg.ConstraintName, true
	}
	return "", false
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- users

type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	// Quota overrides; nil = server default.
	MaxDevices          *int `json:"max_devices"`
	MaxTunnelsPerDevice *int `json:"max_tunnels_per_device"`
}

func (u User) IsAdmin() bool { return u.Role == "admin" }

const userCols = `id, email, password_hash, role, disabled, created_at, max_devices, max_tunnels_per_device`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt,
		&u.MaxDevices, &u.MaxTunnelsPerDevice)
	return u, notFound(err)
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash, role string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, $2, $3) RETURNING `+userCols,
		email, passwordHash, role))
	if _, ok := uniqueViolation(err); ok {
		return User{}, ErrConflict
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) { return scanUser(r) })
}

// UserPatch changes selected user fields. For the quota fields a negative
// value resets the override to the server default.
type UserPatch struct {
	Role                *string
	Disabled            *bool
	MaxDevices          *int
	MaxTunnelsPerDevice *int
}

// UpdateUser applies a patch. Disabling a user also ends all of their
// sessions; their devices stop authenticating at the next heartbeat.
func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) (User, error) {
	var u User
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET
			role = COALESCE($2, role), disabled = COALESCE($3, disabled),
			max_devices = CASE WHEN $4::int IS NULL THEN max_devices WHEN $4 < 0 THEN NULL ELSE $4 END,
			max_tunnels_per_device = CASE WHEN $5::int IS NULL THEN max_tunnels_per_device WHEN $5 < 0 THEN NULL ELSE $5 END,
			updated_at = now()
			WHERE id = $1 RETURNING `+userCols, id, p.Role, p.Disabled, p.MaxDevices, p.MaxTunnelsPerDevice))
		if err != nil {
			return err
		}
		if u.Disabled {
			_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
		}
		return err
	})
	return u, err
}

func (s *Store) SetPassword(ctx context.Context, userID int64, hash string, keepSession []byte) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash); err != nil {
			return err
		}
		// Log out every other session.
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keepSession)
		return err
	})
}

// ---------------------------------------------------------------- sessions

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, ttl time.Duration, ip, userAgent string) error {
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at, ip, user_agent)
		VALUES ($1, $2, now() + make_interval(secs => $3), $4, $5)`, tokenHash, userID, ttl.Seconds(), ip, userAgent)
	return err
}

// SessionUser resolves a session to its (enabled) user.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx, `SELECT u.id, u.email, u.password_hash, u.role, u.disabled, u.created_at, u.max_devices, u.max_tunnels_per_device
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND NOT u.disabled`, tokenHash))
	if err != nil {
		return u, err
	}
	// Cheap activity tracking: write at most every 5 minutes per session.
	_, _ = s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = now()
		WHERE token_hash = $1 AND last_seen_at < now() - interval '5 minutes'`, tokenHash)
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// ---------------------------------------------------------------- API tokens

type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string, hash []byte, prefix string, expiresAt *time.Time) (APIToken, error) {
	var t APIToken
	err := s.db.QueryRow(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, name, prefix, created_at, last_used_at, expires_at`,
		userID, name, hash, prefix, expiresAt).Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	return t, err
}

func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, prefix, created_at, last_used_at, expires_at
		FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIToken, error) {
		var t APIToken
		err := r.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
		return t, err
	})
}

func (s *Store) RevokeAPIToken(ctx context.Context, userID, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_tokens SET revoked_at = now()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) APITokenUser(ctx context.Context, hash []byte) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx, `SELECT u.id, u.email, u.password_hash, u.role, u.disabled, u.created_at, u.max_devices, u.max_tunnels_per_device
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL
		  AND (t.expires_at IS NULL OR t.expires_at > now()) AND NOT u.disabled`, hash))
	if err != nil {
		return u, err
	}
	_, _ = s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, hash)
	return u, nil
}

// ---------------------------------------------------------------- devices

type Device struct {
	ID           string     `json:"id"`
	UserID       int64      `json:"user_id"`
	Name         string     `json:"name"`
	Disabled     bool       `json:"disabled"`
	Revoked      bool       `json:"revoked"`
	Paired       bool       `json:"paired"` // agent has collected its token
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastIP       string     `json:"last_ip"`
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	AgentVersion string     `json:"agent_version"`
	// Online: a heartbeat was seen recently (agents ping every 15s).
	Online bool `json:"online"`
}

// OnlineWindow is how recently a heartbeat must have arrived for a device to
// count as online (three missed 15s heartbeats).
const OnlineWindow = 45 * time.Second

const deviceCols = `id, user_id, name, disabled, revoked_at IS NOT NULL, token_hash IS NOT NULL,
	created_at, last_seen_at, last_ip, hostname, os, arch, agent_version,
	COALESCE(revoked_at IS NULL AND last_seen_at > now() - interval '45 seconds', false)`

func scanDevice(row pgx.Row) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Disabled, &d.Revoked, &d.Paired,
		&d.CreatedAt, &d.LastSeenAt, &d.LastIP, &d.Hostname, &d.OS, &d.Arch, &d.AgentVersion, &d.Online)
	return d, notFound(err)
}

// ListDevices lists a user's non-revoked devices, or every device (including
// revoked) when userID is nil (admin view).
func (s *Store) ListDevices(ctx context.Context, userID *int64) ([]Device, error) {
	q := `SELECT ` + deviceCols + ` FROM devices WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at`
	args := []any{userID}
	if userID == nil {
		q = `SELECT ` + deviceCols + ` FROM devices ORDER BY created_at`
		args = nil
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Device, error) { return scanDevice(r) })
}

func (s *Store) GetDevice(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.db.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = $1`, id))
}

func (s *Store) UpdateDevice(ctx context.Context, id string, name *string, disabled *bool) (Device, error) {
	return scanDevice(s.db.QueryRow(ctx, `UPDATE devices SET
		name = COALESCE($2, name), disabled = COALESCE($3, disabled)
		WHERE id = $1 AND revoked_at IS NULL RETURNING `+deviceCols, id, name, disabled))
}

// RevokeDevice permanently invalidates the device token and frees its ports.
func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE devices SET revoked_at = now(), token_hash = NULL
			WHERE id = $1 AND revoked_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `DELETE FROM allocations a USING devices d
			WHERE a.device_id = $1 AND d.id = a.device_id RETURNING a.type, a.remote_port, d.user_id`, id)
		if err != nil {
			return err
		}
		type rel struct {
			typ  string
			port int
			user int64
		}
		rels, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rel, error) {
			var x rel
			return x, r.Scan(&x.typ, &x.port, &x.user)
		})
		if err != nil {
			return err
		}
		for _, x := range rels {
			if err := releasePort(ctx, tx, x.typ, x.port, x.user); err != nil {
				return err
			}
		}
		return nil
	})
}

// AgentDevice resolves a device token (Bearer thd_...) to its device.
// Unknown or revoked tokens give ErrNotFound. Disabled reports whether the
// device or its owner is disabled.
func (s *Store) AgentDevice(ctx context.Context, tokenHash []byte) (id string, disabled bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT d.id, d.disabled OR u.disabled
		FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.token_hash = $1 AND d.revoked_at IS NULL`, tokenHash).Scan(&id, &disabled)
	return id, disabled, notFound(err)
}

// ---------------------------------------------------------------- pairing

type PairingMeta struct {
	Hostname, OS, Arch, AgentVersion, IP string
}

func (s *Store) CreatePairing(ctx context.Context, codeHash, pollHash []byte, m PairingMeta, ttl time.Duration) (time.Time, error) {
	var exp time.Time
	err := s.db.QueryRow(ctx, `INSERT INTO pairings
		(code_hash, poll_secret_hash, hostname, os, arch, agent_version, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now() + make_interval(secs => $8)) RETURNING expires_at`,
		codeHash, pollHash, m.Hostname, m.OS, m.Arch, m.AgentVersion, m.IP, ttl.Seconds()).Scan(&exp)
	if _, ok := uniqueViolation(err); ok {
		return exp, ErrConflict // astronomically unlikely code collision; caller retries
	}
	return exp, err
}

// ClaimPairing binds a pending pairing code to a user by creating the device.
func (s *Store) ClaimPairing(ctx context.Context, codeHash []byte, userID int64, deviceID, name string, maxDevices int) (Device, error) {
	var d Device
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var pid int64
		var m PairingMeta
		err := tx.QueryRow(ctx, `SELECT id, hostname, os, arch, agent_version FROM pairings
			WHERE code_hash = $1 AND claimed_at IS NULL AND expires_at > now() FOR UPDATE`, codeHash).
			Scan(&pid, &m.Hostname, &m.OS, &m.Arch, &m.AgentVersion)
		if err != nil {
			return notFound(err)
		}
		// Serialise quota checks per user; a per-user override beats the default.
		var override *int
		if err := tx.QueryRow(ctx, `SELECT max_devices FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&override); err != nil {
			return err
		}
		if override != nil {
			maxDevices = *override
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&n); err != nil {
			return err
		}
		if n >= maxDevices {
			return ErrQuota
		}
		if name == "" {
			name = m.Hostname
		}
		if name == "" {
			name = deviceID
		}
		d, err = scanDevice(tx.QueryRow(ctx, `INSERT INTO devices (id, user_id, name, hostname, os, arch, agent_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+deviceCols,
			deviceID, userID, name, m.Hostname, m.OS, m.Arch, m.AgentVersion))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE pairings SET claimed_at = now(), claimed_by = $2, device_id = $3 WHERE id = $1`,
			pid, userID, deviceID)
		return err
	})
	return d, err
}

type PollResult struct {
	Pending  bool
	DeviceID string
}

// PollPairing returns Pending until the code is claimed; then, exactly once,
// stores tokenHash on the new device and returns its ID. Afterwards (or after
// expiry) it returns ErrExpired.
func (s *Store) PollPairing(ctx context.Context, pollHash, tokenHash []byte) (PollResult, error) {
	var res PollResult
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var (
			pid       int64
			claimed   bool
			delivered bool
			expired   bool
			deviceID  *string
		)
		err := tx.QueryRow(ctx, `SELECT id, claimed_at IS NOT NULL, delivered_at IS NOT NULL, expires_at <= now(), device_id
			FROM pairings WHERE poll_secret_hash = $1 FOR UPDATE`, pollHash).
			Scan(&pid, &claimed, &delivered, &expired, &deviceID)
		if err != nil {
			return notFound(err)
		}
		switch {
		case delivered:
			return ErrExpired
		case !claimed && expired:
			return ErrExpired
		case !claimed:
			res.Pending = true
			return nil
		case deviceID == nil: // device was deleted after claim
			return ErrExpired
		}
		tag, err := tx.Exec(ctx, `UPDATE devices SET token_hash = $2 WHERE id = $1 AND revoked_at IS NULL`, *deviceID, tokenHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrExpired
		}
		if _, err := tx.Exec(ctx, `UPDATE pairings SET delivered_at = now() WHERE id = $1`, pid); err != nil {
			return err
		}
		res.DeviceID = *deviceID
		return nil
	})
	return res, err
}

// CleanupOptions sets retention periods; zero values keep data forever.
type CleanupOptions struct {
	AuditRetention   time.Duration
	TrafficRetention time.Duration
	PortCooldown     time.Duration
}

// Cleanup removes expired sessions, stale pairings, finished port cooldowns
// and data past its retention period.
func (s *Store) Cleanup(ctx context.Context, o CleanupOptions) error {
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM sessions WHERE expires_at < now()`, nil},
		{`DELETE FROM pairings WHERE expires_at < now() - interval '1 day'`, nil},
		{`DELETE FROM api_tokens WHERE revoked_at < now() - interval '30 days'`, nil},
		{`DELETE FROM port_releases WHERE released_at < now() - make_interval(secs => $1)`, []any{o.PortCooldown.Seconds()}},
		{`DELETE FROM traffic_counters WHERE updated_at < now() - interval '3 days'`, nil},
	}
	if o.AuditRetention > 0 {
		stmts = append(stmts, struct {
			q    string
			args []any
		}{`DELETE FROM audit_log WHERE at < now() - make_interval(secs => $1)`, []any{o.AuditRetention.Seconds()}})
	}
	if o.TrafficRetention > 0 {
		stmts = append(stmts, struct {
			q    string
			args []any
		}{`DELETE FROM traffic_daily WHERE day < (now() - make_interval(secs => $1))::date`, []any{o.TrafficRetention.Seconds()}})
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- allocations

type Allocation struct {
	ID             int64     `json:"id"`
	DeviceID       string    `json:"device_id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	RemotePort     int       `json:"remote_port"`
	LocalIP        string    `json:"local_ip"`
	LocalPort      int       `json:"local_port"`
	Enabled        bool      `json:"enabled"`
	BandwidthLimit string    `json:"bandwidth_limit"`
	ProxyProtocol  string    `json:"proxy_protocol"`
	CreatedAt      time.Time `json:"created_at"`
	DeviceName     string    `json:"device_name,omitempty"` // only in cross-device listings
}

const allocCols = `id, device_id, name, type, remote_port, local_ip, local_port, enabled, bandwidth_limit, proxy_protocol, created_at`

func scanAlloc(row pgx.Row) (Allocation, error) {
	var a Allocation
	err := row.Scan(&a.ID, &a.DeviceID, &a.Name, &a.Type, &a.RemotePort, &a.LocalIP, &a.LocalPort,
		&a.Enabled, &a.BandwidthLimit, &a.ProxyProtocol, &a.CreatedAt)
	return a, notFound(err)
}

func (s *Store) ListAllocations(ctx context.Context, deviceID string) ([]Allocation, error) {
	rows, err := s.db.Query(ctx, `SELECT `+allocCols+` FROM allocations WHERE device_id = $1 ORDER BY id`, deviceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Allocation, error) { return scanAlloc(r) })
}

// ListUserAllocations lists the tunnels of all of a user's active devices.
func (s *Store) ListUserAllocations(ctx context.Context, userID int64) ([]Allocation, error) {
	rows, err := s.db.Query(ctx, `SELECT a.id, a.device_id, a.name, a.type, a.remote_port, a.local_ip, a.local_port,
			a.enabled, a.bandwidth_limit, a.proxy_protocol, a.created_at, d.name
		FROM allocations a JOIN devices d ON d.id = a.device_id
		WHERE d.user_id = $1 AND d.revoked_at IS NULL ORDER BY d.created_at, a.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Allocation, error) {
		var a Allocation
		err := r.Scan(&a.ID, &a.DeviceID, &a.Name, &a.Type, &a.RemotePort, &a.LocalIP, &a.LocalPort,
			&a.Enabled, &a.BandwidthLimit, &a.ProxyProtocol, &a.CreatedAt, &a.DeviceName)
		return a, err
	})
}

type PortPool struct{ Start, End int }

func (p PortPool) Contains(port int) bool { return port >= p.Start && port <= p.End }

// PortPolicy decides which public ports may be handed out.
type PortPolicy struct {
	Pool PortPool // automatic allocation range
	// Extra ports the relay also allows (outside the pool). Only admins and
	// users holding a reservation may use them.
	Extra        map[int]bool
	Cooldown     time.Duration // a released port is kept from other users this long
	MaxPerDevice int           // default tunnel quota (users may have an override)
}

// Port-policy errors.
var (
	ErrPortNotAllowed = errors.New("port is not available for tunnels")
	ErrPortReserved   = errors.New("port is reserved")
	ErrPortCooldown   = errors.New("port was released recently and is cooling down")
)

func releasePort(ctx context.Context, tx pgx.Tx, typ string, port int, owner int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO port_releases (type, remote_port, released_from) VALUES ($1, $2, $3)
		ON CONFLICT (type, remote_port) DO UPDATE SET released_at = now(), released_from = EXCLUDED.released_from`,
		typ, port, owner)
	return err
}

// pickPort returns the lowest free pool port: not allocated, not reserved and
// not cooling down (unless the cooldown is the requesting user's own port).
func pickPort(ctx context.Context, tx pgx.Tx, typ string, owner int64, pol PortPolicy) (int, error) {
	var port int
	err := tx.QueryRow(ctx, `SELECT p FROM generate_series($1::int, $2::int) AS p
		WHERE NOT EXISTS (SELECT 1 FROM allocations WHERE type = $3 AND remote_port = p)
		  AND NOT EXISTS (SELECT 1 FROM reserved_ports WHERE type = $3 AND remote_port = p)
		  AND NOT EXISTS (SELECT 1 FROM port_releases WHERE type = $3 AND remote_port = p
		        AND released_at > now() - make_interval(secs => $4) AND released_from IS DISTINCT FROM $5)
		ORDER BY p LIMIT 1`, pol.Pool.Start, pol.Pool.End, typ, pol.Cooldown.Seconds(), owner).Scan(&port)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPoolExhausted
	}
	return port, err
}

// checkPort validates an explicitly requested port for a device owned by owner.
func checkPort(ctx context.Context, tx pgx.Tx, typ string, port int, owner int64, asAdmin bool, pol PortPolicy) error {
	var reservedFor *int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM reserved_ports WHERE type = $1 AND remote_port = $2`, typ, port).Scan(&reservedFor)
	switch {
	case err == nil: // reserved
		if !asAdmin && (reservedFor == nil || *reservedFor != owner) {
			return ErrPortReserved
		}
		return nil // reservations bypass pool membership and cooldown
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if !pol.Pool.Contains(port) && !(asAdmin && pol.Extra[port]) {
		return ErrPortNotAllowed
	}
	if asAdmin || pol.Cooldown <= 0 {
		return nil
	}
	var cooling bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM port_releases WHERE type = $1 AND remote_port = $2
		AND released_at > now() - make_interval(secs => $3) AND released_from IS DISTINCT FROM $4)`,
		typ, port, pol.Cooldown.Seconds(), owner).Scan(&cooling); err != nil {
		return err
	}
	if cooling {
		return ErrPortCooldown
	}
	return nil
}

// lockDevice locks a live device row and returns its owner and tunnel quota.
func lockDevice(ctx context.Context, tx pgx.Tx, deviceID string, defMax int) (owner int64, maxTunnels int, err error) {
	var override *int
	err = tx.QueryRow(ctx, `SELECT d.user_id, u.max_tunnels_per_device FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.id = $1 AND d.revoked_at IS NULL FOR UPDATE OF d`, deviceID).Scan(&owner, &override)
	if err != nil {
		return 0, 0, notFound(err)
	}
	maxTunnels = defMax
	if override != nil {
		maxTunnels = *override
	}
	return owner, maxTunnels, nil
}

// retryPortRace runs f, retrying when an automatic port pick lost a race.
func retryPortRace(auto bool, f func() error) error {
	for attempt := 0; attempt < 5; attempt++ {
		err := f()
		if c, ok := uniqueViolation(err); ok {
			if c == "allocations_type_remote_port_key" && auto {
				continue
			}
			return ErrConflict
		}
		return err
	}
	return ErrConflict
}

// CreateAllocation inserts a tunnel. RemotePort 0 means "lowest free port in
// the pool"; concurrent requests racing for the same port are retried.
// asAdmin lets administrators use reserved and extra ports.
func (s *Store) CreateAllocation(ctx context.Context, a Allocation, pol PortPolicy, asAdmin bool) (Allocation, error) {
	auto := a.RemotePort == 0
	var out Allocation
	err := retryPortRace(auto, func() error {
		return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			owner, maxTunnels, err := lockDevice(ctx, tx, a.DeviceID, pol.MaxPerDevice)
			if err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM allocations WHERE device_id = $1`, a.DeviceID).Scan(&n); err != nil {
				return err
			}
			if n >= maxTunnels {
				return ErrQuota
			}
			port := a.RemotePort
			if auto {
				if port, err = pickPort(ctx, tx, a.Type, owner, pol); err != nil {
					return err
				}
			} else if err := checkPort(ctx, tx, a.Type, port, owner, asAdmin, pol); err != nil {
				return err
			}
			out, err = scanAlloc(tx.QueryRow(ctx, `INSERT INTO allocations
				(device_id, name, type, remote_port, local_ip, local_port, enabled, bandwidth_limit, proxy_protocol)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+allocCols,
				a.DeviceID, a.Name, a.Type, port, a.LocalIP, a.LocalPort, a.Enabled, a.BandwidthLimit, a.ProxyProtocol))
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `DELETE FROM port_releases WHERE type = $1 AND remote_port = $2`, a.Type, port)
			return err
		})
	})
	return out, err
}

func (s *Store) GetAllocation(ctx context.Context, deviceID string, id int64) (Allocation, error) {
	return scanAlloc(s.db.QueryRow(ctx, `SELECT `+allocCols+` FROM allocations WHERE id = $1 AND device_id = $2`, id, deviceID))
}

// AllocationPatch changes selected tunnel fields. RemotePort: nil = keep,
// 0 = pick a new automatic port, otherwise the requested port.
type AllocationPatch struct {
	Name           *string
	LocalIP        *string
	LocalPort      *int
	Enabled        *bool
	BandwidthLimit *string
	ProxyProtocol  *string
	RemotePort     *int
}

// UpdateAllocation applies a patch. Changing the public port releases the old
// one (subject to cooldown) using the same rules as creation.
func (s *Store) UpdateAllocation(ctx context.Context, deviceID string, id int64, p AllocationPatch, pol PortPolicy, asAdmin bool) (Allocation, error) {
	auto := p.RemotePort != nil && *p.RemotePort == 0
	var out Allocation
	err := retryPortRace(auto, func() error {
		return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			owner, _, err := lockDevice(ctx, tx, deviceID, 0)
			if err != nil {
				return err
			}
			cur, err := scanAlloc(tx.QueryRow(ctx, `SELECT `+allocCols+` FROM allocations
				WHERE id = $1 AND device_id = $2 FOR UPDATE`, id, deviceID))
			if err != nil {
				return err
			}
			port := cur.RemotePort
			if p.RemotePort != nil && *p.RemotePort != cur.RemotePort {
				if auto {
					port, err = pickPort(ctx, tx, cur.Type, owner, pol)
				} else {
					port = *p.RemotePort
					err = checkPort(ctx, tx, cur.Type, port, owner, asAdmin, pol)
				}
				if err != nil {
					return err
				}
			}
			if p.ProxyProtocol != nil && *p.ProxyProtocol != "" && cur.Type != "tcp" {
				return ErrPortNotAllowed // caller validates; defence in depth
			}
			out, err = scanAlloc(tx.QueryRow(ctx, `UPDATE allocations SET
				name = COALESCE($3, name), local_ip = COALESCE($4, local_ip), local_port = COALESCE($5, local_port),
				enabled = COALESCE($6, enabled), bandwidth_limit = COALESCE($7, bandwidth_limit),
				proxy_protocol = COALESCE($8, proxy_protocol), remote_port = $9, updated_at = now()
				WHERE id = $1 AND device_id = $2 RETURNING `+allocCols,
				id, deviceID, p.Name, p.LocalIP, p.LocalPort, p.Enabled, p.BandwidthLimit, p.ProxyProtocol, port))
			if err != nil {
				return err
			}
			if port != cur.RemotePort {
				if err := releasePort(ctx, tx, cur.Type, cur.RemotePort, owner); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `DELETE FROM port_releases WHERE type = $1 AND remote_port = $2`, cur.Type, port)
			}
			return err
		})
	})
	return out, err
}

func (s *Store) DeleteAllocation(ctx context.Context, deviceID string, id int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var (
			typ   string
			port  int
			owner int64
		)
		err := tx.QueryRow(ctx, `DELETE FROM allocations a USING devices d
			WHERE a.id = $1 AND a.device_id = $2 AND d.id = a.device_id
			RETURNING a.type, a.remote_port, d.user_id`, id, deviceID).Scan(&typ, &port, &owner)
		if err != nil {
			return notFound(err)
		}
		return releasePort(ctx, tx, typ, port, owner)
	})
}

// ---------------------------------------------------------------- reserved ports

type ReservedPort struct {
	Type       string    `json:"type"`
	RemotePort int       `json:"remote_port"`
	UserID     *int64    `json:"user_id"` // nil = administrators only
	UserEmail  string    `json:"user_email,omitempty"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
	InUseBy    string    `json:"in_use_by,omitempty"` // device ID currently holding it
}

func (s *Store) ListReservedPorts(ctx context.Context, userID *int64) ([]ReservedPort, error) {
	rows, err := s.db.Query(ctx, `SELECT r.type, r.remote_port, r.user_id, COALESCE(u.email, ''), r.note, r.created_at,
			COALESCE(a.device_id, '')
		FROM reserved_ports r
		LEFT JOIN users u ON u.id = r.user_id
		LEFT JOIN allocations a ON a.type = r.type AND a.remote_port = r.remote_port
		WHERE $1::bigint IS NULL OR r.user_id = $1
		ORDER BY r.remote_port, r.type`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReservedPort, error) {
		var x ReservedPort
		err := r.Scan(&x.Type, &x.RemotePort, &x.UserID, &x.UserEmail, &x.Note, &x.CreatedAt, &x.InUseBy)
		return x, err
	})
}

// ReservePort creates or updates a reservation. It fails with ErrConflict if
// the port is currently used by a device of someone other than the holder.
func (s *Store) ReservePort(ctx context.Context, r ReservedPort) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var holder *int64
		err := tx.QueryRow(ctx, `SELECT d.user_id FROM allocations a JOIN devices d ON d.id = a.device_id
			WHERE a.type = $1 AND a.remote_port = $2`, r.Type, r.RemotePort).Scan(&holder)
		if err == nil && (r.UserID == nil || *holder != *r.UserID) {
			return ErrConflict
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if r.UserID != nil {
			if _, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, *r.UserID)); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO reserved_ports (type, remote_port, user_id, note) VALUES ($1, $2, $3, $4)
			ON CONFLICT (type, remote_port) DO UPDATE SET user_id = EXCLUDED.user_id, note = EXCLUDED.note`,
			r.Type, r.RemotePort, r.UserID, r.Note)
		return err
	})
}

func (s *Store) DeleteReservedPort(ctx context.Context, typ string, port int) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM reserved_ports WHERE type = $1 AND remote_port = $2`, typ, port)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- plugin.Store

var _ plugin.Store = (*Store)(nil)

// Device returns the device as the frps plugin sees it. A device is enabled
// only if it is paired, not revoked, not disabled, and its owner is enabled.
func (s *Store) Device(ctx context.Context, id string) (plugin.Device, bool, error) {
	var (
		hash    []byte
		enabled bool
	)
	err := s.db.QueryRow(ctx, `SELECT d.token_hash,
			d.token_hash IS NOT NULL AND d.revoked_at IS NULL AND NOT d.disabled AND NOT u.disabled
		FROM devices d JOIN users u ON u.id = d.user_id WHERE d.id = $1`, id).Scan(&hash, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return plugin.Device{}, false, nil
	}
	if err != nil {
		return plugin.Device{}, false, err
	}
	return plugin.Device{ID: id, TokenHash: hex.EncodeToString(hash), Enabled: enabled}, true, nil
}

func (s *Store) Allocation(ctx context.Context, deviceID, name string) (plugin.Allocation, bool, error) {
	var a plugin.Allocation
	err := s.db.QueryRow(ctx, `SELECT device_id, name, type, remote_port, enabled, bandwidth_limit
		FROM allocations WHERE device_id = $1 AND name = $2`, deviceID, name).
		Scan(&a.DeviceID, &a.Name, &a.Type, &a.RemotePort, &a.Enabled, &a.BandwidthLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

func (s *Store) RecordLogin(ctx context.Context, deviceID string, info plugin.LoginInfo) error {
	_, err := s.db.Exec(ctx, `UPDATE devices SET last_seen_at = now(), last_ip = $2,
		hostname = $3, os = $4, arch = $5, agent_version = $6 WHERE id = $1`,
		deviceID, info.IP, info.Hostname, info.OS, info.Arch, info.Version)
	return err
}

func (s *Store) RecordSeen(ctx context.Context, deviceID string) error {
	_, err := s.db.Exec(ctx, `UPDATE devices SET last_seen_at = now() WHERE id = $1`, deviceID)
	return err
}

// ---------------------------------------------------------------- audit

type AuditEntry struct {
	ID            int64          `json:"id"`
	At            time.Time      `json:"at"`
	ActorUserID   *int64         `json:"actor_user_id"`
	ActorDeviceID string         `json:"actor_device_id,omitempty"`
	Action        string         `json:"action"`
	Target        string         `json:"target"`
	IP            string         `json:"ip"`
	Details       map[string]any `json:"details,omitempty"`
}

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	details, _ := json.Marshal(e.Details)
	if e.Details == nil {
		details = []byte("{}")
	}
	var dev *string
	if e.ActorDeviceID != "" {
		dev = &e.ActorDeviceID
	}
	_, err := s.db.Exec(ctx, `INSERT INTO audit_log (actor_user_id, actor_device_id, action, target, ip, details)
		VALUES ($1, $2, $3, $4, $5, $6)`, e.ActorUserID, dev, e.Action, e.Target, e.IP, details)
	return err
}

func (s *Store) ListAudit(ctx context.Context, userID *int64, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT id, at, actor_user_id, COALESCE(actor_device_id, ''), action, target, ip, details
		FROM audit_log WHERE ($1::bigint IS NULL OR actor_user_id = $1) ORDER BY id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		var details []byte
		err := r.Scan(&e.ID, &e.At, &e.ActorUserID, &e.ActorDeviceID, &e.Action, &e.Target, &e.IP, &details)
		if err == nil && len(details) > 2 {
			_ = json.Unmarshal(details, &e.Details)
		}
		return e, err
	})
}

// ---------------------------------------------------------------- traffic

// TrafficDelta is traffic counted since the previous sample for one proxy.
// In/Out are the deltas; CurIn/CurOut the raw frps counters to remember.
type TrafficDelta struct {
	ProxyName     string // "<device-id>.<tunnel-name>" as frps reports it
	DeviceID      string
	Name          string
	In, Out       int64
	CurIn, CurOut int64
}

// LoadTrafficCounters returns the last frps counters seen per proxy.
func (s *Store) LoadTrafficCounters(ctx context.Context) (map[string][2]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT proxy_name, last_in, last_out FROM traffic_counters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var n string
		var in, o int64
		if err := rows.Scan(&n, &in, &o); err != nil {
			return nil, err
		}
		out[n] = [2]int64{in, o}
	}
	return out, rows.Err()
}

// AddTraffic adds deltas to today's (UTC) per-tunnel totals and stores the
// raw counters, atomically. Proxies without a matching allocation are skipped.
func (s *Store) AddTraffic(ctx context.Context, ds []TrafficDelta) error {
	if len(ds) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, d := range ds {
			if d.In > 0 || d.Out > 0 {
				b.Queue(`INSERT INTO traffic_daily (allocation_id, day, bytes_in, bytes_out)
					SELECT id, (now() AT TIME ZONE 'UTC')::date, $3, $4 FROM allocations WHERE device_id = $1 AND name = $2
					ON CONFLICT (allocation_id, day) DO UPDATE SET
						bytes_in = traffic_daily.bytes_in + EXCLUDED.bytes_in,
						bytes_out = traffic_daily.bytes_out + EXCLUDED.bytes_out`, d.DeviceID, d.Name, d.In, d.Out)
			}
			b.Queue(`INSERT INTO traffic_counters (proxy_name, last_in, last_out) VALUES ($1, $2, $3)
				ON CONFLICT (proxy_name) DO UPDATE SET last_in = EXCLUDED.last_in, last_out = EXCLUDED.last_out, updated_at = now()`,
				d.ProxyName, d.CurIn, d.CurOut)
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

type TrafficDay struct {
	Day      string `json:"day"` // YYYY-MM-DD (UTC)
	BytesIn  int64  `json:"bytes_in"`
	BytesOut int64  `json:"bytes_out"`
}

// TrafficHistory returns daily totals for one tunnel, oldest first.
func (s *Store) TrafficHistory(ctx context.Context, allocationID int64, days int) ([]TrafficDay, error) {
	rows, err := s.db.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), bytes_in, bytes_out FROM traffic_daily
		WHERE allocation_id = $1 AND day > (now() AT TIME ZONE 'UTC')::date - $2::int ORDER BY day`, allocationID, days)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrafficDay, error) {
		var d TrafficDay
		return d, r.Scan(&d.Day, &d.BytesIn, &d.BytesOut)
	})
}

// TunnelTraffic is a tunnel's traffic over a period (for top lists).
type TunnelTraffic struct {
	AllocationID int64  `json:"tunnel_id"`
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Owner        string `json:"owner"`
	BytesIn      int64  `json:"bytes_in"`
	BytesOut     int64  `json:"bytes_out"`
}

// Overview holds platform-wide numbers for the admin dashboard.
type Overview struct {
	Users         int             `json:"users"`
	Devices       int             `json:"devices"`
	DevicesOnline int             `json:"devices_online"`
	Tunnels       int             `json:"tunnels"`
	PortsInPool   int             `json:"ports_in_pool"`
	TodayIn       int64           `json:"today_bytes_in"`
	TodayOut      int64           `json:"today_bytes_out"`
	Last30In      int64           `json:"last30_bytes_in"`
	Last30Out     int64           `json:"last30_bytes_out"`
	TopTunnels30d []TunnelTraffic `json:"top_tunnels_30d"`
}

func (s *Store) Overview(ctx context.Context, pool PortPool) (Overview, error) {
	o := Overview{PortsInPool: pool.End - pool.Start + 1}
	err := s.db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM devices WHERE revoked_at IS NULL),
		(SELECT count(*) FROM devices WHERE revoked_at IS NULL AND last_seen_at > now() - interval '45 seconds'),
		(SELECT count(*) FROM allocations),
		(SELECT COALESCE(sum(bytes_in), 0) FROM traffic_daily WHERE day = (now() AT TIME ZONE 'UTC')::date),
		(SELECT COALESCE(sum(bytes_out), 0) FROM traffic_daily WHERE day = (now() AT TIME ZONE 'UTC')::date),
		(SELECT COALESCE(sum(bytes_in), 0) FROM traffic_daily WHERE day > (now() AT TIME ZONE 'UTC')::date - 30),
		(SELECT COALESCE(sum(bytes_out), 0) FROM traffic_daily WHERE day > (now() AT TIME ZONE 'UTC')::date - 30)`).
		Scan(&o.Users, &o.Devices, &o.DevicesOnline, &o.Tunnels, &o.TodayIn, &o.TodayOut, &o.Last30In, &o.Last30Out)
	if err != nil {
		return o, err
	}
	rows, err := s.db.Query(ctx, `SELECT a.id, a.device_id, a.name, u.email, sum(t.bytes_in), sum(t.bytes_out)
		FROM traffic_daily t JOIN allocations a ON a.id = t.allocation_id
		JOIN devices d ON d.id = a.device_id JOIN users u ON u.id = d.user_id
		WHERE t.day > (now() AT TIME ZONE 'UTC')::date - 30
		GROUP BY a.id, a.device_id, a.name, u.email
		ORDER BY sum(t.bytes_in + t.bytes_out) DESC LIMIT 10`)
	if err != nil {
		return o, err
	}
	o.TopTunnels30d, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TunnelTraffic, error) {
		var t TunnelTraffic
		return t, r.Scan(&t.AllocationID, &t.DeviceID, &t.Name, &t.Owner, &t.BytesIn, &t.BytesOut)
	})
	return o, err
}

// Ping checks database connectivity (readiness probe).
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// UserTrafficDaily returns a user's total traffic per day over the last `days`
// days (UTC), summed across all of their tunnels. Days with no traffic are
// omitted; callers fill the gaps.
func (s *Store) UserTrafficDaily(ctx context.Context, userID int64, days int) ([]TrafficDay, error) {
	rows, err := s.db.Query(ctx, `SELECT to_char(t.day, 'YYYY-MM-DD'), sum(t.bytes_in)::bigint, sum(t.bytes_out)::bigint
		FROM traffic_daily t
		JOIN allocations a ON a.id = t.allocation_id
		JOIN devices d ON d.id = a.device_id
		WHERE d.user_id = $1 AND t.day > (now() AT TIME ZONE 'UTC')::date - $2::int
		GROUP BY t.day ORDER BY t.day`, userID, days)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrafficDay, error) {
		var d TrafficDay
		return d, r.Scan(&d.Day, &d.BytesIn, &d.BytesOut)
	})
}

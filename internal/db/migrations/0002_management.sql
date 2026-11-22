-- Phases 4-8: agent config sync, full tunnel management, premium ports,
-- port cooldown, per-user quotas, traffic statistics.

-- Agents authenticate to the API with their device token (Bearer thd_...).
CREATE UNIQUE INDEX devices_token_hash_key ON devices (token_hash) WHERE token_hash IS NOT NULL;

-- PROXY protocol header towards the local service (tcp only).
ALTER TABLE allocations
    ADD COLUMN proxy_protocol TEXT NOT NULL DEFAULT '' CHECK (proxy_protocol IN ('', 'v1', 'v2'));

-- Per-user quota overrides (NULL = server default).
ALTER TABLE users
    ADD COLUMN max_devices            INT CHECK (max_devices BETWEEN 0 AND 1000),
    ADD COLUMN max_tunnels_per_device INT CHECK (max_tunnels_per_device BETWEEN 0 AND 1000);

-- Port cooldown: a released port is not handed to someone else for a while,
-- so players of the old server don't land on a stranger's service.
CREATE TABLE port_releases (
    type          TEXT        NOT NULL CHECK (type IN ('tcp', 'udp')),
    remote_port   INT         NOT NULL,
    released_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_from BIGINT REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (type, remote_port)
);

-- Reserved ("premium") ports, e.g. 25565/tcp. user_id NULL = admin-only.
-- They may lie outside the automatic pool (then they must be in the relay's
-- extra allowed ports). Auto-allocation never hands them out.
CREATE TABLE reserved_ports (
    type        TEXT        NOT NULL CHECK (type IN ('tcp', 'udp')),
    remote_port INT         NOT NULL CHECK (remote_port BETWEEN 1 AND 65535),
    user_id     BIGINT REFERENCES users (id) ON DELETE SET NULL,
    note        TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (type, remote_port)
);

-- Traffic per tunnel per UTC day (bytes, as counted by frps).
-- bytes_in = from players to the server, bytes_out = from the server to players.
CREATE TABLE traffic_daily (
    allocation_id BIGINT NOT NULL REFERENCES allocations (id) ON DELETE CASCADE,
    day           DATE   NOT NULL,
    bytes_in      BIGINT NOT NULL DEFAULT 0,
    bytes_out     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (allocation_id, day)
);
CREATE INDEX traffic_daily_day_idx ON traffic_daily (day);

-- Last counters read from frps per proxy, so deltas survive control restarts.
CREATE TABLE traffic_counters (
    proxy_name TEXT        PRIMARY KEY,
    last_in    BIGINT      NOT NULL,
    last_out   BIGINT      NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

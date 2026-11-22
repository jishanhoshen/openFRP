-- Phase 3: users, sessions, API tokens, devices, pairing, allocations, audit log.

CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    disabled      BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- Browser sessions. Only a SHA-256 of the cookie value is stored.
CREATE TABLE sessions (
    token_hash   BYTEA       PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip           TEXT        NOT NULL DEFAULT '',
    user_agent   TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Personal API tokens (Bearer). Only a SHA-256 is stored; prefix is for display.
CREATE TABLE api_tokens (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    token_hash   BYTEA       NOT NULL UNIQUE,
    prefix       TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id);

-- Agents. token_hash is NULL until the agent collects its token after pairing,
-- and is cleared on revocation, so such devices can never authenticate.
CREATE TABLE devices (
    id            TEXT        PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT        NOT NULL,
    token_hash    BYTEA,
    disabled      BOOLEAN     NOT NULL DEFAULT false,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ,
    last_ip       TEXT        NOT NULL DEFAULT '',
    hostname      TEXT        NOT NULL DEFAULT '',
    os            TEXT        NOT NULL DEFAULT '',
    arch          TEXT        NOT NULL DEFAULT '',
    agent_version TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX devices_user_idx ON devices (user_id);

-- Device pairing: the agent starts a pairing and shows a short code; a
-- logged-in user claims the code; the agent's next poll receives its token.
CREATE TABLE pairings (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code_hash        BYTEA       NOT NULL UNIQUE,
    poll_secret_hash BYTEA       NOT NULL UNIQUE,
    hostname         TEXT        NOT NULL DEFAULT '',
    os               TEXT        NOT NULL DEFAULT '',
    arch             TEXT        NOT NULL DEFAULT '',
    agent_version    TEXT        NOT NULL DEFAULT '',
    ip               TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    claimed_at       TIMESTAMPTZ,
    claimed_by       BIGINT REFERENCES users (id) ON DELETE CASCADE,
    device_id        TEXT REFERENCES devices (id) ON DELETE CASCADE,
    delivered_at     TIMESTAMPTZ
);
CREATE INDEX pairings_expires_idx ON pairings (expires_at);

-- Public port allocations. (type, remote_port) is globally unique: this is
-- the authoritative port-conflict check.
CREATE TABLE allocations (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id       TEXT        NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    type            TEXT        NOT NULL CHECK (type IN ('tcp', 'udp')),
    remote_port     INT         NOT NULL CHECK (remote_port BETWEEN 1 AND 65535),
    local_ip        TEXT        NOT NULL DEFAULT '127.0.0.1',
    local_port      INT         NOT NULL CHECK (local_port BETWEEN 1 AND 65535),
    enabled         BOOLEAN     NOT NULL DEFAULT true,
    bandwidth_limit TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (type, remote_port),
    UNIQUE (device_id, name)
);

CREATE TABLE audit_log (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_user_id   BIGINT REFERENCES users (id) ON DELETE SET NULL,
    actor_device_id TEXT,
    action          TEXT        NOT NULL,
    target          TEXT        NOT NULL DEFAULT '',
    ip              TEXT        NOT NULL DEFAULT '',
    details         JSONB       NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
CREATE INDEX audit_log_user_idx ON audit_log (actor_user_id, at DESC);

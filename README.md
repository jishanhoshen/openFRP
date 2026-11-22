# TunnelHub (working name)

Self-hosted Playit.gg-style tunneling: users run an agent on a home PC, players
connect to the relay's public IP and an allocated port, and traffic is forwarded
over the agent's outbound connection. No port forwarding is needed at home.

Built on [frp](https://github.com/fatedier/frp) **v0.71.1** (Apache-2.0), with frp's
client embedded in our own agent and frps policed by our control plane.

## Status

| Phase | State |
|---|---|
| 1. Tunnel infrastructure (frps, TLS, QUIC, port pool) | Built, e2e-tested in Docker. **Not deployed to a relay yet** |
| 2. Client agent (Linux/Windows service, reconnect, live reload) | Built, e2e-tested on Linux. Windows binary cross-compiled, not run on Windows |
| 3. Auth & device management (PostgreSQL, users, sessions, API tokens, pairing, revocation, audit) | Built, tested |
| 4. Agent config sync (tunnels managed in the control plane, live push, revocation) | Built, e2e-tested |
| 5. Web dashboard (login, devices, tunnels, account, admin) | Built. Served by the control plane. **Not checked in a real browser** |
| 6. Premium ports, port cooldown, per-user quotas | Built, integration-tested |
| 7. Traffic statistics and live status (from frps) | Built, unit + e2e tested |
| 8. Hardening (CSP, login throttling, cookie prefixes, retention, backups, firewall template) | Built. **Firewall template not validated with `nft`, not applied** |

Last verified 2026-11-22: `make vet test e2e` pass; `make agents` cross-compiles for
linux/amd64, linux/arm64 and windows/amd64.

## Layout

```
cmd/agent/            tunnelhub-agent: pairing, config sync, embedded frp client, system service
cmd/control/          tunnelhub-control: API, dashboard, frps plugin, stats poller; migrate; create-user
internal/agentcfg/    agent.yaml schema, validation (local-target restriction), save/load
internal/agent/       runner (connect, reconnect, hot-reload), config sync, pairing client
internal/api/         HTTP API: auth, tokens, devices, pairing, tunnels, agent sync, admin
internal/auth/        argon2id passwords, random tokens, pairing codes
internal/db/          pgx pool + embedded SQL migrations (0001 init, 0002 management)
internal/plugin/      frps server-plugin handler: Login / NewProxy / Ping / CloseProxy
internal/stats/       polls frps for per-tunnel status and traffic; persists daily totals
internal/store/       PostgreSQL data layer (also implements plugin.Store)
internal/web/         embedded dashboard (static HTML/CSS/JS, no inline script)
deploy/               relay stack: compose (caddy, control, postgres, frps), configs, scripts
scripts/test.sh       go test against a throwaway postgres container
test/e2e/             isolated docker-network end-to-end scenario
```

## How it works

**Pairing** (like Playit's claim flow):

```
agent:  POST /api/v1/agent/pair/start           -> shows code "ABCD-EFGH"
user:   POST /api/v1/devices/claim {code}       -> device created in the user's account
agent:  POST /api/v1/agent/pair/poll            -> receives device token (once) + relay settings,
                                                   writes agent.yaml (0600), connects
```

**Tunnel sync** (Phase 4): once paired, the agent long-polls
`GET /api/v1/agent/config` with its device token. Creating, editing, disabling or
deleting a tunnel in the dashboard wakes the agent, which applies the change to the
running frp client without restarting. `agent.yaml` keeps the last list as a cache
for starts while the control plane is unreachable. With `control.url` set, the
tunnel list is owned by the control plane.

**Revocation**: the config poll returns 401 at once, so the agent closes its tunnels
and stops reconnecting (it stays running idle until uninstalled). Relay-side, the
plugin also rejects the device's next heartbeat, which covers a modified agent.

**Enforcement on the relay** (frps -> control plane plugin, fail-closed):

```
Login     device token valid? device paired, not revoked/disabled? owner enabled?
NewProxy  tunnel (name, type, port) allocated to this device and enabled?
          -> server-side bandwidth limit injected; only tcp/udp allowed
Ping      every 15s: re-checks the device. Revoked -> stock agent disconnects at once;
          a modified agent that ignores it is dropped after frps heartbeatTimeout (60s)
```

**Port policy** (Phase 6):
- Automatic ports come from the pool (default 20000–29999), lowest free first.
- Reserved ports (e.g. 25565/tcp for a Minecraft server) are assigned by an admin to a
  user, or to admins only. Automatic allocation never hands them out.
- Extra ports outside the pool (`TH_EXTRA_PORTS`) are usable by admins and holders of
  a reservation only. They must also be in frps `allowPorts`.
- When a tunnel is deleted or moves, its port cools down (default 24h) and other users
  cannot get it. The previous holder can take it back.
- Quotas: default 10 devices per user and 20 tunnels per device. Admins can set
  per-user overrides.

**Statistics** (Phase 7): the control plane polls frps' dashboard API every 30s. It
turns frps' "today" byte counters into deltas and stores daily totals per tunnel.
The dashboard shows live status (online, open connections) and daily traffic. A
counter reset (midnight or frps restart) is handled as a fresh count. Bytes between
the last poll and a reset can be lost, so totals are approximate.

**Security properties**
- Passwords: argon2id (19 MiB, t=2). Login timing equalised for unknown emails.
  Throttled per IP and per account.
- Sessions, API tokens (`thk_…`), device tokens (`thd_…`), poll secrets: 256-bit random,
  only SHA-256 stored. Device token delivered exactly once.
- Session cookie `__Host-th_session` (HttpOnly, Secure, SameSite=Lax) when secure cookies
  are on. Cross-site unsafe requests rejected by `CrossOriginProtection`.
- Dashboard CSP: `default-src 'none'`, no inline script or style; `frame-ancestors 'none'`;
  plus `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, `COOP`/`CORP`.
- Rate limits per IP: login, register, pairing start/poll, code claim, agent config.
- Other users' devices/tunnels answer 404 (no ID probing). Admins can manage all.
- `(type, port)` globally unique in the DB; auto-allocation is race-safe (tested with
  concurrent requests). Local targets must be loopback/private. Bandwidth limits settable
  by admins only.
- Disabling a user ends their sessions and cuts their devices off the relay.
- Audit log for logins, registrations, token/device/tunnel changes, admin actions.
  Retention 365 days by default.

## API

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /healthz`, `GET /readyz`, `GET /api/v1/info` | – | liveness, DB readiness, public settings |
| `POST /api/v1/auth/register` | – | sign up (only if `OPEN_REGISTRATION=true`) |
| `POST /api/v1/auth/login` / `logout` | – / session | session cookie |
| `GET /api/v1/me`, `POST /api/v1/me/password` | user | profile, change password (logs out other sessions) |
| `GET/POST /api/v1/tokens`, `DELETE /api/v1/tokens/{id}` | user | personal API tokens (Bearer) |
| `GET /api/v1/devices`, `POST /api/v1/devices/claim` | user | list / claim pairing code |
| `GET/PATCH/DELETE /api/v1/devices/{id}` | owner/admin | view / rename, pause (`disabled`), remove |
| `GET /api/v1/tunnels` | user | all tunnels of the user's devices, with live status |
| `GET/POST /api/v1/devices/{id}/tunnels` | owner/admin | list / create tunnel (`remote_port` 0 = automatic) |
| `GET/PATCH/DELETE /api/v1/devices/{id}/tunnels/{tid}` | owner/admin | view / edit (name, local target, `enabled`, `remote_port`, `proxy_protocol`) / delete |
| `GET /api/v1/devices/{id}/tunnels/{tid}/traffic?days=N` | owner/admin | daily traffic (default 30 days) |
| `GET /api/v1/reserved-ports`, `GET /api/v1/audit` | user | own reservations, own activity |
| `POST /api/v1/agent/pair/start`, `…/poll` | – | agent pairing |
| `GET /api/v1/agent/config?rev=&wait=` | device token | tunnel list, long-poll for changes |
| `GET /api/v1/admin/overview` | admin | platform totals and top tunnels |
| `GET/POST /api/v1/admin/users`, `PATCH …/{id}` | admin | list / create users; role, disabled, quota overrides (`-1` = default) |
| `GET /api/v1/admin/devices`, `DELETE …/devices/{id}` | admin | all devices / remove |
| `GET/PUT/DELETE /api/v1/admin/reserved-ports/{tcp\|udp}/{port}` | admin | reserve a port for a user or admins only |
| `GET /api/v1/admin/audit` | admin | audit log |

The dashboard is served at `/` and `/claim?code=…` (the claim page takes the code
from the link the agent prints).

## Build & test (all inside Docker; nothing installed on the host)

```bash
make tidy     # go mod tidy -> go.sum
make vet
make test     # unit + integration tests (36 tests) against a throwaway postgres:17 container
make e2e      # full stack on an isolated internal docker network (~3 min)
make agents   # dist/tunnelhub-agent-{linux-amd64,linux-arm64,windows-amd64.exe}
```

The e2e scenario: register a user → start an agent with no config → it shows a
pairing code → claim it → agent saves credentials and syncs → create two tunnels via
the API (automatic ports) → TCP and UDP traffic through the relay over QUIC → a
temporary tunnel is deleted and its port closes → a tunnel is disabled and
re-enabled → tunnels stay up for 70 s (longer than the heartbeat timeout) → device
status and traffic statistics recorded → revoke the device → tunnels go down and the
agent cannot reconnect.

## Relay setup (once a relay host is chosen; not done yet)

```bash
cd deploy
cp .env.example .env        # TH_DOMAIN, RELAY_ADDR, FRPS_ADMIN_PASSWORD, EXTRA_PORTS, ...
mkdir -p secrets && chmod 700 secrets
openssl rand -hex 32 > secrets/frps_token
openssl rand -hex 32 > secrets/db_password
chmod 600 secrets/*
scripts/gen-certs.sh relay.example.com 203.0.113.10   # SANs: RELAY_ADDR (+ IP)
docker compose up -d
docker compose run --rm -T control create-user -email you@example.com -admin < /dev/tty
# firewall: 80/tcp, 443/tcp+udp, 7000/tcp+udp, TUNNEL_PORT_START-END tcp+udp, 22/tcp from admin only
# do NOT open 7500 (frps dashboard) or 8081 (plugin)
scripts/backup.sh           # nightly via cron; copy backups off the host
```

Notes:
- The frps dashboard (used for statistics) listens on `10.250.0.1:7500`, the edge
  Docker bridge gateway. Only containers on that bridge can reach it. The control plane
  authenticates with `FRPS_ADMIN_PASSWORD`.
- Extra reserved ports: add the port to `TH_EXTRA_PORTS` in `.env` and to frps
  `allowPorts` (`{ single = 25565 }`) in `deploy/frps/frps.toml`, then restart both.
- `deploy/host/nftables.conf.example` is a template for host firewalling. It is not
  applied. Validate it with `nft -c` before use.

## Agent install

```bash
# Linux: pair, then install as a service
sudo tunnelhub-agent pair -api https://tunnels.example.com
sudo tunnelhub-agent install && sudo tunnelhub-agent start

# or let the service pair itself (code shown in logs and /etc/tunnelhub/pairing-code.txt)
sudo tunnelhub-agent install -api https://tunnels.example.com && sudo tunnelhub-agent start

# Windows (admin PowerShell)
tunnelhub-agent.exe pair -api https://tunnels.example.com
tunnelhub-agent.exe install; tunnelhub-agent.exe start
```

After pairing, create tunnels in the dashboard. The agent picks them up without a restart.

## Defaults chosen (change before deploying if needed)

FRP v0.71.1 · Go 1.26 · PostgreSQL 17 · pgx v5.12.0 · QUIC agent transport (TCP
fallback) · port pool 20000–29999 tcp+udp · port cooldown 24h · 20 tunnels/device ·
10 devices/user · registration closed (admin creates users) · heartbeat 15 s /
timeout 60 s · config long-poll 25 s · stats every 30 s · audit and traffic retention
365 days · private CA for agent↔relay TLS · Let's Encrypt (Caddy) for the API and dashboard.

## Known gaps and limits

- **Not verified**: the dashboard has not been run in a real browser (JS parses and the
  API it calls is tested, but layout and interaction are unchecked). The Windows agent
  has not run on Windows. The nftables template has not been loaded.
- The agent's sync path is tested on Linux only. A revoked agent stays running in an
  idle state until it is uninstalled.
- Traffic totals are approximate around frps counter resets (see Statistics).
- Extra ports need a manual edit of frps `allowPorts`. The frps admin password is passed
  as an environment variable to the control plane, not a file.
- Rate limits and login throttling live in memory (one control-plane instance).
- Single relay. No multi-region, no fixed premium IP addresses.
- No self-service password reset, email verification or 2FA. Admins create accounts.
- The e2e test no longer checks a rogue tunnel from a local file, since the agent ignores
  its file tunnels when synced. The "tunnel not allocated" refusal is covered by the
  plugin unit tests.

## Open decisions

1. **Relay host**: rented VPS (recommended) vs. the current home server.
2. **Domain name** for the API and dashboard (`TH_DOMAIN`), required for HTTPS pairing.
3. Registration model (currently closed; admin creates users) and quotas.
4. Which project "Porthole" refers to. It was not evaluated in the comparison because
   the name was ambiguous.

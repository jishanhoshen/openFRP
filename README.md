# openFRP

**Share a server running on your own PC with friends, through a public address you control.**

openFRP is a self-hosted tunnel, similar to playit.gg. You run a small **relay** on a server with a public IP. You install an **agent** on the PC that runs your game or app. Players connect to the relay, and the relay forwards their traffic to your PC.

Your PC does not need any open ports on its router. The agent connects outward to the relay.

It is built on [frp](https://github.com/fatedier/frp) v0.71.1 (Apache-2.0).

## How it works

```
 Player  ──►  Relay (public IP, port 20000)
                 │
                 │   the agent keeps an outgoing connection open
                 ▼
            Your PC (agent)  ──►  Your game server (127.0.0.1:26001)
```

1. You create a tunnel in the dashboard, for example "my game, UDP, local port 26001".
2. The dashboard gives it a public port, such as `20000`.
3. Players connect to `relay-address:20000`.
4. The relay forwards the traffic through the agent to your game server.

Only the **relay** needs port forwarding on its network. Your PC does not.

## Features

- **Pairing with a code.** Start the agent, enter the code it shows in the dashboard, and the PC is linked to your account.
- **TCP and UDP tunnels**, with automatic public ports.
- **Live changes.** Add, disable or remove a tunnel and the agent applies it without a restart.
- **Instant revocation.** Remove a device and it disconnects at once.
- **Dashboard** for devices, tunnels, traffic and account management, plus an admin area.
- **Reserved ports** for servers that need a fixed port, such as Minecraft on 25565.
- **Port cooldown.** A released port is kept from other users for a while.
- **Quotas** for devices and tunnels per user.
- **Traffic statistics** per tunnel, per day.
- **Agent for Linux and Windows.** It can run in the foreground or as a service.

## Status

openFRP is early software.

- The relay, control plane and agent are tested end to end in Docker.
- The web dashboard has not yet been tested in a real browser.
- The Windows agent is built but has not been run on Windows in every setup.
- HTTPS for the dashboard needs a domain name. Until then, use it only on a trusted network.

Do not use it to host anything important without reviewing the security notes below.

## Quick start

### 1. Run the relay

You need Docker and Docker Compose on a server.

```bash
cd deploy
cp .env.example .env                  # edit RELAY_ADDR, TUNNEL ports, etc.
mkdir -p secrets && chmod 700 secrets
openssl rand -hex 32 > secrets/frps_token
openssl rand -hex 32 > secrets/db_password
openssl rand -hex 24 > secrets/frps_admin_password
chmod 600 secrets/*
scripts/gen-certs.sh <relay-ip-or-hostname>
docker compose -f compose.yaml -f compose.local.yaml up -d
```

`compose.local.yaml` serves the dashboard on plain HTTP port 8080. For an internet-facing setup, use `compose.yaml` with Caddy and a domain instead.

Create an admin account:

```bash
docker compose -f compose.yaml -f compose.local.yaml run --rm -T control \
  create-user -email you@example.com -admin
```

Forward these ports on your router to the relay:

| Port | Protocol | Purpose |
|---|---|---|
| 7000 | TCP and UDP | Agent connection |
| 20000–29999 | TCP and UDP | Player traffic |

### 2. Install the agent on your PC

Build the agent or download a release, then run:

```bash
tunnelhub-agent run -api http://<relay-ip>:8080
```

It shows a pairing code and a link. Enter the code in the dashboard under **Link a computer**. The agent saves its settings and connects.

### 3. Create a tunnel

In the dashboard, open your device and add a tunnel:
- **Protocol:** TCP or UDP
- **Local port:** the port your game or app uses on your PC
- **Local address:** `127.0.0.1` when the game runs on the same PC

The dashboard shows the public address, for example `relay-ip:20000`. Give this address to your players.

## Building from source

Everything is built and tested inside Docker. Nothing is installed on your host.

```bash
make vet      # static checks
make test     # unit and database tests
make e2e      # full end-to-end test on an isolated Docker network
make agents   # agent binaries for Linux (amd64, arm64) and Windows
```

## Security notes

- Device tokens, passwords and API tokens are stored only as SHA-256 hashes (passwords use argon2id).
- The relay only accepts tunnels that were allocated to a device in the database.
- Local targets must be loopback or private addresses, so a device cannot use the relay as a proxy to the internet.
- Sessions use `__Host-` cookies when secure cookies are on. The dashboard has a strict Content Security Policy.
- Local development uses plain HTTP and insecure cookies. Do not expose that setup to the internet.
- The frps dashboard is bound to the Docker bridge and must not be opened in the firewall.
- Never commit `deploy/.env`, `deploy/secrets/` or `deploy/tls/`. They are in `.gitignore`.

## Limitations

- No HTTPS on the dashboard until a domain is set up.
- No automatic subdomain per tunnel yet. Players use `IP:port`.
- Traffic totals are approximate around counter resets.
- Only one control-plane instance, and rate limits are kept in memory.
- Some games use SRV DNS records to find ports. openFRP does not manage DNS records.

## Development

```
cmd/agent/         agent: pairing, sync, service mode
cmd/control/       control plane: API, dashboard, frps plugin, statistics
internal/          agent, API, store, plugin, stats, web dashboard, and more
deploy/            relay stack, Caddy, frps config, scripts
test/e2e/          end-to-end scenario
```

## Credits

- [frp](https://github.com/fatedier/frp) by fatedier, Apache-2.0. openFRP uses frp's client and server as its tunnel engine.

## License

Copyright 2026 Jishan Hoshen.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

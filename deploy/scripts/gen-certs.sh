#!/usr/bin/env bash
# Generate a private CA and the relay's TLS certificate for agent<->relay traffic.
# Agents pin ca.crt (server.caFile), so a public CA / domain is not required.
#
# usage: deploy/scripts/gen-certs.sh <relay-hostname-or-ip> [more SANs...]
# output: deploy/tls/{ca.crt,ca.key,server.crt,server.key}
#   - ca.key: keep OFFLINE after generating (only needed to re-issue server.crt)
#   - ca.crt: ship to every agent
set -euo pipefail

[ $# -ge 1 ] || { echo "usage: $0 <relay-hostname-or-ip> [more SANs...]" >&2; exit 2; }
out="$(cd "$(dirname "$0")/.." && pwd)/tls"
mkdir -p "$out"
umask 077

if [ -e "$out/ca.key" ]; then
  echo "refusing to overwrite existing $out/ca.key" >&2
  exit 1
fi

san=""
for name in "$@"; do
  if [[ "$name" =~ ^[0-9.]+$ || "$name" == *:* ]]; then san+="IP:$name,"; else san+="DNS:$name,"; fi
done
san="${san%,}"

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 \
  -subj "/CN=TunnelHub Relay CA" \
  -keyout "$out/ca.key" -out "$out/ca.crt"

openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -subj "/CN=$1" -keyout "$out/server.key" -out "$out/server.csr"

openssl x509 -req -in "$out/server.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" \
  -CAcreateserial -days 825 -out "$out/server.crt" \
  -extfile <(printf "subjectAltName=%s\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature,keyEncipherment\n" "$san")

rm -f "$out/server.csr" "$out/ca.srl"
chmod 644 "$out/ca.crt" "$out/server.crt"
echo "created in $out (SAN: $san). Move ca.key offline."

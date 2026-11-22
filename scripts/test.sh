#!/usr/bin/env bash
# Run Go unit + integration tests inside Docker against a throwaway PostgreSQL.
# Nothing is installed on the host; the network and database are removed afterwards.
set -euo pipefail
cd "$(dirname "$0")/.."

GO_IMAGE="${GO_IMAGE:-golang:1.26-bookworm}"
PG_IMAGE="${PG_IMAGE:-postgres:17-alpine}"
name="tunnelhub-test-$$"

cleanup() {
  docker rm -f "$name-pg" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create --internal "$name" >/dev/null
docker run -d --name "$name-pg" --network "$name" --tmpfs /var/lib/postgresql/data \
  -e POSTGRES_PASSWORD=test -e POSTGRES_DB=test "$PG_IMAGE" >/dev/null

for _ in $(seq 1 60); do
  docker exec "$name-pg" pg_isready -U postgres -d test >/dev/null 2>&1 && break
  sleep 0.5
done

docker run --rm --network "$name" -u "$(id -u):$(id -g)" \
  -e HOME=/tmp -e GOCACHE=/src/.cache/go-build -e GOMODCACHE=/src/.cache/gomod \
  -e TEST_DATABASE_URL="postgres://postgres:test@$name-pg:5432/test?sslmode=disable" \
  -v "$PWD:/src" -w /src "$GO_IMAGE" go test -race -count=1 "$@" ./...

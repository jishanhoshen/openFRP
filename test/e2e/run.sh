#!/usr/bin/env bash
# Build and run the end-to-end test in an isolated Docker network, then clean up.
# Exit code is the scenario's: 0 = all checks passed.
set -euo pipefail
cd "$(dirname "$0")"

cleanup() { docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker compose build
set +e
docker compose up --abort-on-container-exit --exit-code-from scenario
rc=$?
set -e
if [ $rc -ne 0 ]; then
  echo "---- e2e FAILED (exit $rc); recent logs ----"
  docker compose logs --no-color --tail=100 frps control agent
fi
exit $rc

#!/usr/bin/env bash
# Runtime smoke: build, migrate an empty database, boot the backend, probe the
# open endpoints and log correlation, then prove the backup/restore round-trip.
# The script never touches the configured data directory and needs no network.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
workdir="$(mktemp -d)"
port="${SMOKE_PORT:-18080}"
pid=""

cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

make -C "$root" build >/dev/null
mkdir -p "$workdir/data"
cat > "$workdir/config.json" <<EOF
{
  "schema_version": 1,
  "database": {"driver": "sqlite", "path": "$workdir/data/job-agent.db"},
  "server": {"listen": "127.0.0.1:$port"}
}
EOF

agent="$root/dist/job-agent"
migrate="$root/dist/job-agent-migrate"
"$migrate" -config "$workdir/config.json" up
"$migrate" -config "$workdir/config.json" version | grep -q ", dirty: false"

"$agent" "$workdir/config.json" >"$workdir/server.log" 2>&1 &
pid=$!
ready=false
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.2
done
if [[ "$ready" != "true" ]]; then
  cat "$workdir/server.log" >&2
  echo "server did not become healthy" >&2
  exit 1
fi

curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null
curl -fsS "http://127.0.0.1:$port/api/v1/version" >/dev/null
curl -fsS "http://127.0.0.1:$port/metrics" | grep -q job_agent_uptime_seconds
for endpoint in \
  /api/v1/dashboard/summary \
  /api/v1/jobs \
  /api/v1/review-sessions \
  /api/v1/profile-state/resources \
  /api/v1/profile-state/revisions; do
  curl -fsS "http://127.0.0.1:$port$endpoint" >/dev/null
done
curl -fsS -H "X-Request-ID: smoke-trace" "http://127.0.0.1:$port/healthz" >/dev/null
grep -q '"request_id":"smoke-trace"' "$workdir/server.log"

kill "$pid"
wait "$pid" 2>/dev/null || true
pid=""
"$agent" db backup --config "$workdir/config.json" --output "$workdir/backup.db" | grep -q "^BACKUP "
rm "$workdir/data/job-agent.db"
"$agent" db restore --config "$workdir/config.json" --input "$workdir/backup.db" --force | grep -q "^RESTORE "
"$migrate" -config "$workdir/config.json" version | grep -q ", dirty: false"

echo "SMOKE OK"

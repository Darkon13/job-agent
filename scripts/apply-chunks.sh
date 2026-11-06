#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
CHUNK="${1:-8}"
ROUNDS="${2:-1000}"
go build -o /tmp/hhapi-batch ./cmd/hhapi-batch
for _ in $(seq 1 "$ROUNDS"); do
  ./scripts/refresh-cookies.sh || true
  out="$(/tmp/hhapi-batch -token "$HOME/.config/job-agent/hh-token.json" -db data/job-agent.db -limit "$CHUNK" 2>&1 | tee /dev/stderr)"
  if grep -q '^CAPTCHA:' <<<"$out"; then
    echo "Капча ждёт решения: go run ./cmd/hhapi-captcha -solve"
    while [ -s data/captcha-pending.txt ]; do sleep 10; done
    echo "Капча решена, продолжаю"
    continue
  fi
  grep -q 'done:' <<<"$out" || { echo "batch stopped"; exit 0; }
done

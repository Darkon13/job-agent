#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
CHUNK="${1:-8}"
ROUNDS="${2:-25}"
go build -o /tmp/hhapi-batch ./cmd/hhapi-batch
for _ in $(seq 1 "$ROUNDS"); do
  out="$(/tmp/hhapi-batch -token "$HOME/.config/job-agent/hh-token.json" -db data/job-agent.db -limit "$CHUNK" 2>&1 | tee /dev/stderr)"
  if grep -q '^CAPTCHA:' <<<"$out"; then
    echo "Капча сохранена: go run ./cmd/hhapi-captcha -solve"
    exit 0
  fi
  grep -q 'done:' <<<"$out" || { echo "batch stopped"; exit 0; }
done

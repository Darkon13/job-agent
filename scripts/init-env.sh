#!/usr/bin/env sh
# Creates .env from .env.example with freshly generated secrets. With --print
# it only prints the two lines so the operator can paste them into .env by
# hand. The script never overwrites an existing .env: rotate tokens by editing
# the file instead.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

mode="write"
case "${1:-}" in
  --print|-p) mode="print" ;;
  "") ;;
  *)
    echo "usage: scripts/init-env.sh [--print]" >&2
    exit 2
    ;;
esac

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
    return
  fi
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
}

browser_worker_token="$(random_hex)"
api_token="$(random_hex)"

if [ "$mode" = "print" ]; then
  printf 'BROWSER_WORKER_TOKEN=%s\nJOB_AGENT_API_TOKEN=%s\n' "$browser_worker_token" "$api_token"
  echo "скопируйте эти строки в .env (или выполните scripts/init-env.sh без флагов)" >&2
  exit 0
fi

if [ -f .env ]; then
  echo ".env already exists; edit it to rotate tokens" >&2
  exit 1
fi

sed \
  -e "s|^BROWSER_WORKER_TOKEN=.*|BROWSER_WORKER_TOKEN=${browser_worker_token}|" \
  -e "s|^JOB_AGENT_API_TOKEN=.*|JOB_AGENT_API_TOKEN=${api_token}|" \
  .env.example > .env
chmod 600 .env

echo "created .env with generated BROWSER_WORKER_TOKEN and JOB_AGENT_API_TOKEN"

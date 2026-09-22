#!/usr/bin/env sh
# Creates .env from .env.example with freshly generated secrets. The script
# never overwrites an existing .env: rotate tokens by editing the file instead.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

if [ -f .env ]; then
  echo ".env already exists; edit it to rotate tokens" >&2
  exit 1
fi

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
    return
  fi
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
}

browser_worker_token="$(random_hex)"
api_token="$(random_hex)"

sed \
  -e "s|^BROWSER_WORKER_TOKEN=.*|BROWSER_WORKER_TOKEN=${browser_worker_token}|" \
  -e "s|^JOB_AGENT_API_TOKEN=.*|JOB_AGENT_API_TOKEN=${api_token}|" \
  .env.example > .env
chmod 600 .env

echo "created .env with generated BROWSER_WORKER_TOKEN and JOB_AGENT_API_TOKEN"

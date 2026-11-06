#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
WT="$(grep '^BROWSER_WORKER_TOKEN=' .env | cut -d= -f2-)"
curl -s -m 30 -H "Authorization: Bearer $WT" http://127.0.0.1:8088/v1/profiles/main/storage-state > /tmp/hh-state.json || exit 0
python3 - <<'PY'
import json
from pathlib import Path
data = json.load(open('/tmp/hh-state.json'))
lines = ["# Netscape HTTP Cookie File"]
for cookie in data.get('cookies', []):
    domain = cookie.get('domain', '')
    if 'hh.ru' not in domain:
        continue
    expires = cookie.get('expires', 0)
    if expires and expires > 0:
        expires = int(expires)
    else:
        expires = 0
    secure = 'TRUE' if cookie.get('secure') else 'FALSE'
    lines.append('\t'.join([domain, 'TRUE', cookie.get('path', '/'), secure, str(expires), cookie.get('name', ''), cookie.get('value', '')]))
Path.home().joinpath('.config/job-agent/hh-cookies.txt').write_text('\n'.join(lines) + '\n')
PY

#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p backups
stamp="$(date +%Y%m%d-%H%M)"
python3 - "$stamp" <<'PY'
import sqlite3, sys
src = sqlite3.connect('data/job-agent.db')
dst = sqlite3.connect(f'backups/job-agent-{sys.argv[1]}.db')
src.backup(dst)
dst.close(); src.close()
PY
ls -t backups/job-agent-*.db 2>/dev/null | tail -n +15 | xargs -r rm -f

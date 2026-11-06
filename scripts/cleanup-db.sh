#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import sqlite3, time
db = sqlite3.connect('data/job-agent.db')
cutoff = int((time.time() - 7 * 24 * 3600) * 1_000_000_000)
n = db.execute("UPDATE tasks SET status='dismissed', updated_at=? WHERE status='failed' AND updated_at < ?", (time.time_ns(), cutoff)).rowcount
db.execute("DELETE FROM captcha_pending" if False else "SELECT 1")
db.commit()
print(f"dismissed old failed tasks: {n}")
PY

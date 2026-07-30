#!/bin/zsh
set -euo pipefail

db="${KENT_REPAIR_DB:-/Users/nek/.kent/db/main.sqlite3}"
backup_dir="${KENT_REPAIR_BACKUP_DIR:-$(dirname "$db")}"
server_port="${KENT_REPAIR_SERVER_PORT:-53082}"
repair_sql="/tmp/kent-v60-new-session-offline-repair-20260730.sql"
legacy_backup="/Users/nek/.kent/db/main.sqlite3.pre-v60-20260729T1440+0200.backup"
stamp="$(date +%Y%m%dT%H%M%S%z)"
backup="$backup_dir/$(basename "$db").pre-current-node-repair-$stamp.backup"
staged_backup="/tmp/$(basename "$backup").partial"

if lsof -nP -iTCP:"$server_port" -sTCP:LISTEN >/dev/null; then
    echo "ABORT: Kent is still running on port $server_port."
    exit 1
fi

if [[ ! -f "$db" ]]; then
    echo "ABORT: database does not exist: $db"
    exit 1
fi
if [[ ! -d "$backup_dir" ]]; then
    echo "ABORT: backup directory does not exist: $backup_dir"
    exit 1
fi
if [[ ! -f "$repair_sql" ]]; then
    echo "ABORT: repair SQL does not exist: $repair_sql"
    exit 1
fi
if [[ ! -f "$legacy_backup" ]]; then
    echo "ABORT: authoritative pre-V60 backup does not exist: $legacy_backup"
    exit 1
fi
if [[ -e "$backup" || -e "$staged_backup" ]]; then
    echo "ABORT: backup destination already exists."
    exit 1
fi

# An offline WAL-mode database may have no -shm sidecar. A read-only connection
# cannot initialize that bookkeeping and fails with "unable to open database
# file". The listener guard above guarantees Kent is stopped, so open the
# database normally for SQLite's backup operation.
sqlite3 "$db" ".timeout 5000" ".backup '$staged_backup'"
if [[ ! -s "$staged_backup" ]]; then
    echo "ABORT: SQLite created an empty safety backup: $staged_backup"
    exit 1
fi
mv "$staged_backup" "$backup"
echo "Backup created: $backup"

sqlite3 "$db" <"$repair_sql"
echo "Repair completed."

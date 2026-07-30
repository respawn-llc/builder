#!/bin/zsh
set -euo pipefail

db="${KENT_REPAIR_DB:-/Users/nek/.kent/db/main.sqlite3}"
persistence_root="${KENT_REPAIR_PERSISTENCE_ROOT:-$(dirname "$(dirname "$db")")}"
backup_dir="${KENT_REPAIR_BACKUP_DIR:-$(dirname "$db")}"
repair_sql="${KENT_REPAIR_SQL:-/tmp/kent-v60-new-session-offline-repair-20260730.sql}"
legacy_backup="${KENT_REPAIR_LEGACY_BACKUP:-/Users/nek/.kent/db/main.sqlite3.pre-v60-20260729T1440+0200.backup}"
root_lock="$persistence_root/app-server.lock"
stamp="$(date +%Y%m%dT%H%M%S%z)"
backup="$backup_dir/$(basename "$db").pre-current-node-repair-$stamp.backup"
staged_backup="/tmp/$(basename "$backup").partial"

if [[ ! -d "$persistence_root" ]]; then
    echo "ABORT: persistence root does not exist: $persistence_root"
    exit 1
fi

if [[ "${KENT_REPAIR_ROOT_LOCK_HELD:-0}" != "1" ]]; then
    export KENT_REPAIR_ROOT_LOCK_HELD=1
    exec perl -MFcntl=:flock -e '
        use strict;
        use warnings;

        my $lock_path = shift @ARGV;
        open my $lock, ">>", $lock_path or do {
            print STDERR "ABORT: cannot open Kent persistence root lock $lock_path: $!\n";
            exit 1;
        };
        if (!flock($lock, LOCK_EX | LOCK_NB)) {
            print STDERR "ABORT: Kent persistence root is already owned: $lock_path\n";
            exit 1;
        }
        my $status = system @ARGV;
        if ($status == -1) {
            print STDERR "ABORT: failed to launch locked repair: $!\n";
            exit 1;
        }
        if ($status & 127) {
            exit 128 + ($status & 127);
        }
        exit $status >> 8;
    ' "$root_lock" "$0" "$@"
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

# The app-server root lock above is Kent's process-wide persistence ownership
# fence. Holding it keeps every server topology out while backup and repair use
# ordinary SQLite connections.
sqlite3 "$db" ".timeout 5000" ".backup '$staged_backup'"
if [[ ! -s "$staged_backup" ]]; then
    echo "ABORT: SQLite created an empty safety backup: $staged_backup"
    exit 1
fi
mv "$staged_backup" "$backup"
echo "Backup created: $backup"

sqlite3 "$db" <"$repair_sql"
echo "Repair completed."

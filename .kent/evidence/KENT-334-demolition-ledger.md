# KENT-334 Demolition Ledger

Integrated base: `3c7d45a62`

## Gate

The final handwritten non-test production diff must be net-negative. Tests,
generated SQL, migrations, documentation, and `.kent` working evidence are
excluded from this calculation.

## Final inventory — 2026-07-27

- Recompute the ledger after every coherent commit from `3c7d45a62` with the
  command below. It includes untracked handwritten production files and
  excludes tests, generated SQL, migrations, documentation, `.kent`, and the
  frozen `tui-rs/` tree.
- Final dirty-worktree snapshot at `2026-07-27T21:52:05+0200`,
  `HEAD=d7875e6321b20afcab6a0b4998200006cff0cd02`, production-patch SHA-256
  `457f7bdaf247f0f94ff93d5ac0c6126de111c9d12f7fc17bb4c3f03948ac185c`:
  221 included files, `+13107 / -13630`, net `-523`.
- The net-negative gate passes. Manual QA and deployment are excluded from the
  takeover goal. The clean full server/desktop invocation was assertion-clean
  through the fixed cap but cap-incomplete; the User explicitly deferred that
  cap on 2026-07-27, and the packages unfinished at cutoff passed together in
  focused verification. The remaining non-Rust tests, CI checks, docs
  verification, and builds are recorded in `KENT-334-cutover-checklist.md`.

## Latest main integration inventory — 2026-07-28

`origin/main` advanced by 33 commits and was merged at `35cf25ea3`. Those
upstream commits add handwritten production code independently of KENT-334, so
the PR-owned inventory is measured against the merged `origin/main`. After that
verification, `origin/main` advanced again through PR `#653` and was integrated
at `53aa3ea09`. The final second-merge inventory is:

- snapshot at `2026-07-28T17:47:52+0200`;
- 195 included handwritten production files;
- `+12343 / -14088`, net `-1745`;
- production-patch SHA-256
  `3e3d771102f3fe188469f8fe5d2280506b139edc22bc5cc5a1f0277842c42783`.

The KENT-owned net-negative gate passes. For attribution clarity, current
`origin/main` contributes net `+2740` handwritten production lines relative to
the historical `3c7d45a62` base. Combining that upstream delta with KENT-334's
net `-1745` yields the historical-base aggregate net `+995`; that aggregate is
not KENT-334-owned growth.

For post-main-integration recomputation, run the inventory command below with
`base=origin/main`. The historical `3c7d45a62` command remains below as the
pre-integration record.

```sh
set -eu -o pipefail
base=3c7d45a62

included() {
  file_path=$1
  case "/$file_path/" in
    */.kent/*|*/docs/*|*/tui-rs/*|*/server/metadata/migrations/*|\
    */server/metadata/sqlitegen/*|*/internal/testharness/*|\
    */__tests__/*|*/__fixtures__/*|*/testdata/*|*/fixtures/*|\
    */test-support/*|*/tests/*)
      return 1
      ;;
  esac
  case "$file_path" in
    server/metadata/queries.sql|*_test.go|*.test.*|*.spec.*|*.snap|\
    AGENTS.md|*/AGENTS.md|README*|*/README*|CHANGELOG*|*/CHANGELOG*|\
    CONTRIBUTING*|*/CONTRIBUTING*|SECURITY*|*/SECURITY*|\
    CODE_OF_CONDUCT*|*/CODE_OF_CONDUCT*)
      return 1
      ;;
  esac
  return 0
}

{
  git diff --numstat --no-renames "$base" -- .
  git ls-files --others --exclude-standard |
    while IFS= read -r file_path; do
      [ -f "$file_path" ] || continue
      lines=$(awk 'END { print NR }' "$file_path")
      printf '%s\t0\t%s\n' "$lines" "$file_path"
    done
} |
while IFS=$'\t' read -r added deleted file_path; do
  included "$file_path" || continue
  case "$added:$deleted" in
    *-*)
      printf 'binary production path requires manual accounting: %s\n' \
        "$file_path" >&2
      exit 2
      ;;
  esac
  printf '%s\t%s\t%s\n' "$added" "$deleted" "$file_path"
done |
awk -F '\t' '
{
  added += $1
  deleted += $2
  files += 1
}
END {
  printf "files=%d added=%d deleted=%d net=%+d\n",
         files, added, deleted, added - deleted
}'

{
  {
    git diff --name-only --no-renames "$base" -- .
    git ls-files --others --exclude-standard
  } |
    sort -u |
    while IFS= read -r file_path; do
      included "$file_path" || continue
      if git cat-file -e "$base:$file_path" 2>/dev/null ||
          git ls-files --error-unmatch "$file_path" >/dev/null 2>&1; then
        git diff --binary --no-renames "$base" -- "$file_path"
      elif [ -f "$file_path" ]; then
        printf 'UNTRACKED %s\n' "$file_path"
        shasum -a 256 "$file_path"
      fi
    done
} |
  shasum -a 256

git rev-parse HEAD
```

## Server/API demolition evidence

- The Current Node controller owns admission, live-scope completion,
  interruption, restart recovery, and automatic successor release. Legacy
  scheduler, automatic-registration, Run/Placement store, transition-history,
  cancellation, and server read-model paths have been removed.
- Direct Session Task ownership plus Current Node binding is the durable
  server authority for current-session inspection. The remaining references
  to removed Task Cancel behavior are isolated to the explicitly deferred Go
  remote client and CLI.

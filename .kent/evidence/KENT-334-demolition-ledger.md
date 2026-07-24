# KENT-334 Demolition Ledger

Integrated base: `3c7d45a62`

## Gate

The final handwritten non-test production diff must be net-negative. Tests,
generated SQL, migrations, documentation, and `.kent` working evidence are
excluded from this calculation.

## Current inventory — 2026-07-24

- The earlier `+5981 / -1412` count is not accepted evidence because it has no
  command, commit, or worktree-status hash and the worktree was changing.
- Recompute the ledger after every coherent commit from `3c7d45a62` with the
  command below. It includes untracked handwritten production files and
  excludes tests, generated SQL, migrations, documentation, `.kent`, and the
  frozen `tui-rs/` tree.
- This is an interim implementation count, not a passing result.
- Snapshot at `2026-07-24T13:43:47Z`,
  `HEAD=791217047b82444e4a7e40e1bf9b2b8a51edbc8b`, production-patch SHA-256
  `c5d1b0e348bed561856bd43d42ff3cc1f0971867e6038a76d4bfc70ebaf51899`:
  62 included files, `+6442 / -1356`, net `+5086`. The gate is failing.
- Remaining high-value removals: legacy scheduler/reconciliation,
  `AutomaticStartRegistration`, Run/Placement store/domain/query paths,
  Run-bearing runtime/transcript/session contracts, legacy transition-history
  projections, and their production client adapters.

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

## Replacement present; demolition incomplete

- Current Node controller implementations exist for admission, live-scope
  completion, interruption, restart recovery, and automatic successor intent
  release. The legacy scheduler and automatic registration remain in
  production composition, so execution authority is not yet singular.
- Direct Session Task ownership plus Current Node binding replaces persisted
  workflow Run metadata for current-session inspection. Run-bearing
  runtime/status consumers remain and are not counted as demolished.

# KENT-334 Demolition Ledger

Integrated base: `3c7d45a62`

## Gate

The final handwritten non-test production diff must be net-negative. Tests,
generated SQL, migrations, documentation, and `.kent` working evidence are
excluded from this calculation.

## Final inventory — 2026-07-26

- Recompute the ledger after every coherent commit from `3c7d45a62` with the
  command below. It includes untracked handwritten production files and
  excludes tests, generated SQL, migrations, documentation, `.kent`, and the
  frozen `tui-rs/` tree.
- Final dirty-worktree snapshot at `2026-07-26T18:05:02+0200`,
  `HEAD=62a6e55775deee1b5e214a563ae420ffe0774ca0`, production-patch SHA-256
  `ef10dd89a4d1d9f99a5495068f105f72397124236bdc324879ec7a4f800743f2`:
  206 included files, `+10908 / -13520`, net `-2612`.
- The net-negative gate passes. Manual QA and deployment are excluded from the
  takeover goal; all scoped non-Rust tests, CI checks, docs verification, and
  builds are recorded in `KENT-334-cutover-checklist.md`.

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

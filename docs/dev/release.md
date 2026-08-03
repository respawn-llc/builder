# Release

This is the current release flow for `kent`.

## Recommended Path

Use `workflow_dispatch`. It is the simplest path and does not require the `autorelease` PR label flow.

1. Make sure the release commit is on `main` and pushed.
2. Set `VERSION` to the release version, usually without the `v` prefix, for example:

```text
0.2.0
```

3. Commit and push that change.
4. Trigger the release workflow:

```bash
gh workflow run release.yml --repo respawn-llc/kent
```

5. Wait for the `release` workflow in `respawn-llc/kent` to finish.
6. Wait for the tap automation in `respawn-llc/homebrew-tap` to finish.
7. Verify the GitHub release and Homebrew install.

## Release Notes

### Audience Boundary

Kent's server API, RPC methods, transport contracts, and protocol generations are internal interfaces used only by official Kent clients. Public release notes must not expose route names, protocol versions, wire schemas, DTO changes, or RPC contract changes.

Translate client/server compatibility changes into user actions, such as upgrading and restarting the Kent server, CLI/TUI, and Desktop together. Treat an interface as a public third-party integration API only when an owning product specification and public documentation explicitly define that support.

Announce only capabilities available through released Kent artifacts or public documentation. Exclude private repository helpers, operator scripts, and internal automation.

The exact workflow-generated changelog appendix described below is the sole exception to this curated audience boundary. Keep it collapsed and verbatim, and do not copy its internal commit terminology into the curated notes.

### Editorial Structure

GitHub renders the release name and tag above the body. Start the body with its opening summary; do not repeat the release title as a Markdown H1.

Lead with the release's new capabilities and user-visible improvements. Put routine coordinated-upgrade guidance after the highlights. Lead with an upgrade warning only when users must act before upgrading to prevent data loss, outage, security exposure, or a broken installation.

Use release voice in the curated body. The opening says what the version adds or changes, and entries use verbs such as `Added`, `Changed`, `Removed`, and `Fixed`. Do not apply evergreen documentation voice to a versioned changelog. Avoid `now` and `no longer`; write `Changed` or `Removed` and name the previous public behavior when migration requires it.

Curated entries describe outcomes, not mechanisms. Name what users can do, what they see, what failed in the previous release, or what action they must take. Terms such as transactions, schedulers, projections, runtime ownership, transport streams, RPCs, protocol versions, DTOs, and review remediation do not belong in the curated body unless a documented third-party integration makes the term part of the public contract.

Examples:

- `Added Project-scoped Task Search across Task titles, bodies, and Comments.`
- `Fixed resumed TUI Sessions crashing when their transcript was temporarily unavailable.`
- `Removed --page-token; use --offset and --limit.`

Do not write:

- `Task Search supports...` — evergreen documentation voice does not identify the release change.
- `Reserved a write transaction before interruption.` — implementation mechanics do not state the user outcome.
- `Improved Task Search selection stability.` — stabilization of a capability first shipped in this release is not a separate release item.

### Kent Implementation-Detail Examples

These 25 examples came from the `v2.4.0...v2.5.0` source diff. Each entry quotes an exact commit subject and its short hash. Do not copy them into curated notes. Apply the stated rewrite or omit the item.

1. `feat: centralize metadata SQLite extensions` (`daba3d3f6`) — omit; storage composition is not a user outcome.
2. `feat: add task search index schema foundation` (`65b694630`) — fold into `Added Task Search`; do not announce index structure.
3. `feat: add task search index creation triggers` (`02101ff5f`) — fold into `Added Task Search`; do not announce database triggers.
4. `feat: synchronize task search source edits` (`c9d9f3626`) — write `Task and Comment edits appear in search immediately` only when public behavior requires that guarantee.
5. `feat!: add task search RPC` (`e8a7aa299`) — announce the released Desktop or CLI search surface, not its transport.
6. `fix!: bump task search protocol version` (`0dd37d78e`) — state the coordinated client/server upgrade action without naming protocol generations.
7. `feat: expose revisioned workflow observations` (`ea71fa018`) — translate to the visible Task-status behavior or omit.
8. `feat: capture stable workflow status snapshots` (`4c7b7f98f`) — combine with the same Task-status outcome; do not add a second entry.
9. `fix: reserve manual move write transaction` (`ec0012dbd`) — fold into the final Manual Move capability or omit as same-release stabilization.
10. `fix: serialize manual move previews` (`9e1383cd5`) — fold into the final Manual Move capability or omit as same-release stabilization.
11. `fix: defer workflow successors until scope retirement` (`55e35b61d`) — write the prior-release workflow-overlap defect only when users could encounter it; otherwise omit.
12. `fix: admit workflow runs after runtime preparation` (`1d027863b`) — write the prior-release failure to start work only when users could encounter it; otherwise omit.
13. `fix: release gate-owned agent capacity` (`2336bc48d`) — translate to the documented `workflow.concurrency` behavior.
14. `refactor: share keyed mutation lanes` (`afd3381c4`) — write `Concurrent edits do not overwrite newer changes` when that previous-release defect is verified.
15. `fix!: make transcript batch publication atomic` (`c315a1a5e`) — write the visible partial-transcript defect when users could encounter it; otherwise omit.
16. `fix: preserve repaired tool step identity` (`b31094c0c`) — write `Fixed Sessions with interrupted tool calls reopening with transcript corruption or crashing`.
17. `fix: deduplicate stream terminal resets` (`5e0b57c7e`) — write `Fixed failed model responses duplicating or leaving partial assistant output`.
18. `fix: initialize transcript feed before wake` (`856c68e87`) — write the resumed-TUI crash once, in user terms.
19. `fix: wait for transcript runtime wake` (`c0955534f`) — merge with the same resumed-TUI crash entry; do not duplicate it.
20. `fix: publish question state before releasing waiters` (`5ac765f8f`) — write the missing or stale Question behavior when it existed in the previous public release.
21. `fix: memoize workflow question acceptance` (`6d2191c94`) — omit; this is internal stabilization.
22. `fix: classify local providers as openai compatible` (`dbcc53f15`) — state the new-Session action for local or custom OpenAI-compatible endpoints without describing provider classification.
23. `fix: centralize worktree create ownership` (`d28dff3ee`) — write `Added distinct TUI feedback for Base-ref errors during Worktree Create`.
24. `fix: move worktree deletion validation to contract owner` (`ab68f744c`) — omit; an internal validation owner is not a user outcome.
25. `fix: remove workflow identity sentinels` (`ed93c1e2a`) — omit; internal identity cleanup is not a user outcome.

### Curated Notes And Generated Changelog

The release workflow creates a generated changelog. A rewritten release body has two distinct parts:

1. A curated public section that follows the Audience Boundary and Coverage Gate.
2. The workflow-generated changelog preserved verbatim under `<details><summary>Generated changelog</summary>`.

The generated appendix is source history, not authority for curated product claims. Do not promote its commit titles into the curated body without independently passing the public-surface and previous-release-baseline checks.

Keep the generated appendix byte-for-byte unchanged while editing the curated section. Label a GitHub comparison link `Source comparison`; do not call the link a full changelog when the generated changelog is absent.

If the appendix was lost or overwritten, regenerate it with the exact action version and configuration pinned by the release workflow for that tag range. Do not hand-reconstruct it from local commit subjects.

### Coverage Gate

Before drafting:

1. Identify the previous public release tag.
2. Inventory merged PRs, linked issues, release commits, migration notes, public docs, measured outcomes, and user-facing safety changes.
3. Classify each item as a public capability, a bug users could encounter in the previous public release, required user action, same-release stabilization, or private/internal work.
4. Verify that each public capability, prior-release bug fix, measured outcome, and required action appears exactly once in the draft.
5. Verify that same-release stabilization and private/internal items are absent from the draft.

Do not classify release-note content from commit prefixes alone. A `fix:` commit can stabilize a feature first shipped in the same release, while a technical implementation change can contain a major user-facing storage, performance, or safety improvement.

Preserve exact verified measurements and explain their user benefit. Treat protections against incorrect user work as public capabilities even when implemented through reminders, model context, validation, or guardrails.

Use the previous public release as the baseline for `Bug Fixes`. Fixes made while developing features first shipped in the same release are stabilization work, not public bug fixes; describe the final capability once under the appropriate highlight.

The remaining sections document internal release operations. Their repository scripts and local cleanup commands are not public release-note content unless they produce a separately verified public product capability.

### Draft, Review, And Publication Gate

Do not draft by repeatedly editing a published release.

1. Capture the existing release body and exact generated changelog before making an edit.
2. Build the coverage ledger and complete the curated body in a scratch file.
3. Append or preserve the exact generated changelog according to the section above.
4. Verify every curated capability against a released client surface or public documentation. An internal API, specification-only design, or unreleased client implementation is insufficient.
5. Run an independent general-purpose editorial reviewer with this document and the release-notes and docs-writing skills. Do not use a code-review role. Scope the review to the curated section and state the exact generated appendix exemption.
6. Apply findings and continue the same reviewer until it returns exactly `CLEAN`.
7. Publish the completed body once.
8. Re-read the published body and verify the generated appendix still matches the workflow output byte-for-byte.

If independent review is unavailable, do not rewrite an already-published release until a second cold editorial pass has checked the same criteria.

## What The App Release Workflow Does

The `release` workflow in `/.github/workflows/release.yml`:

1. Reads `VERSION`.
2. Normalizes it to a git tag `vX.Y.Z`.
3. Creates and pushes the tag if it does not already exist.
4. Builds static release binaries through `scripts/build.sh` with the shared release profile.
5. Packages the release archives and writes `checksums.txt` through `scripts/release-artifacts.sh`.
6. Verifies the checksum manifest and smoke-tests packaged binaries on Linux, macOS, and Windows before publishing.
7. Smoke-tests the Windows installer against staged release assets before publishing.
8. Publishes the GitHub release.
9. Checks out `respawn-llc/homebrew-tap`.
10. Runs `scripts/update-brew-tap.sh` for formula `kent` (and, once `--desktop-url`
    is passed with the published macOS `.dmg`, regenerates the `kent-desktop` cask).
11. Opens a PR in the tap repo with label `pr-pull`.

## What The Tap Automation Does

The tap repo automation is part of the release, not an optional follow-up.

1. The tap PR runs `brew test-bot`.
2. On success, `brew pr-pull` runs.
3. `brew pr-pull` pushes bottle metadata to tap `master`.
4. After that, `brew update && brew install kent` should resolve to the new version.

## Recovery If The Tap Step Fails After Publish

If the app release workflow publishes `vX.Y.Z` successfully but fails in `update_brew_tap`, do not cut a second app release.

1. Fix the workflow or tap updater script on `main` first if the failure is in release plumbing.
2. Create the tap change manually from this repo using `scripts/update-brew-tap.sh` against a fresh clone of `respawn-llc/homebrew-tap` on a branch like `chore/kent-vX.Y.Z`.
3. Open the tap PR with label `pr-pull`.
4. Wait for `brew test-bot`, then `brew pr-pull`, and only then consider the release complete.

## Verification

Verify all of these before considering the release done:

1. The GitHub release `vX.Y.Z` exists in `respawn-llc/kent` and contains the expected assets plus `checksums.txt`.
2. The tap PR in `respawn-llc/homebrew-tap` is closed by the automation.
3. The formula on tap `master` has the new tag URL and bottle block.
4. A standalone Unix install works and passes checksum verification when the release publishes `checksums.txt`:

```bash
curl -fsSL https://kent.sh/install.sh | sh
kent --version
```

5. A standalone Windows install works and passes checksum verification:

```powershell
irm https://kent.sh/install.ps1 | iex
kent --version
```

6. A fresh Homebrew install works after `brew update`:

```bash
brew update
brew tap respawn-llc/tap
brew install kent
kent --version
```

If short-name resolution is stale on a machine, use the fully qualified formula name:

```bash
brew install respawn-llc/tap/kent
```

7. Clean up the Homebrew and direct installs to restore your local development build:

```bash
brew uninstall kent 2>/dev/null || true
sudo rm -f /usr/local/bin/kent
which kent # should point to your local development bin directory, e.g. ./bin/kent
```

## Notes

- Installed binary name stays `kent`. Formula name is `kent`.
- Official release targets are `darwin/arm64`, `linux/amd64`, `linux/arm64`, `windows/amd64`, and `windows/arm64`. macOS Intel is unsupported.
- The smoke-test workflow uses `*-latest` where GitHub provides it; ARM still requires the pinned hosted-runner labels `ubuntu-24.04-arm` and `windows-11-arm` because GitHub does not publish `ubuntu-latest-arm` or `windows-latest-arm` aliases.
- Do not create the git tag manually unless you are intentionally bypassing the workflow behavior.
- Linux release binaries must stay statically linked; do not switch the release pipeline to PIE or other dynamic-linking modes.
- Keep archive packaging and release verification logic in `scripts/release-artifacts.sh`; `release.yml` should stay orchestration-focused.
- When polling workflows, use long poll times (10-20 minutes). Avoid short polls or waits.

## Alternate Path

The workflow can also run automatically when a merged PR carries the `autorelease` label. That path uses the same workflow and the same downstream tap automation.

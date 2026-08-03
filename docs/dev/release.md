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

### Rejected Curated-Note Details

The following 25 entries quote the actual wording from rejected `v2.5.0` curated revisions. They demonstrate implementation mechanics, generic interaction details, unsupported surfaces, duplicate facts, and same-release stabilization that do not belong in curated release notes.

1. **Rejected:** “Desktop boards include a Project-scoped Task Search command palette across every linked Workflow. Search covers Task titles, complete bodies, and Comments; preserves ranked Task grouping; loads results through infinite scroll; and opens the selected Task in Task Detail. `Command-S`, `Control-S`, and `Alt-Space` open Search, with stable keyboard and pointer selection.” **Rule:** announce the searchable content and result destination; omit infinite scroll and standard list-navigation mechanics.
2. **Rejected:** “`kent task search` provides global or Project-scoped search with status filters, optional Comment matching, case-sensitive literal matching, raw FTS5 queries, JSON output, and offset-based infinite pagination.” **Rule:** keep supported query modes and migration-relevant flags; remove the `infinite pagination` label.
3. **Rejected:** “Searches can include Comments, filter by Task status, span several Projects, return structured JSON, and continue through bounded offsets without retaining server-side pagination state.” **Rule:** keep public filters and continuation; omit server state-management mechanics.
4. **Rejected:** “Search uses the same live-aware Task status as Task List, Workflow Board, and Task Detail, and successful Task or Comment changes become searchable in the same operation.” **Rule:** omit read-model and transaction guarantees from highlights; document them on the owning reference page.
5. **Rejected:** “Tasks own their active Workflow state directly through Current Nodes. A Task normally has one Current Node and can have several during fan-out; Agent Current Nodes bind to retained Sessions, while Script Current Nodes retain the state required to resume their script. Task Detail exposes Current Nodes, retained Session count, exact live work, and an infinite-scroll activity stream.” **Rule:** announce the added Task Detail and Activity information; omit ownership, persistence, and infinite-scroll mechanics.
6. **Rejected:** “Workflow transitions preserve Session assignment, agent role, completion mode, incoming values, continuation sources, and previous-target Sessions across sequential work, loops, fan-out, joins, approval, interruption, and resume.” **Rule:** summarize the observable Session-continuity improvement instead of enumerating retained state.
7. **Rejected:** “Script Nodes do not consume `workflow.concurrency`; that setting limits automatically scheduled Agent Nodes. Explicit Resume and Approval operations remain available independently of automatic Agent scheduling capacity.” **Rule:** state the configuration change; omit scheduler behavior that does not change user action.
8. **Rejected:** “Manual Move can select any valid incoming Workflow Transition instead of requiring a direct edge from the Task's Current Node. It supports authored Transition keys, required values, fan-out destinations, editable resolved values, Execution Target selection, no-op detection, and authoritative preview. The move reserves its write transaction before interrupting active work, preserves supplied values and commentary, and leaves the Task unmoved when validation or startup fails.” **Rule:** announce the added Manual Move choices and inputs; omit transaction sequencing and same-release stabilization.
9. **Rejected:** “Automatic managed-worktree paths use compact Project parents and numeric or Task Short ID leaves. Existing recorded roots and explicit roots remain authoritative. Automatic roots must stay outside the source workspace, collision handling is bounded, and failed creation removes only an empty reserved leaf while retaining partial Git state for diagnosis.” **Rule:** announce shorter generated paths; keep allocation and cleanup rules in Worktree documentation.
10. **Rejected:** “Worktree Create resolves a new branch's Base ref before mutation and places Base-ref errors beside that field in the TUI; other failures remain form-level errors with their Git diagnostics. Worktree deletion previews use the resolved topology target and report Clean, Dirty, or Unknown state, while the actual deletion rechecks the target before mutation.” **Rule:** announce visible Base-ref feedback; omit mutation ordering and unavailable client surfaces.
11. **Rejected:** “Model-visible shell post-processing limits each line to 1,000 Unicode code points in `builtin`, `user`, and `all` modes while preserving full captured logs. Local image reads run in an interruptible worker and return a recoverable error after 10 seconds instead of hanging the Session.” **Rule:** preserve the limits and user benefit; omit mode enumeration and worker implementation.
12. **Rejected:** “Manual compaction accepts requests only after completed agent work, admits queued requests at Agent Step boundaries, and preserves request order through reviewer completion and continuation.” **Rule:** state that premature Manual Compaction is rejected; omit scheduling boundaries and reviewer ordering.
13. **Rejected:** “Resume returns after durable requeue and scheduler admission instead of waiting for Session setup, Execution Target restoration, Script launch, or agent startup. A rejected enqueue restores the interrupted state.” **Rule:** write that Resume returns without waiting for startup and leaves failed work interrupted.
14. **Rejected:** “Workflow successors wait for the completed execution scope to retire, preventing overlapping ownership, stale assignments, and continuation deadlocks.” **Rule:** state a verified previous-release overlap symptom once, or omit the runtime ownership model.
15. **Rejected:** “Concurrent Project, workspace, and Workflow mutations revalidate their prepared state before commit, preventing stale operations from overwriting newer changes.” **Rule:** write that concurrent edits do not overwrite newer work.
16. **Rejected:** “The TUI hydrates the canonical transcript before waiting for runtime activity. A dormant or resumed Session waits for a wake or resume event instead of failing when a live transcript stream is unavailable.” **Rule:** write the resumed-TUI crash that users encountered; omit hydration and stream mechanics.
17. **Rejected:** “Accepted Steers survive disconnects, and background shell completions remain deliverable across runtime transitions and transient delivery failures.” **Rule:** write the lost-message or missing-notification symptom without runtime and delivery machinery.
18. **Rejected:** “Failed streamed attempts produce one reset for the active assistant stream without a duplicate terminal reset or a fabricated final assistant response.” **Rule:** write the duplicated or partial assistant-output defect.
19. **Rejected:** “Dangling tool-call repair preserves the original Agent Step when available, rejects conflicting ownership before persistence, and avoids reopening a malformed transcript that can crash projection.” **Rule:** write the transcript-corruption or reopen crash fixed for users.
20. **Rejected:** “Transcript batches reject invalid variants before publication, preventing partially applied prompt, tool, and assistant updates.” **Rule:** describe a visible partial-transcript defect only when users could encounter it; otherwise omit.
21. **Rejected:** “Fast Mode reports effective provider support without constructing a runtime that can panic on missing capabilities.” **Rule:** write that Fast Mode no longer fails during startup for unsupported providers.
22. **Rejected:** “Local and custom OpenAI-compatible endpoints use third-party-compatible message behavior instead of first-party OpenAI-only phase semantics. The corrected classification applies when creating a new Session; existing Sessions retain their original provider contract.” **Rule:** state the new-Session action without provider-classification or phase terminology.
23. **Rejected:** “CLI and Desktop surfaces omit internal prefixed Workflow identifiers that cannot be used as selectors.” **Rule:** omit internal identifiers; document only accepted public selectors when migration requires it.
24. **Rejected:** “The Current Node cutover removes public Workflow Run and Placement selectors. `kent task cancel` is replaced by `kent task interrupt`, Manual Move, or Task deletion according to intent. `kent task complete --run` is removed; forced completion selects `--session` or `--task`. `kent task approve` accepts the pending Approval ID, and Task List no longer supports `run_count` sorting.” **Rule:** list the concrete CLI changes without naming the internal cutover or removed architecture.
25. **Rejected:** “Task Detail now clears obsolete actions after refresh and treats empty Session and Script collections as valid results.” **Rule:** omit same-release stabilization of Task Detail behavior first shipped in this release.

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

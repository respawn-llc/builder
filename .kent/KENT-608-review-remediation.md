# KENT-608 review remediation

- [x] Audit the current retained boundary tests against the new review findings.
- [x] Add real retained continuation boundary coverage for assignment delivery, partial Resume, cancellation, contention, selected results/progress, rejection/history, and Thinking.
- [x] Replace the retained boundary matrix with compact real Workflow/Runtime Control cases for non-resumable/moved and TaskResumeNoOp rejection, selected assistant-final output, TUI partial failure, selected delivery/Resume failure, and pre-delegation file-backed contention.
- [x] Restore compact public retained-conflict transport/UI coverage.
- [x] Consolidate the retained-continuation scenarios into one boundary matrix. The retained suite now covers the required public boundaries and keeps the selected post-turn compaction assertion in the retained Run Prompt Thinking case.
- [x] Keep the committed test budget at or below the approved ceiling. The final audit against `966e754a4` reports 899 added test lines; the remote error-frame and exact-finalization fixtures remain required observable boundary coverage.
- [x] Run focused and full relevant package tests, `just build go`, formatting, and diff checks. Verification passed after one unrelated flaky Runtime Control test was rerun successfully:
  `go test ./server/workflowrunner ./server/runtimecontrol ./server/runprompt ./cli/app/internal/runtimeattach ./server/runtime ./server/workflowexecution ./server/workflowsvc -count=1 -timeout 10m`;
  focused shared-client/CLI conflict tests; `just build go`; and `git diff --check`.
- [x] Commit the remediation round and complete KENT-608.

## Follow-up review remediation (August 30, 2026)

- [x] Synchronize pre-Resume cancellation by receiving `done` before assertions.
- [x] Require exact persisted Current Node assignment identity in both compacted-session regressions.
- [x] Replace synthetic post-mutation sibling Resume failure with pre-mutation failure; assert exact attempted branch set and durable branch states.
- [x] Add separate headless CLI sibling-warning scenario while retaining TUI selected-only output.
- [x] Add valid old-worker delivery race and distinct selected-turn/exact-finalization error coverage.
- [x] Add selected post-turn compaction progress coverage through a compact-capable client.
- [x] Assert selected Resume failure leaves prompt history empty.
- [x] Make public conflict coverage table-driven across lifecycle states and protocol error-frame decode.
- [x] Cover retained Run Prompt Thinking mutation/effective execution for dormant and live Sessions.
- [x] Keep added test lines at or below 900 by removing redundant coverage while preserving the required boundary matrix.

## Follow-up review remediation (September 3, 2026)

- [x] Synchronize retained Thinking setup compaction and compare selected compaction against the captured post-setup baseline.
- [x] Derive fan-out completion transitions from exact branch-scoped Workflow assignment identities and verify selected/sibling durable branch outcomes.
- [x] Scope exact-finalization failure injection to the selected retained Session and verify successful sibling completion is unaffected.
- [x] Restore a controllable ordinary prompt-history gate and prove a fresh full result-wait interval after history release.
- [x] Add locked-contract assertion rejection with zero provider calls and zero prompt history, plus observable background-progress exclusion.
- [x] Replace forbidden substring guidance matching with an exact client-owned expected rendering.
- [x] Record the user's September 3, 2026 approval to retain the historical 15-test-file diff because the two-file reduction would require contradictory tests or a compatibility seam.

## Second follow-up review remediation (September 3, 2026)

- [x] Wait for retained setup compaction's existing idle boundary before capturing the post-setup compaction baseline.
- [x] Restore scenario-specific durable Current Node scheduling-state assertions for retained branch failures.
- [x] Add a public selected-continuation progress case proving an unregistered background Step is excluded from user-visible progress.
- [x] Configure authorization-denial Run Prompt coverage with the metadata-backed history owner and assert zero selected-session history.
- [x] Keep the ordinary history timeout regression's technical persistence wait separate from its fresh selected-result wait.

## Third follow-up review remediation (September 4, 2026)

- [x] Restore explicit first/second selected provider request assertions for older pending input while preserving the same-resource background overlap case.
- [x] Keep the complete ticket's added-test-line budget decision aligned with the latest user instruction; retain the approved behavioral coverage despite the 1,072-line compliance finding.
- [x] Verify the focused retained tests, formatting, and diff checks, then commit and complete KENT-608.

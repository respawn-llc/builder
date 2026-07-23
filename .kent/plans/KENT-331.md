Recon

- Failure surface is in `server/session/store.go` and is session-wide via a per-directory `append-recovery.json` journal. `requireMetadataPersistenceLocked()` returns `s.recoveryErr` first, then reads `s.readAppendRecoveryRecord()` and rejects if record exists and does not satisfy `(Phase == committed && digest(meta) == record.Post.SHA256)`. On mismatch it calls `closeMutationAuthorityLocked(...)`, which stores a `recoverable`-esque error in store state and poisons further metadata mutations (`ErrStoreRecoveryRequired` wrapping from `storeRecoveryError` in `server/session/persistence_observer.go`).

- Recovery record format and I/O are in `server/session/append_recovery.go`:
  - journal file: `append-recovery.json` with phases `prepared|committed`, metadata snapshots and optional event suffix identity.
  - writes use atomic temp+rename and directory sync (`writeAppendRecoveryRecord`), reads validate version/shape (`record.validate`), and clear uses remove+sync.
  - `recoverAppendTransaction()` allows open to continue when current digest matches either `record.Pre` or `record.Post`; committed phase uses suffix hash validation and may truncate events if prepared or suffix mismatched.
  - this is crash-recovery logic intended for a single owner sequence.

- Append path and metadata coupling is in `server/session/event_log_capability_append.go` and `server/session/store.go`:
  - append/update workflows call recovery checks through `persist` mutation paths (`appendAndPersist`, `persistMetaLocked`, many `mutate*` methods).
  - mutation paths are `mutationMu`-protected and local to one `Store` instance, while append-recovery file is shared by session directory.
  - this makes cross-instance overlap possible if more than one `Store` for same session sees different `digestMeta` snapshots.

- Runtime-level single-owner intention already exists around active runtime resources:
  - `server/sessionruntime/authority_resource.go` stores one `*session.Store` in each `agentResource`, and `withStore` increments/decrements callback counts to serialize callbacks per resource.
  - `server/sessionruntime/authority_store.go` gate path: `WithSessionStore` checks `a.resources[sessionID]` then calls `resource.withStore`; only if no resource exists does it `MaterializeSessionDescriptor` and run callback with that direct store.
  - `TestDormantSessionStoreCallbacksAreSerialized` in `server/sessionruntime/authority_test.go` confirms dormant store callbacks do not overlap.

- Runtime lifecycle replacement is already serialized by gates and resource state (`server/sessionruntime/authority_execution.go`, `authority_resource.go`, `execution_target.go`):
  - `SessionAdmissionGate` blocks start during maintenance and is checked by maintenance/open paths.
  - `replaceResource` drains/retains existing runtime via `retireResourceForReplacement`.
  - `withMaintenanceResource` runs maintenance while holding the session gate; if callback returns `retire=true`, exact resource is retired.
  - `openResource` can publish ready state and registers a single `agentResource` per `sessionID`; `closeAdmittedResourceLocked` is the authoritative transition to removal.

- Session open/reopen flow is in `server/session/store.go` (`MaterializeSessionDescriptor` -> `OpenByID`/`Open` -> `openPersistedSession` -> `recoverAppendTransaction`).
  - On open, store recovery can mutate `s.meta` to pre/post states before normal mutation gates start.

- Event append recovery already has metadata-only covered tests and materialization behavior:
  - `server/session/metadata_boundary_test.go` for metadata-only recovery transitions.
  - `server/session/store_part2_test.go` around missing events file behavior and event-log reconciliation failure pathways.
  - `server/session/current_event_log_test.go` and `server/session/event_append_cursor_test.go` exercise committed/replayed event behavior.
  - `server/session/store_test.go` covers committed/uncommitted append receipts and observer failure interactions.

- Concurrency/replacement test affordances already present:
  - `server/sessionruntime/authority_test.go` has lifecycle/retirement/maintenance tests around `replaceResource`, `retireResourceForReplacement`, and gate-backed maintenance.
  - `server/sessionruntime/lifecycle_test.go` includes `TestAuthoritySyncExecutionTargetRecoversOrRetiresAfterPersistenceFailure` and `TestAuthorityBlocksSessionStartsDuringMaintenance` for maintenance/race behavior under pinned runtime.
  - `server/sessionruntime/authority_execution.go` and `execution_target.go` are likely places for deterministic hooks around overlapping callbacks/restarts.

Design

Approved smallest-sufficient scope card:

- Observable outcome: mutable launch/workflow planning for an existing Session and runtime activation/replacement for that same Session no longer use overlapping writable Stores. Every event or metadata mutation reported committed by those paths remains present after the race and after restart; genuinely irreconcilable recovery identifies the preserved Session artifacts and conflicting digests.
- Route only the verified mutable existing-Session planning and prepared-override paths through the existing `sessionruntime.Authority.WithSessionStore` gate. Reuse the runtime-owned Store when a resource exists and materialize one dormant Store only while admitted.
- Re-check cancellation after the existing gate opens so a canceled waiter performs no mutation. Do not replace the gate, add a mutation-transfer queue, retry stale Store operations, or add another coordinator.
- Remove the launch plan's unused append-capable event-log value and the planner behavior that reopens/rematerializes the planned existing Session after admission.
- Keep runtime replacement's existing drain-before-successor behavior and keep runtime commands bound to their Exact Execution Scope and Resource Generation.
- Keep the append-recovery journal algorithm and format unchanged. Preserve the existing crash-loss allowance of up to one in-progress model step; no-loss verification covers acknowledged committed mutations.
- For genuinely irreconcilable durable state, preserve all files, block only that Session, and enrich the existing recovery error with Session identity, recovery/events paths, current and recovery metadata digests, and available suffix identity.
- Verification is limited to one deterministic reproduction of the verified planning/runtime collision, focused restart recovery, race execution, and event/metadata no-loss assertions.
- Non-goals: passive Session-view Store opens or committed-snapshot architecture; fresh independent, fork, clone, or derived-child admission; a repository-wide Store ownership invariant or guard; cross-process locking; repair commands; protocol, SQLite, append-recovery format, migration, GUI/TUI, generated-code, or unrelated Store-caller refactors.
- Estimate: production 350–650 changed lines across 5–9 files; tests 300–550 changed lines across 3–7 files; generated code 0 files/0 lines. Confidence is medium-high.
- Contract impact: internal Go launch/planning composition may change. Wire protocol, persistence schema/format, product specifications, user migration, and client behavior do not change.

Architecture

### Existing-Session Planning Ownership

- Extend the existing launch planner with Store-accepting variants of the current planning and override operations. These variants reuse the same planning logic but receive one already-admitted `*session.Store`; they do not open a Session, materialize a second Store, or return a Store-backed capability.
- `sessionlaunch.Service` owns admission for `OpenExisting` requests. After config/auth-only preparation, it builds the existing scoped descriptor and executes all Session-dependent reads and mutations—persisted role/lock resolution, continuation update, locked request-shape backfill, prepared overrides, and final plan snapshot—inside one `Authority.WithSessionStore` callback.
- `workflowrunner.Starter` uses its existing `withSessionStore` helper for the same Store-accepting planning/override variants whenever it reuses an existing Session. New Session creation, fork/clone creation, and unrelated maintenance retain their current paths.
- Keep create-new planning on the existing planner entry point. The new admitted variants are selected only when the launch intent already names an existing Session, matching the approved scope.

### Lifecycle Ordering

- `Authority.WithSessionStore` remains the sole coordinator for this fix. Immediately after the existing per-Session gate is acquired, re-check `context.Cause`; if canceled, release the gate without resolving a resource, opening a Store, or invoking the callback.
- If a ready runtime resource exists, planning borrows that resource's Store through `resource.withStore`; Store-local mutation serialization orders planning mutations with runtime appends. If no resource exists, one dormant Store is materialized and used only for the admitted callback.
- Runtime activation and replacement already acquire the same gate. Therefore planning that enters first completes before a runtime Store is opened; activation/replacement that enters first completes its existing drain-and-successor transition before planning resolves and reuses the current Store.
- Do not retain the Store outside the callback and do not reacquire the same Session gate from inside it. Runtime commands continue to address their original Resource Generation and are not forwarded to a successor.

### Plan Value And Recovery

- Remove `MaterializedEventLog` from `launch.SessionPlan` and stop materializing an event log solely to build a plan. Runtime construction remains the existing owner of append-capable event-log materialization.
- Remove the existing-session override path that reconstructs a Store from `SessionPlan`; admitted callers apply overrides with the same Store before the callback returns. `SessionPlan` remains an immutable value snapshot containing only descriptor/config/metadata-derived values.
- Keep the append-recovery state machine, journal format, write/fsync ordering, restart recovery, and append-certainty receipts unchanged. The fix prevents the verified foreign-Store interleaving instead of teaching Store to merge or adopt another writer's journal.
- Add one typed irreconcilable recovery detail carried under `ErrStoreRecoveryRequired`. Populate it for either of two proven conflict classes: stable metadata state matching neither recovery snapshot, or a validated recovery record whose event suffix fails range, sequence/count, or digest identity validation. Include Session ID, operation, recovery/events paths, current/pre/post digests, phase, and optional suffix fields whenever that evidence is available. Valid prepared recovery, valid committed recovery, safe suffix rollback/truncation, and same-owner committed-observer retry remain on the existing recovery path; typed conflict paths preserve every artifact.

### Scope Boundary

- No passive-view, child-creation, global ownership, protocol, schema, migration, or client architecture changes are part of this implementation. No new lock registry, file lease, queue, retry state machine, or repository-wide guard is introduced.

Planning

Approved implementation envelope:

- Outcome: mutable launch/workflow planning for an existing Session and runtime activation/replacement for that Session cannot overlap writable Stores; acknowledged event and metadata commits survive the race and restart; irreconcilable recovery identifies preserved artifacts and conflicting digests.
- Estimate: production 350–650 changed lines across 5–9 files; tests 300–550 changed lines across 3–7 files; generated code 0 files/0 lines. Confidence is medium-high.
- Affected subsystems: launch planner, session launch service/composition, workflow runtime starter, Session runtime Authority admission, and Session recovery diagnostics.
- Contract impact: internal Go launch/planning composition only. No wire protocol, persistence schema/format, product specification, migration, generated-code, or client behavior change.

- [x] **Reproduce the verified planning/runtime collision (RED).** Add one service-level deterministic test for `OpenExisting` planning against runtime activation or replacement of the same Session. Use the existing persistence gate and lifecycle channels to hold planning before its stale metadata observation, drive a runtime-owned event-plus-metadata commit, and assert the fixed contract: runtime admission does not pass the planning owner, then both accepted mutations remain in the resolved metadata and event log after reopen. The current code must fail by exposing the overlap or losing one committed fact. Completion: the focused test fails before production changes and uses channel barriers with bounded failure timeouts only—no sleeps, private calls, test-only production hooks, direct journal fabrication, or rendered-error matching.

- [x] **Admit existing-Session launch planning through Authority (GREEN).** Extract Store-accepting planner operations from the current `PlanSession` and prepared-override implementations, without duplicating their planning logic. Add Authority composition to `sessionlaunch.Service`; for `OpenExisting`, run selected role/lock reads, continuation/backfill mutations, prepared overrides, and final value snapshot in one `WithSessionStore` callback. Keep create-new requests on their current path. Re-check cancellation immediately after gate acquisition. Completion: the tracer test passes; existing create/open/override service tests pass; a focused cancellation test proves no callback, Store open, or mutation occurs after a waiter is canceled.

- [x] **Use the admitted Store for workflow continuation and remove plan-owned persistence capability (RED→GREEN).** Add a focused workflow continuation/replan test that races replacement and verifies the current Resource Generation's Store is reused. Route only existing-Session workflow planning and override application through the starter's existing `withSessionStore`; leave new/fork/clone paths unchanged. Remove `MaterializedEventLog` from `SessionPlan`, stop plan-time event-log materialization, and remove existing-session Store reconstruction from override application. Completion: focused workflow/launch/runtime tests pass, planning returns only value data, and runtime construction remains the sole append-capable event-log materializer for this flow.

- [x] **Add actionable irreconcilable recovery detail without changing recovery (RED→GREEN).** Add typed tests for both supported conflict classes: persisted metadata matching neither recovery snapshot, and a valid committed recovery record whose suffix fails range, sequence/count, or digest identity. Fingerprint the recovery record, metadata, and event file before and after each case. Wrap both paths in the same structured recovery-state detail under `ErrStoreRecoveryRequired`, populating optional suffix fields when available, while leaving valid prepared/committed restart recovery, safe suffix rollback, and same-owner observer completion untouched. Completion: each conflict test independently asserts `errors.As` to the structured detail, `errors.Is(ErrStoreRecoveryRequired)`, the expected metadata/suffix evidence, and byte-for-byte artifact preservation; existing append-certainty/recovery tests pass.

- [x] **Verify race, restart, and no-loss behavior within the reduced scope.** Run focused packages under `-race` for Session, sessionruntime, launch/sessionlaunch, and workflowrunner; run the deterministic collision test with `-count=100`; run the complete server test suite and `./scripts/ci-check.sh all`; rebuild with `./scripts/build.sh server --output ./bin/kent`. Self-check that passive views, child creation, journal format, protocol/schema, specs, and clients were not changed. Completion: all checks pass with no race report, committed event/metadata loss, leaked recovery record, manual repair requirement, or out-of-scope diff.

### Review remediation

- [x] **Fail closed when existing-session launch planning has no Authority.** Remove the direct Store materialization fallback from `sessionlaunch.Service`; an `OpenExisting` request without runtime Authority must fail before descriptor resolution, Store access, or mutation. Keep create-new planning available without this dependency and cover both paths with focused tests.
- [x] **Eliminate legacy existing-plan Store reconstruction.** Narrow or remove planner override entry points that can recreate a Store from an existing `SessionPlan`; existing-session overrides must exclusively use the already-admitted Store-taking operations, while any retained legacy path accepts only create-new plans.
- [x] **Project irreconcilable recovery evidence to the operator.** Retain the typed recovery detail and recovery sentinel wrapping, and enrich the existing error surface with Session identity, operation, recovery/events paths, metadata digests, phase, suffix identity, and underlying cause without altering recovery format or wire protocol.
- [x] **Restore planning-first deterministic collision regressions.** Hold existing-session planning at the persistence barrier, prove its Authority admission owns the Session gate with `TryBlockSessionStarts`, then launch activation/replacement before releasing planning. The pre-fix direct-Store path must fail the admission assertion; timeouts are failure guards only.
- [x] **Remove duplicate test descriptor helpers.** Inline the one-off open-descriptor construction at each use so the reviewed duplicate helpers do not remain.
- [x] **Remove test-only production lifecycle adaptation.** Delete the exported lifecycle callback adapter and keep the regression synchronized exclusively through existing Session persistence and admission boundaries.
- [x] **Keep this ticket within the approved Session scope.** Remove the provider-adapter and remote interactive persistence experiment; a malformed provider error remains an error unless separately authorized and modeled by an explicit provider contract.
- [x] **Track the raw provider/transcript artifact separately.** Per the User decision on July 23, 2026, KENT-347 owns the persisted raw provider-error JSON artifact. It is not a KENT-331 acceptance condition; evaluate this ticket solely against its concurrent Session recovery definition of done.
- [x] **Encode optional recovery diagnostics structurally.** Preserve the enriched operator message without using empty-string variables as absence sentinels.
- [x] **Re-run scoped verification.** Run focused tests while implementing, repeat the five changed-path packages under `-race`, repeat collision regression at `-count=100`, then run the required full server, CI, and build verification before completion.
- [x] **Resolve final PR review findings without duplicated paths.** Extract the shared prepared prompt-facing target transformation and the shared admitted workflow post-plan sequence, and make typed-nil irreconcilable recovery errors retain `ErrStoreRecoveryRequired` classification.
- [x] **Lock the observed rejected-review scheduler ordering.** Model Implementation → Review → Reject → `continue_session`, prove the original Implementation Session is selected, block `AttachRunSession`, and prove the later Script intent cannot start until durable attachment completes in the same scheduler `Process` invocation.

Testing

Progress:

- 2026-07-23: Added the deterministic `OpenExisting` planning/runtime-activation tracer. It fails on the pre-fix behavior because runtime activation completes while the planning persistence gate is held.
- 2026-07-23: Routed `OpenExisting` launch planning through `Authority.WithSessionStore`, added admitted planner/override variants, and rechecked cancellation after admission. The tracer, full launch package, focused authority cancellation test, and launch package pass.
- 2026-07-23: Routed existing workflow continuation planning, re-planning, and overrides through the admitted Store; removed the plan-owned event-log capability and plan-time materialization. The deterministic workflow replacement race, workflowrunner, launch, sessionlaunch, and sessionruntime packages pass.
- 2026-07-23: Added typed irreconcilable append-recovery diagnostics for stable metadata divergence and validated committed suffix conflicts. Both artifact-preservation tests pass with `errors.Is(ErrStoreRecoveryRequired)` and `errors.As` assertions; the full session package passes.
- 2026-07-23: Verification passed: focused Session/sessionruntime/launch/sessionlaunch/workflowrunner race tests, the deterministic collision test at `-count=100`, a no-cache full server suite, CI checks, and the server build. The full-suite runner used its supported six-package parallelism setting to stay within the 180-second active-test cap.
- 2026-07-23: Second review remediation restores the approved planning-first ordering: the persistence barrier proves planning is in-flight, `TryBlockSessionStarts` proves it owns the Authority gate, and activation/replacement is launched only afterward. The test-only production lifecycle adapter and out-of-scope provider/CLI experiment were removed; pending full verification.
- 2026-07-23: Second remediation verification passed: the planning-first activation/replacement regressions at `-count=100`; the five Session/sessionruntime/launch/sessionlaunch/workflowrunner packages under `-race`; all Go packages with `KENT_TEST_GO_PACKAGE_PARALLELISM=6 ./scripts/test.sh server ./... -count=1`; `KENT_TEST_GO_PACKAGE_PARALLELISM=6 ./scripts/ci-check.sh all`; and `./scripts/build.sh server --output ./bin/kent`.
- 2026-07-23: User explicitly directed that the independent raw provider/transcript JSON artifact be tracked in KENT-347 and not expand or block KENT-331. QA should assess KENT-331 only against the approved Session recovery scope.
- 2026-07-23: Final PR remediation added the nil recovery-detail guard, removed both reviewed duplicate planning sequences, converted the race test from an already-attached resume to an unattached `continue_session` successor, and added the exact rejected-review → durable attachment → later Script ordering regression. Adversarial re-review reported no P0–P2 findings.
- 2026-07-23: Latest verification passed the two continuation regressions at `-count=100`, focused changed-path regressions at `-race -count=10`, every `workflowrunner` top-level test under `-race` across three disjoint harness shards, the other four changed packages under `-race`, and every Go package exactly once across eight package shards plus twelve exhaustive `server/runtime` test-name shards. Aggregate unsharded invocations exceeded the enforced 180-second active-test cap without a test failure; no cap was disabled.

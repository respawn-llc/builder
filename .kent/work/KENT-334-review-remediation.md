# KENT-334 full-diff review remediation

Updated: 2026-07-27

- [x] Linearize completion and Interrupt with one permit → Authority lock order.
- [x] Add the approved Task interruption fence across Interrupt's join window.
- [x] Move Start, Resume, Approval, and executable Move mutation plus explicit
      admission ownership into Workflow Execution.
- [x] Make explicit starts asynchronous and independent across parallel branches.
- [x] Recover eligible ready and admitted Current Nodes as interrupted.
- [x] Surface automatic-admission failures through durable interruption or a
      controller lifecycle error.
- [x] Make Project Delete artifact cleanup staged, reversible, surfaced, and retryable.
- [x] Project truthful queued status from Exact Execution Scope phase.
- [x] Replace normal pending-Approval absence errors with an existence query.
- [x] Correct the approved stale Resume spec wording.
- [x] Run full non-Rust verification and continue all three code-review sessions
      until every reviewer reports GREEN.

## Round-three checkpoint — 2026-07-27

Reviewer status:

- architecture/correctness `45134d2a-e206-4f36-8d05-e5474c13b591`:
  **GREEN**
- concurrency/lifecycle `d243af7f-058e-4e8b-978d-b30d7f08ffa9`:
  **GREEN**
- migration/API/read models `5b45b4dc-1714-42a1-b238-1b54bb1e1570`:
  four findings remain; not GREEN

## Round-four checkpoint — 2026-07-27

Reviewer status:

- migration/API/read models `5b45b4dc-1714-42a1-b238-1b54bb1e1570`:
  **GREEN**
- architecture/correctness `45134d2a-e206-4f36-8d05-e5474c13b591`:
  awaiting rerun after its latest three findings
- concurrency/lifecycle `d243af7f-058e-4e8b-978d-b30d7f08ffa9`:
  one startup-recovery notification finding remains

Additional completed remediation:

- Workflow Approval resolved events now use the `workflow_approval` kind through
  the real Broker. The end-to-end Finalizer test covers pending and resolved
  delivery.
- Desktop Question focus validates against the full prepared Ask-ID set with
  order-independent set equivalence, rather than the currently unresolved
  subset.
- Desktop interrupted-Current-Node copy and i18n keys no longer use stale
  Workflow Run terminology.
- Approval application captures its exact pre-delete attention projection in
  the committing transaction and returns `TaskAttentionResolution`.
- Manual Move captures every superseded pending Approval before replacement or
  deletion and returns the same typed post-commit resolution.
- Workflow Service finalizes those returned resolutions after the controller
  mutation. Its regression covers approval-required Move, superseding Move, and
  applying the replacement Approval in order.
- Project Delete does not need an Approval finalization path: the authoritative
  blocker query classifies every Task with a pending Approval as non-terminal,
  and deletion returns before staging or deleting rows. The existing store test
  proves this. Speculative unreachable Project Delete attention plumbing was
  removed.
- Current Node controller owns an injected production attention lifecycle.
  Admission/runner failures and exact-scope failures publish actionable
  interruption attention only after durable persistence.
- Startup recovery now uses generated `UPDATE ... RETURNING` to identify the
  exact newly interrupted Current Nodes. Controller coverage proves the
  recovered references are passed to attention publication.
- `ResumeCurrentNode` captures the old actionable interruption projection and
  clears the marker in one transaction. `ResumeTask` finalizes only successful
  clears after releasing the mutation permit, including partial-error results.
- Finalizer serializes authoritative provider query plus pending Broker
  publication against resolved publication under one mutex. A deterministic
  blocked-publisher regression proves pending cannot be overtaken by resolved.
- The permanent resolved-Approval and resolved-interruption maps were deleted;
  mutation-returned resolutions are the lifecycle authority and Finalizer
  retains no unbounded tombstone history.
- New Current Node/fanout/Approval schema tests were moved from
  `workflow_schema_test.go` to `current_node_schema_test.go`. Their SQLite
  constraint assertions now use `errors.As` with `*sqlite.Error` and exact
  primary/extended constraint codes.
- Runtime-admission integration coverage was moved from
  `sessionlaunch/service_test.go` to `runtime_admission_test.go`.

Focused evidence:

- workflowattention, workflowexecution, workflowstore, workflowsvc,
  projectview, and core suites passed together;
- generated metadata query freshness and SQLite query-plan suites passed;
- metadata Current Node schema tests passed;
- sessionlaunch passed;
- Desktop typecheck and all 30 files / 160 tests passed before these Go-only
  lifecycle changes;
- `git diff --check` passes and `tui-rs/**` remains unchanged.

### Remaining startup-recovery notification defect

`CurrentNodeController.Recover` runs during Core construction, before Desktop
can subscribe. Its live Broker publication is therefore dropped. The durable
Inbox remains correct, but the required Desktop notification surface is not
replayed after connection.

The mapped implementation direction is a bounded, lazy root-subscription
snapshot:

1. Subscribe to the live Broker first.
2. Return a wrapper subscription that pages durable Workflow Approval and
   interrupted-Current-Node notification identities from the existing
   Workflow Attention cursor query.
3. Materialize each current notification through Finalizer snapshot methods
   that share the same mutex and authoritative providers as live publication.
4. Yield snapshot events one page/item at a time, then drain buffered live
   events. Rewrite sequence numbers locally for one monotonic subscription.
5. Do not snapshot live Questions, preserving their existing live-only
   behavior and restart semantics.
6. Do not retain an in-memory replay cache or load the full Inbox. Existing
   Broker buffer/gap handling remains the bounded live-event backpressure
   mechanism while snapshot pages are consumed.
7. Wire the snapshot source in Core after Workflow Attention and Finalizer are
   constructed.
8. Add an integration regression that durably leaves executable Current Node
   work before Core startup, lets startup recovery interrupt it, subscribes
   afterward, and receives the interrupted-Current-Node snapshot event.

### Startup-recovery snapshot implementation checkpoint — 2026-07-27

The startup-recovery notification defect is now implemented:

- Root attention subscription registers with the live Broker before any
  durable snapshot query.
- `RuntimeRegistry` wraps configured root subscriptions with two joined
  workers and one-slot channels. One worker continuously drains live Broker
  events while the other lazily materializes at most one durable item ahead
  from a 32-reference page. The wrapper rewrites both sources onto one local
  monotonic sequence.
- Workflow View reuses `ListWorkflowDurableAttentionCandidates` keyset
  pagination and returns sealed typed references for Workflow Approval and
  interrupted Current Node identities. Continuation is typed and nullable;
  no empty-string absence sentinel was added.
- Finalizer snapshot methods re-query the authoritative pending projection
  and enqueue the one-slot snapshot item while holding the same mutex used by
  live query-to-publish and resolution. A resolution that wins suppresses
  stale snapshot pending state; a snapshot that wins is available to the
  merge before its matching live resolution can publish.
- While no snapshot item is ready, live Questions continue flowing during an
  arbitrarily slow durable page/item materialization. Once a snapshot item is
  ready, bounded fairness allows at most one unrelated live event ahead of it.
  One deterministic regression delivers 65 distinct Questions while the
  snapshot source is blocked; another replenishes live traffic after a
  snapshot is ready and proves the snapshot is emitted after one live event,
  before the next.
- When matching snapshot and live events are concurrently available, the
  merge emits pending before resolved and removes an equal-or-older duplicate
  pending revision. Desktop also rejects duplicate or stale pending revisions
  already surfaced for the same notification ID, covering the live-first
  overlap without a resident server dedupe map.
- Snapshot pages contain only still-current durable Workflow Approval and
  actionable interrupted-Current-Node notifications. Questions remain
  live-only, and no root `snapshot_complete` event was added.
- There is no Broker replay cache, permanent tombstone history, whole-Inbox
  load, unbounded resident collection, or second live-event buffer. Worker
  cancellation is joined by subscription Close.
- Core wires the Workflow View plus Finalizer snapshot source before Current
  Node startup recovery.
- `TestCoreRootAttentionSnapshotsCurrentNodeInterruptedDuringStartupRecovery`
  creates ready executable work before Core startup, lets `Recover` interrupt
  it during composition, subscribes only after Core is returned, and receives
  the exact `interrupted_current_node` snapshot.
- Registry coverage proves lazy paging, live draining during blocked
  materialization, bounded one-item snapshot flow, and local sequence
  rewriting. Workflow View coverage proves typed multi-page references.
  Finalizer coverage proves snapshot materialization does not publish a
  second live event and cannot be overtaken by matching resolution.
- Full focused suites for registry, Workflow View, Workflow Attention, Core,
  and transport pass.

Round-five architecture cleanup also:

- replaced the remaining legacy SQLite constraint error-string helper with
  typed `*sqlite.Error` primary and exact extended result-code assertions;
- split `server/metadata/db_test.go` into focused migration-scenario suites;
- split the Current Node persistence structure guard by model/query/guard
  concern;
- split Workflow Execution controller tests by admission, lifecycle,
  interruption, Question handling, and shared fixture;
- split Workflow View Current Node tests by board, task detail/status,
  activity, attention, and shared fixture.

## Round-six reviewer gate — 2026-07-27

All three original sessions explicitly report **GREEN** against the latest
worktree:

- architecture/correctness `45134d2a-e206-4f36-8d05-e5474c13b591`;
- concurrency/lifecycle `d243af7f-058e-4e8b-978d-b30d7f08ffa9`;
- migration/API/read models `5b45b4dc-1714-42a1-b238-1b54bb1e1570`.

The exact final `./scripts/test.sh server desktop` gate has not passed yet.
Its first isolated run:

- exposed one load-sensitive failure in
  `TestStreamIdleWatchdogPingResetsTimer`; the same package passes in
  isolation;
- then reached the repository's 180-second active-test cap without another
  assertion failure;
- completed expensive packages including `cli/app` (103.566s),
  `server/core` (88.315s), `server/transport` (98.286s),
  `cli/app/internal/ptyfixture` (122.560s), and `server/worktree` (62.919s);
- had not completed `server/metadata`, whose isolated full suite currently
  takes about 155 seconds.

Per repository policy, do not bypass the cap. The next remediation step is to
make the slow/flaky tests deterministic and fast enough, then rerun the exact
full matrix before ledger/commit completion.

Additional completed remediation:

- Authority owns queued, running, and non-authorizing finalizing Script phases.
  Script running publication happens only after `cmd.Start` succeeds.
- Task Interrupt fences and joins finalizing Script scopes when another active
  scope authorizes the Task-wide operation.
- The Task fence is indexed only by canonical Current Node and Exact Execution
  Scope keys. The Current Node authority guard passes.
- Waiting Questions remain non-interruptible and keep the Task non-quiescent
  after active sibling execution is interrupted.
- Explicit admission is bounded at eight whole workers through setup,
  compensation, and durable failure cleanup. Automatic capacity remains
  separately bounded.
- Admission failure persistence is single-write and cannot replace a Task
  Interrupt's `user_interrupt` reason.
- `FailCurrentNodeScope` checks the Task fence inside the mutation permit.
  Unrelated admission worker errors do not block terminal Script failure
  persistence.
- Workflow Execution is split into focused controller, lifecycle, admission,
  interruption, and interrupt-state modules while retaining one
  `workflowruntime.Controller`.
- Task-list SQL gives durable `done` first precedence across list filtering,
  sorting, and cursors, matching detail and board.
- Test Git repositories ignore host global/system Git config, including commit
  signing, so the full suite does not wait on the operator's GPG agent.

New deterministic coverage includes:

- Task Interrupt joins a finalizing sibling and cannot release its successor.
- Task Interrupt's user reason wins a finalizing scope failure race.
- Waiting Questions survive sibling Task Interrupt and continue to block
  Quiescence.
- Explicit setup workers stay bounded while sibling admissions proceed.
- Runtime-start failures persist exactly once.
- Script startup failure never projects running or authorizes Interrupt.
- A terminal Task remains `done` in detail, board, list filters, status sorting,
  and cursor pagination while its source Exact Scope is still retiring.

Focused packages and the authority guard pass. A broad multi-package run reached
the 180-second harness cap under concurrent review-agent and external Go-test
load; every package that completed before the cap passed. Final verification
must rerun in isolation.

Two delegated implementation agents reported running the unscoped build
command despite the frozen-Rust instruction. No `tui-rs/**` file changed.
Do not rely on those runs as evidence and do not run any further unscoped build
or Rust command.

### Remaining migration/API/read-model findings

1. Generic model approval attention is dropped:
   `server/registry/attention_notifications.go` emits an approval without
   `approval_id`, while `shared/serverapi/attention_notification.go` now
   requires one. Model generic approval and durable Workflow Approval as
   distinct typed payloads; do not make `approval_id` an empty sentinel.
2. The authoritative spec requires Task information to expose `can_delete`,
   but `WorkflowTaskActions` and `TaskProjector` do not provide it. Add the
   server-authoritative hint from the same live state, update clients/contracts,
   and retain the mutation-time Quiescence recheck.
3. `TestRollbackPickerWorksAfterInterruptedRuntimeAndTUIRestart` fails on this
   branch at `cli/app/ui_rollback_test.go:810` because runtime never reaches the
   model request. It passes at base `3c7d45a62`. Reproduce in isolation and fix
   the branch regression without touching or testing frozen Rust.
4. CLI `task show --json` and human output omit `current_nodes` and
   `retained_session_count` from `WorkflowTaskDetail`. Add both with
   product-boundary coverage.

### Migration finding implementation checkpoint — 2026-07-27

All four migration findings are now implemented locally and await the original
migration review session:

1. Generic model approvals and durable Workflow Approvals are separate
   notification variants. Generic `approval` carries a required message and
   targets a Session prompt. `workflow_approval` carries a required
   `approval_id` and targets a Task Approval focus. Go validation, Workflow
   Attention publication, CLI notification handling, Desktop schemas, and
   Desktop presentation use the typed split without empty-ID sentinels.
2. `WorkflowTaskActions.can_delete` is projected from
   `CurrentNodeController.CurrentTaskQuiescence`, which shares the exact
   controller predicate used by Task Delete's mutation-time Quiescence check.
   Task detail and board cards consume the live snapshot. Desktop disables the
   board Delete action when the hint is false.
3. The rollback restart test fixture now pins `GlobalConfigDir` to a temporary
   directory instead of reading the operator's real global Kent context before
   reaching the fake model. The test also reports an early runtime failure
   directly instead of misreporting it as a model-request timeout. The
   previously failing test passed twenty consecutive isolated runs before this
   fixture hardening and twenty consecutive isolated runs after it. The
   post-hardening runtime-to-model-request path dropped from roughly
   0.5–0.9 seconds to roughly 0.3–0.4 seconds.
4. CLI `task show --json` now includes `current_nodes` and
   `retained_session_count`. Per the User's explicit output decision, human
   output keeps only Current Session IDs and adds `Retained sessions: <count>`;
   it does not duplicate Current Node identity.

Focused evidence at this checkpoint:

- generic Approval registry, Workflow Approval finalizer, broker, server API,
  and CLI app packages passed;
- Desktop attention notification tests, full Desktop tests, and Desktop
  typecheck passed before the final `can_delete` rerun;
- controller Quiescence and Workflow View `can_delete` boundary tests passed;
- CLI task-show JSON and human-output tests passed;
- rollback restart passed twenty consecutive isolated runs before the
  hermetic global-config fixture change;
- `git diff --check` passed and `tui-rs/**` remains unchanged.

The broad server-focused command that overlapped other test processes reached
the repository's 180-second cap after all displayed packages passed. It is not
final evidence. Run the final non-Rust matrix in isolation after all reviewers
are GREEN.

After fixing all four, continue the same migration reviewer session until it
explicitly reports GREEN. Then rerun all three reviewers if any fix overlaps
their reviewed lifecycle/architecture surfaces.

Review sessions:

- architecture/correctness: `45134d2a-e206-4f36-8d05-e5474c13b591`
- migration/API/read models: `5b45b4dc-1714-42a1-b238-1b54bb1e1570`
- concurrency/lifecycle: `d243af7f-058e-4e8b-978d-b30d7f08ffa9`

## Round-one findings

- P0: completion acquired the Authority lock before the mutation permit while
  Interrupt acquired them in the opposite order.
- P1: completion could publish a successor after Task Interrupt returned.
- P1: Start, Approval, and executable Move split durable mutation from
  admission ownership across permit windows.
- P1: Approval could start a target before the completed source scope retired.
- P1: Resume waited for slow setup and stopped admitting parallel siblings
  after the first failure.
- P1: ready work stranded by a crash or clean shutdown was not recovered.
- P1: Project Delete could irreversibly remove artifacts before DB commit and
  swallowed final cleanup failure.
- P1: public `queued` status had no authoritative projection.
- P2: automatic admission failures could disappear silently.
- P2: normal pending-Approval absence emitted query ERROR diagnostics.

## Explicit User approvals

- Approved the wording-only Workflow spec correction from interrupted Runs to
  interrupted Current Nodes.
- Approved the minimal controller-owned Task interruption fence required while
  Interrupt releases the shared permit to join Exact Execution Scopes.

## Active coding sessions

- Project Delete staged/retryable cleanup:
  session `96c54915-79e8-4c97-b5f2-455ad5fdbc2a`, shell `3856`.
  Write scope: `server/workflowstore/project_delete.go`,
  `server/projectview/service.go`, and their tests.
- Queued status plus Approval-absence query diagnostics:
  session `903b6a8d-7a1e-4a2b-89cb-ce92608f5ec9`, shell `3943`.
  Write scope: Session Runtime phase/snapshot files, Workflow View status/list
  files and tests, pending-Approval completion/query files, generated queries.

Both write agents finished. Their slices were integrated and focused package
tests pass.

## Local lifecycle work in progress

## Implementation checkpoint

- Workflow Execution now owns typed lifecycle operations for Task Start,
  Resume, Approval, and manual Move. The old low-level controller `Start` and
  `Resume` entrypoints were removed.
- Explicit starts use a controller worker independent of automatic concurrency.
  Resume returns after durable requeue plus ownership registration.
- Completion and Task Interrupt use permit → Authority → controller lock order.
- Task-wide Interrupt exact-linearizes running authorization, drains queued
  Authority gates plus explicit/automatic controller work, and keeps its Task
  fence until selected scopes and admissions retire.
- Approval targets produced by a completed live source are held until
  `ExecutionFinalized`.
- Background admission failures durably interrupt ready/admitted nodes and
  record a controller lifecycle error when persistence itself fails.
- Startup recovery interrupts eligible ready and admitted Current Nodes while
  excluding pending-Approval sources.
- Authority owns queued/running phase. Queued scopes are visible in Task read
  models but do not authorize Interrupt.
- Project Delete stages the whole sessions directory to a deterministic
  tombstone, restores it on DB rollback, and retries post-commit cleanup.
- Pending-Approval existence uses `SELECT EXISTS`.
- The approved spec wording now says “interrupted Current Nodes”.

Deterministic lifecycle coverage added:

- completion versus Task Interrupt has no deadlock and cannot release a
  successor;
- Approval waits for source-scope retirement;
- Resume returns before blocked setup and admits sibling branches independently;
- Task interruption fence rejects lifecycle mutations until retirement;
- Task Start mutation and explicit ownership are indivisible to Delete;
- Task Interrupt drains an Authority-queued gate alongside a running scope;
- startup recovery covers ready + admitted and excludes Approval;
- automatic/explicit setup failure becomes durable interruption.

Focused verification passed:

```text
./scripts/test.sh server \
  ./server/workflowexecution \
  ./server/workflowrunner \
  ./server/workflowstore \
  ./server/sessionruntime \
  ./server/workflowsvc \
  ./server/workflowview \
  ./server/projectview \
  ./server/metadata/querygen \
  ./server/metadata/sqlitegen
```

`git diff --check` and server `gofmt -l` were clean at the checkpoint.

## Round-seven full-suite runtime remediation — 2026-07-27

The exact non-Rust gate continued to expose test-infrastructure bottlenecks
after all three reviewers were GREEN. The remediation keeps the 180-second cap
and does not skip any package or test:

- The stream-idle watchdog now records the latest activity synchronously and
  rechecks it when its timer fires. A queued ping can no longer lose a select
  race to an expired timer. The LLM package passed ten uncached repetitions.
- Metadata migrations use a per-database `goose.Provider` instead of mutating
  Goose package globals. A concurrent-eight-database regression failed under
  `-race` before the change and passes afterward.
- Metadata fixtures use explicit isolated `ConfigRoot` values instead of
  process-wide HOME/environment mutation. 144 of 148 top-level tests now run
  in parallel; the four package-global hook/logger tests remain serial.
  The full package passes uncached in about 75 seconds instead of roughly
  155 seconds.
- Runtime's synchronized `sessiontest.Persistence` and isolated Session roots
  support parallel execution. A call-graph audit identified every test that
  mutates HOME/CWD, retry globals, prompt globals, or their helper closures.
  637 of 712 top-level tests now run in parallel; coordination-sensitive tests
  remain serial. Runtime plus Runtime Control pass together uncached, with
  Runtime at 71.768 seconds instead of about 165 seconds.
- The serial PTY package's 13 independent ongoing scenarios now use bounded
  concurrency of four. The package passes in about 25 seconds instead of
  98–140 seconds.
- Lifecycle PTY configuration/readiness documents are atomically published.
  This fixes the observed partial readiness JSON race at the writer.
- Runtime Control's common Session fixture no longer performs the duplicate
  `EnsureDurable` call after `session.Create`.
- The harness package-concurrency cap is now 10, still below the old
  oversubscribed 18-package setting. Diagnostics at 11 and 12 workers increased
  contention and were rejected.

Additional load-sensitive fixtures were made deterministic:

- rollback restart, ownerless Runtime Control, workflow steering, workflow
  terminal completion, and goal-loop waits retain channel synchronization and
  use five-second diagnostic ceilings;
- five live-loop Runtime tests proven coordination-sensitive under aggregate
  load were returned to the serial set;
- release channels and the active goal engine are cleaned up on failed
  assertions so a failed test cannot leak work into later tests;
- the raw shell background fixture uses an interactive stdin handshake instead
  of scheduler timing.

Latest exact clean-cache evidence:

- `go clean -testcache && ./scripts/test.sh server desktop`
- log:
  `/var/folders/4y/3b8zshg92kn3ryxd1x2f1rs00000gn/T/kent-bg-shells-3312524323/10099.log`
- No assertion failed before the unchanged 180-second cap.
- Runtime completed in 166.574 seconds. Only Transport, Workflow Store, and
  Worktree were unfinished at cutoff.
- The exact missing tail then passed together:
  `./scripts/test.sh server ./server/transport ./server/workflowstore
  ./server/worktree -count=1`; Workflow Store took 3.162 seconds, Worktree
  16.355 seconds, and Transport 17.185 seconds.
- Runtime plus Runtime Control passed together uncached after the serial-set
  correction, with Runtime at 75.672 seconds.
- Increasing package workers to 11 or 12 and front-loading the tail packages
  increased contention, so those scheduling experiments were rejected and did
  not change the harness.
- The User explicitly directed on 2026-07-27 to “ignore the cap for now.”
  Therefore the single-invocation cap gate is deferred, not recorded as a pass.
  `--no-wall-clock-cap` was never used.

Final non-Rust verification:

- `./scripts/build.sh server desktop --output ./bin/kent` — PASS
- `./scripts/ci-check.sh deps` — PASS
- `./scripts/ci-check.sh frontend-lint` — PASS
- `./scripts/ci-check.sh vet` — PASS
- direct non-Rust `gofmt -l` — clean
- `./scripts/test.sh desktop` — PASS, 31 files / 161 tests
- `pnpm --dir docs test` — PASS
- `pnpm --dir docs build` — PASS
- `pnpm --dir docs smoke:built` — PASS
- `git diff --check` — clean
- `git status --short -- tui-rs` — empty

The three original reviewer sessions remain explicitly GREEN. Subsequent
changes are confined to the LLM watchdog, metadata migration/test
infrastructure, Runtime/Core/Shell test fixtures, PTY fixtures, and the test
harness; they do not touch the reviewed KENT lifecycle/read-model production
surfaces, so the authoritative handoff did not require reviewer continuation.

Final pre-commit demolition ledger from integrated base `3c7d45a62`:

- 221 included handwritten production files;
- `+13107 / -13630`, net `-523`;
- production patch SHA-256
  `457f7bdaf247f0f94ff93d5ac0c6126de111c9d12f7fc17bb4c3f03948ac185c`.

## Main integration completion — 2026-07-28

- Merged `origin/main` at `35cf25ea3` into the branch without restoring the
  deleted Workflow Run/Placement scheduler.
- Ported the independent Session-store retirement fix and added
  `TestCurrentNodeContinuationWithActiveTranscriptSubscriberDoesNotBlockLaterAutomaticScript`.
  Reverting the lock-order fix reproduces the admission-gate deadlock; the
  corrected regression passes ten consecutive runs.
- Corrected `TestGoalAuthorityLiveSetUsesRuntimeCommand` to count only typed
  Goal feedback and Goal status events, excluding unrelated Runtime-open
  activity. It passes twenty consecutive runs.
- Exact clean-cache gate:
  `go clean -testcache && ./scripts/test.sh server desktop` — **PASS** in
  134.724 seconds under the ordinary 180-second cap. No bypass was used.
- Server/Desktop build, dependency policy, frontend lint, vet, Desktop tests,
  docs test/build/smoke, formatting, diff checks, and frozen-Rust checks pass.
- All three original reviewer sessions report **GREEN** against the latest
  staged worktree:
  - architecture/correctness `45134d2a-e206-4f36-8d05-e5474c13b591`;
  - concurrency/lifecycle `d243af7f-058e-4e8b-978d-b30d7f08ffa9`;
  - migration/API/read models `5b45b4dc-1714-42a1-b238-1b54bb1e1570`.

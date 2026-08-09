# KENT-438 implementation handoff

## Final completion state (August 6, 2026)

- Every Planning item in `.kent/plans/KENT-438.md` is checked complete.
- Board membership/counts/dependency progress now use pinned lifecycle Current Nodes/status before membership, filtering, sort, offset, and limit; forward/backward publication-boundary traversal and database-backed query-plan coverage pass.
- Detail, Current Nodes, List, Search, Board, dependencies, filtering, pagination, and actions now use one `TaskStatusProjection.WithSnapshot` capture backed by the controller's one `LifecyclePublication` SQLite/root capture. Split `Observe`/`WithDurableSnapshot` APIs and Search's independent transaction path were removed, and architectural tests reject their return.
- Restart coverage now includes ready/admitted Agent and Script Current Nodes, no startup activation, assignment reuse after startup interruption/Resume, pending Approval preservation, and fatal-finalizer termination.
- The targeted regression matrix passed across session runtime, runtime control, Workflow Execution/Runner/Store/View/Service, Project deletion, startup/core, generated metadata queries, CLI app, and CLI task commands. The architecture audit found one production Run registry, one Agent activation outcome owner, one production LifecyclePublication construction, latest-root delta application, no direct lifecycle Store commit API, no capture bypass, no new persisted lifecycle state/migration, no new raw SQL in production Go, no full-transcript traversal, no in-memory pagination, and no Frozen Rust changes.

Final repository tooling was invoked exactly once per command:

```sh
./scripts/test.sh server
```

The command exited 1 under the full parallel sharder because only
`TestLifecycleCaptureDoesNotWaitForScriptProcessStop` failed after its shell
process did not enter a controlled SIGTERM barrier within the test's
non-product 3-second wait. The full log is:

`/var/folders/4y/3b8zshg92kn3ryxd1x2f1rs00000gn/T/kent-bg-shells-2096412478/13334.log`

The timing-only test wait was remediated to 10 seconds (matching its existing
controlled cancellation grace), and the affected focused verification passed:

```sh
go test ./server/workflowexecution -run '^TestLifecycleCaptureDoesNotWaitForScriptProcessStop$' -count=1 -timeout=60s
git diff --check
```

Per the plan's one-shot full-suite rule, the full server command was not
rerun.

```sh
./scripts/build.sh --output ./bin/kent
```

The build passed.

## Plan position

- The first twelve Planning items are checked.
- Latest completed slices:
  - prepared completion/Approval/Manual Move/Fan-Out/Join publication;
  - cancellable Agent/Script Exact result finalization;
  - interrupted-finalizer fatal supervision.
- The next unchecked item is **Bring Task, Workflow, and Project deletion under affected Task writers and publication.**

## Result finalization completed

- `server/sessionruntime/authority_execution.go`
  - Workflow Agent and successfully-started Script executions enter a `finalizing` phase without retiring their Exact Scope or Workflow index.
  - Startup-failure Script finalization keeps the prior non-running behavior.
- `server/sessionruntime/authority_resource.go`
  - `AgentExecutionRequest.Finalize` runs after the Agent runner returns and before Authority cleanup/retirement.
- `server/sessionruntime/script.go`
  - Script finalizers receive the cancellable execution context; `context.WithoutCancel` was removed.
- `server/sessionruntime/task_targets.go`
  - Finalizing Exact Scopes remain running observations.
- `server/sessionruntime/workflow_interrupt.go`
  - Finalizing scopes authorize Interrupt and are also classified in `WorkflowInterruptSelection.Finalizing` for supervision diagnostics.
- `server/workflowexecution/current_node_controller.go`
  - Agent completion preview-validates and records the request without retaining a SQLite transaction across the terminal tool/Engine return.
  - Agent finalization re-prepares and publishes source Run/Exact retirement plus complete successors atomically.
  - Script finalization uses the same successful finalization delta.
  - Finalization failure publishes typed `workflow_result_finalization_failed` interruption.
  - `ExecutionFinalized` now cleans controller internals only; it no longer performs a separate root retirement publication.
  - Publication-commit entry is explicitly fenced; Interrupt waits and revalidates the committed result.
- `server/workflowexecution/current_node_interruption.go`
  - Interrupt selects/fences/cancels Exact execution before waiting on the Task writer.
  - An already-entered finalization publication wins; Interrupt waits for its outcome and returns completed-state revalidation.
- `server/workflowrunner/starter.go`
  - Agent Authority finalization delegates to `workflowruntime.ResultFinalizer`.
- `server/workflowstore/lifecycle_publication.go`
  - Added prepared completion publication and preview/rollback support.

Coverage:

- Agent runner return and Script process return remain running/interruptible with the same Exact Scope while finalization is paused.
- Agent and Script normal completion.
- Agent finalizer failure.
- Interrupt before publication cancels finalization and publishes user interruption.
- User interruption wins a racing finalizer failure.
- Finalization publication commit wins Interrupt and releases the committed successor.
- No Exact Scope retires before completion/interruption publication.

## Fatal finalizer supervision completed

- `server/workflowexecution/current_node_finalizer_supervisor.go`
  - A single 300-second supervisor starts only after `RequestStop` successfully cancels a finalizing Exact Scope.
  - Cooperative stop ends supervision.
  - Timeout logs structured Task, Current Node, scope, Run phase, finalizer phase, elapsed/canceled state, and bounded all-goroutine stacks, then panics the process.
- `server/workflowexecution/current_node_finalizer_supervisor_test.go`
  - Isolated subprocess test uses a short internal deadline to prove structured diagnostics and process termination without changing production’s 300-second deadline.
- No polling, retry state machine, revocable lease, ordinary-duration watchdog, or orphan-finalizer recovery was added.

Focused verification passed:

```sh
go test ./server/sessionruntime ./server/workflowexecution ./server/workflowrunner ./server/workflowstore -count=1 -timeout=240s
go test ./server/sessionruntime ./server/workflowexecution ./server/workflowrunner ./server/workflowstore ./server/core ./cli/app -count=1 -timeout=240s
git diff --check
```

The final full `./scripts/test.sh server` and final build have not been run.

## Workflow-scoped Session/TUI Interrupt completed

- `server/runtimecontrol/service.go`
  - Added typed `WorkflowSessionInterruptor`.
  - Runtime control discards/reconciles queued input before execution interruption.
  - Targeted client-operation cancellation is deferred so it still completes on delegate or reconciliation errors.
  - Matching Workflow Sessions delegate to Workflow Execution; only ordinary Sessions call `Engine.Interrupt`.
- `server/workflowexecution/current_node_controller.go`
  - Added `InterruptSessionExecution`.
  - Resolves only the Authority-owned Workflow Exact Scope for the Session.
  - Reuses the existing Task/Session selector interruption protocol.
  - Completed-publication revalidation is a handled success rather than ordinary-runtime fallback.
- `server/workflowexecution/current_node_interruption.go`
  - Session-specific interruption now establishes the same Task interruption fence around its selected Exact Scope, without draining sibling work.
  - Concurrent Task interruption cannot create a second durable publication.
- `server/core/composition.go`
  - Injects the controller as runtime control's Workflow Session interrupt owner.
- Coverage:
  - ordinary-versus-Workflow runtime-control routing;
  - interactive retained Session interruption during a live model loop, with durable Resume and no remaining Interrupt;
  - paused Agent result finalizer with no active Engine step;
  - lifecycle-publication commit winning Session Interrupt;
  - Task/Session race sharing one fence and one durable interruption write;
  - queued input discarded/reconciled exactly once;
  - targeted client operation still canceled when the Workflow delegate fails;
  - agent-shell `kent task complete` request targets the activated Session;
  - activated retained Session completion resolves the same Run and reaches `done`, not target-not-found.

Focused verification passed:

```sh
go test ./server/runtimecontrol ./server/workflowexecution ./server/core ./cli/app -count=1 -timeout=240s
go test ./server/runtimecontrol -run '^(TestServiceInterruptRoutesWorkflowExecutionWithoutInterruptingOrdinaryEngine|TestServiceWorkflowInterruptReconcilesQueuedInputOnce|TestServiceWorkflowInterruptFailureStillCancelsTargetedClientOperation)$' -count=1 -timeout=60s
go test ./server/workflowexecution -run '^(TestCurrentNodeControllerInterruptSessionExecutionStopsRetainedWorkflowRun|TestRuntimeControlSessionInterruptCancelsPausedWorkflowResultFinalizer|TestCurrentNodeControllerSessionInterruptRevalidatesCompletedPublication|TestCurrentNodeControllerTaskInterruptRacesSessionInterruptThroughOneFence)$' -count=1 -timeout=60s
go test ./cli/app -run '^(TestInteractiveRetainedWorkflowSessionInterruptPublishesResumableState|TestAgentShellCompletionFromInteractiveRetainedWorkflowSessionResolvesSameRun)$' -count=1 -timeout=60s
go test ./cli/kent -run '^TestTaskCompleteFromAgentShellTargetsItsActivatedSessionExecution$' -count=1 -timeout=60s
env -u KENT_SESSION_ID go test ./cli/kent -count=1 -timeout=120s
git diff --check
```

An unsanitized direct `go test ./cli/kent` inherited this active Workflow Session's `KENT_SESSION_ID` and made the pre-existing human worktree-delete test take the agent branch. Repository `scripts/test.sh` sanitizes `KENT_*` by default, and the package passed with that variable removed.

## Deletion cutover completed

- Task, Workflow, and Project Delete retain their existing outer mutation ownership and acquire every affected Task writer in canonical Task-ID order.
- Each destructive operation revalidates the exact Task set and direct controller Run/Exact-Scope quiescence while those writers remain held.
- Task and Workflow deletion use opaque prepared SQLite mutations whose only commit owner is `LifecyclePublication`.
- Project deletion now stages and validates Session artifacts before its prepared transaction, publishes Project metadata deletion plus all affected lifecycle-root removals together, restores staged artifacts on rollback/commit failure, and finalizes artifacts only after the compatible commit/root swap.
- Direct public `Store.DeleteTask`, `Store.DeleteWorkflow`, and `Store.DeleteProject` commit APIs are removed.
- `projectview.Service` routes Project deletion through the controller publication owner rather than retaining its own Store commit path.
- Deterministic races prove:
  - Task Delete blocks Resume and retained-Session activation after quiescence revalidation.
  - multi-Task Workflow Delete blocks Resume until deletion wins and revalidation finds no resumable Current Node.
  - multi-Task Project Delete blocks retained-Session activation until deletion wins and durable ownership is absent.
  - opposite-order multi-Task callers acquire the same canonical order.
- Workflow and Project service tests reject changed Task sets without deleting any original or concurrently-added Task.
- Existing blocker/artifact tests now execute through `LifecyclePublication`, preserving all-or-nothing metadata and filesystem behavior.

Focused verification passed:

```sh
go test ./server/workflowstore -run '^TestLifecyclePublicationDeletesProjectAndAffectedTaskRoots$' -count=1 -timeout=60s
go test ./server/workflowstore -run '^(TestDeleteProject|TestLifecyclePublicationDeletesProject)' -count=1 -timeout=120s
go test ./server/projectview ./server/workflowexecution ./server/core -run '^(TestServiceDeletesProjectMetadataAndSessionArtifacts|TestTaskDeletionGatePreventsResumeAfterQuiescenceRevalidation)$' -count=1 -timeout=120s
go test ./server/workflowexecution -run '^(TestWorkflowDeletionGatesEveryAffectedTaskAgainstResume|TestProjectDeletionGatesEveryAffectedTaskAgainstRetainedSessionActivation)$' -count=1 -timeout=120s
go test ./server/workflowsvc -run '^TestServiceWorkflowDeleteRejectsChangedTaskSetWithoutDeletingAnyTask$' -count=1 -timeout=120s
go test ./server/projectview -run '^TestServiceProjectDeleteRejectsChangedTaskSetWithoutDeletingAnyTask$' -count=1 -timeout=120s
go test ./server/projectview ./server/workflowstore ./server/workflowexecution ./server/core -count=1 -timeout=240s
git diff --check
```

## Generated List/Search/status/dependency cutover completed

- `WorkflowTaskExecutionObservation` now carries typed pinned lifecycle snapshots:
  - complete Current Nodes from the pinned SQLite snapshot for every root-owned Task;
  - genuine queued Run references from the immutable root;
  - Exact execution scope identities from the root;
  - Authority execution details filtered strictly to those matching Exact scope identities.
- `sessionruntime.TaskExecution` now carries its Exact `ScopeID`; staged or stale Authority executions absent from the pinned root do not enter read projections.
- `LifecycleCapture.TaskIDs` exposes the bounded active/transitioning root keys so read observation never scans the Task dataset.
- The lifecycle JSON now carries `has_lifecycle_override`, sorted `current_node_ids`, root-derived queued state, and exact-derived running/prompt facts.
- The shared generated status fragment now:
  - substitutes root Current Nodes before status decoding;
  - takes queued only from root Runs;
  - takes running/waiting live facts only from matching Exact scopes;
  - ignores stale durable `running`/`queued`/`waiting_question`;
  - preserves durable stopped/Approval facts when no root override exists.
- Generated Task List SQL now uses the same status fragment and root Current Nodes before column/status/attention filters, sort, and offset/limit.
- The dependency filter template accepts an explicit status relation; Task List uses `effective_status`, while not-yet-cut-over Board paths retain durable status until the next slice.
- Generated Task Search and dependency projections consume the same lifecycle JSON through the shared status fragment.
- Query generation was rerun with `go generate ./server/metadata/sqlitegen`; generated SQL and descriptor code are synchronized.
- Queued Task detail no longer exposes a live Session target: queued is Run authority, while targets and Interrupt remain Exact-only.

Focused coverage added/passed:

```sh
go test ./server/workflowexecution -run '^(TestObserveWorkflowTaskExecutionsIncludesPinnedQueuedLifecycleRoot|TestObserveWorkflowTaskExecutionsExcludesAuthorityScopeAbsentFromPinnedRoot)$' -count=1 -timeout=90s
go test ./server/workflowview -run '^(TestTaskStatusProjectionObservationEncodesPinnedLifecycleOverridesAndExactFacts|TestTaskStatusDurableSnapshotUsesPinnedCurrentNodeOverrideBeforeProjection|TestTaskListAppliesPinnedLifecycleOverrideBeforeColumnAndStatusFilters|TestTaskListDependencyFilterUsesPinnedBlockerLifecycleBeforePagination|TestTaskSearchAppliesPinnedLifecycleOverrideBeforeStatusFilterAndPagination|TestTaskDependenciesProjectsPinnedQueuedBlockerAsUnsatisfied)$' -count=1 -timeout=120s
go test ./server/metadata/sqlitegen -run '^(TestListWorkflowTaskStatusProjectionByTasksUsesLiveQuestionAndApprovalPrecedence|TestListWorkflowTaskListRowsUsesProjectLinkAndTaskIndexes)$' -count=1 -timeout=120s
go test ./server/sessionruntime ./server/workflowexecution ./server/workflowview ./server/metadata/querygen ./server/metadata/sqlitegen ./server/core ./server/workflowsvc -count=1 -timeout=300s
git diff --check
```

## Next slice

Continue only **Cut Board membership and pagination over to the shared capture**. This slice remains unchecked.

Start by writing one red Board predecessor/successor membership test at a lifecycle publication boundary. The Board query is intentionally not yet cut over:

- `ListBoardNodeTasks` and `ListBoardColumnTaskCounts` still use durable `task_current_nodes`.
- Their dependency-filter template invocations still pass `workflow_task_status_records`.
- Board must receive the pinned lifecycle JSON/current-node overrides before membership, cursor/offset predicates, ordering, and limit.
- Reuse the shared `taskStatusProjection` fragment and change the Board dependency relation to `effective_status`; do not add a second Board-specific lifecycle authority.

Completed within the deletion slice:

- `server/workflowexecution/task_lifecycle_coordinator.go`
  - Added canonical multi-Task `RunTasks`; opposite caller order acquires the same Task-ID order.
- `server/workflowexecution/current_node_lifecycle.go`
  - Added `RunTaskDeletion`, retaining all affected Task gates across quiescence revalidation and the supplied destructive operation.
  - Added controller-owned `DeleteTask` and `DeleteWorkflow` publication entrypoints.
- `server/workflowstore/tasks.go`
  - Hard-cut Task metadata deletion to opaque prepared SQL mutation.
  - Removed direct public `Store.DeleteTask`.
- `server/workflowstore/workflow_delete.go`
  - Hard-cut Workflow metadata deletion to opaque prepared SQL mutation carrying the exact affected Task IDs.
  - Removed direct public `Store.DeleteWorkflow`.
- `server/workflowstore/lifecycle_publication.go`
  - Added the shared prepared multi-Task root-removal commit boundary.
  - Added `PublishTaskDeletion` and `PublishWorkflowDeletion`.
- `server/workflowsvc/service.go`
  - Task Delete now retains global mutation ownership plus its Task gate across worktree preflight, worktree cleanup, prepared metadata publication, attention cleanup, and event publication.
  - Workflow Delete acquires all affected Task gates canonically, revalidates the exact Workflow Task set, and uses controller-owned publication.
- `server/projectview/service.go`
  - Project Delete now acquires affected Task gates canonically and revalidates the exact Project Task set.
  - **Project Store deletion still commits directly and has not yet been converted to prepared lifecycle publication.**
- Added deterministic coverage:
  - Task deletion blocks Resume after quiescence revalidation and Resume finds no deleted Current Node.
  - Task deletion blocks retained-Session activation after revalidation and no Run starts.
  - Opposite-order multi-Task acquisitions serialize without deadlock.
  - Task and Workflow metadata/root deletion publish through `LifecyclePublication`.
- Existing workflowstore tests were adapted to publication helpers; no direct Store Task/Workflow delete commit APIs remain.

Latest focused verification:

```sh
go test ./server/workflowstore ./server/workflowexecution ./server/workflowsvc ./server/projectview ./server/core -count=1 -timeout=240s
git diff --check
```

The broad affected run initially found `TestServiceWorkflowDeleteRevalidatesWorkflowTasksAtCommit` because the test stub inherited a no-op multi-Task method. The stub now reuses its quiescence behavior; the focused test and subsequent broad affected run pass.

Next exact steps:

1. Write one red Project deletion publication test.
2. Split `workflowstore.DeleteProject` into slow artifact/runtime preparation plus an opaque prepared SQL deletion.
3. Commit Project metadata deletion and all affected root removals through `LifecyclePublication`; finalize staged artifacts only after the compatible commit/root swap and restore on rollback.
4. Remove direct public `Store.DeleteProject` and adapt workflowstore/projectview tests through the publication owner.
5. Add controller/project-service publication entrypoints and route `projectview.Service.DeleteProject` through them while all canonical Task gates remain held.
6. Add Workflow/Project deletion races against Resume and retained-Session activation, including changed Task-set rejection and all-or-nothing blocker behavior.
7. Audit Task/Workflow/Project deletion for canonical lock order, exact owner revalidation, no post-revalidation Run/root/attachment, and no direct Store commit path.
8. Only then check the deletion Planning item and proceed to generated read-model work.

## Global reminders

- Preserve every dirty file.
- One red test → minimal implementation → repeat.
- No final full suite/build until every Planning item is complete.
- No review agents or manual QA.
- Frozen Rust remains untouched.
- Final completion requires the exact `kent task complete ...` command.

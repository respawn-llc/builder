# KENT-438 correctness re-review

- [x] Read the full updated plan and isolate changes since the prior rejection.
- [x] Verify explicit user activation vs technical reattachment against current client/server paths.
- [x] Verify hidden prepare and infallible visibility commit against Authority/Registry/runner boundaries.
- [x] Verify lifecycle abort and canonical hydration/live-event sequencing.
- [x] Verify protocol-102 compatibility deletion scope and ledger.
- [x] Trace remaining scope, ownership, deadlock, callback, and completion risks against origin/main.
- [x] Validate plan estimates/checklist determinism and reach verdict.
- [x] Execute the workflow transition with concrete findings or approved estimate.

# KENT-438 correctness re-review 2

- [x] Read the full revised plan and isolate the three requested changes.
- [x] Verify capability identity/ownership/cleanup against loopback and remote transport paths.
- [x] Verify Agent capacity lease lifetime through Exact/retiring.
- [x] Verify hydration gate/latch ordering and abort interaction.
- [x] Audit new capability/release machinery for simplification, bounds, and scope.
- [x] Scan unchanged architecture and completion criteria for new contradictions.
- [x] Validate estimates, ledger, and deterministic verification.
- [x] Execute the workflow transition.

# KENT-438 correctness re-review 3

- [x] Read the full revised plan and isolate the capability simplification/cleanup changes.
- [x] Verify the single Run proof against restart and overlapping gateway ownership.
- [x] Verify owner-specific terminal Registry cleanup after Run removal.
- [x] Recheck Agent capacity and hydration latch fixes.
- [x] Scan original acceptance, retained-Session completion, and unchanged architecture for blockers.
- [x] Validate ledger, estimate, and deterministic verification criteria.
- [x] Execute the workflow transition.

# KENT-438 correctness re-review 4

- [x] Confirm the updated plan/spec artifact delta since the prior approval.
- [x] Read the full 344-line plan and all 17 implementation slices.
- [x] Recheck protocol-102 hard-cut scope, retained Run proof, overlapping gateway cleanup, and transcript ordering.
- [x] Recheck original stranded-node and cross-surface Resume/Interrupt/completion acceptance.
- [x] Validate estimates, file ledger, and deterministic completion criteria against origin/main evidence.
- [x] Run formatting/contradiction checks and reach verdict.
- [x] Execute the workflow transition.


# KENT-438 correctness re-review 5

- [x] Confirm the artifact delta since the immediately prior approval.
- [x] Re-read the changed plan in the context of the prior full-document review.
- [x] Verify the Ongoing Mode lifecycle-prelude correction preserves one ordered sequence-numbered subscription.
- [x] Trace the new requirement to the planned shared message contract, Registry handoff, TUI reducer, and tests.
- [x] Recheck owning-spec ledger, file/line forecast, and all 17 slices.
- [x] Run repository-wide contradiction and diff-format checks.
- [x] Execute the workflow transition.


# KENT-438 correctness re-review 6

- [x] Confirm the reduced-scope plan/origin-main delta.
- [x] Read the full 333-line plan and all 17 slices.
- [x] Trace initial activation and automatic reactivation through protocol-101 client/server paths.
- [x] Trace Exact observation to existing Authority attachment and retirement races.
- [x] Recheck original cross-surface Resume/Interrupt/completion acceptance and reduced ledger.
- [x] Validate findings against origin/main code and deterministic test gaps.
- [x] Execute the workflow transition.


# KENT-438 correctness re-review 7

- [x] Confirm the typed-operation/attach-only plan delta.
- [x] Read the full 341-line plan and all 17 slices.
- [x] Verify automatic reconnect cannot Resume and Exact attachment is atomic.
- [x] Check technical reattachment identity across retirement, replacement, and restart generation reuse.
- [x] Trace every newly approved user-message/steering Resume path against origin/main and the file ledger.
- [x] Validate estimates, deterministic tests, and diff formatting.
- [x] Execute the workflow transition.


# KENT-438 correctness re-review 8

- [x] Confirm the follow-current and RunPrompt plan delta against the prior rejection.
- [x] Read the full 357-line plan and all 18 implementation slices.
- [x] Trace selected retained RunPrompt prompt/steer submission and result ownership against origin/main.
- [x] Recheck typed activation/open/technical reattachment semantics and exact attachment races.
- [x] Audit Run generation, prepared mutation, interruption, deletion, and fatal-shutdown interactions for blockers.
- [x] Validate original ticket and cross-surface completion acceptance, ledger, estimates, and deterministic tests.
- [x] Execute the workflow transition.

## Cycle 8 blocking findings

1. Selected retained RunPrompt submission is ordered after Exact registration, but the canonical Workflow runner immediately starts SubmitWorkflowTurn after lease release. The plan does not define how the prompt/agent_steer is admitted into that same first Workflow turn, or how result capture is correlated without starting a second UserTurn or losing to ErrAgentBusy. Existing RunPrompt progress is also bound at AgentRuntimePlan construction, which this retained path bypasses.
2. The typed initial activation operation has no concrete source in the current TUI lifecycle. All existing-session startup, picker, and navigation paths collapse to OpenExistingSessionLaunchIntent, and sessionLaunchPlan/runtimeattach.Request currently carry no distinction. The plan says Resume vs Open but does not map real client actions.
3. Selected retained RunPrompt currently accepts RunPromptOverrides and has defined headless ask/progress/timeout semantics. The Workflow-owned path cannot apply those overrides to a Run already owned by Workflow Execution, and the plan neither rejects nor defines them; it also does not define the changed Question behavior.


# KENT-438 correctness re-review 9

- [x] Confirm the user_activation/RunPrompt-removal delta.
- [x] Read the full 342-line plan and all 17 implementation slices.
- [x] Reconcile the new product boundary against the user-approved Resume/Open/technical recovery decisions.
- [x] Verify the original TUI/Board/CLI single-authority and kent task complete acceptance remains complete.
- [x] Recheck client activation semantics, exact attachment, and restart recovery against origin/main.
- [x] Validate scope ledger, estimates, and deterministic tests.
- [x] Execute the workflow transition.

## Cycle 9 blocking findings

1. Preserving selected-Session RunPrompt leaves an ordinary ReplaceAgentResource authority for an interrupted retained Workflow Session. This contradicts the plan own no-competing-Runtime invariant and reproduces the no-matching-run completion failure.
2. Collapsing every fresh Session activation into user_activation removes the previously approved attach-only plain Open operation and makes in-app navigation resume interrupted work. This is an unapproved product expansion; restore distinct Resume/Open intent or obtain explicit reversal.


# KENT-438 correctness re-review 10

- [x] Confirm the retained-RunPrompt continuation and activation decision delta.
- [x] Read the full 365-line plan and all 18 implementation slices.
- [x] Trace the proposed RunPrompt preparation handle, shared builder, progress, result, cancellation, and completion contracts against origin/main.
- [x] Recheck cross-surface Run/Exact deduplication, Session activation, Interrupt, and kent task complete acceptance.
- [x] Audit Run generation, prepared mutation, capacity, deletion, fatal shutdown, and restart paths for remaining blockers.
- [x] Validate ledger, estimates, deterministic criteria, and diff formatting.
- [x] Execute the workflow transition.

## Cycle 10 blocking findings

1. Activation and Interrupt routing do not cover later execution-starting TUI operations. After the joined Workflow Exact retires or is interrupted, the still-open attachment can call runtimecontrol SubmitUserTurn/shell/compact, whose origin/main RunAgentExecution path starts an ordinary CurrentAgentResource execution with no Workflow lease. This recreates Board Resume plus kent task complete no-matching-run despite the plan own no-ordinary-runtime invariant.
2. The fatal-disposition matrix omits outcome-less Exact finalization. A RunPrompt or interactive Workflow turn may return without completing its Current Node; origin/main ExecutionFinalized then must interrupt the admitted row. If that write fails, removing the Run leaves the exact stranded state KENT-438 exists to eliminate.
3. Retained RunPrompt Begin preserves idle-only behavior only against a Run owning the selected successor Current Node, not against any active Authority execution for the selected Session. A retiring predecessor Exact can still own that Session after its committed successor was interrupted and its Run removed, so Begin can Resume/plan and then collide with the active predecessor.


# KENT-438 correctness re-review 11

- [x] Confirm the three requested corrections and origin/main baseline delta.
- [x] Read the full 389-line plan and all 19 implementation slices.
- [x] Trace the new attached-operation boundary through SubmitUserTurn, shell, compact, Goal, RunPrompt, and Authority guards.
- [x] Verify outcome-less finalization ownership/fatal ordering and predecessor-retiring RunPrompt behavior.
- [x] Recheck #733 completion-lock interaction, prepared transactions, Run registry, deletion, restart, and read projection.
- [x] Validate the updated ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 11 blocking findings

1. The ordinary-start guard is a check-then-act race against newly committing retained Session ownership. Adapter routing and the Authority guard can both observe no direct Current Node, then a prepared Workflow mutation can commit a retained successor before the lease-less StartAgentExecution installs. No shared Session admission critical section is defined, so a Run and ordinary exact can coexist.
2. Exact registration is treated as operation readiness. A queued/launching attached operation waiter invokes its callback as soon as the Run reaches exact, but the Workflow Runner may not have begun its first turn. SubmitUserTurn can win the idle Engine and run before the required Workflow turn, unlike origin/main’s ErrSessionRunStarting/active-run queue behavior.
3. Cancellation of an operation-created pre-Exact Run is undefined. The plan says cancellation must not stop another request’s Run, but does not say what happens when the canceled request created the Run and its single-use callback has not executed; leaving it schedules canceled input later, while removing it without durable interruption strands the Current Node.


# KENT-438 correctness re-review 12

- [x] Confirm the revised-plan and origin/main delta since Cycle 11.
- [x] Read the full 401-line plan and all 19 implementation slices.
- [x] Verify the approved non-guarantee for currently-unbound ordinary starts is scoped consistently.
- [x] Trace first-operation acceptance and cancellation ownership for all attached-operation profiles.
- [x] Recheck cross-surface Resume/Interrupt/completion, RunPrompt, restart, and fatal-disposition paths.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 12 blocking findings

1. One profile-acceptance milestone is being asked to represent two different boundaries: the owning profile actually winning Engine/exclusive-step admission, and the Runtime Command becoming committed so cancellation can no longer prevent its effect. The existing operation paths do not expose one common event through the planned `func(context.Context, *runtime.Engine) error` callback: ordinary `SubmitWorkflowTurn` has no acceptance hook; user submit may first run pre-submit compaction and only later accepts the user message; shell/compact expose active hooks; and Goal can be accepted from typed receipts even when the callback returns an error. The plan therefore cannot both prevent joiner preemption and preserve per-operation commit/cancellation semantics with the proposed callback shape.
2. Pre-submit compaction is not an `ExecutionAdapter.RunAgentExecution` start caller on origin/main; it executes inside the already-routed user-turn callback. Re-entering the router for a separate pre-submit operation would make it join/wait on the same still-unaccepted profile whose acceptance depends on that compaction finishing, creating a self-deadlock. Leaving it nested makes the planned separate pre-submit operation kind and start-boundary tests impossible.
3. The cancellation rules are internally contradictory and conflict with the retained Runtime Control path. The profile section says post-accept cancellation only ends response waiting and must not interrupt the Run, while Session Interrupt says to preserve targeted-operation cancellation and delegate an interruptible launching/Exact Workflow Run to persist-before-stop. Origin runtimeops intentionally marks accepted active submit/shell/compact operations as interruptible, and Runtime Control then interrupts the Engine. The TUI contract simultaneously says Ctrl+C may interrupt a retained launching Run and that Ctrl+C does not cancel a submission before its Agent Turn starts. The plan never distinguishes transport cancellation, targeted pre-active operation cancellation, targeted post-commit interruption, and generic Session Ctrl+C, so the implementation and reconciliation outcome are undefined.



# KENT-438 correctness re-review 13

- [x] Confirm the revised-plan and origin/main delta since Cycle 12.
- [x] Read the full 422-line plan and all 19 implementation slices.
- [x] Trace the split owner-ordering and Runtime Command effect authorities per profile.
- [x] Verify nested pre-submit behavior and cancellation/Interrupt reconciliation against origin/main.
- [x] Recheck cross-surface Resume/Interrupt/completion, RunPrompt, restart, and fatal-disposition paths.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.


## Cycle 13 blocking findings

1. The two-phase targeted-cancellation protocol has no irrevocable completion rule after the Workflow interruption transaction commits. The TUI Interrupt RPC has a 3-second request timeout, while persistence and stop cleanup can outlive it. If the request is canceled after durable interruption but before `runtimeops` commits cancellation, invokes the cancel handle, and stops/cleans the Run, the Current Node is interrupted while the operation and Exact authority can remain live. Define durable commit as the point after which phase two finishes under server-owned bounded cleanup independent of request context, and add deterministic request-loss barriers immediately before and after that commit.
2. The canonical routing section incorrectly places a queued-submit target in the active Workflow interruption path. Origin/main excludes `RuntimeOperationKindQueuedMessage` from active interruption and only discards that queue item; the parent submit is already terminal `submitted` or `committed` and also does not interrupt. Plan line 296 would turn removal of queued input into durable Current-Node interruption and Exact stop, or leaves the term ambiguous. Keep queued-message discard/reconciliation entirely in Runtime Control and reserve Workflow stop for an actually active interruptible submit, shell, or compaction target.
3. The blanket transport-cancellation rule is still undefined for Goal-created Runs. User turn, shell, and compaction use detached `runtimeops` attempt contexts, but Goal is request-memoized and passes the transport context directly into `ExecutionAdapter`. If that context is canceled after the Goal request creates a queued or launching Run, the plan does not say whether the stored driver continues exactly once, runs later after its memo entry was deleted, fails the Exact and interrupts the Current Node, or is removed. Define Goal profile/memo lifetime and retry behavior across transport cancellation, with pre-Exact and Exact-before-ordering barriers proving no duplicate Goal effect and no ownerless Run.
4. The TUI pre-active choice relies on `RuntimeStatus.WorkflowSession`, which reflects the Engine current-node config rather than current direct durable binding and can remain present after Workflow movement away from that Session. The plan says the server revalidates binding but never defines the no-longer-bound result. Specify that stale retained classification releases the prepared cancellation and follows ordinary pre-active detach without canceling the ordinary submission, and test move-away plus stale cached status.


# KENT-438 correctness re-review 14

- [x] Confirm the revised-plan and origin/main delta since Cycle 13.
- [x] Read the full 430-line plan and isolate the four requested corrections.
- [x] Verify post-commit cancellation ownership against Runtime Control/request memo/server shutdown lifetimes.
- [x] Verify queued-message confinement and stale retained-status fallback against origin/main control flow.
- [x] Verify admitted Goal memo lifetime, retry identity, and terminal paths against origin/main.
- [x] Recheck cross-surface Resume/Interrupt/completion, RunPrompt, restart, and fatal-disposition paths.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.


## Cycle 14 blocking findings

1. The admitted Goal exact-once guarantee is incompatible with the existing requestmemo capacity fallback. Plan lines 297-299 require every admitted Goal to remain represented by one memo-owned driver under the existing TTL/capacity policy, but origin/main requestmemo.Memo.Do executes run directly without inserting an entry when all 1,024 slots are in flight. At capacity, a Goal can therefore create/join a Run untracked; transport loss followed by a same-ID retry can start a second driver. Define admitted-work capacity as fail/wait-before-admission rather than un-memoized execution, scope that behavior so unrelated Memo callers do not silently change, and add a saturation test proving no Run/Goal effect starts without a memo entry.
2. Post-commit Interrupt cleanup still has no duplicate-request ownership contract. Plan lines 140, 209, 215, 301, and 327-329 define an irrevocable cleanup owner, but do not say what Runtime Control does when the same Interrupt is retried while that owner is stop-pending or after it finishes. RuntimeInterruptRequest already carries ClientRequestID, while origin/main Service.Interrupt ignores it and has no Interrupt memo. Define one owner keyed by the existing request ID or exact cleanup identity: duplicates must join/observe the same cleanup, never re-prepare or fall through to another Workflow/ordinary interrupt, and after cleanup must return final/fresh reconciliation without affecting a later resumed Run. Add same-ID barriers during post-commit cleanup and after a replacement Run starts.
3. The initial red-test slice has impossible baseline completion criteria. Plan line 396 says origin/main tests must expose queued-message targeting reaching Workflow stop, post-commit cleanup loss, admitted-Goal duplication, and stale retained routing. Origin/main already excludes queued-message targets from active interruption, and the other three mechanisms do not exist until this redesign is introduced. Keep the first red slice to actual baseline bugs; move these design-regression guards into the cancellation/Goal/TUI implementation slices and classify the already-correct queued-message case as a green characterization guard.


# KENT-438 correctness re-review 15

- [x] Confirm the revised-plan and origin/main delta since Cycle 14.
- [x] Read the updated 438-line plan in the context of the prior full-document review.
- [x] Verify strict admitted-work capacity and lifetime against requestmemo semantics.
- [x] Verify Interrupt request deduplication, fresh response, and replacement-Run isolation.
- [x] Verify test-slice resequencing and queued-message characterization.
- [x] Scan the remaining architecture/planning for second-order regressions from the new memo owner.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.


## Cycle 15 blocking findings

1. The strict Interrupt memo has no admission/completion matrix for the non-Workflow-cleanup paths it now owns. Plan lines 142 and 333 apply the memo to every Runtime Interrupt, but lines 335-337 define retention only around a committed Workflow stop. Runtime Control also has queued-message discard, absent/terminal target, joiner-only cancellation, stale not-retained, ordinary Engine interrupt, and pre-commit persistence-failure outcomes. The plan never says when those paths mark the strict entry admitted, which terminal no-op/error outcomes remain cached, or when transport cancellation deletes the entry for retry. If stale not-retained or an ordinary/queued side effect is treated as pre-admission, the same request ID can later execute against a newly bound/resumed Run; if the cleanup-only completion rule is applied literally, no-cleanup entries never complete and consume all 1,024 slots. Add one explicit outcome table: local side effects become admitted before mutation; absent/terminal/not-retained outcomes complete as cached no-ops; cancellation/failure before any effect rejects and deletes; Workflow pre-commit failure remains retryable; committed Workflow cleanup completes only after removal/fatal disposition. Add same-ID tests for stale not-retained followed by rebind, ordinary Interrupt followed by a new active run, queued-message discard, and capacity reclamation.


# KENT-438 correctness re-review 16

- [x] Confirm the revised-plan and origin/main delta since Cycle 15.
- [x] Read the updated 452-line plan in the context of the prior full-document review.
- [x] Verify every strict Interrupt matrix row against origin/main pending-queue and target behavior.
- [x] Verify transport cancellation, entry eviction, and same-ID isolation for local/Workflow outcomes.
- [x] Recheck cross-surface Resume/Interrupt/completion and attached-operation acceptance.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.


## Cycle 16 blocking findings

1. The Interrupt matrix still drops the existing independent pending-queue drain for targeted no-op/local outcomes. Plan rows 340 and 343 classify an absent/terminal target or joiner-only cancellation as having no Workflow/Engine mutation beyond the target result, while origin/main Runtime Control always walks `PendingOperationRefs` and discards queued-message refs even when the target is terminal, absent, or does not interrupt the active Engine. The plan only assigns pending-queue drain to ordinary Session Interrupt and committed Workflow phase two. Implemented literally, Ctrl+C racing a terminal target or canceling a joiner can leave the queued messages it currently removes; alternatively, adding the drain ad hoc would violate the matrix admission rule because the request was cached as a no-op before that effect. Make pending-queue disposition an explicit independent axis: stale not-retained remains the approved no-discard exception; retained Workflow stop defers drains to post-commit phase two; ordinary/terminal/joiner local paths with queued refs admit before draining; cached no-op applies only when no target or queue effect remains. Add deterministic terminal-target-plus-queued-item and joiner-target-plus-queued-item guards.


# KENT-438 correctness re-review 17

- [x] Confirm the revised-plan and origin/main delta since Cycle 16.
- [x] Read the updated 460-line plan in the context of the prior full-document review.
- [x] Verify the target/outcome-by-pending-queue matrix and strict memo lifecycle against origin/main.
- [x] Recheck request loss, same-ID replay, replacement-Run isolation, and queue reconciliation.
- [x] Scan the remaining architecture/planning for second-order regressions from the matrix correction.
- [x] Validate ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 17 blocking findings

1. The revised matrix requires admitted local Interrupt work to survive caller cancellation, but the coordination architecture still says the only request-detached work is post-commit Workflow cleanup and an admitted Goal driver (plan line 174). Local queued-message drains, joiner cancellation, and ordinary Engine Interrupt are now explicitly server-owned continuations after admission (lines 335, 342-344, 354, and 358). Implementing the earlier exclusivity statement leaves those effects request-owned and allows a lost 3-second RPC to stop them after memo admission; implementing the later matrix violates the stated lifecycle inventory and scope audit. Add admitted local Runtime Interrupt continuation to the explicit server-lifecycle ownership list, define its terminal cleanup/removal rule, and keep the caller as only a waiter.
2. Pre-effect targeted-cancellation preparation has no cancellation rule while it waits for Runtime Command's existing commit barrier. The plan reserves the strict memo entry before preparation and promises that transport cancellation before any effect rejects/deletes the entry, but origin/main `runtimeops.CancelOperationTarget` and commit acquisition wait on `barrier.done` without selecting the request context. If that barrier is delayed, a canceled Interrupt can remain stuck before admission, retain one of the 1,024 strict slots indefinitely, and make same-ID retries join the stuck entry even though no effect happened. Require the new prepare API to abort barrier acquisition on request cancellation without installing a tombstone/fence or later effect, and add a deterministic barrier-held cancellation/retry test proving the entry is reclaimed and the retry performs exactly one target/queue disposition.


# KENT-438 correctness re-review 18

- [x] Confirm the revised-plan and origin/main delta since Cycle 17.
- [x] Read the current 461-line plan using the prior complete review plus the complete revised-section scan.
- [x] Verify admitted local Interrupt lifetime and strict-entry completion ownership.
- [x] Verify context-cancelable Runtime Command commit-barrier preparation against origin/main.
- [x] Recheck cross-surface Resume/Interrupt/completion and pending-queue behavior.
- [x] Validate slices, ledger, estimates, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 18 blocking findings

None.


# KENT-438 correctness re-review 19

- [x] Confirm the revised-plan and origin/main delta since Cycle 18/compliance review.
- [x] Read the current 471-line plan using the prior complete review plus the complete revised-section scan.
- [x] Verify retained-Workflow TUI-startup exception coverage and ordinary lazy-open preservation.
- [x] Audit the new canonical requestmemo hard cut against origin/main identity/capacity ordering and callers.
- [x] Recheck strict Goal/Interrupt same-ID ownership under saturation.
- [x] Validate updated ledger, estimates, slices, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 19 blocking findings

1. The new canonical requestmemo hard cut is ambiguous in exactly the case strict Goal/Interrupt ownership depends on. Design line 141 and Architecture line 310 say both entrypoints return capacity-unavailable whenever all 1,024 entries are in flight, while the same plan requires same-ID callers to join or receive the request-ID-reuse error. Origin/main `Memo.Do` checks an existing request ID and validates its payload before testing capacity, so an in-flight duplicate still joins at full capacity. Implemented literally from the new wording, a duplicate of one of the 1,024 admitted Goal/Interrupt requests can receive capacity instead of joining its one owner, and a mismatched duplicate can hide the reuse error. Specify that capacity is only a new-ID reservation failure after existing-ID lookup and payload validation. Add deterministic core and Goal/Interrupt tests with all slots in flight proving same ID/same payload joins, same ID/different payload returns reuse error, a genuinely new ID gets capacity, and none starts a second callback/effect. The hard-cut caller audit/checklist should enumerate all current Memo owners (`processview`, `promptcontrol`, `runprompt`, `runtimecontrol`, `sessionlaunch`, `sessionservice`, and `workflowsvc`), not only the four currently named in planning line 463/ledger line 414.


# KENT-438 correctness re-review 20

- [x] Confirm the revised-plan and origin/main delta since Cycle 19.
- [x] Read the current 472-line plan using the prior complete review plus the complete revised-section scan.
- [x] Verify existing-ID lookup/payload validation precedes new-ID capacity rejection for both memo entrypoints.
- [x] Verify full-capacity Goal/Interrupt duplicate, mismatch, new-ID, and no-second-effect guards.
- [x] Verify the global hard-cut prerequisite and all seven Memo-owner audits are sequenced and ledgered.
- [x] Recheck TUI activation, cross-surface Run authority, pending-queue, estimates, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 20 blocking findings

None.


# KENT-438 correctness re-review 21

- [x] Confirm the revised-plan and origin/main delta since Cycle 20/compliance review.
- [x] Read the current 473-line plan using the prior complete review plus the complete revised-section scan.
- [x] Verify number-agnostic request-identity ownership and supported-entrypoint saturation strategy.
- [x] Search the full plan for stale numeric capacity assertions and test-only capacity assumptions.
- [x] Recheck request identity ordering, all seven owners, TUI/Run authority, estimates, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 21 blocking findings

1. The revised number-agnostic request-identity contract still has one literal implementation-capacity test in Architecture line 325: “Hold all 1,024 admitted-work entries in flight.” That directly contradicts Recon line 73, Design line 143, Composition lines 394/397, the ledger, and Planning lines 464-471, all of which say tests must reach typed saturation through supported production entrypoints without asserting the admitted count or adding a capacity hook. An implementer following line 325 would lock the internal tunable back into Goal tests; following the later sections leaves two competing completion criteria. Replace line 325 with the same supported-entrypoint drive-to-capacity procedure used elsewhere, then prove new-ID rejection, duplicate precedence, no effect, release/completion reclamation, and one subsequent new-ID admission without asserting the count.


# KENT-438 correctness re-review 22

- [x] Confirm the revised-plan and origin/main delta since Cycle 21.
- [x] Read the current 473-line plan using the prior complete review plus the complete revised-section scan.
- [x] Verify the stale numeric capacity criterion is removed repository-wide from the plan.
- [x] Verify supported-entrypoint saturation covers duplicate precedence, no effect, reclamation, and subsequent admission.
- [x] Recheck requestmemo ownership, all seven callers, TUI/Run authority, estimates, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 22 blocking findings

None.


# KENT-438 correctness re-review 23

- [x] Confirm the revised-plan and origin/main delta since Cycle 22/compliance review.
- [x] Read the current 488-line plan using the prior complete review plus the complete estimate/ledger/planning revision scan.
- [x] Validate the eight-vertical file/LoC arithmetic and Product-approved scope/cap supersession.
- [x] Recheck deletion-first constraints and cross-surface SSoT acceptance coverage.
- [x] Audit whether revised implementation slices match the newly disclosed blast radius.
- [x] Validate diff formatting and workspace state.
- [ ] Execute the workflow transition.

## Cycle 23 blocking findings

1. The new bottom-up estimate exposes that Planning item 15 is not an implementation slice: the “attached-operation router, request identity, Goal/Interrupt lifetime, Runtime Command cancellation” vertical is forecast at 23–30 changed files and 5,300–8,200 changed lines, yet almost all of it remains one checkbox at lines 479–480. That single gate combines a global seven-owner `requestmemo` hard cut, the `ExecutionAdapter` callback-to-driver API cutover, owner-ordering hooks in Engine operations, retained Workflow routing and Goal lifetime, two-phase Runtime Command cancellation, the complete Runtime Interrupt target×queue matrix/server-owned cleanup, and CLI Ctrl+C behavior. A late cancellation or reconciliation defect cannot be isolated from the earlier API/ownership cutovers, and the plan gives implementers/reviewers no independently finishable green boundary after the memo prerequisite. Split this item into separately completable hard-cut slices with focused tests and no temporary dual authority: (a) canonical requestmemo contract/caller migration; (b) typed drivers plus owner-ordering/nested-pre-submit behavior while preserving ordinary execution; (c) retained attached-operation/Goal Run routing and transport-loss lifetime; (d) Runtime Command preparation plus strict local/Workflow Interrupt matrix and request-loss cleanup; and (e) TUI Ctrl+C/cross-surface integration. Keep the approved behavior and total estimate unchanged, but give each boundary deterministic completion criteria and deletion checks before the next begins.


# KENT-438 correctness re-review 24

- [x] Confirm the revised-plan and workspace delta since Cycle 23.
- [x] Read the complete current 499-line plan.
- [x] Verify that former item 15 was decomposed into 15a–15e with hard-cut deletion criteria.
- [x] Correlate the revised slice boundaries and Task coordinator contract with origin/main call sites.
- [x] Recheck the rename-aware ledger, estimate, deterministic completion criteria, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 24 blocking findings

1. Planning slice 15b is still not independently green. Its production scope is the typed-driver/ordinary-Session cutover, and the retained Workflow router/Run join path does not exist until 15c, while selected RunPrompt routing does not exist until item 16. Nevertheless 15b's completion criterion requires Exact-registration-versus-joiner barriers for “each owning profile.” That behavior can only be exercised through the later Workflow Run/router/builder path; a 15b-only test would have to fake the future router and would not prove the product invariant. Limit 15b to the closed drivers, ordinary execution, nested pre-submit, and each driver's ordering-notification contract; put retained Workflow Exact-before-ordering join barriers in 15c, and put the selected-RunPrompt barrier in item 16. Each slice must compile and pass against the production boundary available at that checkpoint.
2. The proposed `TaskMutationCoordinator` leaves reentrancy authorization live for as long as a context object exists, not for the lexical lifetime of the outer Task writer. Architecture line 184 only says a nested call may bypass acquisition when the same Task ID is “already carried by the context”; origin/main's current `MutationPermit` uses exactly that pointer-marker pattern. A callback or server-owned continuation that retains the enriched context can call `Run` after the outer operation has released its writer and bypass both a concurrent same-Task writer and a pending deletion freeze, defeating the new sole serialization authority. Specify an unforgeable active writer token that is invalidated before outer release and revalidated under the coordinator lock on every nested entry, and require detached launch/Goal/Interrupt contexts to be derived without that token. Add an escaped-context test: capture the nested context, let the outer call return, then prove reuse cannot enter while another same-Task writer or deletion freeze owns coordination.
3. The rename-aware ledger and major-vertical estimate do not describe the same changed-file set. Rows 15a and 15b alone list 17 distinct production paths (nine requestmemo/caller paths and eight driver/runtime paths), before 15c–15e add more, but the corresponding attached-operation major vertical forecasts only 11–14 production files. If the seven caller files in 15a are audit-only and expected to remain unchanged, they cannot be presented as changed-path ledger owners; if they require migration, the vertical and total file forecasts are understated. Separate audit-only inspected paths from changed paths, assign every expected changed path exactly once, and recompute the per-vertical and rounded-union forecasts before approval.


# KENT-438 correctness re-review 25

- [x] Confirm the current plan/workspace delta since Cycle 24.
- [x] Re-read the revised coordinator, attached-operation slices, ledger, estimates, and their surrounding full-document context.
- [x] Verify 15b is independently green and retained/RunPrompt ordering barriers moved to the first production slice that owns them.
- [x] Verify active-token lifetime, token-free detached contexts, and escaped-context tests close the coordinator bypass.
- [x] Verify audit-only requestmemo callers are separated and the 15–18 / 64–82 file arithmetic reconciles with the ledger.
- [x] Recheck cross-surface Resume/Interrupt/completion acceptance, stale numeric/estimate wording, workspace state, and diff formatting.
- [ ] Execute the workflow transition.

## Cycle 25 blocking findings

None.

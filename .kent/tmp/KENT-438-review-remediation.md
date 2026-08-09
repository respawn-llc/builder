# KENT-438 review remediation

## Review findings

- [x] **P0 compatible capture architecture:** `LifecyclePublication` must retain the complete immutable exact/live-blocking observation. Remove the post-capture Authority join and controller-registry quiescence sample. Route Attention through the same capture.
- [x] **P1 Script start ordering:** keep Script Authority/run state queued until `cmd.Start` succeeds; publish exact/running only from start confirmation. Start failure must publish typed interruption.
- [x] **P1 runtime failure ownership:** Authority retirement and Run deletion must wait for successful typed interruption publication. Publication failure must retain the exact owner and return through a retryable/continuing finalizer protocol, not strand the Current Node.
- [x] Add repository-wide guards for read-model Authority access outside the publication/capture owner.
- [x] Run focused regression/build, commit the complete remediation round, then execute `kent task complete ...`.

## Authoritative architecture direction

The approved plan already decides the product/architecture behavior, so no new
user decision is needed:

1. Extend the immutable lifecycle root's exact entry from Scope identity only
   to a complete typed execution observation:
   - Current Node and Exact Scope;
   - Agent Session or Script target;
   - live phase (`running` or mandatory `finalizing`);
   - complete pending Question/Session-Approval facts needed by status and
     Attention.
2. Runs remain the sole queued authority. Authority remains the sole execution
   and Interrupt authority. The immutable root is observation only.
3. Exact start, phase/prompt changes, and retirement publish typed exact-field
   deltas. Old captures need no later Authority lookup.
4. Read quiescence is derived from the pinned published root: root-owned Tasks
   are non-quiescent; selected Tasks absent from the root are the prior stable
   stopped publication and are quiescent. Never sample staged controller Runs
   after pinning the root.
5. Attention consumes the same `TaskStatusProjection.WithSnapshot` pair and
   root-owned complete prompt facts; it does not read Authority directly.
6. Runtime/finalizer failure keeps the Authority execution in mandatory
   finalization and the controller Run owned until interruption publication
   succeeds. The callback error cannot be interpreted as a completed durable
   disposition.

## TDD order

1. [x] Red old-root retirement capture test: pin a capture containing an exact
   execution, publish/retire the live scope, then materialize the old capture;
   it must return the complete prior execution without Authority.
2. [x] Red staged-Run quiescence test: pin old stopped root while a Run is staged
   before publication; projection must retain prior `CanDelete=true`.
3. [x] Implement complete typed exact observations/deltas and root-derived
   quiescence; remove Authority join from
   `workflowexecution/task_execution_observation.go`.
4. [x] Red live Question/Approval Attention old-capture test, then route Attention
   through the shared capture and complete root prompt facts.
5. [x] Red slow/failed Script `cmd.Start` tests proving queued until process start,
   exact publication only afterward, and typed interruption on failure.
6. [x] Red Agent and Script runtime-failure publication-error tests proving Run and
   exact Authority ownership remain until a later successful interruption
   publication.
7. [x] Add architecture guards, run affected packages, build once, and commit.

## Existing evidence

- Review evidence is valid:
  - `workflowexecution/task_execution_observation.go` currently pins root/SQLite
    then samples Authority and controller state later.
  - `sessionruntime/script.go` starts the process asynchronously after returning
    the execution handle.
  - `FinalizeCurrentNodeResult` currently treats a runtime error with no pending
    completion as already durably handled even when interruption publication
    failed.
- Preserve all existing dirty KENT-438 files.
- Do not call review agents or perform manual QA.

# KENT-438 review remediation round 2

## User decision

- [x] The reviewer-proposed 1,000-line decomposition threshold is not present
  in AGENTS.md, the approved KENT-438 plan/specs, or repository guards.
- [x] Nikita chose a non-blocking follow-up. Created KENT-458,
  "Decompose workflow lifecycle modules."

## Behavioral blockers

- [x] Distinguish committed lifecycle disposition from post-commit event
  delivery failure. Completion/interruption must retire and confirm committed
  ownership while surfacing notification errors.
- [x] Make queued-to-running publication the single boundary that enables both
  root running state and Authority Interrupt eligibility. Preserve cancellation
  through the handshake.
- [x] Pass cancellable contexts through production Agent/Script finalization
  until the short publication boundary begins; cancellation must abandon
  prepared success and publish typed interruption.
- [x] Add deterministic completion/interruption notification-failure tests,
  Agent/Script pre-publication Interrupt barriers, and production runner
  finalizer-cancellation tests.
- [x] Run focused regression and final build, commit the full remediation
  round, then transition KENT-438.

## Current checkpoint

- Added `LifecyclePublicationOutcome` and changed completion/interruption
  publication contracts to return committed state separately from delivery
  errors.
- Added store-level red/green coverage for completion publisher failure and
  interruption event-context cancellation; both prove the root/durable
  disposition committed while the notification error remains surfaced.
- Updated controller completion/interruption paths and test publication fakes
  to consume committed outcomes. Focused compile for workflowstore,
  workflowexecution, and workflowsvc passes.
- Added controller-level completion/interruption tests proving committed event
  errors remain surfaced while Authority and controller ownership retire.
- Added a typed `WorkflowRunningPublication` handshake. Authority remains in a
  publishing phase and consults the immutable Exact publication for Interrupt
  eligibility; the controller stages Run/index ownership before the root swap
  and rolls it back on publication failure.
- Added deterministic Agent and Script publication barriers proving Interrupt
  is unavailable before Exact publication and targets the matching scope after.
- Production Agent and Script finalizers now retain cancellation through
  finalizing publication and successful result handling, using
  `WithoutCancel` only for typed interruption cleanup. Production finalizer
  tests cover cancellation during finalizing publication.

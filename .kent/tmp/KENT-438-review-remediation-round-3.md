# KENT-438 review remediation round 3

## Adjudication

- [x] The root-derived Interrupt predicate is a valid architecture regression
  against the approved plan: Authority must remain sole running/Interrupt owner.
- [x] Exact registration accepting a concurrently stopped publishing Run is a
  valid race introduced by round 2.
- [x] The repeated file-size finding remains rejected for KENT-438 by Nikita's
  prior explicit decision and is tracked as non-blocking KENT-458.
- [x] The retained-Session successor association gap is an in-scope invariant
  counterexample explicitly directed into KENT-438.

## Remediation

- [x] Replace root-derived `WorkflowRunningPublished` with an Authority-owned
  staged activation capability.
- [x] Commit exact-root registration and Authority running activation under one
  Authority linearization boundary, with controller ownership revalidation.
- [x] A publishing Run stopped before activation must return a typed
  no-longer-live outcome and must never start its Agent runner or Script body.
- [x] Rewrite Agent/Script publication barriers so they prove Authority-owned
  activation, including Task/Session Interrupt and Manual Move classifications.
- [x] Atomically publish retained-Session successor provenance with the
  successor Current Node; cover crash-before-Starter Resume and idempotency.
- [x] Run focused regressions, compile sweep, final build, commit, and complete.

# KENT-438 review remediation round 4

## User adjudication

- [x] Replace callback-under-`Authority.mu` with a staged, typed activation
  token committed by Lifecycle Publication.
- [x] Repair already-persisted missing retained-Session successor provenance
  during Resume when durable ownership structurally agrees.
- [x] Keep the broad file decomposition out of KENT-438; KENT-458 owns it.

## Remediation

- [x] Add a red recovery test that removes successor provenance after
  publication, resumes it, and proves repair plus repeated idempotence.
- [x] Move consistent missing-provenance repair into the canonical Store
  binding boundary while preserving explicit contradictory-ownership failure.
- [x] Add red running-publication tests for a typed activation token with no
  externally supplied callback.
- [x] Make Lifecycle Publication validate/build first, activate the staged
  Authority execution, and then perform only the infallible root swap.
- [x] Update controller Agent/Script activation and test publications as one
  mechanism; preserve pre-activation Task/Session Interrupt and Manual Move.
- [x] Run focused tests, server compile sweep, final build, duplication scan,
  `git diff --check`, commit, and complete.

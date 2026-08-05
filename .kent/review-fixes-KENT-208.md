# KENT-208 review remediation — second round

- [x] Aggregate Task-wide Current Node interruptions and publish one wake after the aggregate commit.
- [x] Make live access Questions answerable through Task selection and remove timestamp identity fallback.
- [x] Preserve Script discriminators when a current node also has a Session context.
- [x] Restore deleted unrelated regression suites and add owning-boundary coverage.
- [x] Restore exact live-control authority for existing steer, stop, and wait paths.
- [x] Preserve authoritative Run terminal reason and diagnostic data.
- [x] Surface wake publication failures through owning operation boundaries.
- [x] Revert unauthorized TUI specification behavior change.
- [x] Reduce final production diff to the amended 1,650-line ceiling and verify.

## Third-round review

- [x] Preserve committed Current Node completion results and continue successor starts when wake publication returns an error.
- [x] Roll back Registry pending-prompt and Attention projections when Exact-Scope Task wake publication fails.
- [x] Exclude Workflow Transition Approvals from `kent question --task` while retaining live access Questions.
- [x] Represent Run terminal reason presence without an empty-string field sentinel.
- [x] Add owner-boundary coverage for committed completion errors, aggregate interruption wakes, publication identity/errors, and transition-approval filtering.
- [x] Keep pre-commit completion failures from mutating controller lifecycle state.
- [x] Cover initial Run-watch Question arbitration and Exact-Scope prompt publication rejection.

## Fourth-round review

- [x] Omit stale live access approvals before creating Task question candidates, covering show and answer paths.
- [x] Remove unapproved QuestionBatch compensation after the user rejected that hardening.
- [x] Remove unapproved aggregate Current-Node interruption coordination after the user rejected that hardening.
- [x] Remove inferred committed-result authority and partial-success continuation after the user rejected that hardening.
- [x] Keep wake publication errors surfaced at the existing operation boundary; accept ghost prompt/batch projections per the user decision.
- [x] Keep the production ceiling at the previously authorized 1,650 lines; follow-up design approval is required for any stronger coordination.

## Fifth-round review

- [x] Record the authoritative August 5, 2026 User decision in the Design:
  keep post-commit wake errors surfaced, accept stale or ghost prompt and
  Question-Batch projections, preserve durable Current-Node mutations while
  returning existing operation errors/empty results, and do not add
  compensation, aggregate transactions, inferred partial-success semantics,
  successor continuation, retries, or cross-authority recovery.
- [x] Add blocked Run-watch arbitration coverage for prompt wake, terminal
  completion, attention-stream loss, and caller cancellation.
- [x] Add Registry owner-boundary coverage proving Task wake publication comes
  from immutable Workflow Exact Execution Scope identity and is excluded for
  non-Workflow scopes.

## QA remediation round

- [x] Keep malformed `run watch` selectors on the live-control route so leaf
  validation rejects them before any ordinary Run can start.
- [x] When the live attention path closes with `context.Canceled` while the
  caller remains active, wait for and project the authoritative terminal
  interruption instead of returning the stream cancellation as the outcome.
- [x] Render typed interruption reason and diagnostic, with exit code 130, and
  cover the CLI and runtime-control regressions.

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

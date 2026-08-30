# KENT-608 review remediation

- [x] Audit the current retained boundary tests against the new review findings.
- [x] Add real retained continuation boundary coverage for assignment delivery, partial Resume, cancellation, contention, selected results/progress, rejection/history, and Thinking.
- [x] Replace the retained boundary matrix with compact real Workflow/Runtime Control cases for non-resumable/moved and TaskResumeNoOp rejection, selected assistant-final output, TUI partial failure, selected delivery/Resume failure, and pre-delegation file-backed contention.
- [x] Restore compact public retained-conflict transport/UI coverage.
- [x] Consolidate the retained-continuation scenarios into one boundary matrix and record the approved budget exception. The final ticket diff changes 13 test files because seven separate packages require unavoidable compile-time adaptations to the new production continuation contracts; the deleted runtime tests cannot be restored without the removed compatibility symbol. The user approved retaining this exception and relying on existing runtime/workflow integration coverage for compaction internals after the real retained fan-out fixture could not reach a post-turn compaction boundary without unrelated fixture-semantic changes.
- [x] Run focused and full relevant package tests, `just build go`, formatting, and diff checks. Full verification passed:
  `go test ./server/workflowrunner ./server/runtimecontrol ./server/runprompt ./cli/app/internal/runtimeattach ./server/runtime ./server/workflowexecution ./server/workflowsvc -count=1 -timeout 10m`;
  `just build go`; and `git diff --check`.
- [x] Commit the remediation round and complete KENT-608.

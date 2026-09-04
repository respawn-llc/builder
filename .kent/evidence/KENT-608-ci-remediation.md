# KENT-608 CI remediation

- [x] Investigate failed CI / lint and CI / docs checks from run `33836920192`.
- [x] Fix merged API drift in `cli/app.RunPrompt`: use persistent Chat settings mutation for existing-session Thinking instead of removed Runtime Control API.
- [x] Format `docs/src/content/docs/headless.md`.
- [x] Address four inline review findings:
  - [x] Remove duplicate queued-status emission and unmatched mutex unlock.
  - [x] Consolidate `CommandAttempt` ownership in `server/runtime`.
  - [x] Share one user-turn response projection path.
  - [x] Detect persisted Workflow Session ownership after runtime recovery.
- [x] Commit and push `265af99e8` and `78258b954`.
- [x] Reply to and resolve all four reported inline threads.
- [x] Local targeted builds/lints/docs checks pass. One initial `just build go` attempt raced a concurrent protobuf generation and failed to find the ignored descriptor; a sequential regeneration and `go build ./server/... ./cli/...` passed.
- [x] Investigate CI test failure in run `33837737000`: the retained Thinking regression still called the removed runtime-control path, which changed only live memory and left persisted settings at `medium`.
- [x] Update the regression to exercise the current authoritative Chat Settings persistence/application boundary, then verify the focused test passes.
- [x] Commit and push `8380120a2`.
- [x] Confirm the CI run for `8380120a2` completes successfully: run `33838648524` passed build, desktop_bundle, docs, gui, gui-native, lint, test, and windows-installer; CodeRabbit reported pass/rate limited.
- [x] Address the follow-up review round: close failed runtime-control remotes, preserve typed retained-Workflow diagnostics through the Run Prompt API for client-owned warning rendering, reuse the canonical prompt-history projection, ignore non-contract retained launch overrides after assertion validation, retain sibling diagnostics when history persistence fails, remove the contradictory unconditional spec sentence, fix test-goroutine fatal calls, and raise protocol version `146` to `147`.
- [ ] Confirm CI for the follow-up review commit completes successfully.
- [ ] Run `kent task complete --commentary ... --pr_url ... --worktree_session_id ...`.

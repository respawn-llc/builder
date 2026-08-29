# CLI Commands Spec

## General Contracts

- The CLI provides complete control of Kent's supported command surfaces for operators and agents.
- CLI command grouping is not a compatibility contract. Documented behavior, accepted data, and machine-readable output are compatibility contracts.
- CLI output includes stable identifiers needed by later commands.
- Long flags are rendered with double dashes in help, examples, and Kent-authored diagnostics. Single-dash long flags remain accepted for compatibility; standard-library parser failures retain their native formatting.
- Workflow and Task commands report remote-close failures to stderr after command work finishes. A close failure does not change a successful exit code, and an operation failure keeps its existing nonzero exit code.
- JSON mode for every TUI command prints exactly one final JSON object to stdout and uses quiet progress behavior.

## Project And Workspace Commands

### Project deletion

- `kent project delete <project-id>` selects a Project only by its canonical Project ID.
- Project deletion never deletes or moves workspace files.
- The server owns Project deletion and its blockers. The CLI never deletes Project data or workspace files directly.
- Project deletion is non-interactive and requires `--confirm` before requesting deletion.
- Before confirmation, the CLI checks whether the Project contains any Task whose state is not terminal, including a Backlog Task.
- If unfinished work exists in a Kent agent shell, the command is human-only and reports exactly `Project deletion is human-only because project <project-id> contains unfinished work.`
- Task state may change before deletion is processed; the server's deletion blockers remain authoritative.
- If confirmation is absent after the conditional human-only check permits the caller, the command makes no deletion request and reports exactly `Project deletion was not confirmed. Rerun with --confirm to delete project <project-id>.`
- Successful plain output is exactly `Deleted project <project-id>. Workspace files were not deleted.`
- Blocked plain output preserves every blocker in server order with its code, client-derived message, and positive count when present. The CLI adds no blocker guidance.
- A successful deletion exits with status 0.
- Missing confirmation, conditional human-only denial, blocked deletion, Project lookup failure, and other operational failures exit with status 1.
- Usage errors exit with status 2.
- `--json` emits exactly one JSON object on stdout for every operational outcome after valid argument parsing.
- Successful JSON uses `status: "ok"` and includes the canonical Project ID as `result.project_id`.
- Failed JSON uses `status: "error"` and includes an `error` object with `code` and `message`.
- Every failed JSON result after valid argument parsing includes the canonical requested Project ID as `error.project_id`.
- A blocked JSON result includes every server blocker in server order at `error.blockers`.
- Each JSON blocker preserves the server's `code`, derives `message` from the typed blocker facts, and includes `count` only when the count is positive.
- Non-blocked JSON errors omit `blockers`.
- Stable Project deletion error codes are `confirmation_required`, `human_only_unfinished_work`, `project_not_found`, `project_delete_blocked`, and `request_failed`.

### Workspace detach

- `kent detach` is the canonical detach command.
- `kent project detach` is an equivalent alias exposed in live help but omitted from public documentation.
- Both forms require `--project <project-id>`.
- Both forms accept either one positional workspace path or `--workspace <workspace-id>`.
- The selectors are mutually exclusive.
- The path defaults to the current directory when neither selector is supplied.
- Before making the API request, the CLI resolves `.` from its loaded current workspace and converts every path selector to an absolute path. The server never interprets a CLI-relative path against its own working directory.
- Paths refer to the Kent server's filesystem.
- Detach applies immediately without a prompt or confirmation flag.
- Successful plain output is the detached workspace ID followed by a newline.
- Blocked plain output reports only the first blocker in Kent's response order, including its code, count when present, and client-derived explanation and next-step guidance.
- Blocked plain output exits with status 1 and changes nothing.
- Plain operational failures write no successful result and exit with status 1.
- Usage errors exit with status 2.
- `--json` emits exactly one JSON object on stdout for every operational outcome after valid argument parsing.
- Successful JSON uses `status: "ok"` and includes `project_id` and `workspace_id` in `result`.
- Failed JSON uses `status: "error"` and includes an `error` object with `code` and `message`.
- A failed JSON result includes `project_id` and `workspace_id` together only after the workspace relationship has resolved.
- A blocked JSON result includes a non-empty `blockers` array containing every bounded blocker.
- Each blocker includes `code` plus client-derived `message` and `guidance`. It includes `count` only when the count is positive.
- A retryable concurrent conflict includes `retryable: true`.
- `result`, `error`, Project/workspace IDs, `blockers`, `count`, and `retryable` are omitted when absent. They are never represented by `null`, `false`, zero, or an empty collection.
- Stable detach error codes are `project_not_found`, `workspace_not_attached`, `workspace_detach_blocked`, `workspace_detach_conflict`, and `request_failed`.
- When Kent cannot recover an inaccessible selected path's identity, `request_failed` directs the operator to retry with `--workspace <workspace-id>`.
- Other `request_failed` outcomes do not include workspace-ID fallback guidance.
- Successful operation exits with status 0. Failed operation exits with status 1.

### Default workspace

- `kent project default` requires `--project <project-id>`.
- It accepts either one positional workspace path or `--workspace <workspace-id>`.
- The selectors are mutually exclusive.
- The path defaults to the current directory when neither selector is supplied.
- Path selectors use the same pre-request absolute-path resolution as detach.
- The command applies immediately without confirmation.
- Selecting the current default workspace succeeds without changing the Project.
- Successful plain output is exactly `done` followed by a newline.
- Successful JSON uses `status: "ok"` and returns the authoritative updated Project at `result.project`.
- Failed JSON uses the same typed error envelope and exit-status policy as detach.
- Stable default-workspace error codes are `project_not_found`, `workspace_not_attached`, and `request_failed`.
- When the selected replacement is not attached, Kent directs the operator to attach it before retrying default selection.
- When Kent cannot recover an inaccessible selected path's identity, `request_failed` directs the operator to retry with `--workspace <workspace-id>`; unrelated request failures do not.

### Detach blocker guidance

- `default_workspace` directs the operator to choose another attached default workspace with `kent project default`, then retry detach. If the replacement is not attached, the operator attaches it with `kent attach` first.
- `active_sessions` directs the operator to stop active runs or rebind Sessions that use the workspace, then retry detach.
- `non_terminal_tasks` directs the operator to move editable Backlog Tasks to another source workspace, or complete, manually move, or delete dependent Tasks, then retry detach.
- `executable_current_nodes` directs the operator to stop execution and move, complete, or delete affected Tasks until no executable Current Node uses the workspace, then retry detach.
- `managed_owned_worktrees` directs the operator to delete dependent worktrees or their owning quiescent Tasks, then retry detach.
- `missing_history_snapshot` directs the operator to re-save an editable Task's source workspace. If the affected Task history cannot be edited, the operator keeps the binding and reports the blocker because detach is unsafe.
- For an unknown blocker code, Kent uses generic client-owned wording, preserves available typed details, directs the operator to resolve that code and retry, and tells the operator to update the CLI and server together. Kent never invents a command for an unknown blocker.

### Project attachment and Session location

- `kent project [path]` inspects the Project bound to a path.
- `kent attach [path]` attaches a workspace to the Project bound to the current directory.
- `kent attach --project <project-id> [path]` selects the Project explicitly.
- Each omitted path means the current directory.
- Project, attach, and rebind commands use the configured daemon and never take local ownership of persistence.
- `kent rebind <session-id> <path>` keeps a Session in its source Project. If the target belongs to both the source and other Projects, it selects the source binding. If it belongs only to other Projects, it fails without mutation, identifies the source Session and Project, and gives complete commands to attach it to the source Project or make an explicit cross-Project move.
- `kent rebind --project <project-id> <session-id> <path>` is required for cross-Project movement. It may attach an unbound target path to the explicit Project and reports that attachment, but rejects a path already attached only to other Projects.
- Failed rebinds never change bindings or Session attachment.
- Sessions attached to Workflow Nodes cannot move across Projects.
- Same-Project rebind is explicit. A human request waits for the current Agent Step and rejects rebind when the Session owns a background command.
- Cross-Project rebind rejects a human request immediately while the Runtime is non-idle and accepts an idle or Dormant Session.
- When the Session's active agent invokes rebind for its own Session, the command returns a scheduled acknowledgement without waiting for the Agent Step to finish. The move applies at the next between-Agent-Step boundary before queued user work.
- A self-agent rebind ignores Session-owned background commands. Those commands continue in the directories where they started.
- A cross-Project move either changes both Session location and artifact location or leaves both unchanged.

## Question Commands

- `kent question` shows the first pending ordinary Question or live internal access request. `kent questions` is an alias.
- The Question CLI requires exactly one of `--session <session-id>` or `--task <task-id-or-short-id>`.
- A Task Short ID uses `--project`, which defaults to the Project attached to the current workspace.
- A Session selector cannot target the invoking agent's `KENT_SESSION_ID`.
- A Task selector uses the live Workflow Question authority. If pending ordinary Questions or access requests belong to exactly one Session, the command selects that Session.
- If pending Task Questions belong to several Sessions, the command exits with failure, answers nothing, and lists each candidate Session name and ID.
- A Task selector that selects the invoking agent's `KENT_SESSION_ID` is rejected.
- The show command writes the Question or access-request text.
- When ordinary suggestions or access options exist, the show command writes `Suggestions:` and a one-based numbered list.
- An ordinary recommended suggestion ends with ` (recommended)`.
- Access-option labels come from the authoritative internal Approval request.
- The show command writes `No questions pending` and succeeds when no ordinary Question or access request is pending.
- `kent question answer` requires `--option <one-based-number>`, non-blank `--commentary <text>`, or both.
- An ordinary Question preserves the existing option and freeform answer behavior.
- A live internal access request requires `--option`; Kent maps that option through the authoritative ordered option object to its typed Approval decision and includes optional `--commentary` directly in the existing Approval answer.
- Commentary alone never implies an access decision.
- `kent question answer` writes `No pending questions at the moment for that session` and exits with status 1 when no ordinary Question or access request is pending.
- After Kent accepts an answer, the command reads the selected Session's authoritative pending Questions and access requests again.
- When another prompt is pending in that Session, the command writes `Next question: <question text>` followed by suggestions or access options.
- Otherwise it writes `Done, session resumed`.
- `kent questions list [--session <session-id>] [--max-handoffs <count>] [--json]` lists successfully answered ordinary Questions from one Session.
- `kent question list`, `kent question history`, `kent questions history`, and `kent run questions <session-id>` are equivalent hidden aliases.
- The Run alias accepts `--max-handoffs` and `--json` before its positional Session ID.
- An explicit Session selector takes precedence over the invoking agent's Session ID. When neither is available, the command fails because a Session ID is required.
- The command may target the invoking agent's own Session.
- Question history does not accept a Task selector. Help directs operators to use `kent task sessions` to choose a Session when starting from a Task.
- Question history excludes unanswered, canceled, interrupted, declined, malformed, and unpairable Questions. It excludes internal Approvals and access requests.
- Question history reads newest to oldest and emits every eligible Question in the selected history windows.
- `--max-handoffs` defaults to 25 and rejects values below 1. The count includes the current unfinished window, so `--max-handoffs 1` reads only that window.
- Every history replacement starts an older window, regardless of replacement mode.
- The command has no Question-count limit.
- Human output streams each Question as it is read. Exactly one blank line separates output blocks.
- Each v2 human Question block contains the normalized presented Question body, `Answer: <answer>`, optional `Commentary: <commentary>`, and `At: YYYY-MM-DD HH:MM:SS` in the machine's local time.
- A selected-option Answer starts with its one-based option number and the full normalized presented Suggestion text. Optional Commentary contains only the separately authored freeform commentary.
- A freeform-only response appears as Answer without an option number or Commentary line.
- Presented multiline Question, Answer, and Commentary text remains verbatim. Existing Question normalization may remove leading and trailing whitespace from Question bodies and Suggestions. Labels do not indent or escape continuation lines.
- `At` is the time the answer became committed.
- For an event-log v1 Session, human output uses the normalized presented Question body and the persisted flattened completion output verbatim as Answer. It omits Commentary and `At`.
- Human mode immediately emits `[Session history is large, this command may take a while to finish]` before scanning when the Session event log is at least 1 GiB.
- When the selected handoff count omits older history, human mode emits `[Older Question history omitted; increase --max-handoffs to include more]` after the streamed Questions.
- No answered Questions is a successful human result containing `No answered questions found`.
- On a read or decode failure, human mode keeps already emitted Questions, reports the error on standard error, and exits with failure.
- On interruption, human mode keeps already emitted Questions, writes `Interrupted` to standard error, and exits with status 130.
- JSON output is one object containing `history_omitted` and `questions`. It omits human performance warnings.
- Each JSON Question contains `question`, `answer`, `selected_option_number`, `commentary`, and `at`. A present `at` is the answer commit time in the standard JSON representation of an RFC 3339 UTC timestamp, including fractional seconds when present.
- A freeform-only JSON Question has `null` selected-option number and Commentary. An option answer without Commentary has `null` Commentary.
- An event-log v1 JSON Question uses the persisted flattened completion output verbatim as `answer` and has `null` selected-option number, Commentary, and `at`.
- No answered Questions is a successful JSON result with an empty `questions` array.
- JSON output streams in bounded memory without a temporary spool. It opens the result object and `questions` array before reading all Questions, appends `history_omitted` when the server reports completion, and closes the object only on successful transport completion.
- A read, decode, or transport failure may leave a partial invalid JSON document on standard output. The command reports the error on standard error and exits with failure.
- JSON interruption may leave a partial invalid JSON document, writes `Interrupted` to standard error, and exits with status 130.
- Question-history help warns that JSON output can be slow.
- An unknown Session fails with the ordinary Session-not-found error.
- An existing Session with a valid empty event log succeeds with no answered Questions.
- A missing or unreadable event log is an operational failure.

## Goal And Service Commands

- Models may use normal shell commands `kent goal show`, `kent goal complete`, and first-time `kent goal set <objective>` for the current Session, but other Goal commands detect invocation by the agent and refuse it.
- Agent `goal set` is allowed only when no active or paused Goal exists. Completed Goals do not block the next agent-set Goal.
- An allowed agent `goal set` or confirmed `goal complete` prints the projected scheduled Goal and returns before the Goal mutation applies. Earlier accepted Goal mutations participate in that projection.
- Goal completion is explicit CLI state mutation, not natural-language inference.
- Goal CLI never mutates Session storage directly. It submits Goal commands to the server.
- Any `kent service` command that affects server state detects invocation by Kent itself and refuses to run because it is human-only.

## Headless Run And Shared Control

- `kent run "prompt"` is the headless and subagent interface.
- Run connects to an existing configured or discovered server and never starts one.
- If no server is reachable, Run fails with guidance to use `kent serve` or `kent service install`.
- `kent run --agent <role> "prompt"` selects a configured role.
- `--fast` selects the built-in fast role and cannot be combined with `--agent`.
- Named roles are file-only `[subagents.<role>]` settings and inherit main settings unless overridden.
- Headless execution runs one non-interactive prompt with ordinary Session persistence.
- New unnamed Sessions are named `<session-id> subagent`.
- Timeout is unlimited unless `--timeout` is given. `--timeout` limits waiting for the selected agent or Workflow execution to complete; it excludes launch and lifecycle preparation, prompt-history persistence, teardown, and other technical operations, which retain their own bounds. Total command duration may therefore exceed the stated duration.
- Default progress mode is `--progress-mode=stderr`: committed assistant commentary and final text go to stdout; lifecycle notices go to stderr.
- New Sessions announce the actual configured launch command followed by `run steer <session-id> "prompt"` only after steering is available.
- Resumed Sessions do not announce it.
- Compaction-start and recoverable-failure notices remain visible; routine tool, Reviewer, and completion status does not.
- `-q` and `--quiet` select final-result-only `--progress-mode=quiet`.
- A blocked model-originated child launch returns attempted depth and configured limit.
- Model-facing command output for that block says: “You are already a subagent, so you shouldn't spawn more subagents to prevent overloading the machine and infinite recursion. Do not attempt to use subagents anymore and complete the task on your own”.
- JSON output for that block uses `subagent_max_depth_exceeded` with numeric attempted depth and limit.
- `kent run steer <session-id> <message...>` sends input to an active Session.
- Run steer requires a Session ID, rejects attempts by a Session to target itself, and prints `ok` when accepted.
- The target prints `Steered message: <full text>` with no later delivery notice.
- Run steer never starts or queues work for an idle Session; it fails with the equivalent `kent run --continue <session-id> <message>` command.
- Run steer invoked from another Session emits each accepted submission as a separate developer-role `agent_steer` message in submission order.
- `kent run --continue` invoked from another Session that opens an existing Session uses the same `agent_steer` message. Prompts that create a Session retain their ordinary behavior.
- `kent run --continue` for a retained Workflow Session follows the Task lifecycle contract in `workflow-orchestration.md`. It resumes and steers an interrupted resumable Current Node, applies the completed-Session continuation policy after Node completion, and rejects other lifecycle states with the Task, blocking state, and applicable next action.
- Retained Workflow continuation keeps existing-session Headless Run authorization and locked-contract validation. Explicit `agent_role`, `model`, and `tools` are accepted only when they canonically equal the authoritative Workflow Assignee, locked model, and locked tool set; a conflicting value is rejected before Resume acceptance.
- For `kent run --continue` against an existing Session, explicit `--thinking-level` is a persistent Session Chat-setting mutation sent through the ordinary Thinking control RPC before Run Prompt. The same RPC accepts dormant and live Sessions: it persists the setting without requiring an open runtime and updates an already-open runtime immediately. It is not a temporary launch override, remains in effect for the Session until changed again, and is not rolled back when the later continuation rejects or fails.
- Retained Workflow continuation does not stage or apply provider, base-URL, or model-timeout launch overrides. Theme is client presentation state and is not sent as a Run Prompt override.
- Retained Workflow launch preparation is read-only. Lifecycle rejection or a no-op Resume persists no launch-setting or contract-backfill change; the independent Thinking mutation above is outside that guarantee because it is a Session-wide mutation.
- A successful retained Workflow continuation returns the selected Session name and applicable launch warnings. If the selected branch resumes but a parallel sibling cannot Resume, the sibling diagnostic is an additional warning and the selected result still determines command success and exit status. A terminal failure carries its diagnostic in the error because an RPC error response does not also carry `RunPromptResponse`. Each invocation performs fresh authorization and lifecycle revalidation; Run Prompt has no request identity, memo, or replay result.
- Retained Workflow Run Prompt records the submitted text in prompt history exactly once only after the selected Current Node's Resume commits. Authorization, assertion, lifecycle, and no-op rejection record nothing; a later selected delivery or execution failure does not remove the accepted prompt-history entry.
- When the selected resumed Workflow turn commits the submitted input and completes successfully without a final answer, `kent run --continue` exits with status `0` and uses its existing empty output representation. This retained-Workflow result does not change no-final behavior for unrelated headless commands.
- In default progress mode, a retained Workflow continuation uses the same synchronous progress publisher as ordinary Run Prompt through selected execution finalization. Every selected assistant commentary/final message and required compaction or recoverable-failure notice accepted by that publisher is emitted before its server runtime callback returns. A blocked server transport send may pause the selected Workflow execution and delay cancellation until that send returns. A blocked CLI output writer is downstream: it may delay the client's receive loop, SIGINT handling, and command completion, but does not by itself guarantee that server execution pauses. Progress is never silently discarded; once cancellation or transport loss is observed, observation ends and the already-accepted Workflow execution is not canceled. If selected exact completion is already recorded when cancellation is evaluated, that exact result wins; otherwise the caller's cancellation cause wins and later completion does not replace it.
- A steer issued from another Session contains exactly:

```text
Agent from session <source-session-id> said:
> <submitted steer text>

To respond, run: kent run steer <source-session-id> "message"
```

- Kent inserts one literal `>` followed by one space immediately before the submitted steer text. It does not add quote markers to later lines.
- The message includes the source Session ID and omits its name.
- A present malformed `KENT_SESSION_ID` fails Run steer before submission. An absent or blank value uses human-steer behavior.
- Prompt history stores the complete wrapped message.
- `kent run stop <session-id>` interrupts an active Session regardless of client origin.
- Run stop requires a Session ID, rejects attempts by a Session to target itself, prints `Stopped` when accepted, and prints `No active execution` as a successful no-op for idle or nonexistent Sessions.
- Run stop returns after direct exact-live cancellation. Pending human Steering for the stopped execution is removed when that execution unwinds; the CLI neither waits for that cleanup nor promises restoration.
- `kent run wait <session-id>` waits for an active Session's final result.
- `kent run wait …` always selects the Run wait command. A headless prompt beginning with `wait` uses `kent run -- wait …`.
- Run wait requires a canonical UUIDv4 Session ID, rejects attempts by a Session to target itself, and fails without final-answer output if no execution is active.
- Run wait stays blocked while a regular Question or access request is pending.
- Run wait returns only for a Final answer, no-final result, Execution error, or Interrupted outcome.
- An explicit stop is an Interrupted outcome.
- Final text includes the ordinary continue hint.
- Run wait emits no progress.
- `kent run watch <session-id>` observes the selected Session's active execution once.
- Run watch requires a Session ID and rejects attempts by a Session to target itself.
- Run watch returns immediately when its initial pending projection contains a regular Question or access request.
- Run watch returns a Final answer, no-final result, Execution error, or Interrupted outcome captured from the selected active execution.
- Otherwise Run watch waits for an attention or terminal event from that active execution.
- An attention event triggers another pending projection. Run watch returns a Question or access request only when that projection contains it.
- If a prompt is resolved before the event-triggered projection, Run watch continues toward another observable prompt or terminal outcome.
- If Run watch starts with no active execution and no pending Question or access request, it fails with the no-active-execution error.
- Run watch does not return a historical result and does not wait for a later execution to start.
- Run watch is Session-scoped. It does not target Script Nodes or follow a Session's Task into Script work.
- Run watch renders a Question through the same live-prompt presentation as `kent question --session`.
- Run watch then prints a blank line and a directly targeted answer template.
- A suggested Question uses `Answer with: kent question answer --session <session-id> --option <number>`.
- A freeform Question uses `Answer with: kent question answer --session <session-id> --commentary "<answer>"`.
- An access request always uses the numbered-option answer template. Its labels come from the authoritative live prompt.
- Run watch renders a Final answer and continuation hint through the same presentation as Run wait.
- Human Run wait and watch preserve the existing no-final-result presentation and exit code 1.
- Run watch prints authoritative reason and diagnostic text for Execution error and Interrupted outcomes.
- Human Run watch exits 0 after a Question or Final answer, 1 after an Execution error, and 130 after an Interrupted outcome or explicit stop.
- Run control commands accept only `--persistence-root`, plus `--output-mode=json` for wait and watch.
- Run control commands reject workspace, model, provider, agent, timeout, tools, and progress flags.
- Headless stdin is not a steering channel.
- `kent worktree list` resolves the workspace bound to the current directory without a Session.
- Current Session context or `--session` adds that Session's current-Worktree view.
- Without Session context, the list is markerless, and Kent never infers a Session from workspace history.
- Agent Worktree deletion always retains branches.
- Agent Worktree creation stops after setup and prints a separate enter action.
- Worktree enter and leave for an Active Session Runtime return the Worktree Operation acknowledgement before an active Agent Step or the transition finishes.
- Human-readable `worktree enter` and `worktree leave` confirm acceptance without printing the Worktree Operation ID.
- Worktree `--json` includes the Worktree Operation ID.

## Task Observation

- `kent task wait <task>` and `kent task watch <task>` resolve a Project-scoped Task and observe it through server-owned event notification.
- Task observation never polls read commands or mutates the Task.
- Task wait ignores Questions, access requests, Workflow Transition Approvals, and successful intermediate Node completion.
- Task wait returns for a current-work Interrupted outcome, a current Session or Script Execution error, or Task done.
- Task done is durable and Task wait behavior does not depend on a transient projection.
- Task watch subscribes before its first Task projection.
- The initial projection and each matching Task event trigger a new Task projection.
- Task watch returns a Task Question or access request, current-work Interrupted outcome, or current Session or Script Execution error only when that projection contains the outcome.
- A transient Question, access request, Interrupted outcome, or Execution error that disappears before projection is not guaranteed to be returned.
- Task watch returns when a projection reports that the Task is done.
- Task watch ignores Workflow Transition Approvals and successful intermediate Node completion.
- Typed stream, cancellation, and observation failures remain failures.
- Human Task observation output and exit behavior remain unchanged when JSON mode is absent.

## Observation JSON

- `kent run wait --output-mode=json <session-id>`, `kent run watch --output-mode=json <session-id>`, `kent task wait <task> --json`, and `kent task watch <task> --json` use the same observation envelope.
- Run wait makes a hard cutover from its previous JSON envelope. It does not emit `continue_id`, a rendered continuation command, or compatibility fields.
- Headless `kent run --output-mode=json` retains its separate contract.
- JSON mode is recognizable only when the authoritative flag parser has accepted the command's JSON-enabling flag before a parse failure, or after parsing succeeds with that mode enabled.
- A flag-shaped token consumed as another flag's value does not enable JSON mode.
- Once JSON mode is recognizable, the command writes exactly one JSON object to stdout and writes no human text to stderr for success, usage failure, target resolution failure, connection or startup failure, observation failure, or observer cancellation.
- A successful observed result contains `status`, `target`, and an always-present non-empty `outcomes` array.
- An observation envelope may contain top-level `warnings` only for a remote-close failure after the command opened a remote.
- Run commands use the parsed requested Session as `target: {"session_id":"…"}` and as a Run Question's `answer_target`; they do not derive either identity from the response's top-level Session field.
- Task commands use the resolved persistent Task as `target: {"task_id":"…"}` and do not derive it from the response or duplicate the Task Short ID.
- A Task Question uses its typed Question Session as `answer_target` because one Task may expose Questions from different Sessions.
- A command that cannot establish a canonical target omits `target`.
- Each outcome is a flat object whose `kind` selects its remaining fields.
- Outcome kinds are `question`, `final_answer`, `execution_error`, `interrupted`, and `task_done`.
- A Question outcome contains `question_id`, `text`, ordered `suggestions`, optional one-based `recommended_option_index`, and `answer_target: {"session_id":"…"}`.
- A freeform Question uses an empty `suggestions` array.
- An access-request Question maps its authoritative ordered option labels to `suggestions` and omits `recommended_option_index`.
- A Final answer outcome may contain `result`, `session_name`, `warnings`, and `duration_ms`.
- The existing typed Run no-final-result fact is projected only in JSON as a successful Final answer with omitted `result`; its server contract and human presentation remain unchanged.
- Execution error and Interrupted outcomes contain `reason` and optional `diagnostic`.
- A Task Question outcome may contain `node_key` and does not repeat `answer_target.session_id` as an outcome-level `session_id`.
- Non-Question Task outcomes may contain `node_key`, `session_id`, and `script_path` when those typed facts apply.
- A `task_done` outcome contains only `kind`.
- Run observation returns exactly one outcome.
- Task observation preserves every returned outcome in server order.
- Any Task Execution error produces `status: "error"` and exit 1.
- Otherwise any Task Interrupted outcome produces `status: "interrupted"` and exit 130.
- Otherwise Task observation produces `status: "success"` and exit 0.
- A Run Question or Final answer produces `status: "success"` and exit 0.
- A Run Execution error produces `status: "error"` and exit 1.
- A Run Interrupted outcome produces `status: "interrupted"` and exit 130.
- An operational failure contains `status: "error"` and top-level `error: {"code":"…","message":"…"}` and omits `outcomes`.
- Stable operational error codes are `usage`, `target_not_found`, `no_active_execution`, `interrupted`, `timeout`, `unavailable`, `invalid_response`, and `runtime`.
- Numeric server and RPC transport codes are not exposed.
- Usage errors exit 2.
- Other operational errors exit 1.
- Canceling only the observer writes `status: "error"` with top-level `error: {"code":"interrupted","message":"…"}`, omits `outcomes`, and exits 130.
- Observer cancellation does not represent the Session or Task as interrupted or failed.
- For Run wait, a request-canceled result while the observer context remains active represents interruption of the target execution. It writes `status: "interrupted"`, the requested Session target, one flat `interrupted` outcome with `reason` and optional `diagnostic`, and exits 130.
- Typed stream or unavailable failures remain operational failures even when they also wrap cancellation.
- In JSON mode, the command first determines its primary envelope and exit code, then closes every opened remote exactly once.
- A remote-close failure is appended to that envelope's top-level `warnings` whether the primary result is success, operational failure, timeout, observer cancellation, invalid response, or another observed outcome.
- The cleanup warning never replaces or changes the primary `status`, `target`, `outcomes`, `error`, or exit code. JSON mode writes no cleanup text to stderr and emits no second object.

## Workflow And Task Commands

### Workflow selection and discovery

- Workflow selectors are bare canonical UUID v4 values. Workflow display names and prefixed identifiers are not selectors.
- Every command that accepts a Workflow selector uses the same syntax and emits copyable bare UUIDs in human and JSON output.
- `kent workflow list` is paginated.
- `--project <path-or-id>` filters Workflow list to Workflows linked to the resolved Project.
- Project-filtered Workflow discovery is paginated and does not validate each Workflow graph.
- Project-filtered results are ordered with the Project default first, then by Project-local Task activity, then by Workflow name.
- Project-filtered human results preserve the global one-line format and append default/link status plus Execution Target Policy.
- Project-filtered JSON includes the resolved Project ID once and adds only the default-link fact to each Workflow record.
- `kent workflow inspect <uuid> --summary` returns only global Workflow metadata: name, bare UUID, description, version, and Execution Target Policy.
- Summary inspection does not accept Project context; full inspection retains the Workflow graph.
- `kent task show` does not accept a Workflow selector and reports the selected Task's actual Workflow with a bare UUID.
- Task creation without an explicit Workflow uses the Project default.
- If no default exists and exactly one Workflow is linked, Task creation uses that Workflow.
- Task creation with no linked Workflows explains that a Workflow must be created or linked before retrying.
- Task creation with several linked Workflows and no default does not enumerate candidates.
- It directs the operator to paginated Project Workflow discovery, an explicit `--workflow <uuid>` retry, or default selection.

### Common pagination

- Task Search pagination is defined by the Task Search section.
- Every other paginated Workflow and Task command uses zero-based `--offset` and `--limit`.
- These commands expose neither page tokens nor page numbers.
- An omitted offset starts at the beginning. Any non-negative offset is accepted. A negative offset is invalid.
- `--limit` defaults to 100 and accepts 1 through 100.
- Callers may change the limit between requests.
- An offset at or beyond the current end succeeds with the command's existing empty-result output and no next offset.
- When more results exist, `next_offset` equals the request offset plus the number of results returned.
- Human output writes ``Next offset: `<n>` `` to stderr only when more results exist.
- Machine-readable request contracts use `offset` and `limit`; responses use optional `next_offset`.
- An absent next offset is omitted or null and is never zero.
- Each request applies its offset to the current results for that request's Project, Workflow, selectors, filters, and sorting.
- Callers repeat those query choices when continuing.
- Kent does not bind an offset to a previous query.
- If items are inserted, removed, or reordered between offset requests, later results may repeat or skip items.

### Workflow and Task mutation

- Agents can build and edit complete Workflow definitions through high-level commands and graph inspect/apply.
- Every CLI topology mutation submits one complete Workflow Draft graph through the server's authoritative graph-save operation. Kent never persists a partial intermediate graph for one CLI mutation.
- `kent workflow graph inspect <workflow> [--json]` always emits the graph editing JSON; `--json` does not change its output.
- Graph inspect preserves the authored order of every graph collection.
- `kent workflow graph apply <path-or-dash>` reads graph editing JSON from the selected file. A selector of `-` reads standard input.
- Graph apply changes the complete authored Workflow graph, including graph-owned configuration. `kent workflow update` remains the CLI authority for Workflow name, description, and Execution Target Policy.
- Graph apply requires canonical bare UUID v4 text for each new Node, Node Group, Transition Group, and Transition Branch. It rejects prefixed identities, other UUID versions, non-canonical spellings, and surrounding whitespace for additions. It preserves existing graph entity identities and never assigns temporary or persistent identities.
- Node membership in graph editing JSON uses `group_id` only. Graph inspect never emits `group_key`, and graph apply rejects `group_key` rather than treating it as an alternate membership reference.
- Graph apply ignores unknown JSON object fields. Unknown or misspelled authored fields are not preserved when Kent saves the complete submitted graph.
- Graph apply uses the installed JSON library's duplicate-field semantics. It rejects trailing JSON values and missing required fields before it contacts the server.
- Graph apply loads the current Workflow and compares Workflow Version before it classifies graph entity identities. A mismatch returns `blocked` with `version_changed`, including when a legacy identity in the stale document no longer exists or now belongs to another entity type.
- For a current-version document, graph apply preserves submitted identities that match existing graph entities of the same type, preserves submitted collection order, and rejects additions without canonical bare UUID v4 identities before save.
- Graph apply submits the document to the server's graph-save operation. A non-destructive graph that has no blocker saves immediately.
- When confirmation is required, an unconfirmed graph apply reports the impact and changes nothing. With `--confirm`, the command confirms the impact returned by that invocation and retries the save.
- A Workflow Version or impact-count change before the confirmed save rejects the save and changes nothing.
- Graph-save impact lists every removed graph entity by stable entity type and persistent identity. It reports Task references as aggregate counts and never materializes an unbounded Task-reference collection.
- Graph-save blockers identify every affected graph entity by stable entity type and persistent identity.
- Removed Node Groups appear in impact and aggregate counts. Removing a Node Group alone does not require confirmation.
- Existing confirmation requirements for removed Nodes, Transition Groups, and Transition Branches remain unchanged.
- Retained Sessions and completed Session-to-Node provenance do not own a deleted Node's lifetime. Current Node and Pending Approval references remain graph-edit blockers.
- Graph apply rejects a stale expected Workflow Version even when the submitted graph equals the current graph. A metadata-only Workflow update makes an earlier graph editing document stale.
- Applying a graph that equals the current authored graph returns `unchanged`, exits successfully, and does not increment Workflow Version.
- In JSON mode, graph apply emits one outcome envelope with `saved`, `unchanged`, `confirmation_required`, `blocked`, `invalid_document`, or `request_failed`.
- A stale Workflow Version uses outcome `blocked` and blocker code `version_changed`.
- Graph apply exits `0` for `saved` or `unchanged`, `1` when no save occurs for a typed product or operational outcome, and `2` for invalid command usage.
- Existing high-level Node and Edge commands use graph save for their non-destructive operations and preserve their success output contracts. When one requires destructive confirmation, it changes nothing and directs the caller to graph apply.
- `kent workflow delete <workflow>` reports deletion impact and makes no changes unless `--confirm` is present.
- A confirmed deletion submits the previewed Workflow Version and affected Project, Project Workflow Link, and Task counts.
- If impact changes or deletion has blockers, Kent deletes nothing and reports the blockers.
- `kent workflow edge add|update` accepts `--assignee-selection configured|previous_node` and `--thinking-selection configured|previous_node` for one Edge.
- Omission on add means `configured`.
- `previous_node` initializes a missing default Protected Parameter, while `configured` retains an existing Protected Parameter dormant.
- `kent workflow edge add|update` accepts `--target-assignee-param <key>=<description>` and `--target-thinking-param <key>=<description>` to create or edit the corresponding Protected Parameter while enabling it in the same command or while it is already enabled.
- An empty description after `=` is valid.
- Repeatable `--param <key>=<description>` and `--clear-params` mutate ordinary Parameters only and never delete or convert Protected Parameters.
- Workflow Node mutation keeps the Agent Node's configured Assignee required, uses the existing `--agent` flag, and does not enable selection for incoming Edges.
- Workflow inspection identifies Protected Parameter purposes.
- Human and JSON `kent task show` expose effective Assignee and thinking for Agent Current Nodes and omit them for non-Agent Current Nodes.
- `kent task move` accepts an optional Transition Key plus structured values keyed by Node Key and output name through inline JSON or a JSON file.
- Task move selects the Transition automatically when exactly one is usable.
- Flat `name=value` Task move input is unavailable because it cannot distinguish same-named outputs from different Nodes.
- Task start, resume, and move may select a concrete target for an unlocked Task even when the Workflow has a fixed policy.
- Task creation has no target override.
- Execution Target selection uses `--execution-target none|head|default-branch|ref:<revision>`.
- Custom Git revisions require the explicit `ref:` namespace.
- Task start, move, and resume accept `--branch-name <name>` for initial managed-branch selection or an exact assertion against an existing managed Worktree. The flag is rejected when the operation selects no managed Worktree or when Manual Move is a no-op or does not require Execution Target preparation.
- Task start, resume, approve, and move never prompt interactively.
- Selection-required output identifies the reason and concrete rerun flags.
- Task start exposes the same typed outcome in JSON.
- `kent task start` reports success only after the server's atomic Start cutover. If the command stops waiting or loses its connection first, the server operation continues; the CLI does not replay it and a later command reads authoritative Task state.
- `kent task edit <task>` changes a Task's title, body, or source workspace.
- Task edit requires at least one of `--title`, `--body`, `--body-file`, or `--source-workspace`.
- Task edit preserves the current title when `--title` is absent.
- Agents can use Task edit.
- `--json` prints the Task edit result.
- `kent task create` and `kent task edit` accept `--source-workspace` as a Workspace ID or path.
- A path is resolved through its Project binding.
- An omitted source workspace leaves it unchanged on edit.
- Task comment management accepts both `kent task comment ...` and `kent task comments ...`.

### Task completion

- `shell_command` Workflow completion instructs an agent to run `kent task complete`.
- In an agent Session, Task complete resolves the assigned Task and Current Node from the current Session and requires the matching Exact Execution Scope, Run, and Agent Step from the Kent execution environment.
- Outside an agent Session, Task complete requires `--force` plus a Session or Task selector. It does not select an idle completion authority.
- Human `kent task complete --force` composes existing Workflow operations: Interrupt the selected Task's live execution, wait until that Interrupt completes, then invoke the same Manual Move owner with the selected outgoing Transition, commentary, and Parameter values.
- Forced Task complete may begin while the selected Task is executing. It adds no completion-specific gate, fallback, or lifecycle state.
- The plain-text `kent task complete` acknowledgement omits identifiers.
- Task complete accepts dynamic Parameter flags, repeatable `--param name=value`, and `--json` or `--json-file` completion payload input.
- JSON input modes print JSON responses.
- Live agent completion uses the completion acknowledgement. Forced human completion uses the ordinary Manual Move outcome after its Interrupt phase.
- Neither acknowledgement promises that another Agent Turn will occur.
- It does not expose Approval or Transition state.
- JSON completion output retains its existing field set and does not include the plain-text acknowledgement.

### Labels and Task listing

- Invoking `kent task label delete` is sufficient confirmation and does not prompt or require a separate confirmation flag.
- Every Label catalog and assigned-Label projection uses the Project's authoritative Label sequence, including `kent task label list`.
- `kent task label` has no reorder command.
- Project Label catalog and Task-assignment commands live under `kent task label`; there is no top-level Label command.
- Catalog commands create, list, rename, and delete Labels in the selected Project.
- Human catalog output includes readable names and stable UUIDs.
- Label selectors use repeatable `--label <name-or-uuid>`.
- Task-list negative Label conditions use repeatable `--not-label <name-or-uuid>` with the same selector resolution.
- Canonical UUID v4 text selects by identity.
- Every other selector value is trimmed and matched against the complete Project Label name with the Label catalog's case-insensitive Unicode comparison.
- Label selector values are literal and are never comma-split.
- `kent task label add <task>` and `kent task label remove <task>` require one or more Label selectors and apply all resolved membership changes atomically with idempotent add/remove behavior.
- Label names resolve against the Task's actual Project.
- `--project` scopes Project Short ID lookup.
- Task creation accepts the same repeatable selector and atomically assigns existing Labels.
- Every catalog and assignment command accepts `--json`.
- Catalog JSON returns Label records for create, rename, and list, and the deleted Label ID for delete.
- Assignment JSON returns the Task ID and authoritative resulting Label IDs.
- Human assignment output is a short acknowledgement.
- Human Task show output adds one `Labels:` line only for assigned Labels and quotes every name.
- Task show JSON exposes one `label_ids` field and does not duplicate assignments as named objects.
- Task-list Label filtering uses repeatable literal `--label` selectors for included conditions and repeatable literal `--not-label` selectors for excluded conditions.
- `--label-match any|all` combines every included and excluded condition and defaults to `any`.
- `--unlabeled` selects Tasks with no assignments and is mutually exclusive with both selector flags and an explicitly supplied match mode.
- An explicit match mode without either selector flag is invalid.
- Every selector in one command must resolve before Task creation, assignment, or listing proceeds.
- Selector-resolution failure reports every unresolved selector and never ignores or partially applies the input.
- `kent task list` is Project-scoped and defaults to the Project attached to the current workspace, including when `--workflow` is supplied.
- A Project-only Task list spans every Workflow linked to that Project.
- An explicit Workflow selector narrows the list and must identify an active link in that Project.
- `--status` filters primary typed Task status.
- `--attention` filters typed attention.
- `--column` filters Workflow Node Keys and requires an explicit Workflow.
- Column sorting requires an explicit Workflow.
- Machine-readable Task-list output exposes the complete enriched Task-list result. It includes Workflow display information, assigned Label display information, dependency progress, and pagination information.
- Project-wide machine-readable Task rows include Workflow display information and omit Workflow-relative Current Node information, including when exactly one Workflow matches.
- Workflow-narrowed machine-readable Task rows omit Workflow display information and include all Current Node Keys in board order, including an empty collection.
- Each human Task row starts with `<SHORT_ID>: <TITLE>.` followed by `Status: <status>`.
- A human Task row includes `Labels: <quoted names>` immediately after Status when Labels are assigned, preserving Project Label order.
- A human Task row includes `Workflow: <workflow_name>` only when the filtered query can return Tasks from several Workflows.
- A Workflow-narrowed human Task row includes `Current nodes: <comma-separated node keys>` after the optional Workflow line. An empty collection renders as `Current nodes: (none)`.
- A human Task row includes `Deps: <satisfied>/<total>` after the optional Current Nodes line only when dependency progress has a positive total and at least one dependency is unsatisfied.
- Human Task rows omit dependency progress when it is absent or fully satisfied.
- Human Task rows have no blank separator.
- `kent task list --unblocked` includes Tasks with zero unsatisfied direct Task Dependencies.
- `kent task list --blocked` includes Tasks with one or more unsatisfied direct Task Dependencies.
- The two dependency flags are mutually exclusive.
- Task list filters and sorts before pagination.
- Multiple values for one filter are ORed. Different filter types are ANDed.
- A Task with several Current Nodes exposes all matching column keys in Workflow order.
- Default ordering is `status:asc,updated:desc`.
- Custom `--sort` accepts up to seven ordered `field:direction` selectors for `created`, `updated`, `status`, `column`, `title`, `labels`, and `short_id`.
- Sort selectors can be comma-separated in one flag and may be supplied by repeated flags.

### Task dependencies

- Task dependency commands use canonical group `kent task dep`.
- `kent task deps`, `kent task dependency`, and `kent task dependencies` invoke the same command group but are not shown in help or documentation.
- `kent task dep add --blocker <task> --blocked <task>` adds one directed relationship.
- `kent task dep remove --blocker <task> --blocked <task>` removes one directed relationship.
- `kent task dep list <task>` inspects both direct relationship directions.
- `kent task dep list <task> --direction blocks|blocked-by` inspects one direction.
- Every dependency command accepts `--project` for Task Short ID resolution and `--json` for machine-readable output.
- Dependency add and remove resolve both Task selectors before mutation.
- Plain dependency add and remove output is exactly `done`.
- Dependency add JSON returns Blocker Task ID and Short ID, Blocked Task ID and Short ID, and typed outcome `added` or `already_present`.
- Dependency remove JSON returns the same identities and typed outcome `removed` or `already_absent`.
- Dependency mutation JSON uses `outcome`, `blocker_task_id`, `blocker_short_id`, `blocked_task_id`, and `blocked_short_id`.
- Dependency list JSON uses top-level `task_id`, `short_id`, and `directions`.
- Each direction object uses `direction`, `total_count`, and `items`.
- A compact dependency Task item uses `task_id`, `short_id`, `title`, `workflow_id`, and canonical typed `status`.
- A `blocked-by` direction also uses `unsatisfied_count`.
- Each `blocked-by` item also uses typed `satisfaction`.
- Empty directions are omitted. A Task with no relationships returns `directions: []`.
- The cardinality limit makes every returned direction complete.
- Dependency list output has no continuation token and the command accepts no page token.
- Human dependency-list output omits empty directions and uses this shape:

```text
Blocks <count> tasks:
<short-id>: <title> (<status>)
...
Blocked by:
<short-id>: <title> (<status>)
...
```

- Human `kent task show` uses the same dependency sections and shows every relationship permitted by the cardinality limits.
- Human dependency sections order unfinished related Tasks first and then Task Short ID.
- Human `kent task show` omits dependency output when both directions are empty.
- `kent task show --json` never embeds dependency Task items.
- When at least one relationship exists, `kent task show --json` includes only direct Blocker Task count, direct unsatisfied Blocker Task count, and directly blocked Task count.
- The JSON field is `dependencies` with `blocker_count`, `unsatisfied_blocker_count`, and `blocked_task_count`.
- `kent task show --json` omits dependency summary when all three counts are zero.
- `kent task start` and `kent task move` accept `--ignore-dependencies`.
- Without that flag, an otherwise valid Start or executable Manual Move with unsatisfied dependencies returns a typed `dependency_confirmation_required` outcome containing only the unsatisfied count.
- Dependency-confirmation JSON uses `outcome` and `unsatisfied_dependency_count`.
- Human dependency-confirmation output identifies the count, directs the operator to `kent task show <task>`, and gives the `--ignore-dependencies` rerun.
- Human and JSON dependency-confirmation outcomes exit nonzero because the requested action was not applied.
- `--ignore-dependencies` applies only to that command invocation and does not remove relationships or suppress later Workflow dependency awareness.

## Task Search Command

- `kent task search` covers every Task in one persistence root.
- With no Project filter, search is global.
- Repeated `--project` selectors search the union of those Projects.
- Every Project selector resolves before search begins.
- Selectors accept a Project ID or any registered primary, secondary, or managed-Workspace path.
- Kent deduplicates resolved Project IDs and makes no partial scoped search when any selector is unresolved.
- Task titles and complete bodies are searchable.
- Comments are excluded by default and `--include-comments` adds them.
- Search includes every Task lifecycle status by default.
- Repeatable or comma-separated typed `--status` filters use the same primary Task-status values as Task listing.
- The default positional query is one literal substring.
- Search operators and punctuation have no special meaning in literal mode.
- Literal hits are non-overlapping occurrences ordered from left to right in their source.
- Default literal matching ignores case and removable diacritics according to the FTS5 trigram search contract.
- Default literal matching does not promise universal Unicode case folding or universal diacritic equivalence.
- `--case-sensitive` requires exact original case and diacritics.
- Literal mode also searches Task Short IDs as case-insensitive substrings.
- `--case-sensitive` does not change Short ID matching.
- `--fts5` interprets the positional query as a raw FTS5 expression with public `title`, `body`, and `comment` columns.
- One raw-mode hit represents one matching title, Task-body, or Comment-body source document with one selected snippet, not one phrase occurrence.
- `--case-sensitive` is invalid with `--fts5`.
- Raw FTS5 snippets use empty highlight and omission markers. Clients do not add raw omission markers.
- Raw boolean terms must match within one source document.
- Literal search requires at least one trigram after search normalization. Kent provides no one-character or two-character fallback.
- `--context` defaults to 20 and accepts 1 through 64.
- Literal mode returns that many Unicode grapheme clusters on each side of an occurrence.
- Raw FTS5 mode uses it as the snippet token budget.
- Literal results separate original `before`, `match`, and `after` text and identify truncation on each side.
- Clients add one `…` on each side where text was omitted.
- A normalized literal match includes every complete original grapheme cluster that contributed to the match, including removed combining marks.
- `match` never splits an original grapheme.
- Case-sensitive mode requires exact original code points and does not equate NFC and NFD spellings.
- Each Task with a matching Short ID contributes exactly one Short ID hit.
- If the query occurs more than once in the Short ID, that hit uses the first left-to-right occurrence.
- A Short ID hit contributes to the Task's total hit count and breadth-first pagination.
- A successful Task or Comment creation, edit, or deletion is reflected in search immediately.
- If Kent cannot update search, the source change fails and remains unapplied.
- In literal mode, a complete Short ID match or a query equal to the canonical decimal form of the Task's Project-local sequence ranks before every partial Short ID match.
- Every partial Short ID match ranks before every Task retained only by a title, body, or Comment match.
- Among Tasks without a Short ID match, Search ranks each Task by its strongest matching title, body, or Comment under the requested mode.
- A case-insensitive text candidate without a case-sensitive occurrence cannot retain or rank a Task in case-sensitive mode.
- Ranking weights title, body, and Comment sources.
- A strong body match can outrank another Task's weaker title match.
- Within one Task, a Short ID hit precedes title occurrences, title occurrences precede body occurrences, and Task title and body results precede Comment results.
- Remaining ties are deterministic.
- Search pagination is breadth-first by per-Task hit ordinal.
- Every matching Task's first hit precedes any Task's second hit, and later pages may repeat a Task with later hits.
- Each hit ordinal is one-based.
- Each page selects hits in breadth-first order and then groups the selected hits by Task.
- Task groups follow the order of their first selected hit.
- Hits inside a group retain their absolute per-Task order.
- Equal-rank Comment hits use newest-first creation time.
- Equal creation times use persistent Comment ID descending as the final Comment tie-breaker.
- `--page-size` defaults to 100 and accepts 1 through 100.
- `--offset` is a zero-based hit offset and defaults to 0.
- A supplied offset must be non-negative.
- When more hits exist, `next_offset` equals the request offset plus the number of returned hits.
- A continuation repeats the same search choices with that offset.
- When more hits exist, the CLI writes ``Next offset: `<n>` `` to standard error after either plain or JSON output.
- Search does not retain or persist pagination state between requests.
- A changed result set can make a later offset repeat or skip hits.
- Plain output renders `SHORT-ID: title`, unlabeled title/body hit lines, a lowercase `comments:` heading only when the page contains Comment hits for that Task, Comment hit lines, and `[N more hits]` when later hits remain.
- When a page contains a Short ID hit, the `SHORT-ID: title` header represents it without a duplicate fragment line.
- Plain output exposes no line numbers, persistent Comment IDs, status, Workflow, score, author, or date.
- Plain literal hits use `…` only on a side with omitted source text.
- Plain output trims fragment edges, folds Unicode whitespace runs to one ASCII space, never terminal-width-truncates, and emits each hit as one physical terminal line.
- Structured output preserves the original segments.
- A valid empty search prints `No matches.` and succeeds.
- JSON output is an object with `mode`, `groups`, and optional `next_offset`.
- `mode` is `literal` or `fts5`.
- Empty results use `groups: []`.
- An absent next offset is omitted.
- Each JSON group has exactly `project_id`, `project_key`, `task_id`, `short_id`, `workflow_id`, `title`, `status`, `total_hit_count`, and `hits`.
- Each JSON `status` has `kind`, `native_state`, optional `node_ids`, and optional `attention_types`.
- Each JSON hit has exactly `ordinal`, `source`, and one mode payload.
- `source` has `kind` and optional `comment_id`.
- `source.kind` is `short_id`, `title`, `body`, or `comment`.
- Comment hits include `comment_id`; other hits omit it.
- Literal-mode hits have `literal` and omit `fts5`.
- `literal` has `before`, `match`, `after`, `left_truncated`, and `right_truncated`.
- Raw-mode hits have `fts5` and omit `literal`.
- `fts5` has `snippet`.
- JSON output does not expose ranking scores or a flat duplicate hit list.
- Query validation trims Unicode whitespace from the one positional argument, preserves interior query text, and limits it to 4096 Unicode code points.
- A literal query must contain at least one searchable trigram after search normalization.
- A raw expression follows FTS5 behavior for shorter terms.
- Command-local validation failures use exit code 2.
- These include missing or extra positional queries, blank or overlong queries, malformed or unknown flags, invalid `--status`, invalid `--context`, invalid `--page-size`, invalid `--offset`, invalid flag combinations, and normalized-too-short literal queries.
- An unresolved Project selector, transport failure, schema or index failure, database busy or interruption, and every raw FTS5 evaluation failure use exit code 1.
- SQLite reports malformed raw FTS5 syntax and some FTS schema or configuration failures through the same runtime error class.
- Kent reports every raw FTS5 SQLite evaluation error as one generic operational failure with exit code 1.
- Search uses the authoritative Task status shared with Task lists and Task detail.
- Persisted Task facts and live Task activity are observed independently.
- Task status and status filtering combine independently loaded durable and live facts.
- A Workflow transition or durable Task mutation overlapping the request may produce fields from different completed moments.
- Short ID candidate selection must not scan every persisted Task.
- Task Search must support Short ID matching in a persistence root with up to 1,000,000 Tasks.
- The supported size boundary does not promise a numeric response time.
- Search does not retain all matching Tasks, sources, or occurrences in memory.

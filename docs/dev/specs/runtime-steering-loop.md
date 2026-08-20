# Runtime Steering And Model Loop Spec

## Scope And Authority

- This specification owns observable model-loop and between-Step mutation behavior for one Active Session Runtime.
- A durable Session may have no Active Session Runtime or one Active Session Runtime.
- An existing interactive, headless Run, or Workflow lifecycle opens an Active Session Runtime. A mutation route without the complete activation context never creates one.
- A Workflow lifecycle may retain an Active Session Runtime while no Exact Execution Scope is live. Human Goal Steering remains available only for a no-op or mutation that does not require new ordinary Goal execution.
- A Runtime in Workflow control-only admission accepts passive controls that require no model activation, including permitted Goal mutations, settings, and valid Worktree operations.
- In that posture, a user Goal mutation that requires starting new ordinary Goal execution fails before Steering acceptance through ordinary Goal mutation error feedback.
- In that posture, post-turn Queue submission fails before acceptance and creates no Queue Item.
- Human Send/Steer reactivates the retained Current Node through the same Workflow Resume path as the explicit Resume action, then submits the input to the fresh Workflow Exact Execution Scope. It never starts an ordinary Agent execution.
- Accepting a Workflow assignment changes that Runtime from Workflow control-only admission to ordinary start-preparing admission. Human Send/Steer, settings, and other ordinary Steering are accepted in that posture, but post-turn Queue submission remains unavailable because no Agent Turn has begun. No provider request is eligible until assignment preparation and application succeed and earlier accepted Steering has applied. A later Workflow Resume or assignment remains Workflow-owned. Retained-Session input does not activate model work.
- Each Active Session Runtime is the sole authority for its mutable model and transcript state. Client state is derived from Runtime and durable Session state.
- Steering acceptance establishes FIFO order. Kent promises no order between concurrent producers before one is accepted and gives no producer family priority.
- Accepted work belongs to the Active Session Runtime, not to one client connection. Caller cancellation after acceptance stops only that caller's wait.
- Steering is not persisted, replayed, reconciled, or transferred. Process death may lose pending Steering and the in-flight Agent Step while committed durable state remains authoritative.

## Steering Behavior

- A Steering Intent is one typed request to change Runtime state between Agent Steps.
- Human Send/Steer, Runtime-affecting slash operations, short settings, model-visible technical entries, manual compaction scheduling, active-Runtime Workflow assignments, live Goal mutations, and active-Runtime Worktree enter/leave use Steering.
- Human Send/Steer acceptance returns a server Queue Item identity used for pending display and discard. Kent uses no client request identity, request memo, replay key, generic operation result registry, or ambiguous-result reconciliation.
- Valid human text is not rejected because provider, tool, Worktree, process, Reviewer, Workflow, compaction, or other internal Runtime work is active when that Runtime has an Agent execution that can reach another Step Boundary.
- A Runtime in Workflow control-only admission has no eligible Step Boundary. Human Send/Steer first reactivates Workflow Execution; input becomes eligible only through that reactivation.
- While an Agent Step is running, accepted Steering waits for the next Step Boundary. An Idle Active Session Runtime begins processing accepted Steering without waiting for a prior Step.
- The Runtime applies Steering one item at a time in accepted order. If one item waits for its concrete domain owner, later items remain accepted behind it.
- Validation or eligibility failure before acceptance creates no Queue Item. An expected domain-owner failure after acceptance completes that operation's typed failure and leaves unrelated later Steering accepted.
- Caller disconnection after acceptance does not cancel the accepted operation.
- Interactive submission resolves and accepts under the Session lifecycle owner's coarse admission window. Runtime replacement waits for that short admission attempt. If the generation changed before acceptance, Kent resolves the current Runtime and retries without user resubmission. The wait is caller-cancelable.
- Activation, authentication, metadata, target, filesystem, tool, validation, Runtime-open, and Runtime-publication failures are terminal before acceptance. A canceled or disconnected caller receives no Queue Item.

## Protected Agent Step

- An Agent Step begins when Kent starts a provider request and ends after Kent handles that response and every caused tool call and committed tool result and is ready to make another provider request.
- Provider input, model and generation settings, tool surface, model context, execution target, Working Directory, validation state, and already-steered transcript input remain fixed throughout the Agent Step.
- Ordinary Steering does not mutate those facts while the Agent Step is open.
- A matching Question or Approval answer goes directly to its exact pending-prompt owner.
- Instant Stop goes directly to matching exact live Agent execution.
- Live Workflow completion is an exact operation inside the Agent Step. It requires Exact Execution Scope, Run, and Agent Step provenance and returns its result before that Step ends.
- When terminal Workflow completion commits before the Agent Step ends, the Step remains open until every caused tool call and compatible tool result finishes its ordinary protected-Step handling.
- During that closing interval, the exact execution accepts only remaining caused tool/result handling and exact agent-origin Goal admission with the same Scope, Run, and Agent Step. It rejects Stop, prompt answers, exact steer, and another Workflow completion.

## Step Boundaries

- Every completed Agent Step opens one Step Boundary before another provider request.
- An Exact Execution Scope remains live across its inter-Step Steering processing until it becomes idle, completes, Stops, or fails.
- While that Scope remains live between provider Steps, newly accepted human input is associated with the Scope only. Run and Agent Step changes do not change this association. After the Scope stops being live, later human input is Runtime-level.
- A successful Agent Stop closes the matching Runtime association and begins exact finalization before it releases cancellation. Finalizing exact execution accepts no later Stop, exact steer, prompt, Workflow completion, or exact Goal operation.
- A terminally completed Workflow Agent Step moves from its restricted closing interval to ordinary exact finalization once that protected Agent Step closes.
- The old exact execution remains current and its Runtime association remains start-exclusive until exact retirement finishes. Retirement clears that association. The Runtime processes newly accepted Runtime-level input and selects later exact work only afterward.
- At the boundary, the Runtime processes Steering until no accepted item remains. Items accepted while processing is underway join the same boundary.
- Kent does not start another provider request while Steering accepted before the next-work decision remains pending.
- Human input normally applies at the first boundary after acceptance. One completed Agent Step of delay is the target and two is the maximum: Kent does not begin a third provider Step with accepted human text still uninjected.
- Time spent inside an Agent Step or a concrete long-running owner does not consume this Step budget.
- Each accepted human Steering message applies as its own FIFO item. Cross-Session agent steering keeps its distinct developer-message behavior.
- Starting an Agent Step is not a Steering Intent. After Steering is empty, Kent may start a prepared Workflow execution, run required/requested compaction, continue ordinary model work, or remain idle according to the Session's current product state.
- Compaction is an Agent Step because it makes a provider request. A requested compaction occurs before an already-requested ordinary continuation, and Steering is processed again after compaction before ordinary work continues.

## Workflow Start At A Step Boundary

- The Workflow owner may prepare the target Session, execution target, provider, assignment, and detached Agent execution before it attempts durable Current Node admission.
- Competing preparations may change Session-side facts before Kent knows which Current Node start wins. Kent does not roll back or compensate those accepted preparation effects.
- With an Active Session Runtime, the Workflow assignment enters Steering as one typed FIFO mutation. Without an Active Session Runtime, the dormant Session Store remains authoritative.
- Runtime acceptance and application establish assignment ordering only. They do not reserve the Workflow start, select the winning Current Node, or own a readiness signal or start-result registry.
- Assignment acceptance changes Workflow control-only admission to ordinary start-preparing admission. Human Send/Steer, settings, and other Steering may be accepted behind the assignment. Post-turn Queue submission remains unavailable until the first Agent Turn begins.
- Once Kent attempts the required durable assignment mutation, ordinary Session mutation certainty applies. A committed mutation remains authoritative. A definitely uncommitted mutation closes the Runtime fatally and disposes accepted pending work through the common fatal contract. An indeterminate commit terminates the backend.
- Kent does not deduplicate or replay a repeated assignment.
- The Runtime applies the assignment between protected Agent Steps in accepted FIFO order. Earlier accepted Steering applies before the provider starts.
- The first committed durable Current Node admission wins among competing starts.
- Agent preparation creates no live Exact Execution Scope and does not make the Current Node running, interruptible, or visible as active work.
- Before publication begins, caller cancellation consumes the detached Agent execution and releases its resources without invoking the provider.
- Publication is one short non-interruptible Workflow-owned operation. It revalidates the Current Node, target Runtime generation, and absence of conflicting live execution, then durably admits the Current Node and makes the exact execution visible before releasing Workflow mutation ownership.
- Cancellation that arrives after publication begins does not interrupt durable admission or split committed admission from exact publication.
- If another Workflow lifecycle operation changed the Current Node first, Kent discards the detached execution, preserves the winning Workflow state and successor behavior, and publishes no live Exact Execution Scope.
- A definitely uncommitted admission publishes no Exact Execution Scope and leaves the Current Node ready for explicit recovery. An indeterminate admission terminates the backend.
- The Workflow owner invokes the provider only after publication releases Workflow mutation ownership. Cancellation after publication but before provider invocation finalizes the published execution without invoking the provider.
- After successful publication, Authority owns exact start exclusion through retirement. Manual Move and an authorized Task Interrupt use the ordinary running exact lifecycle.
- A stale or rejected preparation may leave already-accepted Session facts. It does not gain exact liveness or authorize Interrupt.
- Explicit Resume and retained-Session Send/Steer share one recovery path for an interrupted Current Node.
- Process death may occur before start disposition. On the next startup, the existing Workflow restart owner marks affected executable Current Nodes interrupted.
- Kent adds no retry, replay, compensation, rollback, clear wait, notification, listener, polling, caller-independent continuation, cleanup worker, or second admission attempt.
- When no exact execution remains but Workflow control retains the Runtime, the Runtime drains work accepted under ordinary admission before changing to Workflow control-only admission. Post-turn Queue input submitted after that change follows the control-only rejection contract; human Send/Steer follows the Workflow reactivation contract above.
- Previously accepted input that was committed before a Workflow start failure remains ordinary Session transcript state. It is not hidden pending input or a replay record.

## Exact-Start Transition Window

- Valid human input accepted after next work has been selected but before the new Exact Execution becomes visible is accepted as Runtime-level Steering without exact association.
- That input survives Stop of the newly published execution and applies at the next available boundary or after a later explicit activation if the execution ends.
- A command other than human input may return its typed transition failure and be explicitly retried. Kent adds no automatic replay or reconciliation.

## Post-Turn Queue

- The user Queue is a separate server-owned FIFO for messages eligible after the current Agent Turn.
- Queue Items retain server Queue Item identities for pending display and discard and carry no client request identity.
- Eligible Queue messages enter through the same Runtime mutation authority as human Steering. Queue storage and eligibility remain separate from Steering.
- Queue and Steering pending work may be lost on process death.
- A Runtime in Workflow control-only admission rejects new post-turn Queue submission before acceptance and creates no Queue Item. Existing Queue Items must already be absent before the Runtime can enter that posture.
- A Runtime in Workflow start-preparing admission also rejects post-turn Queue submission before acceptance and creates no Queue Item. Queue submission becomes eligible only after an Agent Turn exists under its ordinary after-Turn contract.

## Operation Ownership

| Operation family | Observable ownership |
| --- | --- |
| Human Send/Steer while an ordinary Agent execution is eligible | Steering; acceptance returns a Queue Item without waiting for model work. |
| Human Send/Steer while the Runtime uses Workflow control-only admission | Resume the retained Current Node through Workflow Execution, wait for its fresh Exact Execution Scope, then submit the input through ordinary live Steering. |
| Human Send/Steer without an Active Session Runtime | The interactive lifecycle establishes Runtime attachment before submission; the mutation route does not create one. |
| Cross-Session `kent run steer` | Requires the exact-execution owner and Runtime to identify the same accepting, running Agent Exact Execution Scope; transition, Workflow-completed Step-closing, and finalization gaps return the existing typed unavailable result and never become Runtime-level input. |
| Thinking level, Fast Mode, Reviewer enabled, auto-compaction enabled, Questions enabled | Steering for later Agent Steps. |
| Model-visible technical entry or slash-command result | Steering. Local-only actions remain with their direct owner. |
| Session name, prompt history, draft, Queue discard, and reads | Direct family owner. |
| Provider response and caused tool calls/results | Protected Agent Step. |
| Question or Approval answer | Matching exact pending-prompt owner. |
| Instant Stop | Matching Agent exact execution whose loop is actively running. A queued Agent does not authorize Stop or Task Interrupt. |
| Workflow Script Interrupt | A running Script process authorizes Interrupt. During Script publication, Manual Move and an otherwise authorized Task Interrupt wait on Workflow mutation serialization for running or failed. Cancellation does not use a Runtime association; the running Script lifecycle owns its one finalization transition. |
| User shell slash command | Steering waits for the process owner's typed result before another provider request. |
| Background process | Process owner runs independently; a later Runtime-facing result uses Steering only if a Runtime still exists. |
| Manual or automatic compaction | Manual scheduling uses Steering only when model work is eligible; Workflow control-only admission rejects it before acceptance. Compaction execution is an Agent Step. |
| Reviewer model work | Reviewer owner; Runtime-facing feedback or failure returns through Steering. |
| Worktree Create | Worktree owner; it changes no Session. |
| Worktree enter/leave with an Active Session Runtime | Steering waits for the Worktree owner, then applies the resulting target/reminder or typed failure. |
| Worktree enter/leave without an Active Session Runtime | Worktree and durable Session owners. |
| Worktree delete | Worktree owner applies the Active-Runtime and process blockers below. |
| Live Workflow completion | Exact Workflow execution with Scope + Run + Step provenance. |
| Workflow successor preparation | Workflow owner; assignment Steers an existing Runtime or uses the existing dormant Session path. Runtime acceptance orders the assignment but does not reserve or decide the winning Current Node start. |
| Live Goal mutation | Steering. During live Workflow exact execution, Goal continuation remains passive. A retained Workflow control-only Runtime accepts a no-op or mutation that does not require new ordinary Goal execution and rejects a mutation that requires such execution before acceptance. |
| Exact agent-origin Goal set/complete | One exact admission validates Scope + Run + Step and accepts Steering atomically. Step-end-first rejects without an entry; admission-first returns the scheduled Goal projection, and application waits for the Step Boundary while the shell command does not. Terminal Workflow completion earlier in the same Step does not revoke that admission. |
| Dormant Goal mutation | Durable Goal/Session owner without Runtime creation. |
| Runtime retirement | Session Runtime lifecycle; pending accepted work and retained Workflow control block retirement. |

## Workflow Completion And Human Input

- Live Workflow completion is valid only for matching Exact Execution Scope, Run, and Agent Step provenance.
- A committed terminal completion keeps that exact Scope, Run, and Agent Step available only for remaining caused tool/result handling and same-Step exact Goal admission until the protected Step closes.
- Stop, prompt answers, exact steer, and duplicate Workflow completion are unavailable after terminal completion commits.
- Human Steering accepted while that Step is open applies afterward but does not invalidate valid completion or force that completed Workflow execution to continue.
- Accepted human text remains with its source Session and never transfers to a successor Session.
- Human input accepted after the old exact execution is no longer live may activate fresh interactive work in the source Session.
- Terminal Workflow completion soft-completes only the active Goal already durable when completion commits.
- Goal Steering accepted during that same Step applies afterward in FIFO order, including when completion committed before exact Goal admission.
- If that later Goal mutation leaves an active Goal, Kent keeps it active but does not start Goal continuation from the completed Workflow boundary. A later explicit human message may start ordinary work with that Goal.
- Kent adds no Completion Fence, revision comparison, reducer grant, stale-result registry, or general Workflow/Runtime settlement protocol.

## Worktree Deletion

- Worktree deletion fails when any targeting Active Session Runtime is Executing, processing Steering, has pending Steering, has selected/begun model work, or remains retained for Workflow control. It does not wait for or stop that work.
- A targeting Idle Active Session Runtime that has no retained Workflow control is retired before its Session is retargeted as dormant. Kent then updates durable Session target/reminder state before physical Worktree deletion.
- If human input becomes accepted first, that delete attempt fails. If deletion retires and retargets first, later input activates or attaches to a Runtime on the new target.
- A Session without an Active Session Runtime is retargeted directly in durable state.
- A live background process whose Working Directory is inside the Worktree blocks deletion.
- Repeated explicit Worktree operations are independent. Kent does not deduplicate a retry or reconcile an ambiguous result.

## Stop And Live Restoration

- Instant Stop admits once against matching exact live Agent execution. That admission closes the Scope to new associated human acceptance and makes the Agent execution finalizing before cancellation is released.
- Stop returns without waiting for cancellation unwind, Steering processing, Worktree work, user-shell work, matching human cleanup, or client restoration.
- Human input accepted after Stop admission is Runtime-level and is not removed by that Scope's later cleanup.
- A finalizing exact execution is no longer stoppable. Repeating Stop after stopped cleanup or while the association is retiring cannot create another cleanup.
- Prompt answers, Workflow completion, exact steer, and exact Goal operations that arrive after Stop admission are unavailable for the stopped execution.
- When stopped exact execution reaches its boundary, Kent removes pending human Send/Steer and post-turn Queue items associated with that execution. Non-message Steering remains accepted.
- Human input accepted behind a blocked Worktree or user-shell operation during an inter-Step drain remains associated with the live Exact Execution and is included in that removal.
- Human acceptance and Stop closure have one Runtime-local order. If acceptance occurs first, matching cleanup removes the item. If Stop closure occurs first, the later item is Runtime-level.
- Human input accepted as Runtime-level Steering during the deliberately weak exact-start transition is not associated with the stopped execution and survives.
- Stopped cleanup changes the association to retiring without waking Runtime processing. Exact retirement finishes while that association remains, then clears the retiring association and wakes the Runtime.
- Human input accepted while an association is stopped or retiring remains Runtime-level. Kent processes it only after exact authority retires the old execution and clears the retiring association, before selecting another exact execution.
- Worktree and user-shell operations already being processed continue under their concrete owners after exact Stop.
- When Stop overlaps a human item already being durably appended, Kent does not guarantee one commit-or-restore outcome. The text may commit, be returned for restoration, be duplicated, or be lost.
- Removed text is broadcast once in an ephemeral source-agnostic interruption event with existing Queue Item identities and verbatim text in server admission order.
- Every live client that observes the event restores the listed text in order and then appends text already in its composer.
- The event is not persisted, replayed, acknowledged, or targeted to an initiating client. A client or server that misses it loses the text.

## Script Interruption

- A Workflow Script has no Active Session Runtime association and is user-visible as either running or not running.
- Interrupting a running Script requests cancellation without changing an Agent Runtime association or pre-entering Agent finalization. A queued Script alone does not authorize Interrupt.
- Script preparation creates no live Exact Execution Scope and does not make the Current Node running, interruptible, or visible as active work. Kent uses one short non-interruptible publication operation to revalidate the expected Current Node and absence of conflicting live Script execution before it admits the Current Node and makes the Script execution visible together. If another Workflow lifecycle operation changed the expected Current Node first, Kent discards the prepared start, returns one typed unavailable or conflict result, preserves the winning Workflow state, and performs no interruption. If another publication precondition fails while the Current Node still matches, Kent discards the prepared start and applies the same durable interruption certainty contract as Agent publication. A committed admission makes the Script execution fully visible before process execution begins. A definitely uncommitted admission makes no Script execution visible and leaves the Current Node ready for retry. Manual Move and Task Interrupt that arrive during publication wait until the operation reports running execution, interrupted failure, ready retryable failure, or a stale start that preserved an earlier lifecycle winner.
- After successful publication, Manual Move or an authorized Task Interrupt uses the ordinary running Script lifecycle.
- After process-start failure, process exit, or running cancellation, the Script owner performs the applicable completion or interruption exactly once and retires the execution. Internal cleanup phases do not create a user-visible Script state or outcome-precedence promise.

## Failure And Loss

- Session durability has no Kent-imposed timeout. A slow persistence observer may delay the operation but does not become a synthetic timeout failure.
- A definitely uncommitted required Session mutation is Session-fatal: Kent starts no provider request, fails pending typed outcomes, returns pending human text through the same live interruption event, and closes the Runtime without retry, replay, persistence, or recovery.
- A committed mutation remains authoritative if later projection, publication, or reply delivery fails. Kent surfaces the diagnostic and does not apply the mutation again.
- If the Session Store cannot establish whether the authoritative mutation committed, Kent terminates the backend in every mode with operation, Session, receipt/offset, and underlying-error diagnostics.
- Process death may lose in-flight Steering, commands, Questions, partial Agent Turns, and ephemeral restoration. Kent does not reconstruct or reconcile them after restart.
- A Runtime must panic the backend when accepting or retaining the 10,000th pending Steering Intent for one Session. The Runtime has no lower capacity limit, eviction, backpressure, persistence, retry, or recovery behavior for this invariant.
- Kent has no Boundary Agenda, generic long-work selection/settlement protocol, reducer grant, execution lease, Runtime or Steering admission permit, start gate, completion fence, ownership-transfer token, wave, outstanding-result registry, cancellation arbitration, or close-time settlement guarantee. The Workflow owner may retain its existing short mutation serialization for Task and Current Node decisions; it is not a Runtime admission or execution permit and never spans provider, process, Worktree, or other long work.

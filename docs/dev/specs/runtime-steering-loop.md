# Runtime Steering And Model Loop Spec

## Scope And Authority

- This specification owns observable Session mutation order and the choice of work between Agent Steps for one Active Session Runtime.
- A durable Session may have no Active Session Runtime or one Active Session Runtime.
- Interactive startup, a Headless Run, or Workflow Execution may open an Active Session Runtime when it has the complete activation context.
- A mutation request alone never creates an Active Session Runtime.
- The Active Session Runtime is authoritative for its live model, transcript, Pending Work, and Session-setting state.
- Clients render server state and do not create another ordering authority.
- Boundary-required Session mutations are applied one at a time in acceptance order.
- Kent promises no order between concurrent requests before one request is accepted.
- Acceptance order governs operations that share the same boundary owner.
- Acceptance order does not promise the same order for later provider, tool, process, Worktree, Reviewer, or Workflow execution.
- Accepted mutations belong to the Session, not to one client connection.
- Caller cancellation after acceptance stops only that caller's wait.
- Client disconnection after acceptance does not cancel the accepted mutation.
- Pending mutations are process-local and may be lost on server exit.
- A server exit may lose an in-progress Agent Step while already committed Session state remains authoritative.

## Acceptance And Responses

- Human Send/Steer accepts each message as a distinct Pending Work item and returns its server identity without waiting for model execution.
- Valid human text remains acceptable while provider, tool, Worktree, process, Reviewer, Workflow, or compaction work is active whenever the Session can reach another Step Boundary.
- An Idle Active Session Runtime applies accepted short mutations without waiting for a prior Agent Step.
- Post-turn Queue is a separate server-owned first-in, first-out message collection.
- Post-turn Queue accepts each message as a distinct Queue Item and returns without waiting for its later eligible turn.
- Session Name persists immediately while an Agent Step is running.
- Thinking, Fast Mode, Supervisor, Questions, and Auto-compaction changes may complete failure-prone preparation, durably commit, and return while an Agent Step is running.
- A committed live-Runtime setting change is accepted in Session mutation order and applies at the next between-Agent-Step boundary.
- Setting changes enter neither user-visible Pending Work nor the post-turn Queue.
- The server publishes each successful setting change and its typed transient feedback to every connected client.
- A setting change affects later provider and compaction requests and never alters an Agent Step already running.
- Setting changes create no model-visible entries or transcript rows except for the cache-preserving Thinking configuration items defined by Model Requests And Cache Continuity.
- An operator Thinking change and a Workflow-owned Thinking change have no relative ordering or precedence guarantee when they overlap.
- Kent does not delay either change, assign a shared winner order, or reconcile the two owners. Request acceptance and response order do not determine the effective Thinking value.
- The effective live Thinking value is whichever independently owned write applies last.
- A Thinking write that has become authoritative remains applied if its own later state publication or notification fails.
- A definitely-uncommitted Thinking write does not change the live value.
- Another independently owned Thinking write may replace an authoritative value.
- Boundary-required model-visible technical entries apply in accepted order.
- A user foreground shell command reports its terminal result at the command boundary.
- A foreground shell process may run while later short Session mutations apply.
- A background process becomes independent after Kent reports it as backgrounded.
- Background completion is delivered when applicable and never blocks unrelated model or tool work.
- Manual compaction requests enter Pending Work and return after scheduling.
- Each manual compaction request remains distinct and receives its own later success or typed failure.
- Reviewer execution never delays delivery of the main answer.
- Reviewer feedback or failure arrives later if the originating Runtime remains available.
- An Active-Runtime Worktree enter or leave enters Pending Work and returns the established Worktree Operation acknowledgement without waiting for the target change to finish.
- Attached clients later observe the authoritative target or typed failure.
- An active agent rebinding its own Session returns the scheduled acknowledgement after Kent accepts the exact originating Agent Step, without waiting for the target change.
- A dormant-Session Worktree enter or leave remains a direct Worktree operation.
- Worktree create and delete are direct Worktree operations outside Session mutation ordering.
- A live Workflow assignment applies in accepted Session order.
- Workflow Execution still decides whether its Current Node start wins.
- Question and Approval answers go directly to the matching pending prompt and return after that prompt resolves.
- Instant Stop goes directly through Session Runtime Authority and returns after Stop is admitted.
- Instant Stop does not wait for cancellation cleanup or retirement.
- Live Workflow completion and exact tool output remain part of the matching Agent Step and return their exact result synchronously.
- A validation failure before acceptance creates no Pending Work or Queue Item.
- A typed domain failure after acceptance completes only that operation and leaves unrelated later mutations accepted.
- Interactive submission is accepted against one current Active Session Runtime.
- If Runtime replacement wins before acceptance, Kent resolves the replacement and retries admission without requiring another user submission.
- Authentication, metadata, execution-target, filesystem, tool, validation, Runtime-opening, or Runtime-publication failure before acceptance is terminal for that submission and creates no Pending Work or Queue Item.
- Cancellation while waiting for acceptance creates no Pending Work or Queue Item.

## Pending Work

- Pending Work is the process-local server-authoritative projection of accepted work that has not started.
- Pending Work contains only human messages, manual compaction, and Active-Runtime Worktree enter or leave.
- Pending Work is separate from the internal Engine Intent Queue and has no persistence, replay, reconciliation, or connection-owned lifecycle.
- Every accepted queued action receives one identity.
- The same identity represents that action in Pending Work, removal, compaction status, and Worktree acknowledgement or outcome wherever those surfaces apply.
- Each item carries a concrete typed operation rather than a generic command.
- A human-message item presents its exact submitted text.
- Manual compaction has canonical presentation `/compact` followed by its normalized guidance when guidance is present.
- Worktree enter has canonical presentation `/wt switch <selector>`.
- Worktree leave has canonical presentation `/wt leave`.
- Canonical presentation is independent of command alias, capitalization, spacing, or whether a control or typed command initiated the action.
- The server projects post-turn Queue items first in Queue order and then Steer items in server acceptance order across human messages, manual compaction, and Worktree transitions.
- Projection order promises no order between concurrent requests before server acceptance and no later execution or completion order.
- Human Send/Steer, post-turn Queue, manual compaction, and Active-Runtime Worktree enter or leave share a normal-admission capacity of 100 Pending Work items.
- Normal admission rejects when the server independently observes at least 100 items.
- Concurrent admissions that each observe a lower count may temporarily exceed 100.
- Kent never evicts accepted work to enforce the capacity.
- The Pending Work list route is the only client-facing source of a complete Pending Work collection.
- Transcript hydration and live notifications never carry a complete Pending Work collection.
- Kent broadcasts a payload-free Pending Work Changed notification after each completed membership change.
- A Pending Work Changed notification carries no order, revision, membership, or freshness guarantee.
- Every client that observes a Pending Work Changed notification fetches the latest collection for the exact Session associated with that notification.
- A client that receives a successful Send/Steer, Queue, manual-compaction, Active-Runtime Worktree, or discard response fetches the latest collection for that exact Session.
- Every accepted initial, replacement, reconnect, or Scratch Rehydration hydration clears the client's previous Pending Work collection and fetches the latest collection for the exact hydrated Session.
- An accepted hydration clears the previous collection even when the Session identity is unchanged because Pending Work belongs to the process-local Active Session Runtime.
- A Pending Work list result or failure from an earlier accepted hydration has no effect after a later hydration is accepted.
- A failed Pending Work list read may preserve only a collection fetched successfully during the current uninterrupted hydration scope.
- A failed initial read after an accepted hydration leaves the collection empty.
- Every item is discardable until its operation starts.
- Starting and discarding one item are mutually exclusive; whichever the server accepts first wins.
- A discard after start fails because the item is no longer pending.
- Successful discard removes only that item and returns its canonical presentation to the discarding client.
- A successful discard produces the removal response and a Pending Work Changed notification, after which clients fetch the latest collection.
- Pending Work removal never cancels an operation that has started.
- Discarding a Worktree transition before start emits no Worktree completion, failure, or cancellation outcome.
- Pending Work retains no running, completed, failed, canceled, or historical items.
- A malformed command or missing required field fails before acceptance.
- A Worktree selector resolves when its transition starts.
- Mutable Worktree safety and manual-compaction eligibility are revalidated when the operation starts.
- A user or validation failure after acceptance removes the item and reports the typed failure without restoring its canonical presentation.
- A definitely unapplied technical failure broadcasts one ephemeral source-agnostic restoration containing the canonical presentation to every connected client.
- Every client that observes the technical restoration restores the canonical presentation through its ordinary composer behavior.
- Kent does not target, persist, replay, or acknowledge the restoration and does not count connected clients before broadcasting it.
- If no client observes the restoration, the canonical presentation is lost.
- An operation that committed is never restored because later publication or response delivery failed.
- A Worktree transition whose target applied remains Completed if later Session state or response publication fails.
- Kent surfaces the later Worktree publication diagnostic without restoring the command or reporting the applied transition as failed.
- If Worktree rollback fails, Kent reports neither Completed nor Failed and restores no command because the target and Runtime relationship is indeterminate.
- An indeterminate rollback retires only the affected Active Session Runtime before it can accept or execute more work.
- An indeterminate dormant-Session transition returns its diagnostic without retiring a Runtime.

## Protected Agent Steps

- An Agent Step begins when Kent starts a provider request and ends after Kent handles the response, every caused tool call, and every committed tool result needed before another provider request.
- Model settings, tools, model context, execution target, Working Directory, and already-applied transcript input stay fixed for the complete Agent Step.
- Provider input stays fixed except for additional human instructions delivered through native steering.
- Ordinary Session mutations do not change those facts while the Agent Step is running.
- An accepted mutation that needs a Step Boundary waits until the running Agent Step ends.
- An already-running Agent Step is never preempted by Worktree work, compaction, or Reviewer work. Human input may use native steering as defined below.
- Exact Question, Approval, Stop, Goal, Workflow-completion, and caused-output rules use the matching live Exact Execution Scope described by their owning specifications.

## Native Human Steering

- On supported models at first-party OpenAI API-key and ChatGPT Codex OAuth endpoints, ordinary human Send/Steer must use native mid-turn steering during model generation.
- Unsupported models and other providers must retain ordinary boundary delivery.
- Native steering must preserve earlier output and must not cancel already-started tools.
- Post-turn Queue must retain its post-turn behavior. Inter-agent developer messages must retain ordinary boundary delivery.
- Kent must assume submitted native steers arrived and must use existing error-to-composer restoration behavior on errors.
- Native steering must not add delivery reconciliation, automatic replay, or steering-specific recovery.
- Native steering must not introduce special ordering, concurrency, race, or atomicity guarantees.

## Step Boundaries And Next Work

- At each Step Boundary, Kent applies accepted boundary-required Session mutations in order until the next operation starts or no boundary-required mutation remains.
- Mutations accepted while this processing is underway join the same acceptance-ordered drain.
- Human input that does not use native steering normally applies at the first Step Boundary after acceptance.
- Kent never begins a third provider request with accepted human text still unapplied.
- Time spent inside an Agent Step or concrete long-running domain work does not count as another provider request for that limit.
- Later short mutations may apply while a foreground shell process or Worktree transition is still running.
- Starting another provider request is a separate decision from applying accepted mutations.
- Human Send/Steer requests ordinary model work without making the caller wait for model execution.
- Applicable post-turn Queue work keeps its own Queue order and ordinary after-turn eligibility.
- A Worktree transition has priority over accepted human model work that is still waiting to start.
- An accepted active-agent Session rebind uses the same execution-target transition priority.
- If human model work has already started its Agent Step, the Worktree transition waits for that Step to finish.
- After that Agent Step finishes, the Worktree transition receives the next eligible Step Boundary before another ordinary continuation.
- Later human messages remain accepted and apply while the Worktree transition holds model-work eligibility.
- The Worktree transition does not select, merge, transfer, or execute human Pending Work.
- Foreground shell completion alone does not permit another provider request.
- Kent applies the foreground command's terminal output or typed failure to the Session before another provider request.
- Background execution does not reserve the next Agent Step.
- A background terminal update follows ordinary Session timing when delivery is still applicable.
- A requested manual compaction is selected as the next Agent Step ahead of an already-requested ordinary continuation.
- Kent applies newly accepted short mutations before selecting manual compaction and again after compaction before ordinary work continues.
- Manual compaction and Worktree transitions start in server acceptance order relative to one another.
- Neither operational kind has priority over the other.
- Automatic compaction and Workflow Pre-Compaction keep the selection points defined by the Compaction and Workflow specifications.

## Workflow-Controlled Sessions

- A retained Workflow Session with no live Exact Execution Scope remains available for passive Session controls but cannot start an ordinary non-Workflow Agent execution.
- In that retained posture, eligible settings and passive Goal changes remain available.
- Post-turn Queue and manual compaction fail before acceptance when no Workflow Agent Step can become eligible.
- Human Send/Steer reactivates the interrupted Current Node through the same Workflow Resume authority as the public Resume action.
- Retained-Session reactivation selects the exact Session, Task, Current Node, and parallel branch bound to that Session.
- Retained-Session reactivation does not validate, mutate, or start sibling Current Nodes.
- Kent accepts the original human input only after the selected Current Node publishes a matching fresh Workflow Agent execution.
- If selected preparation, admission, publication, or caller waiting fails first, Kent accepts no original input and starts no ordinary Session execution.
- Public Task Resume may still resume every eligible interrupted Current Node on the Task and returns after durable requeueing.
- Public Task Resume does not wait for startup.
- While an accepted Workflow Agent assignment is preparing and no exact execution is live, Send/Steer and short settings remain accepted.
- Post-turn Queue remains unavailable while an accepted Workflow Agent assignment is preparing.
- The preparing posture begins after Workflow Execution accepts the assignment and before matching exact execution becomes live.
- The preparing posture ends when matching execution becomes live or preparation fails or is canceled.
- Preparation alone does not make a Current Node running, interruptible, or visible as exact live work.
- No provider request starts until the matching assignment is admitted and every earlier accepted short Session mutation has applied.
- Competing or stale preparation may leave already-accepted Session changes.
- Kent does not roll back changes accepted during competing or stale preparation.
- The first durable Current Node admission wins among competing starts.

## Questions, Approvals, And Stop

- A pending Question or Approval remains part of its exact live Agent execution even when no model or tool step is active. It may be interrupted through the ordinary exact Interrupt behavior.
- Manual Move denies a pending Workflow Approval before applying the requested move.
- Every public Instant Stop route revalidates and admits Stop through Session Runtime Authority before it cancels an Agent execution.
- A stale Stop cannot cancel a successor execution.
- Only a matching exact live Agent execution, including one waiting for a Question or Approval, authorizes Instant Stop.
- Only matching live Agent or Script execution authorizes Task Interrupt.
- Stop closes the matching execution to new associated human input before cancellation begins.
- Input accepted after Stop admission belongs to later Runtime work and is not removed by that execution's cleanup.
- A finalizing exact execution is no longer stoppable.
- Repeating Stop during or after finalizing cleanup does not start another cleanup.
- Stop closes pending Question and Approval calls through the ordinary interrupted execution outcome.
- When the stopped execution reaches its boundary, Kent removes its pending human Send/Steer and post-turn Queue messages.
- Non-message Session mutations remain accepted after Stop.
- Worktree and foreground-shell operations already running continue under their own owners after Stop.
- When Stop races a durable human-message append, Kent does not promise one commit-or-restore outcome.
- Text involved in that race may commit, be returned for restoration, be duplicated, or be lost.
- Kent broadcasts removed human text once in an ephemeral interruption event with its item identities and text in server acceptance order.
- Each observing live client restores that text in order and then appends any text already in its composer.
- Kent does not persist, replay, acknowledge, or target the interruption event.
- A client or server that misses the interruption event loses the removed text.

## Reviewer

- Kent builds Reviewer input from the completed answer and the Session state at that answer boundary.
- The main answer commits and becomes visible before Reviewer execution finishes.
- The user may submit more input and ordinary model or tool work may continue while Reviewer runs.
- A Session runs at most one Reviewer request at a time.
- Another eligible answer is not queued for later review while one Reviewer is active.
- Reviewer failures must return through ordinary Session mutation order.
- Empty Reviewer feedback must not start or steer an Agent Turn.
- Nonempty Reviewer feedback must use ordinary Steering to join the active Agent Turn or start an Agent Turn through ordinary Session admission when idle.
- Reviewer feedback must not create a separate execution mode or extend the originating Agent Turn while waiting for review.
- An Agent Turn must become Supervisor-triggered when it accepts Reviewer feedback.
- Ordinary input must not clear the Supervisor-triggered flag during that Agent Turn.
- The Supervisor-triggered flag must clear when the Agent Turn ends and the Runtime becomes idle.
- A Supervisor-triggered Agent Turn must not trigger another Reviewer request.
- Reviewer activity is live best-effort state with values `inactive`, `invoking`, and `addressing_feedback`.
- Reviewer activity is `invoking` while the Reviewer model request is active.
- Reviewer activity must derive `addressing_feedback` from the active Agent Turn's Supervisor-triggered flag.
- Reviewer activity returns to `inactive` when no Reviewer request or Supervisor-triggered Agent Turn remains, or the Runtime closes.
- The active TUI shows Reviewer activity during `invoking` and `addressing_feedback`.
- Reviewer activity creates no transcript lifecycle row and is not retained across Runtime replacement, reconnect, transcript hydration, or application restart.
- Persistent Reviewer activity across later lifecycle boundaries or reconnects is outside this specification.

## Worktree Ownership And Deletion

- Worktree create is owned completely by Worktree management and does not change a Session target.
- Active-Runtime Worktree enter and leave use Pending Work and preserve the priority and non-preemption rules in this specification.
- Worktree deletion never obtains permission by joining Session mutation order.
- Deletion fails without waiting or stopping work when a targeting Active Session Runtime is executing, applying or holding accepted Session mutations, has selected or begun provider or tool work, or remains retained for Workflow control.
- A targeting Active Session Runtime that is truly idle is retired before its durable Session is retargeted.
- Kent retargets dormant Sessions before physical deletion.
- A targeting Session without an Active Session Runtime is retargeted directly in durable state before physical deletion.
- A live background process whose Working Directory is inside the target Worktree blocks deletion.
- If human input is accepted first, deletion fails.
- If deletion retires and retargets first, later input uses the new target.
- Worktree operations are independent requests and repeated requests remain distinct.
- Kent does not replay, deduplicate, or reconcile an ambiguous Worktree retry.

## Fast Mode And Context Selection

- Fast Mode is available only when the active provider supports Kent's fast-request behavior.
- A successful Fast Mode change becomes the Session's authoritative Chat setting and affects the next provider or compaction request, including after the Session is reopened.
- Enabling unsupported Fast Mode fails without changing the Session setting.
- Fast Mode changes do not alter an Agent Step already running and do not create a new Session Contract generation or prompt-cache identity.
- Supported provider requests use the provider's priority service tier when Fast Mode is enabled and omit that tier when it is disabled.
- Context usage and compaction selection use the Session's current provider-reported usage or established current-context estimate and the configured thresholds.
- Kent does not forecast future token growth from previous turns to choose compaction timing.
- Automatic compaction keeps the current consumed-context threshold and rechecks current eligibility before it starts.

## Failure And Loss

- Session durability has no Kent-imposed timeout.
- A slow durable write may delay the operation without becoming a synthetic timeout failure.
- A definitely uncommitted required Session mutation starts no later provider request, fails pending operation results, returns restorable input through its ephemeral restoration event, and closes the Runtime without automatic retry, replay, or recovery.
- A committed mutation remains authoritative if later publication or reply delivery fails.
- Kent reports a failure after a committed mutation and does not apply the mutation again.
- If Kent cannot determine whether an authoritative Session mutation committed outside the indeterminate Worktree rollback case defined by this specification, it terminates the backend with diagnostic context.
- Process death may lose pending mutations, commands, Questions, Reviewer work, partial Agent Turns, and ephemeral input restoration.
- Kent does not reconstruct or replay work lost on process death.
- Except for the Pending Work normal-admission limit defined here, accepting or retaining the 10,000th pending Session mutation for one Session terminates the backend as an invariant violation.
- Kent adds no other smaller capacity limit, eviction, persistence, retry, or recovery behavior.

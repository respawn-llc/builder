Recon

- The current subscription entry point is `RuntimeRegistry.subscribeAuthorityTranscript` in `server/registry/runtime_registry.go:592-625`. It retains the authority resource, calls `Engine.WithTranscriptHydrationSnapshot`, projects committed rows and the active assistant stream through `runtimeview.TranscriptHydrationFromSnapshot`, then separately resolves `TranscriptSessionStatusFromRuntime`, `TranscriptSessionIdentityFromRuntime`, and (when available) the execution target before calling `entry.sessionFeed.Subscribe`.
- `runtime.TranscriptHydrationSnapshot` in `server/runtime/transcript_subscription.go:9-47` is intentionally narrow: it contains committed transcript rows and active assistant-stream facts under `Engine.outputMutationMu`. It does not include runtime read-model state, session status/identity, execution target, live step substate, prompts, queue, backgrounds, context usage, goal, compaction, or tools.
- `sessionFeedSequencer` in `server/registry/session_feed_sequencer.go:15-163` owns a second mutable projection. Its `sessionFeedSnapshot` retains runtime read model, active reasoning/reviewer/compaction, queued messages, prompts, tools, status/identity, context usage, goal, and backgrounds. `Subscribe(base)` locks the sequencer, applies that projection over the supplied base hydration, validates, and only then registers with the broker.
- The overlay is not optional for several fields: `applyToHydration` returns an error when `runtimeReadModel` is absent (`session_feed_sequencer.go:122-129`), replaces `RuntimeReadModelUpdate`, and conditionally replaces status and identity from cached values (`:130-135`). It derives `ActiveStep` from the cached read model and replaces the other retained live domains (`:136-163`).
- The sequencer's state is populated only by publication paths. `PublishRuntimeReadModel` updates its cached runtime read model (`:85-120`); `Publish` validates, drops selected duplicate status/identity/tool-start events, mutates the cached projection, and publishes (`:61-83`, `:290-305`). `apply` clears step-owned state when the read-model active step changes (`:196-220`) and clears active compaction on every terminal compaction event (`:267-273`).
- `RuntimeRegistry.publishRuntimeEvent` publishes projected runtime events and, for selected runtime events, separately recomputes status from the engine (`server/registry/runtime_registry.go:433-445`, `:501-503`). `PublishSessionIdentity` separately resolves identity and may inject an execution target; `PublishSessionStatus` separately recomputes status (`:448-487`). `PublishRuntimeReadModelUpdate` is a separate publication route (`:546-554`). These are the existing producers that can leave the sequencer cache ahead/behind the engine at subscription time.
- The sequencer broker is responsible for ordered delivery and per-subscription sequence numbers, not durable state. `transcriptSubscriptionBroker.Subscribe` enqueues hydration while holding broker ownership, adds the subscriber, and `Publish` sends later events (`server/registry/transcript_subscription_broker.go:20-103`). Each subscriber numbers messages independently in `transcriptSubscription.publish` (`:126-145`), with hydration required as sequence 1 by `transcriptSubscriptionContract` (`:181-219`).
- The broker's subscribe/publish locking does not establish one transaction with the engine snapshot: registry code takes the engine hydration callback, then calls sequencer subscribe; the sequencer lock protects only its own cached state. The future seam must be checked against this ordering boundary and the authority resource lifecycle retention in `authorityRuntimeEntry.retainSubscription` (`runtime_registry.go:202-236`).
- Runtime projection helpers already expose most required facts from the engine:
  - `runtimeview.StatusFromRuntime` reads context usage, compaction count, goal, workflow state, and configuration directly from the engine (`server/runtimeview/projection.go:56-97`).
  - `runtimeview.TranscriptSessionStatusFromRuntime` reads session configuration and workflow association (`:100-128`).
  - `runtimeview.TranscriptSessionIdentityFromRuntime` reads session ID/name and conversation freshness (`server/runtimeview/transcript_subscription.go:223-238`).
  - `runtimeview.TranscriptHydrationFromSnapshot` projects committed rows and active assistant state (`:18-23`).
  - `Engine.CompactionCount()` is available from `server/runtime/status_inspection.go:25-27`; the transcript session-status DTO currently has no compaction count field (`shared/clientui/transcript_session_status.go:10-23`), while `RuntimeStatus` does include it (`server/runtimeview/projection.go:66-87`).
- The committed/runtime snapshot lock currently protects transcript delivery state only. `WithTranscriptHydrationSnapshot` takes `outputMutationMu` and calls `transcriptHydrationSegmentLocked` (`server/runtime/transcript_subscription.go:17-47`). Other engine projection methods used during subscription are called after/beside that callback, so their exact synchronization and relationship to output mutation need inspection before implementation.
- Existing client hydration contract is already broad and typed. `clientui.TranscriptHydration` contains identity, status, runtime read model, committed rows, assistant/reasoning/step/reviewer/compaction, tools, queued messages, prompts, backgrounds, context usage, and goal (`shared/clientui/transcript_message.go:108-124`). Validation enforces nested ownership/coherence in `shared/clientui/transcript_hydration_validation.go`.
- Existing tests cover the current seams:
  - `server/runtimeview/transcript_subscription_test.go` tests projection of deletion rows, active assistant stream, visibility, and snapshot conversion.
  - `server/registry/runtime_registry_test.go:277-368` verifies step-owned cached state is retired after a canonical runtime read-model update makes the engine idle.
  - `server/registry/transcript_subscription_test.go:166-219` verifies invalid event batches do not partially mutate the sequencer, and earlier tests exercise broker hydration and event sequencing.
  - `server/registry/transcript_feed_architecture_test.go:16-166` guards that registry publishers route through the sequencer, legacy read-model kinds are absent, and the new subscription path does not use legacy transcript readers.
  - `server/transport/gateway_test.go:1065-1110` verifies the wire route's first notification is sequence 1 hydration.
  - `server/workflowrunner/current_node_integration_test.go` consumes the subscription and is a likely seam-level client/server integration location.
- The current architecture intentionally avoids legacy transcript readers in subscription. `transcript_feed_architecture_test.go:132-166` forbids segment/page/event-log readers in `runtime/transcript_subscription.go`, `registry/runtime_registry.go`, and `runtimeview/transcript_subscription.go`; any future snapshot composition must preserve the bounded active-segment behavior already supplied by `Engine.WithTranscriptHydrationSnapshot`.
- History from the three cited commits shows the authority split was introduced incrementally: `1d9659a` integrated canonical runtime read models, `2a3fe3d` made the feed snapshot ordered and deterministic, and `95ce7ec` moved tool hydration ownership from the engine projection into the feed sequencer. The current code therefore has explicit tests and architecture guards around the sequencer cache; implementation work will need to distinguish delivery-order responsibilities from state-authority responsibilities rather than simply removing all sequencer state.
- The existing task comment records a concrete missed-event case: after a completed compaction event is missed, cached `activeCompaction` is cleared and `TranscriptSessionStatus` has no compaction count, so scratch rehydration cannot restore the terminal count from the current hydration sources. `StatusFromRuntime` demonstrates that the engine already owns the count, but the transcript hydration contract does not currently carry that fact directly.
- Relevant durable product language is in `docs/dev/specs/terminology.md` (`Scratch Rehydration`, `Active Session Runtime`, `RuntimeActivity`, and `Exact Execution Scope`) and `docs/dev/specs/ongoing-scrollback-buffer.md` (hydration-first ordered stream, no live transcript reads, and reload of transcript/queue/execution/status/tool/steer state). `docs/dev/desktop-chat/contract-gap-analysis.md` identifies hydration-first subscription and the typed status domains as reusable server contracts.

Design

- Scratch Rehydration must replace every current server-owned status fact, including the latest completed compaction count. No active compaction means that compaction is not currently running; it must not preserve a client’s previous count.
- This ticket must not change visible interaction, layout, copy, loading behavior, or recovery UI. Successful hydration must update the existing surfaces, and failed Scratch Rehydration keeps the existing clear-error-and-exit behavior.
- Chat open and reopen must compose fresh transcript and runtime state from the authoritative owners. The Session feed sequencer must only order events and detect continuity loss; its retained state must not overwrite fresh Session identity, execution target, compaction count, or any other current runtime fact.
- A missed prior read-model publication must not make subscription fail.
- This ticket must not add a globally atomic or lossless snapshot-plus-live transaction, global coordination, a ledger, a revision or epoch, durable replay, crash safety, restart replay, restart equivalence, a new lock, or a recovery state machine.
- If an event races with subscription and continuity cannot be established, Kent must use the existing sequence-gap handling and Scratch Rehydration. Kent may discard transient live content during that recovery, but it must not remain silently stale after detecting the gap.
- While connected, the transcript feed must be ordered and sequence-numbered. The client must apply every received event once and in order. Kent does not promise lossless delivery across subscription races, disconnects, crashes, or recovery.
- Chat must fail open or reopen through the existing clear error path when a required authoritative owner fails to provide current state. A Session that genuinely has no execution target may hydrate without one; execution-target lookup failure must not become absence or stale fallback.
- Hydration must preserve the full current-state contents: transcript, runtime read model, Session identity and status, execution target, active step, reasoning, Reviewer, compaction, tools, queued messages, prompts, backgrounds, context usage, and Goal. Each fact must come from its real owner, never a second feed-side cache.
- If preserving the full hydration contract would push KENT-258 beyond 2,000 changed lines, implementation must stop and propose domain-seamed prerequisite tasks instead of omitting facts or expanding the running ticket.
- The user approved a narrow update to the owning product specifications for these decisions.
- Active background hydration must omit preview output. It hydrates the authoritative process identity and lifecycle facts, while the existing process-list refresh owns live output; this ticket must not add a sanitizer or another preview API.

Architecture

### Ownership and snapshot seams

- Keep `RuntimeRegistry.subscribeAuthorityTranscript` as the composition root for Chat hydration. It already retains the exact Session resource and enters `Engine.WithTranscriptHydrationSnapshot`; extend that existing path instead of adding another subscription or read-model pipeline.
- Expand `runtime.TranscriptHydrationSnapshot` into the runtime-native snapshot of engine-owned Chat facts. It must carry the bounded active transcript segment, active assistant stream, current reasoning update, active Reviewer state, active compaction state, in-flight tools, pending queued user messages, context usage, and Goal state. It must not contain registry, metadata, or shell-manager state.
- Keep each fact in its existing authoritative module:
  - `transcriptRuntimeState` / `chatStore` owns committed rows and the active assistant stream.
  - `transcriptRuntimeState` owns the current step-keyed reasoning presentation, updated and reset through the existing output-steering lane.
  - `transcriptLiveToolLedger` owns in-flight tools and must expose an ordered clone snapshot; start order moves here when the feed-side ordered ledger is deleted.
  - `messageLifecycle` owns queued user messages and exposes a read-only pending snapshot through its existing interface. If a flush fails before commit, the lifecycle submits one Queue-restore steering item. That item performs `RestoreFront` and emits the existing typed `accepted` Queue state together under the existing output-mutation lane; the restore therefore lands wholly before hydration or publishes after the new subscriber is registered.
  - `reviewerRuntimeState` owns the active Reviewer step in addition to Reviewer configuration.
  - `compactionRuntimeState` owns both completed compaction count and the active step-keyed compaction status.
  - the existing usage and Goal stores remain authoritative for context usage and Goal state.
  - `pendingPromptStore` owns pending prompts.
  - the shell manager owns active background processes; registry composition reads its existing process snapshots instead of retaining background events.
  - runtimeactivity plus runtimeops remain authoritative for the runtime read model and input reconciliation.
  - runtime Session state owns identity/status; metadata owns the execution target.
- Update the runtime owners before emitting their existing typed events from the output-steering lane. Event publication remains observation of owner state, not the mutation that makes the state authoritative.

### Canonical hydration composition

- Add one unexported registry composition method that produces the final `clientui.TranscriptHydration`. It receives the runtime-native snapshot and reads the other owners directly: a fresh runtime read model, Session identity/status, execution target, pending prompts, and active shell-process snapshots.
- Build the fresh runtime read model first within registry composition and derive `TranscriptHydration.ActiveStep` only from its `Activity.ActiveStep`. Do not independently snapshot or cache a second active-step authority.
- Project step-owned runtime facts only when their typed Step ID matches that canonical active step. A fact for a suspended or superseded step is not the active hydration fact. The projection must not infer ownership from text or silently relabel a mismatched fact.
- Project active background activities by filtering the shell manager’s ordered snapshots to the Session’s running backgrounded processes. Reuse their Activity, process, run, step, command, workdir, and log facts. Leave `TranscriptBackgroundActivity.Preview` absent during active hydration because the owner snapshot may intentionally retain raw terminal control bytes; the existing process-list refresh remains authoritative for live output. Terminal processes remain ordinary live/committed events and are not hydrated as active.
- Project pending prompts with the existing prompt projection and creation-time/Prompt-ID ordering. Project queued messages from the message lifecycle’s pending order as accepted Queue state.
- Validate the completed typed hydration once before broker registration. A projection, runtime-read-model, Session, metadata, or execution-target error fails open/reopen through the existing subscription error path; no owner is replaced with empty or cached state.

### Subscription and publication ordering

- Preserve the existing lock order: engine output mutation, then the Session feed sequencer, then the subscription broker. `WithTranscriptHydrationSnapshot` remains the outer runtime lock. Inside its callback, the sequencer acquires its existing per-Session mutex, invokes the registry hydration builder, validates hydration, and registers it with the broker before releasing the mutex.
- Narrow the prompt-lock change to `pendingPromptStore.CloseSession`: it snapshots and deletes the Session’s prompts under its write lock, releases that lock, and only then publishes resolution callbacks. Preserve `WithLockedAttentionSnapshotResult` unchanged; attention subscription must continue registering its subscriber and enqueueing the initial pending snapshot while holding the prompt lock so a concurrent prompt cannot fall between snapshot and live delivery.
- This sequencing is not a cross-owner transaction. It prevents ordinary runtime publications from passing the hydration registration point, while other owner mutations may appear in hydration, in a later event, or in both. It adds no global lock, revision, epoch, replay, retry, or durable continuity mechanism.
- Change identity and Session-status publication to resolve their authoritative values while holding the sequencer mutex. Remove the caller-supplied execution-target snapshot from identity publication; worktree and settings publishers request a fresh identity publication after their authoritative mutation completes. This prevents a value computed before hydration from being delivered afterward as a stale overwrite.
- Encode execution-target absence and failure separately. The resolver returns an optional target plus an error: `nil` means the Session genuinely has no target, while an error aborts hydration or identity publication. A zero target is never used as an absence or error sentinel.
- Keep runtime read-model versions as the existing freshness mechanism. A read-model update computed before hydration may arrive afterward, but clients already reject an older `ReadModelVersion`; do not add another version domain.
- Keep existing stream-gap behavior unchanged. Subscriber overflow, connection/subscription loss, or a non-contiguous received sequence closes or rejects the stream through the existing `ErrStreamGap`/Scratch Rehydration path. Do not claim detection or recovery for an unobservable race.

### Sequencer simplification

- Reduce `sessionFeedSequencer` to a deep ordering module containing only its mutex and broker. It validates and publishes typed batches, sequences hydration construction with subscriber registration, and closes the broker.
- Delete `sessionFeedSnapshot`, `queuedMessageStateLedger`, all feed-side active-state ledgers, state reconciliation, event deduplication, hydration overlay, and the now-unused generic ordered-feed ledger. The sequencer must never require a previous runtime read-model publication before subscribing.
- Preserve `transcriptSubscriptionBroker`’s per-subscription sequence numbering, hydration-at-sequence-1 contract, overflow closure, and assistant/tool lifecycle validation.

### Wire and client state

- Add completed compaction count to `TranscriptSessionStatus`, project it from `Engine.CompactionCount`, and apply it unconditionally on hydration and live Session-status messages. Active compaction status controls only the active lifecycle; it is not the fallback owner for the completed count.
- Remove the CLI runtime-status count mutation from `TranscriptCompactionStatus` handling so one client projection owns the completed count. Hydration with no active compaction therefore clears the active lifecycle while preserving the authoritative completed count from Session status.
- Treat `TranscriptSessionIdentity.ConversationFreshness` as the canonical incoming freshness fact. Applying identity updates both `RuntimeSessionView.ConversationFreshness` and the mirrored `RuntimeStatus.ConversationFreshness` in one reducer operation, so later status-cache reads cannot overwrite fresh identity state with a stale opposite value.
- Preserve the existing replacement behavior for identity, context usage, Goal, reasoning, Reviewer, compaction lifecycle, tools, Queue, prompts, and active assistant state. Scratch Rehydration continues to reset client live state before applying the new hydration; open process views continue their authoritative process-list refresh.
- Raise the shared protocol version because `TranscriptSessionStatus` changes on the wire. No compatibility shim or dual contract is introduced.

### Scope and verification seams

- Keep bounded transcript access unchanged: runtime composition continues to use the active-segment delivery snapshot and never calls transcript page, tail, revision-walk, or full-event-log readers.
- Guard the public seams rather than exposing internals: runtime snapshot tests establish owner/reset behavior; registry subscription tests establish fresh-authority composition and hydration-before-live ordering; client reducer tests establish complete replacement, especially terminal compaction count; architecture guards establish that the sequencer has no retained hydration authority.
- If moving every listed fact to its real owner exceeds the approved 2,000 changed-line limit, stop before omitting a domain or reintroducing feed-side state and propose domain-seamed prerequisite tickets.
- Keep composition concrete and local: use one unexported registry composer over the existing owner modules. Extend only the existing message-lifecycle snapshot and execution-target resolver seams; do not introduce a generic multi-owner provider or coordinator abstraction.
- Map active background hydration from the shell manager’s existing ordered process snapshot without `RecentOutput`. Do not add a sanitizer, separate preview-summary API, or live-event presentation recomputation in this ticket.
- Use one typed output-steering item for uncommitted Queue restoration. It restores the drained items and emits the existing `accepted` Queue events while holding the existing output-mutation lock. Do not restore outside that lane, and do not add a Queue recovery state machine, reservation ledger, lock, or new wire-event kind.

Planning

- Observable outcome: opening, reopening, or Scratch Rehydrating Chat loads the complete current transcript/runtime view from authoritative owners, and a detected delivery gap uses the existing Scratch Rehydration path without stale feed-side state overwriting the result.
- Approved estimate: medium confidence, 1,300–1,750 changed lines. Production is 18–24 files / 900–1,150 lines; tests are 7–11 files / 380–550 lines; specifications and protocol metadata are 3 files / 10–30 lines. Stop before 2,000 changed lines and propose domain-seamed prerequisite tickets if the full contract cannot fit.
- Affected subsystems: server runtime transcript/output/Queue/Reviewer/compaction state; runtimeview transcript projection; runtime registry hydration, prompt closure, runtimeactivity, and Session feed sequencing; shell-process snapshot composition and core wiring; shared client transcript contracts and protocol version; Go CLI transcript/runtime/freshness reducers; the approved core-runtime and ongoing-scrollback specifications.
- Contract impact: `TranscriptSessionStatus` gains completed compaction count and the shared protocol version increases; internal message-lifecycle snapshot/restore, prompt-close callback timing, and execution-target/identity publication seams change. Queue restoration reuses the existing `accepted` event kind. Persistence and configuration formats do not change. No data migration or compatibility shim is added; incompatible clients continue to be rejected by protocol negotiation.

- [x] Establish the compaction-count contract with one red/green slice.
  - Add a failing shared-contract/projection test showing `TranscriptSessionStatus` carries the latest completed compaction count.
  - Add a failing CLI reducer test starting from a stale nonzero count, applying hydration with no active compaction, and expecting the hydrated Session-status count to replace it.
  - Add the status field, project it from the runtime owner, make Session status the sole CLI owner of completed count, and raise the protocol version without a compatibility shim.
  - Completion criterion: focused shared-clientui, runtimeview, and CLI hydration tests pass; a terminal count no longer depends on `ActiveCompaction`.
  - Progress: Added `TranscriptSessionStatus.CompactionCount`, projected it from `Engine.CompactionCount`, made hydration/live Session-status updates authoritative in the CLI runtime tuple, removed compaction-event count mutation, and raised protocol version 83 to 84. Focused shared-clientui, runtimeview, and CLI tests pass.

- [x] Move reasoning, tool, and Queue hydration facts to their runtime owners with incremental TDD.
  - First add a failing Engine hydration-snapshot test for current reasoning, ordered in-flight tools, and pending queued user messages.
  - Extend the existing transcript/tool/message-lifecycle modules with clone snapshots and update reasoning state through the output-steering lane.
  - Add the next failing cases for reasoning reset, tool completion/abort, and Queue discard/successful flush, then make each terminal owner mutation remove the fact from the next snapshot.
  - Add a failed uncommitted Queue-flush case that drains the item, snapshots hydration in the drain/restore race, restores it, and requires the existing typed `accepted` state after restoration. Use channel-controlled persistence and subscription barriers through production seams; do not rely on sleeps or probabilistic scheduling.
  - Implement restoration as one output-steering item that performs `RestoreFront` and emits one existing `accepted` Queue event per restored item inside the same existing output-mutation critical section. Depending on lock acquisition, hydration contains the item or the later event restores it; it cannot miss both.
  - Completion criterion: runtime/registry tests prove the public Engine snapshot contains owner state in live order, terminal mutations remove it, and a failed commit racing hydration leaves the accepted item visible without a new event kind or reservation/recovery state machine.
  - Progress: Expanded the Engine hydration snapshot with owner-backed reasoning, ordered in-flight tools, and pending Queue messages; updated reasoning/tool owners before their existing events; added runtime/runtimeview replacement and terminal-mutation coverage; and restored failed Queue flushes through one output-steering item that emits existing accepted states. Focused runtime and runtimeview tests pass.

- [x] Move Reviewer, compaction lifecycle, context usage, and Goal hydration facts to their runtime owners with incremental TDD.
  - Add one failing Engine snapshot test for an active Reviewer and active compaction on the owning Step, then implement step-keyed state in the existing Reviewer and compaction runtime modules before their typed events publish.
  - Add terminal-event cases proving Reviewer and active compaction clear while completed compaction count remains.
  - Add context-usage and Goal snapshot cases using the existing usage and Session Goal operations; do not add another retained projection.
  - Completion criterion: focused runtime/runtimeview tests pass and every engine-owned hydration fact can be reconstructed without any session-feed publication.
  - Progress: Added step-keyed Reviewer and active-compaction owner snapshots, terminal clearing with completed-count preservation, and direct usage/Goal snapshots; projected all new owner facts through the existing runtimeview adapter. Focused runtime and runtimeview tests pass.

- [x] Reuse authoritative external owners for execution target, prompts, and background processes.
  - Add failing tests that distinguish a genuinely absent execution target from a resolver failure and require the failure to abort hydration/identity publication.
  - Change the resolver seam to optional-target-plus-error and remove the caller-supplied target from Session-identity publication; update the existing worktree/settings publishers to request a fresh publication after mutation.
  - Add a failing projection/composition test for Session-scoped running backgrounded shell snapshots and map identity/lifecycle/command/workdir/log facts only. Assert `Preview == nil` even when `RecentOutput` contains ANSI/control bytes or raw-mode output; keep terminal processes out of active hydration.
  - Reuse the pending-prompt store and its existing deterministic projection/order.
  - Completion criterion: focused registry, worktree/session-service, and background projection tests pass; no zero target or cached target is used as fallback, raw process output never enters Chat hydration, and no second background preview API exists.
  - Progress: Execution-target resolution now distinguishes absent targets from resolver errors, identity publication resolves fresh owner state without caller-supplied targets, retarget publishers request fresh identity publication, and shell-manager snapshots have a session-filtered active hydration projection that omits Preview. Focused registry/worktree/session-service/background tests compile and pass.

- [x] Replace sequencer-owned hydration with the canonical registry composer.
  - Start with a failing sequencer subscription-seam test where no runtime read-model update was previously published; require builder-produced sequence-1 hydration without retained feed state. Follow it with a registry subscription test requiring a fresh runtime read model, identity/status/target, and the complete owner-backed facts.
  - Implement one unexported registry composer inside the existing Engine hydration callback and sequencer lock. Derive active Step from the fresh runtime read model, include only typed step-owned facts matching it, and validate the final hydration before broker registration.
  - Refactor only `pendingPromptStore.CloseSession` before enabling that lock order: copy and delete the Session prompts while holding the store lock, release it, then publish the close-time resolution callbacks.
  - Leave `WithLockedAttentionSnapshotResult` and `SubscribeSessionAttentionNotifications` ordering unchanged. Their attention-broker registration and initial snapshot enqueue remain under the prompt lock; do not generalize the close-time fix into a store-wide callback rule.
  - Add the next failing cases for pending prompts, Queue order, background filtering, context usage, Goal, active assistant/reasoning/Reviewer/compaction/tools, and authoritative empty values.
  - Completion criterion: registry subscription tests pass with and without prior feed publication; hydration and Scratch Rehydration call the same composer and contain the full approved domain list.
  - Progress: Added the concrete registry hydration composer inside the existing Engine snapshot and Session-feed sequencing path. It builds a fresh runtime read model, Session status/identity, optional execution target, pending prompts, and active background processes, filters step-owned facts against the canonical active step, and validates before broker registration. `pendingPromptStore.CloseSession` now releases its lock before resolution callbacks. Replaced the feed-side hydration overlay and ledgers with builder-under-lock subscription plus direct ordered publication, and added sequence-1/no-prior-read-model and post-subscription delivery behavior tests. Focused registry/runtime/runtimeview/worktree/session-service tests pass.

- [x] Reduce the Session feed sequencer to ordering and delivery.
  - Add or update a behavior test proving hydration is sequence 1 and a publication admitted after subscription construction is delivered afterward.
  - Replace hydration overlay/deduplication with builder-under-lock subscription and direct typed batch publication while preserving broker overflow, close, and assistant/tool contract validation.
  - Delete the feed snapshot, Queue and active-state ledgers, reconciliation, deduplication, clone helpers that only served retained feed state, and the unused ordered-feed ledger module.
  - Completion criterion: sequencer/broker tests pass and repository search plus the architecture guard find no `sessionFeedSnapshot`, feed-side mutable hydration authority, or missed-read-model subscription prerequisite.
  - Progress: Reduced `sessionFeedSequencer` to its mutex and broker, removed feed-side state reconciliation/deduplication and the generic ordered-feed ledger, and preserved broker validation, sequence numbering, overflow, close, and runtime-read-model publication behavior. Sequencer and registry behavior tests pass.

- [x] Lock freshness at the identity/status publication seam and exercise the approved concurrency boundary.
  - Add a failing registry concurrency test that blocks hydration composition, mutates authoritative identity or execution target, requests publication, then proves hydration remains first and the later event resolves current owner state instead of delivering a stale precomputed value.
  - Resolve Session identity/status under the existing sequencer mutex, preserving the established engine-output → sequencer → broker lock order.
  - Add a runtime-event case proving an event admitted while subscription is assembled is delivered after hydration; assert only ordered delivery, not exactly-once appearance across the hydration boundary.
  - Add a deterministic subscription-versus-`ResourceDraining` test with a pending prompt: hold hydration composition under the sequencer, begin resource closure, then release the barrier and require subscription/closure to finish, hydration to remain sequence 1, and prompt resolution to follow without deadlock. The test must fail against the current prompt-store callback-under-lock behavior.
  - Completion criterion: race-focused registry tests pass repeatedly, including prompt closure and failed Queue restoration, and the implementation adds no lock, revision, epoch, replay, retry, ledger, or recovery state machine.
  - Progress: Added builder-under-sequencer-lock publication for Session identity/status so owner resolution occurs at admission time, and moved runtime-event status projection through that seam. Prompt-session closure now snapshots/deletes under the prompt lock and resolves after unlock. Deterministic target-publication and subscription-versus-resource-drain/pending-prompt tests pass.

- [x] Complete client recovery and wire regression coverage.
  - Extend the existing hydration/main-view tests so empty or absent active facts replace stale client live state while operator-owned input/navigation behavior remains untouched.
  - Add the missed-terminal-compaction scenario: leave the client with an old count, omit the terminal live event, apply fresh hydration, and assert the authoritative completed count replaces client state.
  - Add stale-opposite Conversation Freshness cases: seed `RuntimeStatus` and `RuntimeSessionView` with conflicting Fresh/Established values, apply hydrated Session identity, and assert both cache projections take the identity value and remain stable when `currentConversationFreshness` reads the status cache.
  - Make Session identity application update the runtime Session view and mirrored runtime Status freshness together; Session status must not become another incoming freshness source.
  - Keep the existing sequence-gap/subscription-loss tests as the recovery proof; add no alternate gap-fill path.
  - Verify gateway serialization still delivers hydration at sequence 1 with the updated Session-status contract and that incompatible protocol generations remain rejected.
  - Completion criterion: focused CLI controller/runtime-tuple, gateway, and protocol tests pass; compaction count and Conversation Freshness cannot regress after Scratch Rehydration; no visible UI behavior or copy changes.
  - Progress: Session-status hydration/live application already replaces completed compaction count unconditionally; added a conflicting Conversation Freshness reducer test and updated identity application to replace both the Runtime Session view and mirrored Runtime Status cache in one reducer operation. Focused CLI and registry tests pass.

- [x] Audit scope, ownership, bounded reads, and duplication before broad verification.
  - Measure `git diff --numstat`; if total changed lines are forecast to reach 2,000, stop and propose prerequisite tickets before dropping a hydration domain or widening the architecture.
  - Search for any remaining feed-retained copies of runtime read model, identity/status, execution target, reasoning, Reviewer, compaction, tools, Queue, prompts, backgrounds, context usage, or Goal.
  - Run the transcript-feed architecture guards and verify subscription still uses only the bounded active-segment delivery snapshot, never transcript page/tail/full-log readers.
  - Double-check that new projections reuse existing typed adapters and contain no duplicated utility, sentinel absence, test-only production API, or compatibility shim.
  - Completion criterion: the ownership/deletion audit is clean, architecture guards pass, and changed lines remain below the approved hard stop.
  - Progress: `git diff --numstat` reports 1,600 tracked changed lines, below the 2,000-line hard stop. Repository search finds no feed snapshot/ledger or legacy transcript reader, the sequencer architecture guard now requires only mutex plus broker, bounded-reader guards pass, and `git diff --check` is clean.

- [x] Run final verification once after all slices are green.
  - Run focused server/client packages during the slices, then run `./scripts/test.sh` once for the complete supported suite.
  - Run `./scripts/build.sh --output ./bin/kent` once and run `git diff --check`.
  - Do not build or test frozen Rust targets. No manual visual QA is required because the approved scope changes no layout, copy, or interaction.
  - Completion criterion: full tests, the Kent build, and diff validation pass with no unreported failures.
  - Progress: Ran `./scripts/test.sh` once with all Go and desktop tests passing, built `./bin/kent` once with `./scripts/build.sh --output ./bin/kent`, and verified `git diff --check`. The test harness emitted existing Cargo manifest warnings from the repository's frozen Rust tree; no Rust files were changed.

Review remediation

- [x] Resolve subscription-loss UX and ownership/concurrency findings.
  - Progress: Fatal and expected exits share one implementation; the user directed the latest QA copy/recovery concern to remain out of scope for this branch, so the existing subscription-open copy is restored. Prompt mutation admission now changes owner state under the exact-generation lifecycle mutex, reserves the existing Session-feed sequencer before releasing that mutex, publishes the feed while the sequencer is held, and sends attention/workflow notifications afterward. The read-model resolver observes lifecycle atomically without taking the lifecycle mutex, removing the sequencer-to-entry lock cycle while keeping the canonical read-model builder under sequencer admission. Metadata exposes explicit optional execution-target resolution without zero-target inference, and admitted-prompt/drain ordering coverage guards the lifecycle gate.
- [x] Restore runtime owner/reset guards and the hydration admission race proof.
  - Progress: Restored a focused Engine hydration test covering active Reviewer, active compaction, completed-count preservation after terminal clearing, ContextUsage, Goal, and GoalSuspended. Added deterministic registry coverage that blocks hydration composition under the sequencer and proves a concurrent runtime read-model publication cannot pass admission; no revision, replay, ledger, recovery state machine, or new lock was added.
- [x] Remain below the approved hard stop after review fixes.
  - Progress: Consolidated redundant review-only projection fixtures while retaining the owner, queue, client, metadata-absence, no-prior-read-model, and admitted-prompt/drain ordering coverage. The effective diff against base `3db9e6266` is 1,994 changed lines (`git diff --numstat` additions plus deletions).

Testing

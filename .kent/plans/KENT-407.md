## Recon

### Authoritative contracts

- `docs/dev/specs/desktop-chat.md` owns Desktop Chat behavior, including New Chat, delivered-result adoption, drafts, Queue, compaction, Pending Work, and connection loss.
- `docs/dev/specs/runtime-steering-loop.md` owns accepted Session mutation order, Queue and Steer behavior, Pending Work, manual-compaction admission, and exact-live Stop.
- `docs/dev/specs/core-runtime-tools.md` owns shared Session/runtime mutation and client-lifecycle boundaries.
- `docs/dev/specs/server-api-contract.md` owns generated route metadata, unary connection policy, and request cancellation at the transport boundary.
- `docs/dev/specs/project-workspaces.md` owns Project/workspace attachment semantics.
- `docs/dev/specs/terminology.md` defines Session, Active Session Runtime, Queue, Steer, Pending Work, and related domain terms.
- Existing edits to `docs/dev/desktop-chat/contract-gap-analysis.md` and `docs/dev/desktop-chat/question-map.md` contain prior KENT-407 analysis and concurrent KENT-386 coordination. Preserve them, but add no unrelated non-owning documentation.

### Fixed implementation baseline

- KENT-407 implementation and estimate use merged KENT-422 commit `f053c300e` as the parent baseline. Its measured 27-file, `+3,202/-102` delta is prerequisite work and is excluded from the KENT-407 estimate.
- KENT-422 supplies the flat Desktop `ApiService.chat` read adapter, Chat types and schemas, target-specific attachment helpers, runtime-owner transport support, and Desktop API test infrastructure.
- If implementation starts from an older checkout, rebase or merge the completed KENT-422 baseline before KENT-407 changes; do not count or redesign that prerequisite inside this ticket.

### Existing authorities to reuse

- Ordinary independent-main Session creation already owns Session initialization and durability. KENT-407 extends its request with complete initial Chat settings and optional exact input draft.
- `server/sessionruntime` already plans and opens an Active Session Runtime from a persisted Session. New Chat and dormant existing Sessions must use that same planner after Session creation or lookup.
- `server/runtimecontrol` and `server/runtime` already own submission admission, the Engine post-turn Queue, manual-compaction admission, exact-live Stop, Pending Work, and accepted mutation order.
- Existing ordinary `SubmitUserTurn` and its Go/TUI response contract remain unchanged. The new intent-level Chat coordinator maps existing admission facts into Chat-specific accepted or not-accepted results.
- Existing Session Runtime attachment and release policies remain unchanged. KENT-407 chooses the existing policy appropriate to whether work was accepted; it does not redesign global ownerless retirement.
- Workspace Chat draft persistence, its metadata document and schema, specialized materialization, pre-Session settings mutations, and their generated/client routes are the obsolete authority to delete.
- At the fixed baseline, specialized materialization calls `EnsureDurable` and attempts `RemoveDurable` whenever that call returns an error. It has no creation `CommitReceipt`, post-error status lookup, or commit-classification path. This materialization-only cleanup is deleted rather than generalized into ordinary creation.

### Scope boundaries

- KENT-407 adds the typed mutation adapter and thin server operations, not Chat UI, React Query composition, New Chat destination/local storage, settings-popover composition, prompt-picker UI, Goal UI, Worktree UI, Process UI, or Desktop Shell.
- KENT-386 consumes the shared target resolver for Goal Set. KENT-620, KENT-621, KENT-622, KENT-626, and KENT-631 retain their recorded TUI, Shell, Queue-migration, and independent-operation-lifetime ownership.
- Frozen Rust code is excluded.

## Design

- Expose one flat Desktop `chat` mutation adapter extending KENT-422. It contains intent-level Steer, Queue, and manual compaction plus exact-live Stop, fork/edit, and Pending Work list/removal.
- Desktop never calls Session creation, Runtime activation, attachment, release, or another prerequisite operation. Each intent-level mutation is one transport send.
- A Chat target is either an existing Session or New Chat with exact Project/workspace identity and complete displayed initial Chat settings.
- Existing-Session Chat calls may be authenticated without an attached Project. When a Project is attached, the Session must belong to it. New Chat requires the exact active Project, attached Workspace, and authoritative binding.
- New Chat Steer and Queue derive the exact initial Session draft from their one typed activation input. Plain text carries exact text. A prompt/Agent command carries canonical catalog identity plus exact lexical token, separator whitespace, and arguments.
- New Chat manual compaction carries one lexical invocation. The server validates it, reconstructs the byte-exact Session draft, and derives normalized compaction guidance from that same value.
- KENT-386 Goal Set uses the same target resolver and may supply its exact composer draft separately from the Goal objective. Desktop/New Chat uses `start_or_continue`; CLI/model-shell existing-Session callers preserve runtime state.
- Extend ordinary independent-main Session creation with complete initial Chat settings and optional exact draft. Resolve and validate current defaults once through ordinary creation.
- If a displayed Agent or enumerated setting became unavailable because configuration changed after the New Chat settings read, use the newest applicable baseline. Malformed structure remains an error.
- Commit through the ordinary Session creation path. Do not add a prepared-creation protocol, `CommitReceipt`, selected-root certification, post-error row/binding classification, replacement materialization race contract, or topology barrier.
- After ordinary creation returns a Session, invoke the canonical persisted-Session Runtime planner. Pre-Session work must not construct or carry a second Runtime, filesystem, tool, Project-boundary, or managed-root plan.
- KENT-407 adds no synchronization or guarantee between Workspace/worktree topology mutations and Runtime publication and changes no attach, detach, rebind, or managed-worktree writer.
- The Chat coordinator obtains server-owned Runtime preparation only when the operation requires it, delegates to the existing runtime owner, and releases only its attachment.
- Before definite non-acceptance, use the existing close-if-idle release policy. After acceptance, use the existing detach/retain policy so accepted Queue or compaction work remains Session-owned. Do not deepen `Engine.BeginRetirement`, deferred retirement, or global `CloseIfIdle` behavior.
- Request cancellation may terminate target resolution, optional Session creation, Runtime preparation, or admission before acceptance. Any Session already committed remains discoverable. Accepted work is unaffected by later cancellation, connection loss, or response loss.
- KENT-631 owns any future independent Chat-operation lifetime and server-shutdown joining. KENT-407 adds no detached handler work, Core lifecycle owner, Gateway lifetime change, or `ServeServer` shutdown change.
- `chat.steer` reuses the existing submission admission helper and maps only the Chat result. It does not change ordinary `SubmitUserTurn`, its generated response, completed-turn/final-answer behavior, or Go/TUI clients.
- Accepted Chat Steer returns the resolved Session and Queue Item identity immediately. If the reused helper synchronously reports a failure after acceptance, the Chat result retains those identities and may carry that typed diagnostic. Later provider, model, tool, or Runtime failure remains ordinary Runtime/transcript feedback and never rewrites that delivered Chat result.
- Add one public server Queue admission operation over the existing Engine post-turn Queue. It accepts ordinary text or a recognized prompt/Agent command, resolves command execution text server-side, and preserves canonical command presentation and history.
- A queued input starts ordinary work immediately when the prepared Runtime is idle and eligible. During active work it retains existing post-turn Queue ordering and capacity behavior.
- Deepen the existing Queue item only as required to hold execution text and canonical presentation once. Adapt existing Queue writers/readers to that single representation without changing ordinary Submit response behavior or creating another queue.
- Manual compaction uses one existing Engine admission path for idle and active Runtimes. Active-run compaction enters current Pending Work rather than starting another Agent execution.
- Fresh New Chat `/compact` creates the ordinary Session, preserves the exact reconstructed draft, and returns ordinary typed `too_soon`.
- Ordinary creation has one binary boundary: it returns a Session or it returns an error. When it returns a Session, every normal Chat Steer, Queue, or compact result carries that Session, including definite later non-acceptance with a typed failure. When ordinary creation returns an error, Chat returns that operation error with no Session result.
- Keep results small: accepted Steer/Queue carry Queue Item identity, accepted compaction carries Compaction Request identity, Stop returns stopped or idle, fork returns child Session identity, and discard returns typed canonical restoration.
- Delete workspace Chat drafts and specialized materialization in one startup-fatal schema cutover. Preserve ordinary Session drafts/settings and TUI `launch_visible`. Add no compatibility route, fallback read/write, dual authority, or alias.
- Rename the stateless Project/workspace settings read to New Chat and delete all pre-Session settings mutations.
- Hard-move Pending Work list/removal and manual compaction from top-level Desktop API methods into `ApiService.chat` without forwarding aliases.
- Desktop `$` remains ordinary text. TUI Queue and other TUI behavior remain unchanged.
- Preserve the existing behavior-focused specification and analysis-document edits. Add no documentation outside the exact New Chat/Chat mutation, cancellation, and removed workspace-draft contracts.

## Architecture

### Server composition

- Add one operation-independent Chat target resolver. Existing Session resolution returns the authoritative Session. New Chat resolution authorizes the exact target and calls ordinary independent-main creation with initial settings and optional draft.
- Add one Chat mutation coordinator over the resolver, canonical persisted-Session Runtime planner, existing admission helpers, and existing attachment policies. It owns orchestration only; Session creation, Runtime state, Queue state, compaction, and Pending Work remain with their current owners.
- Reuse one target resolver for `chat.steer`, `chat.queue`, `chat.compact`, and KENT-386 Goal Set. Operation-specific input remains outside target identity.
- Reuse one persisted-Session Runtime planner for newly created and dormant existing Sessions. A ready Runtime is attached without replacement.
- Map existing admission facts to Chat-specific results at the coordinator boundary. Do not alter ordinary Submit wire/domain results or TUI projection.
- Select release behavior from the existing policies after the admission result is known: close if idle for definite non-acceptance; detach/retain after acceptance.

### Contract cutover

- Stage additive generated Chat declarations while old workspace-draft/materialization declarations and consumers still compile.
- Add the Chat target, initial settings, optional draft, Steer/Queue activation unions, lexical compaction invocation, Chat-specific results/errors, Queue Item and Compaction Request identities, `chat_target` scope, and new RPCs.
- Do not add an acceptance-shaped replacement for ordinary `SubmitUserTurn`.
- Item 5 removes the workspace-draft/materialization storage, service, Gateway route, shared client, Core wiring, tests, and schema authority immediately. Dependent consumers may remain uncompilable until item 9; those compile failures are the authoritative cleanup index, and no placeholder authority may keep the obsolete operations alive.
- In item 9, adapt every remaining production consumer to the new Chat/settings contracts, remove the old protobuf messages/RPCs and generated outputs, rename settings to New Chat, bump the protocol version, regenerate, and restore complete compilation.
- Leave no aliases, forwarders, dual routes, fallback reads/writes, or compatibility shims.

### Authorization and validation

- Add one fixed `chat_target` route-scope policy and one typed target accessor shared by every supported Chat request.
- Validate malformed or multiply selected target unions before authorization.
- Authorize New Chat against exact Project/workspace attachment and binding.
- Authorize projectless existing-Session calls after authentication; with an attached Project, require authoritative Session membership.
- Reject scope mismatches before service dependency selection or coordinator execution.
- Validate activation unions and lexical command fields structurally. Never parse raw command text to infer a case or duplicate catalog authority.

### Queue and compaction

- Store one immutable queued-user-input value containing model execution text and canonical presentation. Ordinary text sets both to the same value.
- Model submission consumes execution text. Pending Work, discard/interruption restoration, status, and history consume canonical presentation.
- Resolve prompt/Agent commands once before Queue acceptance and record canonical history only after acceptance.
- Keep existing Queue identity, ordering, capacity, drain, restoration, and TUI-local Queue behavior.
- Route idle and active manual compaction to the same current-Engine admission authority without changing global Runtime retirement.

### Desktop adapter

- Extend KENT-422 `ChatApi` and `createChatApi` with Steer, Queue, Compact, Stop, fork/edit, Pending Work list, and Pending Work removal.
- Reuse existing KENT-422 target attachment, parsing, schemas, transport, and error infrastructure.
- Add one discriminated Chat error union and one shared user-facing mapper.
- Return domain values rather than generated response objects.
- Expose no Create, Runtime lifecycle, React Query, local persistence, UI state, replay, reconciliation, or optimistic transcript APIs.

## Planning

- [x] **Establish the fixed KENT-422 parent.** Start implementation from merged commit `f053c300e` or a later main revision containing it. Actual comparison revision: `7c1c8dcd472fdc550835971ea009be5d8d7685c3`. Exclude KENT-422's measured 27-file `+3,202/-102` delta from KENT-407 review and estimates. Completion: the baseline contains one flat KENT-422 Chat read adapter and the KENT-407 diff starts empty relative to that baseline.

- [x] **Finalize only the owning contracts and preserved planning records.** Keep the approved New Chat, cancellation, Queue/Steer, Chat-result, workspace-draft deletion, and target-authorization wording aligned across Desktop Chat, Runtime Steering, Core Runtime and Tools, Server API Contract, Project Workspaces, and Terminology. Preserve the existing contract-gap analysis and question-map edits because they contain prior/concurrent work, but add no unrelated documentation. Record that the selected-root/materialization safety decision is superseded: ordinary creation is reused without a replacement root-race contract. Completion: specs contain no KENT-631 rollout history, no contradiction about pre-acceptance cancellation, no ordinary Submit response cutover, no unknown-creation-status or prepared-artifact contract, no materialization-only root guarantee, and no stale lazy/materialization Context behavior.

- [x] **Stage and validate the additive generated Chat contract.** Add new Chat target, settings/draft, activation, lexical compaction, Chat-specific result/error, identity, route, and scope declarations beside the still-consumed old declarations; regenerate with `just gen`. In red/green contract tests, cover target unions, optional presence, complete settings, lexical reconstruction, accepted/not-accepted Chat results, typed identities/errors, malformed structures, and `chat_target` scope extraction. Do not change ordinary Submit messages or consumers. Completion: generated/shared packages and old consumers compile together, focused contract tests pass, and no alias or second active route exists.

- [x] **Implement and verify target-aware authorization.** Add the shared typed accessor and route policy. Cover exact New Chat Project/workspace binding, projectless existing Sessions, attached-Project membership, wrong Project/Workspace, foreign/missing Session, malformed union, and rejection before service selection. Include KENT-386 Goal callers when that route integrates. Completion: all supported target contexts invoke the service once and every mismatch fails before execution without raw JSON or operation-specific authorization.

- [x] **Extend ordinary creation and delete the workspace-draft authority with red/green coverage.** Add complete creation-only initial settings and optional exact draft to independent-main creation, including current-baseline rebasing for raced configuration changes. Cover valid creation, malformed input, ordinary creation error with no Session result, preservation of ordinary Session drafts/settings, headless/derived rejection, and TUI `launch_visible`. Add the one-way startup-fatal migration and cover legacy draft deletion plus migration failure. Delete workspace draft/materialization storage, service, route, client, and tests without recreating binding/root revalidation, `CommitReceipt`, post-error status lookup, special cleanup classification, prepared-artifact preservation, or topology barriers. Completion: ordinary creation is the sole Session-create path, creation errors use its existing failure behavior, workspace draft/materialization has no live consumer, and focused creation/migration tests pass.

- [x] **Implement and verify the shared Chat target/Runtime coordinator.** Resolve existing Sessions directly and New Chat through ordinary creation, then invoke the canonical persisted-Session Runtime planner for newly created or dormant Sessions. Add behavior tests for New Chat and existing Session Steer with no pre-existing Runtime, preparation/open failure, definite non-acceptance after creation, cancellation during each pre-acceptance phase, committed Session survival, response loss after acceptance, stale attachment release, and release diagnostics. Map the existing admission result into the Chat-specific result without changing ordinary Submit/TUI. Use existing close-if-idle before definite non-acceptance and detach/retain after acceptance. Completion: one resolver and one Runtime planner serve Steer/Queue/Compact and a Goal-shaped caller; accepted work survives release; ordinary Submit contracts, global retirement, topology writers, Gateway lifetime, and shutdown remain unchanged.

- [ ] **Deepen the existing Queue value and add the thin Chat Queue operation with red/green coverage.** Introduce one queued-user-input value carrying execution and canonical presentation, and adapt existing queue writers/readers to it. Reuse ordinary input and command resolution. Cover text and prompt/Agent commands, idle immediate start, active post-turn retention, identity, capacity/rejection, canonical Pending Work/history/restoration, prompt-history failure after acceptance, cancellation before acceptance, and response loss after acceptance. Completion: one Engine Queue remains authoritative, model input and canonical presentation stay distinct where required, accepted Queue work survives detach under existing policy, and ordinary Submit response behavior plus TUI local Queue remain unchanged.

- [ ] **Add one intent-level manual-compaction path with red/green coverage.** Validate the single lexical invocation, reconstruct the exact draft, derive normalized guidance, and use the current Engine's existing admission authority for idle and active Runtimes. Cover eligible idle acceptance, active Pending Work acceptance, typed rejection, pre-acceptance cancellation, response loss after acceptance, and fresh New Chat `too_soon` with Session identity and exact draft. Use existing attachment release policies without changing `BeginRetirement` or deferred retirement. Completion: idle and active compaction share one admission path, accepted work remains Session-owned, and no synthetic Agent execution or second compaction implementation exists.

- [ ] **Complete the generated/consumer/schema hard cutover.** Adapt Session-launch contracts, Gateway, shared Go clients, Core composition, Desktop transport types, and direct consumers while old generated declarations still exist; run focused Go and TypeScript compilation. Then remove old generated declarations and all workspace-draft/materialization consumers in one slice, add the migration, rename settings, bump protocol version, regenerate, and compile immediately. Completion: targeted searches find no obsolete declarations, routes, storage, consumers, aliases, fallbacks, or dual authority.

- [ ] **Implement the Desktop mutation adapter with red/green coverage.** Extend KENT-422's flat Chat adapter with intent-level Steer, Queue, Compact, exact-live Stop, fork/edit, and Pending Work list/removal. Cover New Chat and existing-Session targets, accepted/not-accepted results, typed errors, malformed responses, disconnected calls, one send per invocation, exact lexical compaction, fresh `too_soon`, and absence of Create/Runtime orchestration. Hard-move old top-level Pending Work/compaction exports without aliases. Completion: focused Desktop API tests pass and components can consume only domain values through `ApiService.chat`.

- [ ] **Verify the final scope and product behavior.** Run focused suites while iterating, then `just test server`, `just test desktop`, `just lint --dry-run`, `just build go`, `just check server`, `just check desktop`, applicable full `just check`, and `git diff --check`. Inspect the final diff for ordinary Submit/TUI response changes, new global retirement behavior, `CommitReceipt`, post-error creation lookup/classification, prepared-artifact preservation, selected-root machinery, topology synchronization/tests, stale lazy/materialization Context behavior, detached request work, Core/Gateway/shutdown lifecycle changes, duplicate target/Queue/compaction authorities, compatibility paths, raw command parsing, sentinel absence, Desktop Create/Runtime APIs, React Query/UI/local-storage work, Desktop Shell, TUI Queue migration, unrelated docs, and frozen Rust edits. Completion: all commands pass, creation failures retain ordinary creation semantics with no unknown-status result contract, the diff is relative to the fixed KENT-422 baseline, and only KENT-407 behavior and its behavior-focused tests/specs remain.

## Testing

- Keep committed tests because the user explicitly retained behavior-focused red/green TDD for this feature.
- Consolidate coverage into four implementation-facing groups: generated contract/authorization, ordinary creation/migration, server Chat mutations, and Desktop adapter.
- Test observable product boundaries and typed contracts. Do not add source-shape, literal-copy, file-layout, call-order, or topology-race tests.
- Preserve relevant existing tests and delete obsolete workspace-draft/materialization tests only when replacement product behavior is covered.
- No new tests may require ordinary Submit/TUI response changes, global Runtime-retirement changes, unknown-creation-status classification, selected-root commit certification, or independent Chat-operation lifetime.

## Estimate

- Comparison baseline: merged KENT-422 commit `f053c300e`; its 27 files and `+3,202/-102` lines are excluded.
- Production: 34-52 files, approximately `+1,900-3,200/-1,300-2,300` lines.
- Tests: 18-28 files, approximately `+1,900-3,200/-500-1,100` lines.
- Generated outputs: 18-36 files, approximately `3,000-6,500` lines of churn.
- Confidence: medium. Main uncertainty is generated-output breadth, the workspace-draft schema cutover, canonical Runtime-planner reuse, and the Queue item's execution-versus-presentation propagation.

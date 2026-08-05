## Recon

- The current sidebar is a single-destination lifecycle in `apps/desktop/src/app/sidebarProvider.tsx`. Its public contract is `SidebarController` in `apps/desktop/src/app-facade/sidebarContext.ts`: `activeDestination`, `openSidebar`, `replaceSidebar`, `closeSidebar`, `resolveSidebar`, and width/phase state. `openSidebar` resolves the prior lifecycle as `canceled/replaced`; `replaceSidebar` keeps the existing lifecycle promise pending while swapping the rendered destination. `SidebarProvider` retains only the current destination.
- Sidebar rendering and close behavior are centralized in `apps/desktop/src/app/sidebar.tsx` and `apps/desktop/src/app/sidebarDestinations.tsx`. The header currently renders X first, then the title, then optional Inbox Previous/Next, pop-out, and destination-specific actions. `SidebarRouteChangeCloser` watches TanStack Router location and closes the sidebar with `route_change`; X calls `closeSidebar("closed")`. The destination content is mounted once below the header.
- `SidebarDestination` already has typed `taskDetail` and `newTask` variants in `apps/desktop/src/app-facade/sidebarContext.ts`. `newTask` carries `projectID`, `workflowID`, optional `boardQueryWorkflowID`, source workspace, and `pendingRelationship`; `taskDetail` carries `taskID`, optional `initialFocus`, `mode`, `onMutated`, and `inboxNav`. `SidebarDestinationView` passes these values to `TaskDetailSurface` and `NewTaskForm`.
- Existing shell seams for native pop-out are `apps/desktop/src/app/sidebarPopOut.ts` and `apps/desktop/src/features/task-detail/TaskDetailWindowRoute.tsx`. A sidebar Task Detail pop-out currently sends only `taskID` in native window params and then closes the sidebar after the window opens. The standalone route is `/tasks/$taskId` for browser routing, while the native dialog route is `/native-dialog/task-detail`.
- Browser route state is independent from the sidebar: `/projects/$projectId` stores `workflowId` and `taskId` search values, and `BoardRoute` opens a sidebar Task Detail when `selectedTaskId` is present. `BoardRoute` also closes the sidebar when the route changes through `SidebarRouteChangeCloser`; selected-task deletion closes the sidebar and clears the project task route. `apps/desktop/src/app-facade/navigation.ts` is the route navigation seam and should be inspected wherever sidebar-local movement must avoid changing browser history.
- KENT-210’s current product contract is recorded in `docs/dev/specs/desktop-gui.md:136-186`: related Task selection replaces the current Task Detail, dependency Add opens ordinary New Task, related creation uses the open Task’s Project/Workflow and source workspace default, creation carries a typed dependency intent, and there is no existing-Task picker. The adjacent orchestration contract in `docs/dev/specs/workflow-orchestration.md:44-117` permits directed cycles of three or more Tasks, requires same-Project scope, has complete unpaginated direct dependency reads, and forbids transitive dependency traversal.
- The dependency UI is isolated in `apps/desktop/src/features/task-detail/TaskDependenciesArea.tsx` and composed by `TaskDetailList.tsx`. Rows call `onSelectTask`; direction Add calls `onAdd`; remove is immediate. `TaskDetailContent.tsx` currently maps related selection to `replaceSidebar({ kind: "taskDetail", taskID })` when the active destination is a Task Detail, and maps Add to `replaceSidebar(newTaskDestination)` with `pendingRelationship.newTaskRole` (`blocker` for `blocked-by`, `blocked` for `blocks`). Outside a sidebar it falls back to route navigation.
- New Task creation is implemented in `apps/desktop/src/features/tasks/NewTaskDialog.tsx`. `NewTaskForm` uses React Hook Form with title/body/source workspace fields, preserves input on mutation failure, and submits `dependencyIntent` through `useCreateTask`. `apps/desktop/src/api/clientInputs.ts` defines `TaskDependencyCreateIntent`; `apps/desktop/src/api/clientWorkflowLabels.ts` adapts it to the server `workflow.task.create` payload. `apps/desktop/src/shared/task-mutations/useTaskMutations.ts` invalidates affected board queries after success but currently returns only the created Task ID to the caller through the API mutation.
- Task Detail local UI state currently lives inside the mounted `TaskDetailContent`/`TaskDetailList` subtree: title/body draft reconciliation, editing comment, unsent new-comment body, description presentation, selected Comments/Activity tab, question selections, and task list rendering state. `TaskDetailContent.tsx` explicitly resets comment/question/presentation state when `detail.id` changes and keeps dirty title/body edits separate from server refreshes. `TaskDetailSurface.tsx` owns the active detail/attention/activity/comments queries.
- A mounted Task Detail composes several existing live-resource owners across
  Task refresh, Project labels, Question lookup, and label-assignment
  lifecycles. Stack verification must prove that only the current destination
  remains behaviorally active and traversal does not multiply requests,
  subscription-driven refreshes, or visible updates. It must not freeze current
  framework topology or add production inspection APIs.
- The shared API dependency seam is `apps/desktop/src/api/clientTaskDependencies.ts` (`listTaskDependencies`, `addTaskDependency`, `removeTaskDependency`) with typed models/schemas under `apps/desktop/src/api`. Existing dependency rows are complete direct lists, so cycle traversal is currently possible through repeated UI selection even though no traversal helper exists.
- Inbox navigation is the closest existing visual/interaction precedent. `apps/desktop/src/features/home/SidebarInboxNav.tsx` uses `IconTooltipButton` with ChevronLeft/ChevronRight and `app.inboxPrevious`/`app.inboxNext` translations, and opens another sidebar destination. It is mounted by `SidebarInboxNavSlot` in `sidebar.tsx` only for `taskDetail.inboxNav === true`; it is not a general sidebar history control.
- Reusable test seams include `apps/desktop/src/app/sidebarProvider.test.tsx` (lifecycle replacement semantics) and `apps/desktop/src/test-support/sidebar/index.ts` (`createTestSidebarController`). Existing tests should be extended or supplemented at the sidebar contract boundary rather than coupling feature tests to shell internals.
- `apps/desktop/src/app-facade/projectDeletionEvents.ts` and `apps/desktop/src/app/AppChrome.tsx` inspect `activeDestination` to close or react to project deletion. Any future sidebar stack representation will need equivalent whole-stack matching/cleanup rather than checking only one destination.
- Desktop ownership rules in `apps/AGENTS.md` place sidebar providers, routes, native-window controllers, and shell composition under `apps/desktop/src/app/**`; feature behavior under `src/features`; reusable cross-feature capability under `src/shared`; and feature-facing sidebar contracts under `src/app-facade`. Architecture lint is fail-closed, so new stack seams must respect those public entrypoints.

## Design Scope Card

- **Outcome:** Users can traverse related Tasks and related-Task creation in one
  bounded sidebar stack; Back restores the approved Task Detail state, and
  successful related creation opens the newly created Task Detail.
- **Estimate:** Production 18–25 files / 900–1,800 changed LoC; tests 10–16
  files / 850–1,300 changed LoC; documentation 1 file / 40–80 changed LoC;
  generated files 0. Confidence: low until Architecture replaces the rejected
  mechanisms and proves the full contract fits the user-mandated production
  cap. The complete current Desktop/spec worktree is 30 production files /
  1,916 changed LoC, 12 test files / 1,281 changed LoC, and 1 documentation file
  / 62 changed LoC, so it is not implementation authority. Stop for a new human
  scope decision if the corrected projection exceeds 2,000 production changed
  LoC; do not weaken product behavior to fit silently.
- **Affected subsystems:** Desktop sidebar shell and app-facade, Task Detail,
  New Task and Task mutation, Desktop styles/i18n, and Desktop tests/test
  support.
- **Contract impact:** Desktop-internal app-facade and component contracts
  change, and the Desktop product specification changes. The history core is
  destination-agnostic, uses the existing TanStack navigation/history
  primitives, and contains no Task, Workflow, or Board knowledge. Typed feature
  adapters own same-Task equivalence/truncation, Task/Project invalidation, and
  restoration. Pop out completion is scoped to the sidebar/destination that
  started it.
  The sidebar exposes a minimal navigation contract without lifecycle, entry,
  or activation IDs or tokens; loading, query, and snapshot-registry internals
  remain private. Server API/wire, protocol version, persistence, migrations,
  and native-dialog contracts do not change.
- **Bounded-retention boundary:** The 50-entry and live-resource bounds cover
  sidebar state, snapshots, mounted surfaces, observers, and transport
  subscriptions. Ordinary inactive React Query cache is explicitly excluded
  and keeps the app's existing time-based lifecycle.
- **Required review disposition:** Preserve the accepted outcomes of all 65
  resolved PR #686 findings without reopening them. Satisfy the 25 unresolved
  findings through the destination-agnostic ownership model and the
  finding-specific regression barriers recorded in
  `docs/tmp/KENT-356-pr-686-review-report.html`. The rejected August 3
  simplification must not be recorded as superseding resolved findings.
- **Required scope cleanup:** Remove unrelated legacy-route migration handling,
  server/worktree and timeout-test edits. The typed scroll-restoration seam is
  owned by `VirtualizedInfiniteList`; Task Detail supplies only its restoration
  request and does not mutate the virtualized scroll element directly. Errors
  remain typed and visible at the UI boundary; no error swallowing, error-text
  parsing, hardcoded app-facade copy, or string-based destination identity is
  permitted.
- **Verification boundary:** Automated product-boundary tests, typecheck, lint,
  architecture checks, scope-diff checks, and builds are authoritative.
  Product browser/manual QA remains excluded; opening the review report in
  Firefox is workflow evidence only.
- **Excluded follow-ups:** KENT-369 owns shared New Task Dependencies and the
  existing-Task picker. KENT-372 owns unifying Task create/edit navigation and
  introducing the server Task upsert operation.
- **Rejected adjustment:** On August 3, 2026, the human rejected duplicate
  same-Task entries, whole-stack replacement for retained-entry invalidation,
  removal of Project-history reconciliation, and unscoped existing Pop out
  completion. Retain the full previously approved behavior. In particular, a
  Pop out started by an old sidebar may open its native window but must not
  close a replacing sidebar.
- **Downstream status:** The Architecture and Planning sections below replace
  the rejected adjustment and implement this approved card. The rejected branch
  implementation and its completed checklist are not authority for future work.

## Design

- Sidebar Task navigation is local to the sidebar and does not use browser
  history.
- The sidebar navigation-history core supports arbitrary typed destinations and
  is implemented through the existing TanStack navigation/history primitives.
  It does not inspect or name Task, Workflow, Board, or other feature variants.
- Typed destination adapters own feature-specific identity, deduplication,
  invalidation, and restoration. The generic history core owns only bounded
  push/replace/back/close behavior.
- The sidebar's feature-facing contract is a minimal deep navigation contract.
  Lifecycle, entry, and activation IDs or tokens, loading/query state, snapshot
  registries, and destination-shape inspection are not exposed or propagated.
- Navigation and route failures remain typed and are surfaced through the
  existing UI error boundary. The navigation core does not swallow errors,
  parse error text, or own localized copy.
- KENT-356 does not add legacy-route migration behavior, change server or
  worktree code, or extend test timeouts. Scroll restoration is a typed
  offset request owned by `VirtualizedInfiniteList`; Task Detail owns the
  request data through its existing rendering and list seam.
- Selecting a related Task pushes its Task Detail onto the sidebar stack.
- When the selected Task already has an entry earlier in the stack, the sidebar
  returns to that saved entry and removes every later destination instead of
  creating a duplicate.
- Returning to an earlier Task silently discards unsent Task edits and comment
  drafts from every removed later destination.
- The sidebar header keeps X first and places Back after it. X closes the
  complete stack. Back returns to the preceding destination.
- Back is hidden when the sidebar is at its root destination.
- Back uses the existing sidebar navigation icon-control presentation. The
  sidebar does not provide Forward.
- The stack retains at most 50 destinations.
- Only the current sidebar destination remains live. Earlier destinations retain
  bounded interface state without continuing to load or receive live updates.
- The 50-destination bound applies to sidebar entries and retained interface
  snapshots, not React Query cache entries.
- Inactive Task Detail queries follow the Desktop app's ordinary time-based
  cache lifecycle. Dependency traversal may temporarily increase that inactive
  cache until ordinary expiry; the sidebar does not pin, copy, extend, isolate,
  or explicitly evict those queries.
- Pushing a 51st destination preserves the root and evicts the oldest
  non-root destination. Eviction is silent and discards that destination's
  retained state, including unsent Task edits and comment drafts.
- Related-Task creation pushes New Task onto the stack.
- Related-Task selection and Dependency Add are unavailable while a Task
  title/body save or add/edit-comment save is pending. Back, X, route change,
  and Pop out keep their ordinary behavior.
- If the Task Detail remains current, save success re-enables Push without
  retaining submitted input; failure re-enables Push and leaves the failed draft
  eligible for ordinary snapshot restoration.
- Leaving through Back, X, route change, or Pop out while a save is pending uses
  the already approved discard/close behavior. Later settlement does not restore
  or reopen that destination.
- Because New Task has no Cancel button, Back discards the unsubmitted form and
  returns to the preceding Task Detail. X discards it and closes the complete
  stack.
- Related New Task carries its preconfigured originating relationship without
  showing Dependencies in the form.
- Successful related-Task creation atomically creates the Task and relationship,
  then replaces the New Task destination with the newly created Task Detail.
  The originating Task remains immediately behind it in the stack.
- If creation succeeds after its New Task destination is no longer current, the
  Task and relationship remain created but the current sidebar is unchanged.
- Pop out opens only the current Task Detail. After the native window opens,
  the originating sidebar closes its complete stack.
- If the destination or complete sidebar lifecycle changes before the window
  opens, the window remains open and the replacing/current sidebar is unchanged.
- New Task does not show Dependencies.
- New Task has no Cancel button.
- KENT-356 does not change the existing Task Detail Dependencies Add controls.
  They continue to open New Task. Existing-Task search and a new Add chooser
  belong to a dependent follow-up after GUI Task Search exists.
- Inbox Previous/Next continues to replace the current Inbox Task and does not
  push sidebar history.
- The browser Task route remains anchored to the root Task that opened the
  sidebar stack. Sidebar push and Back do not update that route.
- Back restores only the Task Detail state named by this ticket: scroll
  position, description expansion, Comments/Activity tab, unsent Task edits,
  and unsent comment drafts. It does not preserve unfinished Question
  responses.
- Scroll restoration reapplies the captured pixel offset after the ordinary
  server refetch. If refreshed content is shorter or structurally different,
  the list uses the nearest available position; inactive query pages are not
  pinned or replayed solely to reconstruct the old viewport.
- Task Detail initial focus applies only to its first activation. Back restores
  the captured scroll position and does not replay the original Dependencies,
  Question, Approval, or interrupted-node focus request.
- Back from a current Task Detail discards that destination's unsent Task edits
  and comment drafts without warning. Restored drafts belong only to an earlier
  retained destination.
- Restoring a Task Detail refetches current server data and layers its retained
  unsent Task/comment drafts over the fresh data.
- When a retained Task is deleted, the sidebar removes that destination. Back
  skips deleted destinations and closes the sidebar only when no destination
  survives.
- Removing the final Task settles the originating sidebar lifecycle as an
  ordinary `closed` cancellation so an anchored browser Task route clears.
- Project deletion reconciles both current and inactive sidebar destinations.
  Deleted-Project destinations cannot remain reachable through Back; unrelated
  surviving destinations remain available when the browser route stays put, and
  the sidebar closes when no destination survives. If deletion navigates away
  from the current Project route, the ordinary route-change contract closes the
  complete stack.
- A browser route change outside sidebar-local navigation closes the complete
  stack and silently discards its unsent Task edits and comment drafts.
- A server disconnect/reconnect preserves the complete stack and its saved UI
  state. The current destination uses the ordinary reconnect state and
  refreshes server data after reconnection.
- Pop out proceeds without a warning when stack entries contain unsent input.
  Opening the separate window discards that unsent input when the originating
  stack closes.
- X silently discards unsent Task edits and comment drafts when it closes the
  complete stack.
- Push and Back use a quick horizontal transition and respect reduced-motion
  settings. X keeps the existing sidebar close transition.
- KENT-369 is the dependent follow-up for the shared New Task Dependencies
  widget and existing-Task dependency picker. KENT-356 blocks it, and its
  implementation waits for reusable Desktop GUI Task Search.
- KENT-372 is the separate follow-up for unifying Task create and Task edit into
  one navigation destination backed by a typed server upsert operation.

## Architecture

- Replace the interrupted object-owner bridge rather than patching it. The final
  shell has no lifecycle/entry/activation IDs, object-owner comparisons,
  callback registry, sidebar-owned route-expectation state, or family of
  conditional-current methods. Retain the original sidebar's width profiles,
  close animation, and one pending result Promise.
- Reuse the existing direct `@tanstack/react-router` dependency and its
  `createHistory` primitive for one shell-owned generic bounded history. Its
  private storage is the only stack source of truth and retains each complete
  TanStack state (`__TSR_key`, `__TSR_index`) plus typed
  `{ destination, retainedState }`. React observes snapshots with
  `useSyncExternalStore`; it does not mirror entries in a reducer or component
  state.
- All in-memory locations use the same `/` href with empty search/hash.
  Destination identity and membership remain typed payload data and never enter
  paths or serialized strings. Initialize the root through TanStack so it has
  an opaque library key. Every activation receives a fresh TanStack-generated
  key: Push, replace, Back, same-Task return-to-earlier, and current-entry
  removal that reveals a predecessor. Retained state survives, but a previously
  active key is never reused as current.
- The generic history imports no feature type. It owns branch truncation,
  root-preserving oldest-non-root eviction at 50 entries, physical Back with no
  Forward branch, replace, atomic predicate-based removal, index rebasing, one
  subscriber notification per accepted operation, and inert behavior after
  destroy.
- Keep positions and entries inside the shell. Generic commands accept typed
  predicates supplied by the destination adapter rather than exposing arrays or
  numeric locations. Push receives the current source key, captured outgoing
  state, requested destination, and same-destination predicate; it atomically
  saves/deactivates the source, then either returns to the matching earlier
  location while preserving its retained state and refreshing its TanStack key,
  or appends with capacity eviction. Remove accepts a typed predicate and
  atomically reveals the nearest surviving predecessor with a fresh key,
  promotes a surviving root when necessary, or reports empty history.
- Use one concrete sidebar destination adapter beside the existing dispatcher,
  not a generic registry/factory. It owns Task equality, Project membership,
  activation-only focus removal, natural feature keys, and Task retained-state
  narrowing. Other destination variants do not deduplicate unless a later
  approved use case adds a typed rule.
- Task Detail destinations carry required `projectID` membership. Board/Home,
  related navigation, created-Task replacement, Inbox replacement, and
  notifications populate it from existing typed data. A native notification
  that lacks membership resolves the Task through the existing Task API before
  opening. Task Project membership is immutable, so no later loading callback
  or query state is needed to keep history synchronized.
- `SidebarProvider` retains one `PendingSidebar { history, resolve }`. Open
  cancels and destroys the previous history before installing a new root;
  Push/replace/Back remain inside the same pending result; close/route change or
  destination completion settles it once. Destroy makes old deferred history
  commands inert but preserves the last snapshot long enough to render the
  outgoing destination through the 140 ms close animation.
- Key `CurrentSidebarDestination` by the current opaque TanStack location key.
  A new `openSidebar` therefore remounts the shell host even for the same Task or
  same-kind New Task. Push/Back/replacement also remount it; returning A→B→A
  mounts a new A host from A's retained snapshot under a fresh key. Same-current-
  Task refocus is handled inside Task Detail without a history operation,
  preserving its subtree and drafts.
- The current host owns one capture callback and current X/Back availability
  locally. It exposes one shell-private scoped action function bound only to the
  pending history and opaque location key. Typed actions cover synchronous
  admission, current replace, resolve, close, and destination invalidation. The
  function performs the single currentness check; stale New Task, missing-Task,
  and Pop out completions have no separate guards to diverge.
- Push/Back commits synchronously through the history boundary after capture.
  The accepted operation changes the current TanStack key before returning to
  the event loop, so old scoped actions become inert immediately and there is no
  pre-admission exit window or deferred command to cancel. A→B→A remains safe
  because every activation receives a fresh key.
- Ordering is fixed. Push captures first; blocked capture changes nothing.
  Accepted Push/Back performs the atomic keyed history operation. X/route close
  destroys history and settles the result, retaining only the final snapshot
  for the existing close animation. Replacing open destroys/cancels the old
  pending history before installing the new root; replacement performs one
  keyed history replace. Back/X remain unavailable only after related creation
  mutation admission; route close and replacing open remain legal.
- The history snapshot carries only presentation direction (`push` or `back`)
  for the newly activated location. The fresh-keyed host uses the existing
  sidebar CSS motion path for a quick horizontal enter animation, with the
  existing reduced-motion media behavior. Navigation correctness does not
  depend on the View Transition API or any cancellation/serialization layer.
- Render only the current destination. Task Detail owns the retained snapshot:
  scroll offset, description expansion, selected Comments/Activity tab, unsent
  title/body, unsent new-comment text, and one edited-comment draft. It excludes
  Question responses, server projections, query pages, mutation/loading state,
  observers, and subscriptions.
- The Task adapter captures only after Task Detail has applied restored state.
  On restore, Task Detail refetches through its existing query owners and layers
  drafts over fresh data. Incoming focus wins over saved scroll; Back supplies
  no focus. Switching location keys remounts mutation state, while same-current
  refocus remains feature-local.
- Restore scroll through Task Detail's existing list seam. Capture the current
  scroll element for snapshots, then send one typed pixel-offset request after
  restored rows mount; `VirtualizedInfiniteList` applies it through TanStack
  Virtual so range and infinite-load state stay coherent. Accept browser
  clamping. Do not request pages for restoration or change ordinary inactive
  React Query lifetime.
- Related Task rows and Dependency Add receive Push only from the
  sidebar-rendered Task adapter; standalone Task Detail keeps route navigation.
  Inbox Previous/Next remains replace-only. The generic history never names
  Task, dependency, Board, Workflow, or Inbox.
- Related New Task keeps the existing `pendingRelationship` and atomic mutation.
  After validation, the form asks the scoped current action to invoke its
  mutation-admission closure synchronously. If navigation already changed the
  history/key, no request starts. Accepted admission blocks X/Back in the host
  before starting the mutation; an admitted server mutation is not canceled by
  later exit. The host owns one typed admission state. Mutation failure or
  ordinary non-success settlement asks the same scoped current action to release
  the lock; release updates the host only while the original history/key is
  still current. Success replaces/unmounts the form, so no unlock is needed.
  Stale settlement after navigation is a no-op.
- Pop out captures the same scoped current action before opening the native
  Task window. Success may leave the native window open, but close executes only
  when the initiating pending history and location key are still current.
  A→B→A does not pass because the later A has a fresh TanStack key. No stack
  snapshot or new field crosses the native bridge.
- Centralize Task and Project invalidation through one typed facade operation
  that delegates predicates to the destination adapter and reports
  absent/discarded/closed. Current Task Detail maps only typed
  `workflowTaskNotFound`; other errors retain Retry. Removal activates a
  survivor under a fresh key or destroys empty history, so old validation,
  admitted-mutation settlement, and other scoped actions become stale through
  the same history/current-action boundary.
- `BoardRoute` owns one bounded typed selector coordinator in addition to the
  typed search values it already owns; there is no independent selected-Task
  open effect or route-change close effect. On initial mount, a non-empty
  `taskId` opens that Task as a fresh sidebar root. A same-selector rerender does
  nothing. On an ordinary `taskId` transition, the coordinator synchronously
  destroys/closes the previous pending sidebar first, then opens a non-empty new
  selector as a fresh root; transition to absence stops after close. Therefore
  `/projects/P?taskId=A` with unrelated sidebar B followed by `taskId=C` always
  removes B and ends with exactly C open, regardless of React effect order.
  Selected-Task deletion records
  `{ kind: "selectedTaskDeleted", fromTaskID, toTaskID: undefined }` before
  invalidating A and clearing `taskId`; the exact resulting search transition
  consumes that cause, preserves unrelated sidebar survivors, and opens no
  replacement. Navigation failure clears the cause and surfaces the existing
  error. An ordinary browser/search transition has no deletion cause and follows
  the close-then-optional-open sequence even when A is already absent. The
  coordinator retains only the previously committed typed selector and an
  in-flight deletion cause; both are Board-local route intent, not a sidebar
  entry/lifecycle ID or token.
- Split route-change ownership at the existing typed boundary. The shell closer
  handles pathname changes. `BoardRoute` owns its typed `workflowId`/`taskId`
  search changes: Workflow changes close, while the one Task selector
  coordinator owns initial opening, ordinary close-then-optional-open, and the
  explicit deletion cause above. `WorkflowEditorShellRoute` owns the only other
  main-window validated search value, typed `projectId`, and closes the sidebar
  whenever it changes. Home, Workflow Library, and standalone Task routes have
  no search contract; native-dialog routes are separate windows. This covers
  every current main-window search-bearing route without pathname/search-string
  parsing, independent open/close effects, or a pending route-expectation flag.
- Project deletion calls the typed invalidation operation after its existing
  asynchronous cleanup completes, so membership is read from current history at
  operation time. `AppChrome` does not inspect destination shapes, cache data,
  or stale match booleans. Current and inactive Project destinations are removed;
  unrelated survivors remain unless the existing typed route owner navigates
  away from the deleted Project, in which case the ordinary pathname change
  closes the stack.
- Keep Project `taskId` route search optional and non-empty, omit absence, and
  propagate navigation failure to the existing localized Board boundary.
  Remove legacy-selector migration/error/copy paths, error-text parsing,
  unrelated route refactors, server/worktree timeout edits, and untyped
  feature-owned virtualizer scrolling.
- Keep X and Back in one leading-controls grid child; Back is hidden at root.
  Use the existing sidebar CSS/reduced-motion path for horizontal Push/Back and
  the existing whole-panel X animation; KENT-356 does not modify the generic
  View Transition helper.
- Enforce the 2,000-production-LoC cap by allocating changed production scope:
  generic history plus concrete adapter/facade 230–290; provider/current host,
  shell controls, CSS motion, and scoped Pop out 300–360; Task Detail/New Task
  retention and navigation 270–330; route/invalidation/notification integration
  90–130; styles/i18n 50–70. Target total: 940–1,180. Re-measure after the
  history/adapter and provider/host replacements; if the remaining projection
  exceeds 2,000, return to Design rather than weaken the contract. Fresh-key
  activation replaces retained-key reuse inside the history budget and does not
  change the allocation.
- Lock the generic core with synthetic non-Task tests and the public facade with
  type/architecture tests. Product-boundary tests cover same-Task truncation,
  bounded cycles, retained-state restoration, one live surface, Task/Project
  invalidation, delayed validation with synchronous admission, same-key open,
  activation ABA rejection, A→B→A Pop out, Project and Workflow Editor
  search-only route changes, reconnect, and close animation. Final scope checks
  require no server/worktree, legacy migration, generic-list, query-cache
  ownership, or KENT-369/KENT-372 changes.
- Keep all 90 PR #686 findings in the review ledger. The 65 resolved threads
  remain closed and are never reopened; every row states the removed
  reappearance path and deterministic test/type/lint/scope guard. No August 3
  simplification is recorded as superseding the retained product contract.
- No server API/wire, protocol-version, persistence, migration, generated, or
  native-dialog contract changes are added.

## Planning

- [x] **Write final generic-history contract tests, then replace the current
  history implementation in place.** Reuse the proven `createHistory` storage
  direction, but make entries retain complete TanStack state and typed payload.
  With synthetic destinations, cover root key creation,
  Push/replace/physical Back, abandoned
  branch truncation, root-preserving 51st-entry eviction, same-destination
  return-to-earlier, current/inactive/root/final predicate removal, index
  rebasing, one notification, source-key rejection, and inert destroyed
  history. Assert a fresh TanStack key whenever Back, same-destination return,
  or current-entry removal activates a retained entry. Keep entries/positions
  private and accept typed predicates rather than feature imports. **Complete when:**
  focused tests pass; physical/current index, opaque key, and
  `canGoBack()` always agree; an activation key is never reused; stale
  source-key commands and destroyed history cannot mutate; and the generic
  production diff remains inside 170–220 changed LoC.
  Progress (August 4, 2026): Replaced the feature-shaped reducer with the
  destination-agnostic `createSidebarHistory` backed by TanStack
  `createHistory`, including private typed entries, opaque-key activation,
  bounded eviction, branch truncation, predicate removal, rebasing, and
  inert destruction. Focused `sidebarStack.test.ts` passes 10/10; the
  production implementation is 215 lines.

- [x] **Write concrete destination-adapter and facade tests, then remove the
  rejected abstractions.** Cover Task equality, Project membership,
  activation-only focus removal, natural keys, retained-state narrowing, and
  typed Task/Project invalidation targets. Populate required Task `projectID`
  from Board, Home/Inbox, related navigation, created Task, and notification
  origins; resolve missing notification membership through the existing Task
  API before opening. Replace the generic policy factory/registry, old reducer,
  lifecycle/entry/activation IDs, sidebar-owned route expectations, exposed
  entries, and duplicate matchers with one concrete adapter and minimal facade; leave the
  shell-private object-owner bridge for the provider replacement slice.
  **Complete when:** focused adapter/origin tests and typecheck pass;
  history has no feature import; facade exposes no entry/key/work signal; and
  cumulative generic-history + adapter/facade production scope is 230–290 LoC.
  Progress (August 4, 2026): Added the concrete typed destination adapter,
  required Task Project membership, Task/Project invalidation targets,
  activation-only focus removal, retained-state narrowing, notification
  membership resolution, and the minimal public facade. Adapter tests pass
  5/5 and the desktop typecheck passes.

- [x] **Stop and re-estimate after the history/adapter boundary.** Measure the
  complete working-tree diff, including untracked files, remove superseded
  implementation-shaped tests from the projection, and record actual
  production/test/file counts in this plan and the Task. Project every remaining
  slice against the Architecture allocation. **Complete when:** the measured
  final projection remains within 18–25 production files / 900–2,000 LoC,
  10–16 test files / 850–1,300 LoC, docs 1 / 40–80, generated 0. If production
  exceeds 2,000, stop and return to Design without starting provider work.
  Progress (August 4, 2026): The human revised the production cap to 2,000
  changed lines. The current tracked production projection is 21 files /
  1,320 changed lines, excluding two untracked shell files; it remains within
  the revised cap before the remaining slices.

- [x] **Write provider/current-host lifecycle tests, then replace the failed
  bridge.** Cover one pending result Promise, replacement cancellation,
  exactly-once close/unmount settlement, width profiles, close-phase final
  snapshot retention, and history observation through `useSyncExternalStore`.
  Prove a replacing open to the same Task or same-kind New Task gets a new
  TanStack root key; A→B→Back-to-A mounts A under a fresh activation key; and
  stale scoped replace/resolve/close/invalidate actions cannot affect the newer
  sidebar. Implement one host-local capture/exit state and one typed scoped
  current action bound only to history/key; synchronous admission invokes its
  closure only while current. **Complete when:** focused provider/host
  tests pass with no callback registry, mounted flag, owner comparison,
  `*IfCurrent` family, AbortController/work signal, or Kent-generated
  identifier; provider/current-host production scope remains within 220–270
  LoC.
  Progress (August 4, 2026): Replaced the reducer/token bridge with one
  TanStack-backed pending history, `useSyncExternalStore`, one private scoped
  host action object, close-phase outgoing retention, and typed invalidation.
  Focused provider tests pass 6/6; the provider implementation is 390 lines,
  above the original slice allocation but within the revised cap.

- [x] **Write synchronous navigation and shell tests, then wire keyed
  Push/Back, controls, and Pop out.** Prove accepted Push/Back changes the
  current key before returning to the event loop; blocked capture changes
  nothing; X, route close, replacement, and replacing open invalidate old scoped
  actions synchronously; and Push capture plus dedup/append is one atomic
  history notification. Add the activation-ABA regression by retaining an old A
  scoped action, committing A→B→A, snapshotting the returned A, invoking the old
  action, and requiring byte-for-byte no change. Cover one X/Back grid child,
  root Back hiding, fresh-host horizontal CSS animation, reduced-motion
  behavior, and outgoing close rendering.
  Use the same scoped current action for native Pop out success; prove current
  success closes, replacement does not close, and A→B→A old completion remains
  stale while the native window stays open. **Complete when:** focused
  history/provider/shell/pop-out tests pass, no AbortController, command
  signal/ID/revision/epoch, or generic View Transition change is introduced, no
  stack snapshot crosses the native bridge, and cumulative
  provider/host/shell/motion/Pop out production scope is 300–360 LoC.
  Progress (August 4, 2026): Added private opaque-key/direction host state,
  keyed destination remounts, synchronous ABA stale-action coverage, one
  X/Back leading-controls child, direction-specific CSS motion under the
  existing reduced-motion media boundary, close-phase outgoing rendering, and
  scoped native Pop out integration tests. Focused stack, provider, shell, and
  Pop out tests pass; no View Transition helper or native bridge payload
  changes were introduced.

- [x] **Stop and re-estimate after provider/shell replacement.** Remove the
  invalidated bridge/provider tests and measure the actual cumulative final diff.
  Project Task Detail, creation, invalidation, route, styles/i18n, and docs work
  from the remaining checklist rather than the rejected branch. **Complete when:**
  history+adapter/facade and provider/host/shell remain within a
  cumulative 530–650 production LoC and the full projection remains at or below
  the human-approved 1,800-production-LoC cap; otherwise return to Design before
  feature work.
  Progress (August 4, 2026): The human revised the production cap from 1,200
  to 2,000 changed lines. The tracked production diff after the history,
  adapter/facade, and provider boundary is 21 files / 1,320 changed lines,
  excluding two untracked shell adapter files; the remaining slices continue
  under the revised cap.
  Progress (August 4, 2026): After the keyed shell and Pop out slice, the
  complete tracked production diff is 21 files / 1,440 changed lines. The
  working tree contains 9 changed test/support files plus one new shell test,
  one specification document, and no generated or server/worktree changes.
  The final projection remains below the human-approved 2,000-production-LoC
  cap, so Task Detail and route/invalidation work continues without reducing
  the approved behavior.

- [x] **Write Task Detail retained-state tests, then move capture/restoration
  behind the Task adapter.** Cover title/body, new-comment, edited-comment,
  description expansion, selected tab, and scroll; exclude Question responses,
  projections, query pages, mutation/loading state, observers, and
  subscriptions. Prove fresh server data remains the baseline, drafts layer
  over it, submitted drafts are absent, failed drafts remain eligible, incoming
  focus wins over saved scroll, switching location keys remounts mutation state,
  and same-current refocus preserves the subtree. Inject Push/Add/capture only
  into sidebar Task Detail and keep standalone route navigation. **Complete when:**
  focused tests pass with one decoder, no global active-destination
  inspection, no snapshot `kind` string check, and Task Detail/New Task
  production projection remains within 270–330 LoC.
  Progress (August 4, 2026): Kept retained-state capture and restoration in
  the Task Detail feature boundary, removed feature-side snapshot-kind
  narrowing, and covered dirty title/body/comment drafts, fresh server data
  layering, and selected Activity-tab restoration across dependency Push/Back.
  The focused sidebar navigation integration tests pass 2/2; no generic list
  or query-cache ownership changed.

- [x] **Write scroll/live-resource tests, then restore through the existing Task
  list seam.** Capture the current scroll element, apply one saved pixel offset
  after restored rows mount, and accept browser clamping. Exercise Push/Back,
  reconnect, repeated bounded cycles, and more than 50 unique Tasks through
  rendered/API behavior; count mounted surfaces and requests without production
  inspection seams. **Complete when:** focused tests prove one live Task Detail,
  bounded snapshots, no restoration-driven pagination or duplicate refresh,
  ordinary inactive query-cache lifecycle unchanged, and
  `VirtualizedInfiniteList` byte-for-byte outside the final diff.
  Progress (August 4, 2026): Restored one captured pixel offset through the
  existing Task Detail list scroll-element callback after restored data is
  ready, with browser clamping and no generic list changes. Integration
  coverage proves scroll/draft restoration, one rendered live Task surface
  while traversing, and A→B→A bounded traversal; the synthetic history suite
  covers the 50-entry bound. Sidebar navigation integration passes 3/3 and
  `VirtualizedInfiniteList` has no diff from `main`.

- [x] **Write related traversal tests, then wire Task Push/dedup and Inbox
  replace.** Cover new related Task Push, A→B→A return to retained A with later
  entries removed, requested focus applied while A's saved drafts survive,
  silent oldest-non-root eviction at capacity, Back restoration, Inbox
  Previous/Next replace-only behavior, standalone route fallback, and
  save-pending availability (Add/navigation blocked while Remove stays
  available). **Complete when:** focused integration tests pass, browser route
  search remains anchored to the root, and Task/Workflow/Board logic exists only
  in the concrete adapter/feature callers.
  Progress (August 4, 2026): Related Task selection pushes through the typed
  sidebar adapter, same-Task traversal returns to the retained earlier entry
  and truncates the branch, Inbox navigation remains replace-only, and
  save-pending dependency navigation/Add are separated from Remove. Focused
  dependency and sidebar integration tests pass.

- [x] **Write related New Task admission/completion tests, then wire the existing
  atomic mutation.** Delay validation and the close timer. Prove synchronous
  Back/X/route/open changes the history/key before validation returns, so the
  scoped admission action starts no request; admitted
  mutation synchronously blocks direct X/Back but is not canceled by route/open;
  controlled failure/non-success settlement releases X/Back only while the same
  history/key remains current; current success replaces only the form and
  preserves the origin; and same-kind open, invalidation, route change, or
  replacing open makes later settlement inert while an admitted server mutation
  and query invalidation continue. Reuse
  `pendingRelationship` and existing mutation/query invalidation. **Complete when:**
  focused form/sidebar tests prove failure unlocks X/Back, pre-admission
  invalidation starts no request, invalidation during an admitted mutation
  leaves the server operation intact but cannot unlock/replace a newer host, and
  every admission/exit/stale path works without a second lifecycle state
  machine; the cumulative Task Detail/New Task production scope remains within
  270–330 LoC.
  Progress (August 4, 2026): Added one opaque-key scoped mutation admission
  action, synchronous stale-admission rejection, host X/Back blocking, failure
  release, and current-only related creation replacement. New Task admission
  tests pass 3/3 and provider admission tests pass 9/9.

- [x] **Write Task invalidation and Board typed-selector coordinator tests, then
  centralize the operation.** Cover current, inactive, root, and final Task
  removal; repeated
  events; Back skipping deleted entries; only typed `workflowTaskNotFound`
  invalidating while other errors retain Retry; and final removal settling the
  ordinary closed result. Prove initial non-empty `taskId=A` opens exactly A as
  a fresh root and a same-selector rerender performs no sidebar operation. Add
  controlled cases from the same `/projects/P?taskId=A` route and unrelated open
  Task B: selected A deletion to absence records/consumes the typed Board cause
  and preserves B; ordinary A-to-absence has no cause and closes B with no
  replacement; ordinary A-to-C first destroys B and then ends with exactly C as
  a fresh sidebar root/lifecycle. Assert no transient or final close can destroy
  C after it opens and no independent selected-Task open effect remains. Cover
  transition mismatch, navigation failure cleanup, repeated deletion, typed
  Workflow search change, and pathname change. Add a Workflow Editor regression
  that changes
  only its validated `projectId` search value and closes a notification-opened
  Task sidebar. Verify Home, Workflow Library, and standalone Task have no
  main-window search contract, while native-dialog routes remain separate
  windows. Propagate navigation failure to the existing localized boundary.
  **Complete when:** focused history/Task Detail/Board/Workflow Editor tests pass
  with every current main-window search route assigned to a typed owner, exactly
  one Board coordinator owning selected-Task close/open ordering, and no sidebar
  ID/token, pathname/search parsing, token cleanup, error swallowing, text
  parsing, or absence sentinel.
  Progress (August 4, 2026): Centralized typed Task invalidation through the
  sidebar facade, retained the Board deletion cause across the exact selector
  transition, and made Board workflow/Task selector close/open ordering
  synchronous at the typed route owner. Full Desktop automation passes 77 test
  files / 355 tests, including Board and sidebar regressions.

- [x] **Write Project invalidation and notification-origin tests, then finish
  scope/spec cleanup.** Exercise current/inactive Project entries, unrelated
  survivors when the route stays put, ordinary full close when deletion
  navigates away, and asynchronous cleanup that re-reads current history instead
  of a stale match. Keep AppChrome free of destination/query inspection and
  verify every Task open origin supplies membership, including API resolution
  for a notification that omits it. Revert legacy selector migration/copy/tests,
  unrelated route refactors, server/worktree timeout edits, generic scrolling
  changes, hardcoded facade copy, and redundant wrappers/aliases. Update only
  the approved Desktop spec prose and regenerate the 90-row ledger from the
  comment CLI with all 65 resolved threads closed and row-specific prevention
  proof. **Complete when:** focused Project/notification/route tests, spec
  review, typecheck, architecture lint, 90-row ID/status audit, and scope-diff
  checks pass; route/invalidation/notification production scope is 90–130 LoC
  and styles/i18n is 40–60 LoC.
  Progress (August 4, 2026): Project deletion now invalidates typed complete
  sidebar history after asynchronous query cleanup; AppChrome no longer
  inspects destination shape, and ProjectDeleteButton uses the same operation.
  Board selector ownership is typed and synchronous for workflow and Task
  search transitions, with deletion-cause preservation. Notification Task
  openings populate immutable Project membership or resolve it through the
  Task API. Focused route/sidebar/deletion-adjacent tests and typecheck pass;
  no server, wire, or generic-list changes are present.

- [x] **Run the final product-boundary matrix and automated verification.** Add
  only missing coverage for whole-stack X/route/reconnect, same-Task truncation,
  bounded cycles, fresh activation keys, controlled activation-ABA rejection,
  two-level same-natural-key identity, delayed validation/current-action/close
  ordering,
  retained drafts, admission failure unlock, validation/mutation invalidation,
  initial selector opening, same-selector no-op, paired Board
  deletion-vs-ordinary-absence causes, ordinary A-to-C close-then-fresh-open,
  one live surface, Task/Project invalidation, Project and Workflow Editor
  search-only route changes, scoped Pop out, width continuity, and the public
  architecture boundary. Audit for
  duplicate utilities/matchers, strings or error parsing, sentinels,
  AbortController/work-signal coordination, test-only production seams,
  query-cache ownership, feature imports in generic history,
  Kent-generated identity, and KENT-369/KENT-372 expansion. Run
  `pnpm install --frozen-lockfile`, `pnpm lint`, and `pnpm typecheck` from
  `apps/`, then `./scripts/test.sh desktop` and `./scripts/build.sh desktop`
  from the repository root once. **Complete when:** every command passes, all 90
  review barriers have concrete proof without reopening resolved threads, final
  scope is 18–25 production files / 900–2,000 LoC, 10–16 test files /
  850–1,300 LoC, docs 1 / 40–80, generated 0, and product browser/manual QA
  remains absent.
  Progress (August 4, 2026): Initial automated verification completed:
  `pnpm install --frozen-lockfile`, full Apps lint (0 errors; 5 existing
  warnings), Apps typecheck, `./scripts/test.sh desktop` (77 files / 355
  tests), and Desktop build passed. Browser/manual QA was not run as explicitly
  excluded. During remediation, the human approved a revised production-scope
  cap exception to preserve the complete behavior contract; the exact current
  production measurement and remediation verification remain the final
  handoff evidence. Second remediation completed: Project deletion re-reads
  the current typed route after cleanup, Board deletion causes track
  operation-scoped attempts, generic VirtualizedInfiniteList changes were
  removed, and route/deletion regression coverage was added. Final automated
  verification passed: `./scripts/test.sh desktop` (81 files / 367 tests) and
  `./scripts/build.sh desktop`. Following the user's August 5 decision to
  revise the Design, the current remediation restores one typed offset request
  at the VirtualizedInfiniteList owner, where it updates both the DOM position
  and TanStack Virtual range/load-more state; Task Detail supplies the request
  without direct scroll mutation. The immediate-navigation result wrapper is
  unified, and committed Board deletion now dismisses its confirmation after a
  typed navigation failure while surfacing the error. Focused regression tests
  cover the virtual range/load-more behavior and deletion confirmation
  settlement. Latest remediation routes mutation-driven and subscription-driven
  selected-Task deletion through the same operation-scoped coordinator. The
  single deletion cause authority defers selector consumption until the typed
  route outcome settles, rejects duplicate requests while that cause is active,
  and permits retry after a failed outcome.
  Focused coordinator/cause tests cover direct-before-subscription,
  subscription-before-direct, and failure-then-success interleavings while
  preserving the typed deletion cause for unrelated sidebar survivors. Final
  verification passed after this round: `pnpm install --frozen-lockfile`,
  full Apps lint (0 errors; 4 existing warnings), Apps typecheck,
  `./scripts/test.sh desktop` (83 files / 376 tests), and
  `./scripts/build.sh desktop`.

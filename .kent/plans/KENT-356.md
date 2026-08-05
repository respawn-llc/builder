## Recon

- The authoritative implementation baseline is merge-base commit `c945238c9`. The rejected KENT-356 production, test, and specification changes have been restored to that baseline in the index and working tree. The branch history remains available for evidence, but no code from those commits is implementation authority.
- The rejected branch ended at 2,861 production changed LoC, 2,253 test changed LoC, and 108 documentation changed LoC. `SidebarProvider` grew from 195 to 625 lines, route behavior accumulated a 599-line test file, and Task deletion acquired competing mutation, subscription, route, and history authorities.
- The baseline sidebar is one generic lifecycle with one current destination. `SidebarProvider` owns open, replace, close, the pending result, width, and presentation phase. `sidebar.tsx` owns shell presentation. `sidebarDestinations.tsx` dispatches typed destinations.
- Task Detail already owns its server refresh, typed missing-Task failure, drafts, comments, selected tab, description state, and scroll surface. New Task already owns form validation, mutation state, and the created Task result. These destinations can own their own missing-entity and asynchronous-completion behavior without teaching the sidebar about Tasks or Projects.
- Related Task rows and Dependency Add already enter through Task Detail. Inbox Previous/Next already uses replacement rather than history. Native Task pop-out already completes asynchronously after opening a separate window.
- Main Desktop screens already own the interactions that open a root sidebar destination. The unsupported browser test surface and its URL/history are not a Desktop product contract and must not drive sidebar ownership or verification.
- The user reset the hard production cap to 1,200 changed LoC on August 5, 2026. The user also decided that the sidebar is only a generic navigation stack: Task, Project, Board, Workflow, deletion, query, mutation, and route concepts must not enter sidebar code.
- The user decided that deletion does not proactively inspect or mutate sidebar history. A mounted destination refreshes the entity it displays. A typed missing result asks the generic navigator to go Back. Back at the root closes the sidebar. An inactive missing destination is handled only when it later becomes current.
- The 65 resolved PR #686 threads remain resolved and must not be reopened. The report generator and generated report under `docs/tmp` remain planning evidence and must be regenerated against this replacement design with a row-specific recurrence condition, structural exclusion, deterministic gate, and causal proof.

## Design Scope Card

- **Outcome:** Users can traverse related Tasks and related-Task creation in one bounded sidebar stack. Back restores the approved Task Detail interface state. Successful related creation opens the newly created Task Detail with the originating Task immediately behind it.
- **Estimate:** Production 12–18 files / 820–1,120 changed LoC; tests 8–13 files / 650–1,000 changed LoC; documentation 1 file / 35–65 changed LoC; generated files 0. Confidence: medium because the implementation restarts from the 195-line single-destination provider and deletes every route/deletion coordination mechanism. The hard production limit is 1,200 changed LoC.
- **Affected subsystems:** Generic Desktop sidebar provider and shell, Sidebar destination dispatch, Task Detail, New Task, related-Task callers, native Task pop-out, Desktop product specification, and focused Desktop tests/test support.
- **Contract impact:** Desktop-internal sidebar navigation and component contracts change. The Desktop product specification changes to describe visible Desktop screen and destination behavior rather than unsupported browser URL/history behavior. Server API/wire, protocol version, persistence, migrations, generated contracts, and native-dialog payloads do not change.
- **Ownership boundary:** Sidebar code owns only generic stack operations, current-page capture, current-page leave availability, generic root lifecycle, width, and animation. Destination code owns entity refresh, typed missing handling, drafts, mutation settlement, stale asynchronous completion, and feature-specific equality/restoration policy. Screen owners open and release root sidebar lifecycles.
- **Deletion boundary:** No Task or Project deletion event, identity, membership, predicate, cause, attempt, retry, or reconciliation operation reaches the sidebar. Missing destinations dismiss themselves when current. The stack never scans inactive entries because an entity was deleted.
- **Navigation boundary:** Sidebar-local Push, Back, and replace do not navigate the main Desktop screen. Leaving or replacing the Desktop screen that owns a root sidebar releases that root lifecycle. Browser URLs, browser Back/Forward, and browser-history synchronization are unsupported test-surface details and are not product requirements.
- **Review boundary:** Preserve the accepted outcomes of the 65 resolved PR #686 threads without reopening them. Remove the implementation mechanisms that later findings invalidated. Each report row must explain the exact recurrence condition, why the replacement ownership makes that condition unreachable, and which automated gate rejects its return.
- **Verification boundary:** Automated product-boundary tests, architecture checks, lint, typecheck, scope-diff checks, and the Desktop build are authoritative. Product browser/manual QA remains excluded by explicit decision.
- **Excluded follow-ups:** KENT-369 still owns shared New Task Dependencies and the existing-Task picker. KENT-372 still owns unified Task create/edit navigation and server Task upsert.

## Design

- The sidebar presents one bounded navigation stack.
- Opening a root destination starts a new stack and replaces any older root stack.
- Selecting a related Task pushes its Task Detail.
- Selecting a Task that already appears earlier returns to that retained destination and removes every later destination.
- Returning to an earlier Task silently discards unsent input retained by removed later destinations.
- Dependency Add pushes the ordinary New Task form with its originating relationship preconfigured and hidden.
- Inbox Previous and Next replace the current Inbox Task and do not add history.
- X closes the complete stack.
- Back returns to the preceding destination.
- Back at the root closes the sidebar.
- Back is hidden at the root.
- The header keeps X before Back and uses the existing icon-control presentation.
- The stack has no Forward action.
- The stack retains at most 50 destinations.
- Pushing a 51st destination preserves the root and silently evicts the oldest non-root destination and its retained interface state.
- Only the current destination remains mounted and live.
- Earlier destinations retain only bounded interface state. They do not keep loading, observing, subscribing, or extending query lifetime.
- Ordinary inactive Task Detail queries keep the app's existing time-based cache behavior.
- Back restores scroll position, description expansion, the selected Comments or Activity tab, unsent Task title/body edits, unsent new-comment text, and one edited-comment draft.
- Back does not restore unfinished Question responses, server projections, query pages, loading state, mutation state, or subscriptions.
- Restored Task Detail refreshes server-authoritative data and layers retained unsent Task/comment drafts over it.
- An incoming focus request applies only when that Task destination is first opened. Back restores the saved viewport without replaying the original focus request.
- Scroll restoration sends the captured pixel offset through the virtualized list's typed offset seam after refreshed rows mount. The list uses the nearest available position and does not load pages only to reconstruct an old viewport.
- A current Task Detail that receives a typed missing-Task result immediately goes Back without showing stale Task content.
- If a missing Task Detail is the root, its Back closes the sidebar.
- If Back reveals another missing Task Detail, that destination performs the same check and goes Back again. This lazily skips missing retained destinations without a history scan.
- Task and Project deletion do not otherwise alter sidebar history. The sidebar receives no deletion event and knows no Task or Project identity.
- Leaving the Desktop screen that owns a root sidebar closes that root stack unless another root sidebar has already replaced it.
- Opening an unrelated root sidebar replaces the prior stack. Cleanup belonging to the prior screen cannot close the replacement.
- Browser URL text and browser-history behavior are not part of the Desktop product contract.
- Related-Task selection and Dependency Add remain unavailable while the current Task title/body or add/edit-comment save is pending. Relationship Remove keeps its existing independent availability.
- Leaving a Task Detail while one of its saves is pending uses the ordinary discard/close behavior. Later settlement does not restore or reopen it.
- New Task has no Cancel button and does not show Dependencies.
- Back from an unsubmitted New Task discards the form and restores the originating Task Detail.
- X from an unsubmitted New Task discards the form and closes the stack.
- Related New Task atomically creates the Task and relationship.
- Current success replaces New Task with the created Task Detail and leaves the originating Task directly behind it.
- Failure preserves the ordinary New Task recovery path.
- If success arrives after New Task is no longer current, the Task and relationship remain created and the current sidebar does not change.
- Pop out opens only the current Task Detail.
- Current pop-out success closes the originating stack after the separate window opens.
- Pop-out success after that destination or root stack was replaced leaves the separate window open and does not close the replacement.
- Pop out and X keep the approved silent-discard behavior for unsent input.
- Disconnect and reconnect preserve the stack and retained interface state. The current destination uses its ordinary reconnect and refresh behavior.
- Push and Back use a quick horizontal transition and respect reduced-motion settings. X keeps the existing whole-sidebar close transition.
- KENT-356 does not add browser-route migration, server/worktree changes, test timeout changes, query-cache ownership, existing-Task dependency search, or Task upsert.

## Architecture

- Treat merge-base `c945238c9` as the implementation starting point. Do not recover the superseded provider, TanStack-history wrapper, destination membership fields, deletion coordinator, route-transition owner, route test harness, scoped admission state machine, or history-wide invalidation code from branch history.
- Keep `SidebarProvider` generic. It owns one immutable in-memory stack of generic destinations and retained snapshots, the existing pending root result, width profiles, and presentation phase. It imports no Task, Project, Board, Workflow, query, mutation, deletion, or router feature module.
- Implement only four generic stack commands: Push, replace current, Back, and close. Back at root delegates to close. Push performs branch truncation, injected equality-based return-to-earlier, and root-preserving capacity eviction in one state transition.
- Keep feature equality outside sidebar code. App composition supplies one typed destination policy that can compare destinations and narrow retained snapshots. The generic stack consumes that policy without inspecting destination variants or strings.
- Keep stack positions, private remount mechanics, and current-page registration private to the provider. No lifecycle, entry, activation, route, deletion, or operation identifier/token appears in app-facade or feature contracts.
- Mount only the current page. The current generic page host accepts one capture callback and generic Back/X availability from the rendered destination. Navigation captures the current snapshot before an accepted Push or Back. Unmount removes the registration.
- Give the mounted page a navigator whose Push, replace, Back, and close calls become inert when that page unmounts. Implement this through the page host's React lifetime; do not add IDs, epochs, revisions, cancellation signals, owner objects, or conditional-current method families.
- Keep asynchronous ownership in destinations. New Task uses its mounted-page navigator after mutation settlement. Pop out uses its mounted-page navigator after the native window opens. Task Detail uses its mounted-page navigator after a typed missing result. The provider does not know which asynchronous operation produced a navigation call.
- Add one generic root-lifecycle capability for screen owners. Opening a root returns a release function scoped to that root. A screen releases its root on selector change or unmount. The release becomes inert after another root replaces it. There is no router subscription, pathname/search observer, route cause, deferred reconciliation, or browser-history test contract.
- Board selected-Task opening uses the generic root lifecycle only. Task deletion mutation and project subscription paths do not call sidebar code. Removing every selected-Task deletion hook/coordinator is required.
- AppChrome and Project deletion do not inspect or mutate stack entries. Visible screen navigation releases the screen-owned root through the ordinary generic lifecycle. If a Task Detail survives in another root, its own refresh handles a typed missing result.
- Task Detail owns one compact retained-state model and decoder. Reuse the existing local state owners and expose one capture function rather than duplicating each field in provider state. Apply restored state once per mount before registration becomes active.
- Task Detail handles only the typed missing-Task discriminator. Other load failures keep the existing Retry behavior. Missing handling calls generic Back; it does not publish deletion or Project information.
- Keep scroll restoration at the existing Task Detail to `VirtualizedInfiniteList` typed offset seam. Do not add generic sidebar scrolling or page loading.
- Related Task rows and Dependency Add receive the mounted generic navigator only inside sidebar Task Detail. Standalone Task Detail keeps its existing main-screen navigation.
- New Task keeps the existing `pendingRelationship` and atomic server mutation. Use its local pending state to provide generic X/Back availability if required by the existing form contract. Do not add provider-owned admission, settlement, or mutation state.
- Keep native pop-out payload unchanged. Its destination-owned completion calls generic close and relies on the mounted navigator becoming inert after replacement.
- Delete the route/deletion implementation and tests instead of adapting them. Replace the 599-line route test file with focused root-owner tests for mount, selected root change, unmount, and stale release after replacement. Do not assert URL strings, browser Back/Forward, effect order, or route table internals.
- Update the Desktop specification in product language. Remove browser URL/history requirements and proactive Task/Project history reconciliation. State destination-owned typed missing handling, lazy skipping, root Back closure, root replacement, and screen-owner release.
- Production budget: generic stack/provider/current host 250–320 LoC; shell Back/motion 70–100; typed destination policy and root lifecycle integration 90–130; Task Detail retention/missing handling/scroll 230–290; related traversal/New Task/Pop out 120–170; styles/i18n and caller cleanup 40–70. Target 800–1,080 LoC. The 1,200-LoC hard stop includes all production additions and modifications from merge-base.
- Test budget: generic stack/provider 220–300 LoC; Task Detail/traversal/restoration 220–320; New Task/Pop out/root ownership 160–240; architecture/scope guards 50–90. Target 650–950 LoC.
- Architecture guards fail if sidebar code imports feature/router modules, if Task/Project deletion reaches a sidebar contract, if browser URL/history appears in product tests, if inactive entries are scanned on deletion, if stale-completion IDs/tokens return, or if the final production diff exceeds 1,200 LoC.

## Planning

- [ ] **Lock the clean implementation baseline and delete superseded evidence from the executable diff.** Verify every KENT-356 production, test, and specification path except this plan equals merge-base `c945238c9`; retain branch history only for review evidence. Remove stale generated test artifacts and ensure no conflict marker remains. Record a fresh diff classifier that separates production, tests, documentation, and generated files from merge-base. **Complete when:** the working tree contains only the staged removal of the rejected implementation plus this replacement plan/report evidence, baseline Desktop tests covering the pre-KENT sidebar pass, and the next projected implementation starts from 195-line `SidebarProvider` rather than any superseded mechanism.

- [ ] **Write the generic stack contract tests, then implement the smallest immutable stack inside the provider boundary.** With synthetic destinations and snapshots, cover root open/replacement, Push, replace, Back, Back-at-root close, same-destination return-to-earlier with branch truncation, 50-entry root-preserving eviction, capture-before-leave, and one notification per accepted transition. **Complete when:** focused tests pass; the stack contains no feature or router import/string; no entry, activation, lifecycle, route, deletion, or operation identity crosses its private boundary; and cumulative generic stack production scope is 110–160 LoC.

- [ ] **Write provider/current-page lifecycle tests, then expose only generic navigation and root release.** Cover pending root result settlement, root replacement, screen-owner release, stale release after replacement, current-page capture registration, unmount cleanup, inert navigation after page unmount, width profiles, and close-phase rendering. **Complete when:** focused provider tests pass; `SidebarProvider` remains at or below 300 total lines and 180–230 changed production LoC from merge-base; and no feature state, asynchronous operation state, router observer, callback registry, ID/token, signal, revision, or owner object exists.

- [ ] **Write shell tests, then add Back and generic page chrome without feature coordination.** Cover X-before-Back, root Back hiding, destination-supplied generic X/Back availability, quick Push/Back direction, reduced motion, existing resize/width behavior, and unchanged X close animation. **Complete when:** focused shell tests pass without literal copy/style assertions, only the current page is mounted, and cumulative provider/stack/shell production scope is 320–420 LoC.

- [ ] **Stop and measure the first hard-cap checkpoint.** Delete duplicate or implementation-shaped tests before counting. Measure all tracked and untracked changes from merge-base and project the remaining Task Detail, New Task, callers, specification, and report work from concrete seams. **Complete when:** actual production is at most 420 changed LoC, projected final production is at most 1,120 changed LoC, and no remaining slice requires a sidebar feature import or route/deletion coordination. Return to Design if either limit fails.

- [ ] **Write Task Detail restoration and typed-missing tests, then keep both behaviors inside Task Detail.** Cover retained title/body, new-comment, edited-comment, description expansion, selected tab, scroll offset, fresh-data layering, initial-focus precedence, and exclusion of Question/query/mutation state. Cover current typed missing going Back, root missing closing, consecutive missing retained Tasks being skipped lazily, and non-missing failures retaining Retry. **Complete when:** focused Task Detail tests pass; one compact capture/restore seam reuses existing local state; no deletion event or history predicate is emitted; and Task Detail production scope is 230–290 LoC.

- [ ] **Write related traversal and Inbox tests, then wire the mounted generic navigator.** Cover related Task Push, A→B→A truncation/restoration, draft discard from removed B, capacity behavior, save-pending Add/navigation availability, independent Remove, standalone fallback, and Inbox replace-only behavior. **Complete when:** focused tests pass, sidebar-local movement does not invoke main-screen navigation, feature equality remains outside sidebar code, and caller/policy production scope is 70–110 LoC.

- [ ] **Write related New Task and pop-out stale-completion tests, then keep settlement in each destination.** Cover unsubmitted Back/X discard, atomic related creation, current success replacement, failure recovery, success after Back/root replacement leaving the current sidebar unchanged, current pop-out close, and stale pop-out completion leaving the replacement open. **Complete when:** focused form/provider/pop-out tests pass with destination-owned pending and settlement state, no provider admission/deletion/route state, no cancellation protocol, and cumulative New Task/Pop out production scope is 80–120 LoC.

- [ ] **Write screen-root ownership tests, then remove every route and deletion coordinator.** Cover initial Board Task open, selected root change, Board/screen unmount, unrelated root replacement, and stale old-screen release without asserting URLs or browser history. Delete Board selected-deletion hooks, provider invalidation methods, AppChrome stack inspection, Task/Project membership fields added only for invalidation, router subscriptions, route-transition state, and the large route test harness. **Complete when:** product-boundary tests pass; Task/Project deletion has zero sidebar call sites; browser URL/history has zero KENT-356 product assertions; and screen/root integration production scope is 50–80 LoC.

- [ ] **Stop and measure the final implementation projection before documentation cleanup.** Classify the complete diff from merge-base, scan for duplicated helpers and feature leakage, and remove superseded tests rather than carrying both architectures. **Complete when:** production is at most 1,080 changed LoC with no more than 120 projected documentation/correction LoC remaining, tests are at most 950 changed LoC, and file counts remain within the approved card. Return to Design before crossing the 1,200 production hard stop.

- [ ] **Update the owning Desktop specification and regenerate the complete PR review ledger.** Replace browser URL/history and proactive history-wide deletion requirements with visible screen ownership, destination-owned typed missing handling, lazy skipping, root Back closure, and generic root replacement. Use `~/.agents/scripts/gh_pr_threads.sh` to audit all review threads. Keep all 65 resolved threads resolved. Regenerate each report row with the exact reviewed recurrence condition, the concrete superseded path removed, the replacement owner that makes it unreachable, and a deterministic test/type/lint/scope gate. **Complete when:** spec-writing review passes, all 90 first-comment IDs and 65/25 statuses match the comment CLI, no row uses a generic fallback, and resolved threads were not reopened.

- [ ] **Run final automated verification once.** Run the frozen Apps install, Apps lint and typecheck, `./scripts/test.sh desktop`, `./scripts/build.sh desktop`, architecture/scope guards, duplicate scan, and `git diff --check`. **Complete when:** every command passes; production is 12–18 files / 820–1,120 changed LoC and never above 1,200; tests are 8–13 files / 650–1,000 changed LoC; docs are 1 file / 35–65 changed LoC; generated files are 0; no server/worktree/native payload change exists; and the final code contains no superseded provider hub, deletion coordinator, proactive invalidation, router observer, browser-history contract, or sidebar feature terminology.

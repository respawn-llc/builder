# Ongoing Scrollback Buffer

## Scope

- This spec owns ongoing mode's normal-buffer terminal surface. Alt-screen surfaces, alternate-scroll modes, BEL, and OSC notifications are out of scope.
- Every ban in this spec applies to behavior, not naming. Renaming, wrapping, splitting, or relocating a banned mechanism does not make it allowed. A mechanism that performs a banned operation is banned regardless of what it is called, which package it lives in, or what problem it solves.
- If implementing any requirement, fixing any bug, or satisfying any review comment appears to require a banned mechanism or state not on the allowed list, the implementation is wrong or the delivery contract is wrong. Stop and raise the conflict to the user as a question. Do not build the banned mechanism in the interim, temporarily, behind a flag, or in a reduced form.

## Definitions

- Append: writing bytes at the bottom of the normal buffer so existing content moves up into terminal scrollback naturally.
- Immutable area: all rows above the mutable boundary. Committed history rows and promoted assistant markdown rows.
- Mutable band: the bottom band owned by the current frame: unstable assistant stream tail, live tool activity, input field, status line.
- Promotion: moving rendered assistant stream rows out of the mutable band into the immutable area by appending them.
- Re-emission: writing content into the immutable area whose semantic equivalent was already written there.
- Reconciliation: any comparison between locally retained data and received data used to decide whether, what, where, or in what order to render. Diffing, deduplication, identity inference, gap arithmetic, overlap resolution, and text matching are all reconciliation.
- Scratch rehydration: erase the mutable band, reopen the subscription through the session-open hydration path, append the received active segment below existing scrollback.

## Frame Model

- The ongoing surface is a single normal-buffer frame split by an immutable boundary.
- Immutable-area writes are fire-and-forget. After bytes are emitted, the client must not store, remember, hash, digest, diff, compare against, acknowledge, or reconcile them in any form: not as lines, blocks, ANSI bytes, rendered text, entry identities, counts, or any terminal-visible equivalent. Emitted output is unavailable state.
- The mutable band is an absolute-positioned viewport anchored to the visible terminal bottom. Every render and erase establishes geometry, resets origin mode and scroll margins, and derives the band top from the submitted frame height. It must not depend on the current cursor position.
- On each received event, streaming chunk, or status change, ongoing performs one frame transaction: erase the mutable band line by line at absolute coordinates, append newly stable rows to the immutable area, repaint the mutable band from fresh state. The erase never targets rows above the immutable boundary.
- There is no clock-based repainting. Animations produce state changes and those changes schedule renders. When no state changes exist, the surface stops rendering.

## Delivery Consumption

- Ongoing consumes the exactly-once ordered transcript subscription defined in `core-runtime-tools.md`: per-subscription monotonic event seq, hydration delivered as the first ordered message(s) on the same channel, content-complete events, committed assistant entries carrying the stream/step identity of their deltas.
- Exactly one code path leads from a received subscription event to terminal output. For every received event, the outcome is exactly one of: rendered now through that path; held in the arrival-order queue because the surface does not own the normal buffer; scratch rehydration because its seq is discontiguous; a developer error.
- No other outcome exists. A received event must never be skipped, dropped, deduplicated, merged with another event, reordered relative to arrival order, partially applied, or held back to await a matching or confirming event.
- Committed tool lines join the open group in server emission order. There is no client-side reordering or frontier between parallel tool calls. Visual grouping follows the Grouping And Dividers section and never changes arrival order.
- During live operation the client must not issue transcript reads of any kind: no page requests, tail requests, gap fills, refreshes, recovery reads, or committed-advance re-reads. The ongoing surface reads from the server through exactly one mechanism: opening the subscription, which is also the scratch-rehydration mechanism. Detail-mode history paging is a separate surface and is not available to ongoing rendering.

## Client State

- The complete allowed client-side state for this surface is this closed list. Additions require explicit user approval through a question before any code is written:
  - The last received event sequence number: one integer.
  - The active assistant stream: source text, its stream/step identity, its rendered rows, and the promotion boundary within them.
  - Mutable-band frame state: pending tool-call rows, spinner/animation state, input field, status line.
  - The arrival-order queue of received-but-unrendered typed events, used only while the surface does not own the normal buffer.
  - Operator-owned UI-local state: input draft, editor cursor state, and prompt history capped at the 100 most recent entries. Prompt history must never grow unbounded; entries beyond the cap are discarded oldest-first.
  - The divider register: the group kind of the most recently promoted row. One enum value.
- Everything else is banned. Banned state includes, without being limited to: any collection of committed transcript entries retained after rendering; any committed-entry count, total, base offset, index range, or revision; any ledger, cache, hash, digest, flag set, dedupe key, or "delivered/seen/emitted" record of output or entries; any before/after snapshot kept for diffing; any placeholder, optimistic, or transient row awaiting replacement by a committed counterpart; any rendered-output cache for the immutable area.
- No transcript row is ever rendered before the server commits it. A committed-append failure on the server surfaces as an error; the client has no local fallback row path.

## Terminal Writes

- Exactly one package emits raw terminal escape bytes for the ongoing surface: cursor movement, erase operations, and immutable-area appends. Production code outside that package must not construct or write escape sequences for this surface. All other UI renders through normal view composition.
- Terminal bytes are written only from the terminal-owning thread. Goroutines schedule work; they never write terminal bytes. Blocking I/O, subprocess waits, sleeps, and expensive CPU work on that thread are developer errors.
- Any immutable-area write while a mutable frame exists happens inside one frame transaction under one exclusion boundary: erase band, append stable rows, repaint band, release.

## Assistant Streaming

- Streaming is source-backed. Deltas append to the in-memory stream source; the full source renders through the assistant markdown projection for the active theme and width; rows whose rendering is prefix-stable promote into the immutable area; the volatile tail stays in the mutable band.
- Promotion is monotonic. The promotion boundary never moves backward past rows already appended to the immutable area. If re-rendering the source would change an already-promoted row, that is a developer error, not a trigger to rewrite, restyle, or re-emit.
- Raw deltas are never written into the immutable area. Only rendered markdown projection rows are promoted.
- Finalization matches the committed assistant entry to the active stream by carried stream/step identity, then applies exactly one of three outcomes: committed text equals the streamed source, finalize with no additional writes; committed text extends the streamed source, emit only the missing suffix through the stream path, then finalize; anything else is a developer error. There is no fourth outcome. Finding a finalizer by scanning entries, matching by text similarity, or matching across different stream identities is banned reconciliation.

## Grouping And Dividers

- Every committed row maps to exactly one group kind: user input, assistant output, tool activity, or notice. The mapping is total, owned by the transcript projection, and never inferred from row text.
- Consecutive promoted rows of the same kind form one group. A row of a different kind closes the open group and opens a new one. Group close has exactly one trigger: arrival of a row of a different kind. Nothing detects, waits for, or confirms a group end.
- A divider is a rendered line emitted into the immutable area immediately before the first row of a new group. The divider below a group and the divider above the next group are the same single divider. Dividers are never emitted retroactively, never inserted above existing content, and never emitted between rows of the same kind.
- Divider emission reads exactly one piece of state: the divider register. Incoming kind differs from the register: emit one divider, then the row. Incoming kind equals the register: append the row with no divider. The register updates on every promotion. Deciding divider placement by scanning, retaining, or re-reading promoted content is banned reconciliation.
- An open tool-activity group renders in the mutable band and repaints freely there as calls complete; squished or single-line chain rendering is a mutable-band concern. The group's rows promote into the immutable area when the group closes. Promoted group rows are immutable like all other immutable-area content.
- Assistant output groups promote progressively through stream promotion; the divider for the group is emitted before its first promoted row.

## Queueing While Not Owning The Normal Buffer

- While detail mode, alt-screen overlays, or other surfaces own the terminal, received events accumulate in the arrival-order queue.
- The queue stores typed events only. It must not store rendered lines, ANSI bytes, page snapshots, diffs, or projections of emitted output.
- Drain applies each queued event in order through the same single event path. Drain performs no filtering, merging, deduplication, or reordering.
- If the queue exceeds 1000 events, the entire queue is dropped and scratch rehydration runs. Partial drains of an overflowed queue are banned.

## Scratch Rehydration

- Scratch rehydration is the only path that re-issues already-shown content. The trigger list is exhaustive:
  - The received event seq is discontiguous with the last received seq, or the connection/subscription was lost. The emitted history may misrepresent the conversation and appending cannot repair it.
  - Terminal resize, debounced 1 second, left emitted content visually broken under the previous geometry. Resize events during the debounce restart the timer.
  - The arrival-order queue exceeded 1000 events.
- Never triggers. Each of these is an ordinary append or a bug to fix at its cause, and re-emitting in response to any of them is banned:
  - New content arrived: a delta, a tool call, a notice, any addition.
  - The needed change is addition-only.
  - Internal or app data changed without changing what is correctly shown on screen.
  - A client-side bug lost or corrupted local state.
  - Received data mismatched client expectations.
  - Compaction occurred. Compaction appends rows; it never rewrites shown history.
  - Correct concurrency is inconvenient to implement.
  - The cursor is not where erasing would be convenient.
  - A large paste filled the screen.
- A bug is never resolved by re-emitting committed state, in any code path, under any severity.
- Rehydration erases only the mutable band, reopens the subscription through the same session-open hydration path, and appends the received active segment below existing scrollback. It never clears scrollback, reaches into emitted content, or compares the hydrated segment against anything. Duplicate-looking output after rehydration is acceptable and must not be suppressed.
- Only operator-owned UI-local state survives rehydration. Transcript, queue, running, status, tool, and steering state come from the hydration payload. If hydration fails, the TUI exits with a clear error; it does not fabricate empty state or continue on stale state.

## Errors

- Developer errors in this surface (banned-outcome attempts, renderer prefix instability, geometry violations, invalid frames, finalization mismatches without a delivery gap) panic in debug mode with diagnostics: attempted operation, terminal geometry, quoted payload or frame content, and stack trace.
- In release mode the same diagnostics are written to logs with no user-facing notice, and the surface continues best-effort.
- No error or recovery path may: drop, skip, or defer rendering of received committed content; drop or disable the native surface; hand the ongoing transcript to an app-managed viewport; trigger scratch rehydration; store content for later comparison; or re-emit. An error path that cannot satisfy these constraints exits the TUI with a clear message instead.
- Immediate terminal write failures surface synchronously to the caller.

## Size

- The ongoing scrollback surface, including its raw-terminal package, streaming promotion, live-area rendering, and event application, stays under 10000 production lines total. This is a locked budget enforced in CI, not guidance.

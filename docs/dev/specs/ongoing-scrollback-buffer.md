# Ongoing Scrollback Buffer

## Scope

- This spec owns ongoing mode's normal-buffer terminal surface. Alt-screen surfaces, alternate-scroll modes, BEL, and OSC notifications are out of scope.
- These requirements apply to behavior regardless of how Kent implements or names it.

## Frame Model

- The ongoing surface is a single normal-buffer frame split by an immutable boundary.
- After the TUI emits content into the Immutable Area, it treats that content as unavailable. It does not retain or compare emitted lines, rendered text, entry identities, counts, or terminal bytes to decide future output.
- The mutable band is an absolute-positioned viewport anchored to the visible terminal bottom. Every render and erase establishes geometry, resets origin mode and scroll margins, and derives the band top from the submitted frame height. It must not depend on the current cursor position.
- Immutable writes use OSC 133 output semantics. The mutable band is one OSC 133 redrawable semantic-prompt region, so supporting terminals clear it before resize reflow and the resize event repaints it from mutable frame state. Retired mutable rows return to output semantics before erase, so semantic-prompt marking never survives into immutable rows or permits history replay.
- The client resolves one terminal-resize policy at startup. Exact `TERM_PROGRAM=ghostty` or a non-empty `KITTY_WINDOW_ID` selects OSC 133 repaint unless non-empty `TMUX` is present; tmux, every other identity, and absent identity select legacy width rehydration. Terminal identity matching is exact, with no substring inference from `TERM` or process names.
- Each event, streaming update, or status change produces one uninterrupted terminal update: erase only the Mutable Band, append newly stable rows, and repaint the Mutable Band from current state.
- There is no clock-based repainting. Animations produce state changes and those changes schedule renders. When no state changes exist, the surface stops rendering. The legacy resize fallback uses a one-second debounce to coalesce width changes before scratch rehydration; it is not a repaint timer.

## Mutable Band Height

- The enforced minimum mutable band is: one in-progress tool call, one commentary line, one queued-or-steered message line plus its more-count line when needed, the input upper and lower dividers, one input text line, and one status line.
- If the terminal height and current live content cannot fit the enforced minimum, ongoing hides the live area until the terminal is tall enough. This is the only case where input dividers and the status line are not shown. It must not drop committed content, switch to a transcript viewport, or partially remove unshrinkable pieces to make space.

## Delivery Consumption

- Ongoing Mode consumes one ordered, sequence-numbered transcript subscription. Opening the subscription delivers hydration first. Every later received event has the next per-subscription sequence number. Each event is complete and committed assistant entries identify the Streaming Message that they finalize.
- Each received event has one outcome: render it now, hold it in arrival order while another surface owns the terminal, start Scratch Rehydration after a sequence gap, or surface a developer error.
- No other outcome exists. A received event must never be skipped, dropped, deduplicated, merged with another event, reordered relative to arrival order, partially applied, or held back to await a matching or confirming event.
- A prompt admitted concurrently with Session resource draining may be absent from the closing subscription; Scratch Rehydration reloads authoritative prompt state after reopening.
- Committed tool lines join the open group in server emission order. There is no client-side reordering or frontier between parallel tool calls. Visual grouping follows the Grouping And Separators section and never changes arrival order.
- During live operation the client must not issue transcript reads of any kind: no page requests, tail requests, gap fills, refreshes, recovery reads, or committed-advance re-reads. The ongoing surface reads from the server through exactly one mechanism: opening the subscription, which is also the scratch-rehydration mechanism. Detail-mode history paging is a separate surface and is not available to ongoing rendering.

## Bounded TUI State

- Ongoing Mode retains only:
  - The last received event sequence number: one integer.
  - The active assistant stream: source text, its stream/step identity, its rendered rows, and the promotion boundary within them.
  - Mutable-band frame state: pending tool-call rows, spinner/animation state, input field, status line.
  - One temporary active prompt-answer delivery, including the immutable submitted answer and cancellation ownership. The visible prompt editor remains operator-owned draft state and may diverge from that submitted answer for a future retry. Approval commentary queued before an allow answer belongs to the operator-input queue; denial commentary does not enter that queue.
  - The arrival-order queue of received-but-unrendered typed events, used only while the surface does not own the normal buffer, plus its rehydration-required marker after overflow.
  - Operator-owned UI-local state: input draft, editor cursor state, and prompt history capped at the 100 most recently recorded entries. Persisted prompt history remains complete; session opening supplies only its 100 newest entries by server recording order. The client retains that bounded result locally instead of requesting prompt history after every submission. A newly submitted prompt that would exceed the local cap discards the oldest local entry. The retained entries remain in server recording order, from the oldest retained prompt to the newest.
  - A session-opening response containing more than 100 prompt-history entries is a developer error. Debug mode panics; release mode keeps only the newest 100 entries.
  - The group register: the group kind of the most recently promoted row. One enum value.
- Ongoing Mode does not retain committed transcript entries after rendering, transcript totals or offsets, emitted-output records, before/after transcript copies, optimistic transcript rows, or a rendered-output cache for the Immutable Area.
- No transcript row is ever rendered before the server commits it. A committed-append failure on the server surfaces as an error; the client has no local fallback row path.

## Terminal Updates

- Ongoing Mode serializes terminal updates. Background work never writes to the terminal directly.
- Blocking I/O, process waits, sleeps, and expensive computation never block terminal input or rendering.
- When a Mutable Band exists, each Immutable Area append erases the band, appends the stable content, and repaints the band as one uninterrupted update.

## Assistant Streaming

- Streaming Markdown uses the complete assistant source received so far. The width-aware volatile tail remains in the Mutable Band. Closed blocks that cannot change become stable Logical Lines and promote into the Immutable Area.
- Markdown Promotion accounts for constructs that can restyle or reflow preceding rows. The active source line and any open construct whose visible rows can change remain volatile until Kent proves them stable; a table can wait for its closing row.
- Promotion is monotonic. The promotion boundary never moves backward past rows already appended to the immutable area. If re-rendering the source would change an already-promoted row, that is a developer error, not a trigger to rewrite, restyle, or re-emit.
- Stable prose contains no line breaks generated from terminal width. Markdown soft line breaks flow as spaces; authored hard breaks and authored preformatted line boundaries remain logical line boundaries. GFM tables are the width-formatted Markdown exception and use the terminal width at promotion.
- Raw deltas never enter the Immutable Area. Kent promotes only stable Markdown lines.
- Finalization matches the committed assistant entry to the active Streaming Message identity.
- If committed text equals the streamed source, Kent finalizes without another write.
- If committed text extends the streamed source, Kent emits only the missing suffix and then finalizes.
- Any other relationship is a developer error. Kent never matches finalization by scanning transcript entries or comparing text similarity.

## Grouping And Separators

- Every committed row has one visual group kind: user input, assistant output, tool activity, or notice. Typed transcript facts determine the kind; row text does not. A background shell completion remains a notice entry but uses the tool-activity visual group.
- Consecutive promoted rows of the same kind form one group. A row of a different kind closes the open group and opens a new one. Group close has exactly one trigger: arrival of a row of a different kind. Nothing detects, waits for, or confirms a group end.
- A separator is one blank line emitted into the immutable area immediately before the first row of a new group. The blank line below a group and the blank line above the next group are the same single separator. Separators are never emitted retroactively, never inserted above existing content, and never emitted between rows of the same kind.
- Separator placement uses only the most recently promoted group kind. A different incoming kind emits one blank line before the row. The same incoming kind appends without a separator.
- Pending tool activity renders in the mutable band and repaints freely there until the server emits a committed tool row or abort for that call. A committed tool row appends to the immutable area immediately in server order and must not be retained for delayed group promotion, reordering, or batching.
- Compact tool and notice rows remain one-line width-bound summaries and may ellipsize at emission. This compacting contract is separate from full user/assistant Markdown flow.
- Error-severity notices are a renderer-owned exception to compact notice layout regardless of the selected Ongoing render mode. The renderer emits the complete typed reason payload, including complete runtime-diagnostic detail and complete legacy-untyped text, wrapped without ellipsis; cache-warning, compaction, and tool-output-repair errors use their complete typed reason text.
- Every non-error tool and notice row retains its existing compact or full policy. Agent Steer notices and verbose Reviewer Suggestions retain their existing full Ongoing surface routing.
- Assistant output groups promote progressively through stream promotion; the blank separator for the group is emitted before its first promoted row.

## Queueing While Not Owning The Normal Buffer

- While detail mode, alt-screen overlays, or other surfaces own the terminal, received events accumulate in the arrival-order queue.
- The queue stores typed events only. It must not store rendered lines, ANSI bytes, page snapshots, diffs, or projections of emitted output.
- Drain applies each queued event in order through the same single event path. Drain performs no filtering, merging, deduplication, or reordering.
- If the queue exceeds 1000 events, the surface drops queued events beyond the cap to prevent unbounded memory growth and records that scratch rehydration is required. When ongoing regains the normal buffer, it clears the queued buffer and runs scratch rehydration. Partial drains of an overflowed queue are banned.

## Scratch Rehydration

- Scratch rehydration is the only path that re-issues already-shown content. The trigger list is exhaustive:
  - The received event seq is discontiguous with the last received seq, or the connection/subscription was lost. The emitted history may misrepresent the conversation and appending cannot repair it.
  - The arrival-order queue exceeded 1000 events.
  - A width change under the legacy terminal-resize fallback policy, after the one-second debounce.
- Height changes always repaint the mutable band. Width changes use OSC 133 repaint only for exact Ghostty or kitty capability evidence outside tmux; all other environments use the legacy fallback.
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
- Outside the exhaustive trigger list, a bug is never resolved by re-emitting committed state, in any code path, under any severity.
- Rehydration erases only the Mutable Band, reopens the Session, and appends the received active segment below existing Scrollback. It never clears Scrollback, changes emitted content, or compares the received segment with existing terminal output. Duplicate-looking output after rehydration is acceptable.
- Only the operator's local input and navigation state survives rehydration. Kent reloads fresh server-authoritative transcript, RuntimeActivity, Session identity and status, execution target, active execution, reasoning, Reviewer, compaction, tool, Queue, prompt, background-process, context-usage, and Goal state from the reopened Session. A runtime transport failure keeps the TUI open under the connection-loss contract while Kent retries reopening the subscription. Any other rehydration failure exits the TUI with a clear error; it does not fabricate empty state or continue with stale state.

## Errors

- Developer errors in this surface include banned-outcome attempts, renderer prefix instability, geometry violations, invalid frames, and finalization mismatches without a delivery gap. Kent logs them with the attempted operation, terminal geometry, quoted payload or frame content, and stack trace.
- In debug mode, developer errors in this surface panic after diagnostic capture. In production, they follow the shared failure classification in the Core Runtime and Tools specification and are contained to the affected operation or TUI process. Production containment must preserve the Ongoing output invariants below and must not continue the affected operation with invalid or invented state.
- No error or recovery path may: drop, skip, or defer rendering of received committed content; drop or disable the native surface; hand the ongoing transcript to an app-managed viewport; trigger scratch rehydration; store content for later comparison; or re-emit. An error path that cannot satisfy these constraints exits the TUI with a clear message instead.
- An immediate terminal write failure terminates the affected TUI process under the shared terminal-output failure classification. Kent performs only best-effort terminal restoration and one minimal standard-error diagnostic. It does not repaint, retry stdout, or continue the client.
- When an ongoing transcript subscription cannot open because of a runtime transport failure, the TUI keeps the existing transcript visible, shows the persistent connection-loss status-line notice, and retries without exposing low-level transport details. A successful hydration clears the notice. Other failures while opening or rehydrating Chat retain the existing clear-error and debug-diagnostic path.

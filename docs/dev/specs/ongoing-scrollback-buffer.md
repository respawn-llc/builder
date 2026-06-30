# Ongoing Scrollback Buffer

## Scope

This spec owns ongoing mode's normal-buffer terminal surface. Alt-screen surfaces, alternate-scroll mode changes, BEL, and OSC notifications are outside the ongoing scrollback surface contract.

The runtime transcript, event log, session view, and committed transcript read models remain authoritative. The ongoing TUI does not treat normal-buffer scrollback as readable, mutable, or reconcilable state after bytes are emitted.

## Surface Shape

The ongoing normal-buffer surface is a single normal-buffer terminal frame split by an immutable boundary:

- Immutable area: all rows above the mutable boundary. This includes committed history rows and any assistant Markdown rows that have been promoted as stable.
- Mutable area: the bottom band owned by the current frame. This includes the unstable assistant stream tail, live tool/status activity, input field, and status line.

Immutable-area writes are fire-and-forget terminal operations. After bytes are written above the mutable boundary, the client treats those bytes as unavailable terminal state. The TUI must never store, remember, hash, diff, acknowledge, or reconcile already emitted immutable-area lines, blocks, ANSI bytes, rendered text, projection identities, or terminal-visible equivalents. Immutable output cannot be rewritten, restyled, deleted, moved, overlapped, replayed for comparison, or used as a source of truth.

The mutable area is an absolute-positioned viewport at the bottom of the normal screen. It is cleared and re-rendered from current UI state whenever that state changes. It does not render on a clock. Animations produce changes, and those changes may schedule renders. When no mutable-area state changes or animation ticks exist, the mutable area stays stable and stops rendering.

On each streaming chunk, committed/runtime event, queued emission event, or status-line change, ongoing mode performs one frame transaction:

- Move the cursor to the bottom-owned frame area and erase the mutable area line by line.
- Promote any newly stable projected rows into the immutable area by printing them above the next mutable frame.
- Recompute the mutable area from fresh state.
- Print the unstable stream tail, live tool/status activity, input field, and status line as one new mutable frame.

The mutable erase never targets rows above the immutable boundary. It is acceptable for a terminal's native history to contain duplicated or stale chunks after scratch rehydration because immutable scrollback is physically unreachable beyond the current normal-buffer frame.

## Ownership

`NativeOngoingSurface` is the only production owner for ongoing normal-buffer terminal output. It owns immutable-area writes, assistant streaming, mutable-area rendering, holdoff flushing, active stream-tail reads, terminal geometry, and the shared exclusion boundary around all terminal bytes for this surface.

Production app code must not construct or retain a separate stable writer, live writer, mutable-area implementation, raw physical-terminal byte projection, cursor writer, emitted-line ledger, delivered-stable projection ledger, stable-history cache, native-scrollback cache, or reconciliation cursor for ongoing mode. The app may submit typed live frames, assistant-stream deltas, committed-entry UI projections, tool-completion projections, and other typed emission events through `NativeOngoingSurface`; it may not write around that owner.

The surface accepts these terminal intents:

- Append one already-rendered stable line.
- Append assistant markdown stream content.
- Finish the active assistant stream.
- Render one complete mutable-area frame.
- Flush held normal-buffer work after the normal buffer becomes available.

`OngoingScrollbackBufferImpl` is the only implementation of `NativeOngoingSurface`. Internal mutable-area machinery is package-private implementation detail behind that owner.

## Normal-Buffer Ownership

Ongoing bytes are emitted only while the ongoing transcript surface owns the terminal normal buffer. Alt-screen and other non-ongoing surfaces never receive ongoing-surface bytes.

Before native ongoing writes stable or live bytes for a surface lifecycle, it prepares normal-buffer ownership by exiting alternate screen mode, disabling origin mode, and resetting the terminal scroll region. This preparation is idempotent and protects stable scrollback after abnormal exits or stale terminal mode state.

Normal-buffer preparation is invalidated when normal-buffer ownership becomes unavailable and when the app resumes native ongoing output after an alt-screen surface. The next native immutable or mutable write prepares the normal buffer again before emitting bytes. Resuming after an alt-screen surface also marks the mutable area dirty so an unchanged mutable frame is repainted into the returned normal buffer.

When normal-buffer ownership is unavailable, the TUI queues typed emission events or their UI projections, such as assistant deltas, committed transcript entries, tool-completion events, runtime status events, and live-frame intents. The queue must not store literal ANSI output, emitted terminal text, physical terminal rows, or an immutable-area replay cache. Mutable rendering keeps only the latest desired frame.

When a subscriber becomes available again, including returning from detail mode to ongoing mode, the TUI drains pending typed emission events in order through the normal ongoing emission path. If the pending emission-event queue grows beyond 1000 items, the TUI drops all queued events and runs scratch rehydration instead.

Ordinary alt-screen transitions do not recreate the ongoing surface and do not rehydrate transcript history unless the queued typed emission events overflow. Terminal resize, transcript divergence, and connection/subscription loss are scratch-rehydration triggers.

## Scratch Rehydration

Scratch rehydration is the only path that re-issues already-shown content. It is acceptable only when content already emitted to screen is genuinely broken or misleading to the user and cannot be repaired by appending. The test is the state of the bytes already on screen, not whether app data changed or app code is wrong.

Acceptable triggers, where emitted scrollback is genuinely misleading:

- Connection or subscription loss left a real gap in delivered data, so emitted history misrepresents the conversation. Leaving `the quick brown <gap> lazy dog` on screen as `the quick brown lazy dog` is unacceptable; re-issuing the active segment is the only repair.
- Terminal resize wrapped earlier scrollback at the old geometry, so content far from the current frame is visually broken and unreachable for in-place repair. Re-issuing the active segment since the latest compaction at the new geometry is the chosen, sufficient repair.
- Client-side divergence accumulated past the point where incremental replay can be trusted, such as navigating away and buffering more than the pending-event queue limit, so continuing to append risks a broken transcript.

Never triggers; these are ordinary appends or bugs to fix at the cause, never re-emissions:

- New content was added, including a new assistant delta, tool call, or notice. Additions append.
- The change is addition-only, a positive diff that needs only new rows. Additions append.
- Internal or app data changed without changing what is correctly shown on screen.
- A client-side bug lost current state. The bug is fixed at its cause, never papered over by re-emitting.
- Compaction occurred. Compaction appends one `context compacted` line; it is an append, not a rewrite or a rehydration.
- A client-side bug produced a mismatch between expected and received server data. The bug is fixed at its cause, never masked by re-emitting.
- Implementing correct concurrency is inconvenient.
- The cursor is not positioned where erasing emitted lines would be convenient.
- A large paste filled the screen. The mutable area owns its own frame; emitted history is untouched.

A bug is never resolved by re-emitting committed state. Delivery bugs surface as native errors and fail fast in debug.

Scratch rehydration runs the same in-process session navigation/open hydration path used when the TUI opens a session. It does not restart the TUI process, reinitialize unrelated application state, or erase normal-buffer scrollback.

Scratch rehydration requests an authoritative hydration page from the server containing only the current active transcript segment: entries since the latest compaction boundary, or entries since conversation start when no compaction has occurred. The page includes committed transcript content, queued and steered messages, running state, status-line state, and runtime UI state needed by the normal session-open path.

Before applying scratch rehydration, ongoing mode erases only the mutable area that remains under current-frame ownership. After hydration succeeds, ongoing mode emits the hydrated active transcript segment through the same full-history emission path used on session open. The emitted chunk is appended after existing immutable scrollback. Ongoing mode does not clear scrollback, reach into earlier immutable content, reconcile against previous emissions, or suppress duplicate-looking output. No separator row is inserted solely because scratch rehydration occurred.

Only essential UI-local state survives scratch rehydration. The current input draft, cursor-relevant editor state, and other operator-owned unsent local state may be preserved; transcript, queue, running, status-line, tool, and steering state are rehydrated from the server-owned session view. If the server cannot provide the scratch hydration page, the TUI exits with a fatal error instead of fabricating empty state or continuing against stale local transcript state.

Ongoing transcript rows that must survive scratch rehydration must be server-owned before they are printed. This includes background shell completion/update transcript rows, warnings, cache warnings, reviewer status/suggestions, goal/status feedback, and runtime-local entries persisted through the session event log. Unpersisted app-local error or diagnostic rows are not recovered by scratch rehydration; if such a row was already printed, its old copy remains only in immutable terminal history.

Terminal resize uses a 1 second debounce before scratch rehydration. Resize events received during the debounce restart the timer. Stable output generated during the debounce remains queued as typed emission events unless the queue overflows, in which case the queue is dropped and scratch rehydration runs after resize settles.

## Write Ordering

Any immutable-area write request with a rendered or pending mutable frame first clears the mutable area, then writes newly stable rows above the next mutable frame, then renders the recomputed mutable frame. The app establishes the first mutable frame before initial stable transcript hydration, so production ongoing output never depends on raw cursor-position append for startup history. Stable writes without mutable-frame state write plain append output for package-local no-live use.

Immutable and mutable writes are emitted under the shared exclusion boundary. An immutable write with attached mutable content clears the mutable area, appends promoted immutable rows, and prints the recomputed mutable frame before releasing the boundary. Mutable-frame changes clear and repaint only the mutable area by absolute terminal coordinates.

Erase failure skips both the immutable write and mutable restore. Immutable write failure still attempts mutable restore. A failed or partial append attempts a best-effort reset of the terminal scroll region and cursor state before surfacing the write error. If a held flush contains multiple immutable writes, later writes are attempted after earlier failures.

Active assistant stream row promotion must not restore a mutable frame that contains the previous stream tail. The surface erases stale mutable content, writes promoted rows contiguously, and renders the updated mutable frame from the latest stream tail on the next mutable render. Assistant-stream finish writes all remaining stable stream content before restoring mutable content.

Goroutines never write terminal bytes directly. A goroutine may only schedule work that later executes through approved terminal-main-thread write paths.

All terminal buffer writes assert terminal-main-thread ownership. Blocking or non-terminal work on that thread is a fatal programming error. File I/O, network I/O, subprocess waits, sleeps, and expensive CPU work on the terminal main thread fail unconditionally.

## Assistant Streaming

Assistant streaming is source-backed. `StreamMarkdownAssistantContent` appends incoming assistant content to the surface's active assistant stream state, renders the complete source through the assistant markdown projection for the active theme and terminal width, and exposes the mutable active rendered tail as mutable-area tail lines for rendering with input, status, and pending chrome.

The app keeps the active assistant stream source as volatile stream state, not emitted immutable history. Committed assistant finalization compares the authoritative committed projection against that source, not against mutable-area view text or immutable output. Mutable-area text may be cleared or rehydrated independently; it must not decide whether a native active stream is the committed block being finalized.

Partial assistant chunks must not be raw-written into normal-buffer scrollback. Stable stream rows are rendered markdown projection rows, not raw deltas or raw terminal wrapping. The active stream tail is the only mutable assistant stream content. `FinishAssistantStreaming` promotes the remaining rendered tail into the immutable area, flushes queued immutable appends, clears active stream state, and restores the latest mutable area.

Native streaming promotion is allowed only for renderers with a prefix-stability policy. A document-scoped Markdown renderer that can reinterpret earlier rows when later Markdown arrives keeps all assistant-stream rows in the mutable live tail until finalization.

Already-promoted rendered stream rows are immutable. The immutability key is the canonical rendered terminal state for the row, including text, display width, per-cell style, per-cell hyperlinks, and final pen/link state after the row. Equivalent escape-sequence churn does not change the key. If re-rendering the source changes a promoted row's canonical terminal state, the surface fails fast and scratch rehydration must run instead of rewriting, restyling, clearing, replaying, or reconciling stable scrollback content.

Streaming promotion keeps the mutable rendered tail live. Rendered rows for unterminated source lines remain in the live tail because later source can reflow the line. An empty stable source prefix promotes no rendered rows, even if the markdown renderer emits structural blank rows for empty input. Markdown blocks whose earlier rendered rows can change as the block grows, including active paragraphs, active pipe tables, reference-sensitive paragraphs, list-like blocks, and unclosed fenced code blocks, remain in the live tail until a conservative document-stable prefix or stream finish makes the rendered prefix stable. A closed fenced code block ending at the stream tail is a stable block when all earlier promoted-prefix blocks are also stable. Holdback is monotonic at the promoted-row boundary: heuristics may hold newly rendered rows, but they must not move the promotion limit behind rows already emitted to stable scrollback.

Active tail reads preserve whitespace. Leading spaces, tabs, and indentation in markdown/code-block streams are source content, not empty UI chrome.

Only assistant deltas with structured commentary or final-answer phase may use native assistant streaming. Missing-phase deltas do not use native streaming; that assistant's committed transcript message is written as stable committed transcript content instead of finalizing a partial native stream.

Deltas received while normal-buffer ownership is unavailable keep their phase and remain buffered assistant stream content. When ownership resumes, buffered content is applied to the same source-backed stream state before the mutable frame is restored, unless the typed emission-event queue overflows and scratch rehydration replaces the queue.

## Errors And Invariants

Immediate normal-buffer terminal write failures surface synchronously to the caller. Delayed holdoff flush failures surface through the native surface's delayed-error reporting path.

Contract and invariant violations fail fast with diagnostic detail. Diagnostics include the attempted operation, terminal geometry, calculated visual width when relevant, quoted payload or frame content, raw payload bytes when relevant, and stack trace.

Runtime transcript divergence, invalid ordering, overlap mismatch, active-stream mismatch, connection/subscription loss, and resize are not immutable-area redraw cases. They trigger scratch rehydration. Production exits with a fatal error if scratch rehydration fails; debug mode fails fast with invariant diagnostics. No failure path may compare against, rewrite, delete, or replay already emitted immutable history.

Runtime event batches stop at the first event whose application must await scratch rehydration. Unprocessed events from the same batch remain queued as typed emission events until hydration applies. Native must not deliver later live committed rows while an earlier scratch rehydration is outstanding, because that can move physical scrollback past committed rows that hydration is about to append.

When an authoritative committed projection arrives from live runtime-event delivery while a native assistant stream is active, the surface may finalize the active stream and skip the corresponding committed assistant block only if that block's rendered rows match the active stream source. Transcript page recovery and scratch rehydration never finalize active native assistant streams by text match; they append the hydrated active segment through the normal session-open path. Assistant final-answer and commentary blocks are both valid live finalizers because native assistant streaming is phase-less after markdown projection; non-assistant blocks are never valid stream finalizers. If the committed block differs from the mutable stream, the surface must not finalize or skip it; scratch rehydration runs instead.

The matching assistant finalizer must be the first authoritative appended committed block while native assistant streaming is active. If other committed blocks precede the matching finalizer, native cannot write those earlier blocks before a stream that already exists physically without rewriting scrollback, so scratch rehydration runs instead.

Committed non-assistant rows may arrive while an assistant stream is still active. Those rows are stable transcript history, not stream finalizers. Native queues them behind the active stream through the same stable append path and keeps the assistant stream mutable until a matching assistant finalizer, explicit stream finish, or scratch rehydration.

If a new assistant step starts while native still has an unfinalized active assistant stream from another step, native output is disabled before the new step's delta is rendered and scratch rehydration runs. The app and standard renderer reset to the new step source; native must not append a new step's delta into a previous physical stream.

Deferred committed tails retain their event step identity. A deferred assistant finalizer can clear or finalize an active assistant stream only when its known step identity matches the active stream identity, or when both sides are legacy step-less state and the text matches. Range-only deferred-tail merge must not consume a known-step assistant finalizer whose text matches the active stream but whose step identity differs.

If terminal resize starts while native has an unfinalized active assistant stream, stream promotion state remains volatile until a matching finalizer arrives or scratch rehydration replaces it. Promoted stream rows may already be physical scrollback, but resize must not preserve them as a delivered ledger or compare future committed content against them.

The stable-line append intent accepts exactly one visual terminal line. Visual width is ANSI-aware display cell width according to the active terminal width. App-owned committed projection lines are ANSI-aware clamped before submission to the native surface. If submitted input occupies more than one terminal line or contains embedded carriage return or line feed, it is an invariant violation.

Mutable-area content must be non-empty, contain no embedded carriage return or line feed inside a submitted line, fit within terminal height, and have every line fit within terminal-width ANSI-aware display cells. Native cursor row and column must fit inside the submitted mutable frame.

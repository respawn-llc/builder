# Ongoing Scrollback Buffer

## Scope

This spec owns ongoing mode's normal-buffer terminal surface. Alt-screen surfaces, alternate-scroll mode changes, BEL, and OSC notifications are outside the ongoing scrollback surface contract.

The runtime transcript, event log, session view, and committed transcript read models remain authoritative. The client must not keep a physical-terminal ledger, physical projection, acknowledgement cursor, replay cursor, or emitted-byte copy of content already written into the normal-buffer stable zone.

## Zones

The ongoing normal-buffer surface has two zones:

- Stable zone: immutable terminal scrollback content.
- Live area: the visible mutable terminal area rendered from current UI state.

Stable-zone writes are fire-and-forget. After bytes are written to the stable zone, the client treats those bytes as unavailable terminal state. The client does not track what has been physically emitted there, and it does not rewrite, restyle, replay, gate, acknowledge, or synchronize emitted stable-zone content.

The live area is erased completely and re-rendered from current live-area state whenever that state changes. It does not render on a clock. Animations produce changes, and those changes may schedule renders. When no live-area state changes or animation ticks exist, the live area stays stable and stops rendering.

## Ownership

`NativeOngoingSurface` is the only production owner for ongoing normal-buffer terminal output. It owns stable-zone writes, assistant streaming, live-area rendering, holdoff flushing, active stream-tail reads, terminal geometry, and the shared exclusion boundary around all terminal bytes for this surface.

Production app code must not construct or retain a separate stable writer, live writer, live-area implementation, physical-terminal projection, or cursor writer for ongoing mode. The app may submit complete live frames and assistant-stream deltas through `NativeOngoingSurface`; it may not write around that owner.

The surface accepts these terminal intents:

- Append one already-rendered stable line.
- Append assistant markdown stream content.
- Finish the active assistant stream.
- Render one complete live-area frame.
- Flush held normal-buffer work after the normal buffer becomes available.

`OngoingScrollbackBufferImpl` is the only implementation of `NativeOngoingSurface`. Internal live-area machinery is package-private implementation detail behind that owner.

## Normal-Buffer Ownership

Ongoing bytes are emitted only while the ongoing transcript surface owns the terminal normal buffer. Alt-screen and other non-ongoing surfaces never receive ongoing-surface bytes.

When normal-buffer ownership is unavailable, stable-line appends, assistant stream content, assistant-stream finish, and live-frame rendering are held in order where ordering matters. Live rendering keeps only the latest desired live frame. When normal-buffer ownership resumes, the surface flushes held stable work and restores the latest live frame through the same owner.

Ordinary alt-screen transitions do not recreate the ongoing surface and do not rehydrate transcript history. Terminal resize recreates the ongoing surface for the new geometry, holds native normal-buffer writes while resize events are settling, then rehydrates stable scrollback from authoritative transcript state after one second without further resize events. Resize must not replay from physical-terminal emitted bytes, raw terminal caches, memoized render text, or alternate native-scrollback state.

## Write Ordering

Any stable-zone write request first requires full live-area erasure. The stable-zone write happens only after the current live-area contents are removed from the terminal. After the stable-zone write completes, the latest live area is restored before the terminal frame is considered complete.

Stable and live writes are emitted as one terminal frame under the shared exclusion boundary. A stable write with attached live content erases the live area, writes the stable bytes, restores the live area, and only then releases the boundary.

Erase failure skips both the stable write and live restore. Stable write failure still attempts live restore. Finishing assistant streaming erases once, flushes the stream tail and queued stable-line appends, and restores once. If a held flush contains multiple stable writes, later writes are attempted after earlier failures.

Active assistant stream row promotion must not restore a live frame that contains the previous stream tail. The surface erases stale live content, writes promoted rows contiguously, and renders the updated live frame from the latest stream tail on the next live render. Assistant-stream finish writes all remaining stable stream content before restoring live content.

Goroutines never write terminal bytes directly. A goroutine may only schedule work that later executes through approved terminal-main-thread write paths.

All terminal buffer writes assert terminal-main-thread ownership. Blocking or non-terminal work on that thread is a fatal programming error. File I/O, network I/O, subprocess waits, sleeps, and expensive CPU work on the terminal main thread fail unconditionally.

## Assistant Streaming

Assistant streaming is source-backed. `StreamMarkdownAssistantContent` appends incoming assistant content to the surface's active assistant stream state, renders the complete source through the assistant markdown projection for the active theme and terminal width, and promotes rendered projection rows into the stable zone through normal stable-write transactions. The mutable active rendered tail remains in surface state and is exposed as live-area tail lines for rendering with input, status, and pending chrome.

Partial assistant chunks must not be raw-written into normal-buffer scrollback. Stable stream rows are rendered markdown projection rows, not raw deltas or raw terminal wrapping. The active stream tail is the only mutable assistant stream content. `FinishAssistantStreaming` promotes the remaining rendered tail into the stable zone, flushes queued stable appends, clears active stream state, and restores the latest live area.

Already-promoted rendered stream rows are immutable. The immutability key is the canonical rendered terminal state for the row, including text, display width, per-cell style, per-cell hyperlinks, and final pen/link state after the row. Equivalent escape-sequence churn does not change the key. If re-rendering the source changes a promoted row's canonical terminal state, the surface fails fast instead of rewriting, restyling, clearing, or replaying stable scrollback content.

Streaming promotion keeps the mutable rendered tail live. Rendered rows for unterminated source lines remain in the live tail because later source can reflow the line. An empty stable source prefix promotes no rendered rows, even if the markdown renderer emits structural blank rows for empty input. Markdown blocks whose earlier rendered rows can change as the block grows, including active pipe tables and unclosed fenced code blocks, remain in the live tail until a block boundary or stream finish makes the rendered prefix stable. Holdback is monotonic at the promoted-row boundary: heuristics may hold newly rendered rows, but they must not move the promotion limit behind rows already emitted to stable scrollback.

Active tail reads preserve whitespace. Leading spaces, tabs, and indentation in markdown/code-block streams are source content, not empty UI chrome.

Only assistant deltas with structured commentary or final-answer phase may use native assistant streaming. Missing-phase deltas do not use native streaming; that assistant's committed transcript message is written as stable committed transcript content instead of finalizing a partial native stream.

Deltas received while normal-buffer ownership is unavailable keep their phase and remain buffered assistant stream content. When ownership resumes, buffered content is applied to the same source-backed stream state before the live frame is restored.

## Errors And Invariants

Immediate normal-buffer terminal write failures surface synchronously to the caller. Delayed holdoff flush failures surface through the native surface's delayed-error reporting path.

Contract and invariant violations fail fast with diagnostic detail. Diagnostics include the attempted operation, terminal geometry, calculated visual width when relevant, quoted payload or frame content, raw payload bytes when relevant, and stack trace.

Runtime transcript reconciliation can produce an authoritative committed transcript replacement that is not appendable to already-emitted stable scrollback. In production, the native surface must not append rewritten content, clear/replay history, or panic for that runtime recovery input; it surfaces a native-surface error and disables native ongoing output so the standard renderer can continue from authoritative state. In debug mode, non-appendable native stable recovery fails fast with invariant diagnostics. Direct native stable append calls that violate the append-only contract remain invariant violations.

When an authoritative committed projection arrives while a native assistant stream is active, the surface may finalize the active stream and skip the corresponding committed block only if that block's rendered rows match the active stream source. If the committed block differs from the mutable stream, the surface must not finalize or skip it; it surfaces an active-stream mismatch and disables native output. This runtime mismatch is recoverable in debug mode; only structural projection non-contiguity fails fast.

The stable-line append intent accepts exactly one visual terminal line. Visual width is ANSI-aware display cell width according to the active terminal width. App-owned committed projection lines are ANSI-aware clamped before submission to the native surface. If submitted input occupies more than one terminal line or contains embedded carriage return or line feed, it is an invariant violation.

Live-area content must be non-empty, contain no embedded carriage return or line feed inside a submitted line, fit within terminal height, and have every line fit within terminal-width ANSI-aware display cells. Native cursor row and column must fit inside the submitted live frame.

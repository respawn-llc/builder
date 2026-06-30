# Ongoing Scrollback Buffer

## Scope

This spec owns ongoing mode's normal-buffer terminal surface. Alt-screen surfaces, alternate-scroll mode changes, BEL, and OSC notifications are outside the ongoing scrollback surface contract.

The runtime transcript, event log, session view, and committed transcript read models remain authoritative. The app may keep a bounded delivered-stable projection ledger for native append reconciliation. That ledger records terminal-visible projection blocks only after successful native stable delivery and is never a transcript source of truth, replay cursor, acknowledgement cursor, raw byte cache, or emitted-byte copy. The ledger resets when native ongoing output is shut down or dropped; geometry-only native buffer recreation keeps the ledger because normal-buffer scrollback remains physical history.

## Zones

The ongoing normal-buffer surface has two zones:

- Stable zone: immutable terminal scrollback content.
- Live area: the visible mutable terminal area rendered from current UI state.

Stable-zone writes are fire-and-forget terminal operations. After bytes are written to the stable zone, the client treats those bytes as unavailable terminal state. The client may remember the terminal-visible blocks it successfully asked native to append so future app-working-set projections can be reconciled against physical history. It does not rewrite, restyle, replay, gate, acknowledge, synchronize, or reconstruct emitted stable-zone bytes.

The live area is erased completely and re-rendered from current live-area state whenever that state changes. It does not render on a clock. Animations produce changes, and those changes may schedule renders. When no live-area state changes or animation ticks exist, the live area stays stable and stops rendering.

## Ownership

`NativeOngoingSurface` is the only production owner for ongoing normal-buffer terminal output. It owns stable-zone writes, assistant streaming, live-area rendering, holdoff flushing, active stream-tail reads, terminal geometry, and the shared exclusion boundary around all terminal bytes for this surface.

Production app code must not construct or retain a separate stable writer, live writer, live-area implementation, raw physical-terminal byte projection, or cursor writer for ongoing mode. The app may submit complete live frames, assistant-stream deltas, and delivered stable projection reconciliation state through `NativeOngoingSurface`; it may not write around that owner.

The surface accepts these terminal intents:

- Append one already-rendered stable line.
- Append assistant markdown stream content.
- Finish the active assistant stream.
- Render one complete live-area frame.
- Flush held normal-buffer work after the normal buffer becomes available.

`OngoingScrollbackBufferImpl` is the only implementation of `NativeOngoingSurface`. Internal live-area machinery is package-private implementation detail behind that owner.

## Normal-Buffer Ownership

Ongoing bytes are emitted only while the ongoing transcript surface owns the terminal normal buffer. Alt-screen and other non-ongoing surfaces never receive ongoing-surface bytes.

Before native ongoing writes stable or live bytes for a surface lifecycle, it prepares normal-buffer ownership by exiting alternate screen mode, disabling origin mode, and resetting the terminal scroll region. This preparation is idempotent and protects stable scrollback after abnormal exits or stale terminal mode state.

Normal-buffer preparation is invalidated when normal-buffer ownership becomes unavailable and when the app resumes native ongoing output after an alt-screen surface. The next native stable or live write prepares the normal buffer again before emitting bytes. Resuming after an alt-screen surface also marks the live area dirty so an unchanged live frame is repainted into the returned normal buffer.

When normal-buffer ownership is unavailable, stable-line appends, assistant stream content, assistant-stream finish, and live-frame rendering are held in order where ordering matters. Live rendering keeps only the latest desired live frame. When normal-buffer ownership resumes, the surface flushes held stable work and restores the latest live frame through the same owner.

Ordinary alt-screen transitions do not recreate the ongoing surface and do not rehydrate transcript history. Terminal resize recreates the ongoing surface for the new geometry. While resize events are settling, committed transcript state remains app-owned and native stable delivery waits. Deferred native delivery preserves the source policy of the deferred projection; a recovery projection cannot be upgraded into a live append by a later event during the same resize window. Once resize settles, native reprojects already-delivered ledger blocks for the new geometry and delivers pending appends from the authoritative transcript projection under the preserved source policy. Resize must not replay from physical-terminal emitted bytes, raw terminal caches, memoized render text, or alternate native-scrollback state.

## Write Ordering

Any stable-zone write request first requires full live-area erasure. The stable-zone write happens only after the current live-area contents are removed from the terminal. After the stable-zone write completes, the latest live area is restored before the terminal frame is considered complete.

Stable and live writes are emitted as one terminal frame under the shared exclusion boundary. A stable write with attached live content erases the live area, writes the stable bytes, restores the live area, and only then releases the boundary.

Erase failure skips both the stable write and live restore. Stable write failure still attempts live restore. Finishing assistant streaming erases once, flushes the stream tail and queued stable-line appends, and restores once. If a held flush contains multiple stable writes, later writes are attempted after earlier failures.

Active assistant stream row promotion must not restore a live frame that contains the previous stream tail. The surface erases stale live content, writes promoted rows contiguously, and renders the updated live frame from the latest stream tail on the next live render. Assistant-stream finish writes all remaining stable stream content before restoring live content.

Goroutines never write terminal bytes directly. A goroutine may only schedule work that later executes through approved terminal-main-thread write paths.

All terminal buffer writes assert terminal-main-thread ownership. Blocking or non-terminal work on that thread is a fatal programming error. File I/O, network I/O, subprocess waits, sleeps, and expensive CPU work on the terminal main thread fail unconditionally.

## Assistant Streaming

Assistant streaming is source-backed. `StreamMarkdownAssistantContent` appends incoming assistant content to the surface's active assistant stream state, renders the complete source through the assistant markdown projection for the active theme and terminal width, and exposes the mutable active rendered tail as live-area tail lines for rendering with input, status, and pending chrome.

The app keeps the active assistant stream source as transcript reconciliation state. Committed assistant finalization compares the authoritative committed projection against that source, not against live-area view text. Live-area text may be cleared or rehydrated independently; it must not decide whether a native active stream is the committed block being finalized.

Partial assistant chunks must not be raw-written into normal-buffer scrollback. Stable stream rows are rendered markdown projection rows, not raw deltas or raw terminal wrapping. The active stream tail is the only mutable assistant stream content. `FinishAssistantStreaming` promotes the remaining rendered tail into the stable zone, flushes queued stable appends, clears active stream state, and restores the latest live area.

Native streaming promotion is allowed only for renderers with a prefix-stability policy. A document-scoped Markdown renderer that can reinterpret earlier rows when later Markdown arrives keeps all assistant-stream rows in the mutable live tail until finalization.

Already-promoted rendered stream rows are immutable. The immutability key is the canonical rendered terminal state for the row, including text, display width, per-cell style, per-cell hyperlinks, and final pen/link state after the row. Equivalent escape-sequence churn does not change the key. If re-rendering the source changes a promoted row's canonical terminal state, the surface fails fast instead of rewriting, restyling, clearing, or replaying stable scrollback content.

Streaming promotion keeps the mutable rendered tail live. Rendered rows for unterminated source lines remain in the live tail because later source can reflow the line. An empty stable source prefix promotes no rendered rows, even if the markdown renderer emits structural blank rows for empty input. Markdown blocks whose earlier rendered rows can change as the block grows, including active paragraphs, active pipe tables, reference-sensitive paragraphs, list-like blocks, and unclosed fenced code blocks, remain in the live tail until a conservative document-stable prefix or stream finish makes the rendered prefix stable. A closed fenced code block ending at the stream tail is a stable block when all earlier promoted-prefix blocks are also stable. Holdback is monotonic at the promoted-row boundary: heuristics may hold newly rendered rows, but they must not move the promotion limit behind rows already emitted to stable scrollback.

Active tail reads preserve whitespace. Leading spaces, tabs, and indentation in markdown/code-block streams are source content, not empty UI chrome.

Only assistant deltas with structured commentary or final-answer phase may use native assistant streaming. Missing-phase deltas do not use native streaming; that assistant's committed transcript message is written as stable committed transcript content instead of finalizing a partial native stream.

Deltas received while normal-buffer ownership is unavailable keep their phase and remain buffered assistant stream content. When ownership resumes, buffered content is applied to the same source-backed stream state before the live frame is restored.

## Errors And Invariants

Immediate normal-buffer terminal write failures surface synchronously to the caller. Delayed holdoff flush failures surface through the native surface's delayed-error reporting path.

Contract and invariant violations fail fast with diagnostic detail. Diagnostics include the attempted operation, terminal geometry, calculated visual width when relevant, quoted payload or frame content, raw payload bytes when relevant, and stack trace.

Native stable delivery uses explicit source policies:

- Live append: committed runtime-event delivery. Non-appendable committed rewrites, invalid compaction epochs, and active-stream mismatches are native invariants in debug mode.
- Recovery reconcile: transcript page hydration, reconnect, and recent-tail recovery. A recovery page may append only a proven suffix or overlap and explicit local append-only rows. It must not start a compaction epoch, finalize an active native assistant stream, or write non-contiguous committed corrections into existing scrollback.
- Geometry reproject: resize-only reconciliation of the delivered ledger. Geometry is not transcript authority and must not append transcript rows by itself.

Runtime transcript reconciliation can produce an authoritative committed transcript replacement that is not appendable to already-emitted stable scrollback. In production, recovery and geometry mismatches surface a native-surface error and disable native ongoing output so the standard renderer can continue from authoritative state. In debug mode, native projection mismatches fail fast with invariant diagnostics. They never append rewritten content or clear/replay history.

Runtime event batches stop at the first event that starts transcript hydration or recovery. Unprocessed events from the same batch remain queued until the authoritative page applies. Native must not deliver later live committed rows while an earlier recovery page is outstanding, because that can move physical scrollback past committed rows that recovery is about to insert.

Native committed-projection reconciliation compares terminal-visible block identity: render intent, divider group, rendered lines, and durable source identity. Source identity includes the committed entry range and payload identity so repeated committed rows with identical rendered text remain distinct across resize. UI-only metadata such as selection state, expanded state, and expandability does not affect native append or overlap detection because that metadata is not emitted to the terminal stable zone.

Native committed-projection reconciliation compares the app's current committed projection against the delivered-stable projection ledger, not against the previous bounded app working set. Successful stable delivery appends the delivered blocks to that ledger. Finalizing an active native assistant stream records the matching committed assistant block in the ledger even when that block is not written again, because the stream itself already promoted those rows into stable scrollback.

Compaction replaces the bounded app working set but does not erase terminal scrollback. When the post-compaction committed projection starts with a compaction block and no delivered-history overlap exists, native appends the compaction block as a new physical epoch marker and carries the earlier delivered ledger forward. Later post-compaction rows overlap against that marker. Native must not compare the shrunk working set against the pre-compaction working set as a rewrite.

Local append-only notices that have already been emitted to native stable scrollback remain physical history even when a later authoritative transcript projection does not include them. Local append-only provenance originates on app-owned transcript entries from live local diagnostic/status event sources and is copied to projection blocks. It applies only to diagnostic/status roles, including system, reviewer status, reviewer suggestions, warnings, cache warnings, errors, developer feedback, developer error feedback, and goal feedback. Hydrated pages and ordinary committed events are authoritative transcript content, even when the app-local committed flag is absent. Committed reviewer, tool, and diagnostic rows remain committed transcript content and are not local append-only rows. If the authoritative projection shares the terminal-visible prefix before local append-only rows, native appends any authoritative suffix after those local rows; if the authoritative projection is only behind that local suffix, native writes nothing. Local append-only rows may be physically appended at the tail even when the app projection displays them before already-emitted rows, and later reconciliation treats those notices as already delivered without replaying them. Other local rows inserted ahead of unmatched already-emitted rows are not appendable because native cannot insert them at that earlier position; recovery disables native output and live append fails fast in debug. Native must not rewrite, delete, or replay local rows.

When an authoritative committed projection arrives from live runtime-event delivery while a native assistant stream is active, the surface may finalize the active stream and skip the corresponding committed assistant block only if that block's rendered rows match the active stream source. Transcript page recovery and geometry reproject never finalize active native assistant streams by text match; they surface an active-stream mismatch and disable native output. Assistant final-answer and commentary blocks are both valid live finalizers because native assistant streaming is phase-less after markdown projection; non-assistant blocks are never valid stream finalizers. If the committed block differs from the mutable stream, the surface must not finalize or skip it. Production surfaces an active-stream mismatch and disables native output; debug mode panics for live append with invariant diagnostics.

The matching assistant finalizer must be the first authoritative appended committed block while native assistant streaming is active. Local append-only system notices that the app projection places before the finalizer are appended physically after the finalized stream and recorded in physical ledger order. Reviewer status, reviewer suggestions, and tool-result rows are not eligible for this exception. If other committed blocks precede the matching finalizer, native cannot write those earlier blocks before a stream that already exists physically without rewriting scrollback. Production surfaces an active-stream mismatch and disables native output; debug mode panics.

Committed non-assistant rows may arrive while an assistant stream is still active. Those rows are stable transcript history, not stream finalizers. Native queues them behind the active stream through the same stable append path and keeps the assistant stream mutable until a matching assistant finalizer or explicit stream finish.

If a new assistant step starts while native still has an unfinalized active assistant stream from another step, native output is disabled before the new step's delta is rendered. The app and standard renderer reset to the new step source; native must not append a new step's delta into a previous physical stream.

Deferred committed tails retain their event step identity. A deferred assistant finalizer can clear or finalize an active assistant stream only when its known step identity matches the active stream identity, or when both sides are legacy step-less state and the text matches. Range-only deferred-tail merge must not consume a known-step assistant finalizer whose text matches the active stream but whose step identity differs.

If terminal resize starts while native has an unfinalized active assistant stream, native output is disabled before recreating the surface. Promoted stream rows may already be physical scrollback but are not committed stable ledger rows until a matching finalizer arrives, so resize must not drop stream promotion state and later replay the committed assistant block.

The stable-line append intent accepts exactly one visual terminal line. Visual width is ANSI-aware display cell width according to the active terminal width. App-owned committed projection lines are ANSI-aware clamped before submission to the native surface. If submitted input occupies more than one terminal line or contains embedded carriage return or line feed, it is an invariant violation.

Live-area content must be non-empty, contain no embedded carriage return or line feed inside a submitted line, fit within terminal height, and have every line fit within terminal-width ANSI-aware display cells. Native cursor row and column must fit inside the submitted live frame.

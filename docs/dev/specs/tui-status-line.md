# TUI Status Line

The persistent one-line status bar of the main chat surface, in both ongoing and detail modes: composition, activity indicator, segment semantics, the notice system, and degradation under narrow widths.

Excludes: the `/status` overlay (a different surface that shares data sources), goal semantics (core-runtime-tools :: Goals), what triggers individual notices (owned by each feature's spec).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Placement And Composition

- The status line is the bottom row of the main-surface frame, part of the ongoing mutable band; exactly one line, present in both ongoing and detail modes. (mutable-band membership owner: ongoing-scrollback-buffer :: Definitions)
- Compact and fixed: activity indicator, optional git branch, model label, process metadata, transient warning, right-aligned context meter. (owner: tui-transcript :: Rendering)
- Space contract: every status-line item has a fixed priority; as width shrinks, lower-priority items yield their space in full before any higher-priority item degrades. Priority descending, each with its own degradation behavior:
  1. Activity spinner/dot — always shown, never truncates.
  2. Spinner word (indicator label: `compacting`/`review`/`goal`/`editing`/`error`) — disappears whole.
  3. Context usage percentage — always shown, never truncates.
  4. Context meter bar — disappears whole.
  5. Background process count (`ps N`) — always shown while running processes exist, never truncates.
  6. Status notices / error messages — truncate at the end. Items below (7–9) disappear entirely before this truncation kicks in; items above (2, 4) engage their disappearance only once this item has zero space left.
  7. Model thinking/intent (reasoning status header) — disappears whole.
  8. Model label (model id / thinking level / fast) — disappears whole.
  9. Git branch — disappears whole.
  10. Help slot: idle help hint, or in detail mode the `Enter to expand`/`Enter to collapse` hint (one shown at a time) — disappears whole, yields first.

## Activity Indicator

- Leftmost element: a steady dot when quiescent, an animated spinner while working. Spinning = runtime busy (except while a question prompt is waiting on the user), or compaction running, or reviewer running.
- The indicator carries a color role and an optional one-word label, first match wins: compacting → secondary + `compacting`; reviewer running → success + `review`; rollback selection active → `editing`; goal present → primary + `goal`; runtime error state → error + `error`; otherwise primary with no label.
- The goal indicator spins only while a goal run is executing; an active-but-idle goal shows the idle dot. (owner: core-runtime-tools :: Goals)

## Segments

- Model label: model display name + thinking level; marked `fast` when fast mode is available and enabled; marked model-locked when the session's model contract is locked and differs from the configured model. The model label is the durable visible location for `/thinking` level changes; successful `/thinking` feedback is status-only and is not rendered as a transcript row in ongoing or detail. Exact copy is presentation, not contract.
- Git branch, only when known and healthy: hidden while git facts are unavailable, errored, or the branch is unknown. Git facts come from the server-side status source, refreshed in the background — never blocking render.
- Running background-process count (running/starting only); hidden at zero.
- There is NO server-ownership segment: the TUI never owns a server under the server-only client model. (owner: tui-startup :: Startup Sequence)
- Context meter, rightmost: used-context percentage plus a small proportional bar, colored by zone: below 50% success, 50–79% warning, 80%+ error. Hidden when the context window size is unknown. Usage comes from server runtime status pushed with runtime events.
- Live reasoning status header while the model is reasoning (ladder rung 7); visibility is governed purely by the space ladder.
- Post-interrupt feedback (`interrupted`) is an ordinary status notice delivered through the notice system (ladder rung 6) — there is no separate marker concept, dedicated state, or bespoke render path.
- Help slot (ladder rung 10, one shown at a time): in detail mode, the selected entry's expansion action as `Enter to expand`/`Enter to collapse` (owner: tui-transcript :: Detail Mode); otherwise the idle help hint, hidden while busy/compacting/reviewing or while the help overlay is open.

## Notices

- One notice shows at a time (ladder rung 6). The only persisted notice is server-disconnect. Transient notices overtake it for their duration: a transient shows immediately even while the disconnect notice is up, and the disconnect notice resumes when the transient clears. Whether other status-line items render alongside is governed solely by the space ladder.
- Persisted disconnect notice: set when any runtime request fails with a connection error, cleared automatically when a later request confirms reachability. This is the single global connection handler — individual surfaces never grow their own connection handling. (owner: tui-startup :: Errors)
- There is NO native-terminal-write-failure notice. Terminal write-failure handling is owned by ongoing-scrollback-buffer :: Errors (immediate synchronous surfacing, debug-mode panics) and follows that spec to the letter.
- Transient notices carry exactly one of four kinds — `error`, `warning`, `info`, `success` — mapped to theme color roles, and auto-clear after a default duration (~8s; a default, not a contract). Notice sources are assigned to these four kinds by meaning/best fit (e.g. update-available → `success`); no bespoke per-notice kinds may be added.
- Delivery modes: replace (default — new notice supersedes the current one) and queue (FIFO behind the current notice, consecutive duplicates dropped). Callers pick per notice.
- Notices are client-presentation state only: never persisted to the session, never re-shown after restart.

## Known Drift (Go TUI, frozen)

The shipping Go TUI diverges from the above: its `renderStatusLine` degradation order differs from the ratified priority ladder; a showing notice suppresses the help/activity segment and persisted notices outrank transient ones; notice kinds are an ad-hoc set (neutral/success/error/update-available); `interrupted` is a standalone activity-segment marker; `editing` is a separate segment rather than a spinner-word label; it renders a `server owned` segment for the embedded server; and it has a `nativeLiveAreaError` status-line notice — a temporary debug hack scheduled for removal. All drift, not spec.

# TUI Status Line

## Placement And Composition

- The status line is the bottom row of the main surface and part of the Mutable Band. It uses exactly one line in Ongoing Mode and Detail Mode.
- It contains an activity indicator, optional Git branch, model label, process information, help slot, reasoning-or-notice slot, and context meter.
- Space contract: every status-line item has a fixed priority; as width shrinks, lower-priority items yield their space in full before any higher-priority item degrades. Priority descending, each with its own degradation behavior:
  1. Activity spinner/dot — always shown, never truncates.
  2. Spinner word (indicator label: `compacting`/`review`/`goal`/`editing`/`error`) — disappears whole.
  3. Context usage percentage — always shown, never truncates.
  4. Context meter bar — disappears whole.
  5. Background process count (`ps N`) — always shown while running processes exist, never truncates.
  6. Status notices / error messages — truncate at the end. Items below (7–9) disappear entirely before this truncation kicks in; items above (2, 4) engage their disappearance only once this item has zero space left.
  7. Thinking Status — disappears whole.
  8. Model label (model id / thinking level / fast) — disappears whole.
  9. Git branch — disappears whole.
  10. Help slot: idle help hint, or in detail mode the `Enter to expand`/`Enter to collapse` hint (one shown at a time) — disappears whole, yields first.

## Activity Indicator

- Leftmost element: a steady dot when quiescent and an animated spinner while working.
- The spinner appears for runtime work except while a Question waits for the user or during compaction. It also appears while the server's live Reviewer activity is `invoking` or `addressing_feedback`.
- The indicator carries a color role and an optional one-word label, first match wins: compacting → secondary + `compacting`; live Reviewer activity `invoking` or `addressing_feedback` → success + `review`; rollback selection active → `editing`; goal present → primary + `goal`; runtime error state → error + `error`; otherwise primary with no label.
- The TUI never infers Reviewer activity from transcript rows, notifications, or ordinary Runtime activity.
- A matching live update removes `review` when Reviewer activity becomes `inactive`.
- Reviewer status is best-effort live state.
- Reconnect, Runtime replacement, transcript hydration, or restart may omit an earlier review.
- The Goal indicator spins only while Goal work executes. An active but idle Goal shows the idle dot.

## Segments

- Model label: model display name + thinking level; marked `fast` when fast mode is available and enabled; marked model-locked when the session's model contract is locked and differs from the configured model. The model label is the durable visible location for `/thinking` level changes; successful `/thinking` feedback is status-only and is not rendered as a transcript row in ongoing or detail. Exact copy is presentation, not contract.
- The Git branch appears only when Kent knows it and Git status is healthy. Loading or failed Git status never blocks rendering.
- Running background-process count (running/starting only); hidden at zero.
- The status line has no server-ownership segment because the TUI is always a client.
- The context meter is the rightmost item. It shows used-context percentage and a proportional bar. Below 50% uses Success, 50–79% uses Warning, and 80% or more uses Error. It is hidden when the context-window size is unknown.
- Live Thinking Status while the model is reasoning (ladder rung 7), in a right-aligned slot immediately before the context meter; visibility is governed by the space ladder.
- While a Question or Approval waits for the operator, Thinking Status is hidden. Resumed model work begins without retaining the stale pre-prompt Thinking Status.
- Post-interrupt feedback appears as the ordinary status notice `interrupted`.
- The help slot shows one item at a time. In Detail Mode it shows `Enter to expand` or `Enter to collapse`. Otherwise it shows the idle help hint. The hint is hidden while busy, compacting, reviewing, or while help is open.

## Notices

- One notice shows at a time (ladder rung 6), in the right-aligned reasoning slot immediately before the context meter. A notice replaces the live reasoning status header while visible; reasoning returns when the notice clears if reasoning continues. The only persisted notice is server-disconnect. Transient notices overtake it for their duration: a transient shows immediately during disconnect, and the disconnect notice resumes when the transient clears. Whether other status-line items render alongside is governed solely by the space ladder.
- Any connection failure sets the persistent disconnect notice. A later successful request clears it.
- Terminal write failures surface immediately and do not create a status-line notice.
- Transient notices have one of four kinds: `error`, `warning`, `info`, or `success`. They use the matching theme role and clear automatically after about eight seconds.
- Delivery modes: replace (default — new notice supersedes the current one) and queue (FIFO behind the current notice, consecutive duplicates dropped). Callers pick per notice.
- Notices are presentation state. Kent does not save or restore them after restart.

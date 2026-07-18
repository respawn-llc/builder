# TUI Ask/Approval Prompts

The interactive prompt UI for agent questions (`ask_question`) and approval requests: surface placement, prompt lifecycle, prompt kinds, and keys.

Excludes: ask transcript entries and notification text (tui-transcript), prompt tool semantics (core-runtime-tools :: Tools), editing key semantics (tui-chat-core :: Editing Model).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Surface And Lifecycle

- The prompt renders in the ongoing live region (mutable band), not as an alt-screen overlay; it takes input focus while active. Ask entries in the transcript are owned by tui-transcript (collapsed = question only).
- A newly received prompt initializes its prompt-local editor, cursor/mode, FIFO ownership, and input mode immediately, but becomes visible and focused only after its complete Markdown question projection is ready; the bell rings at that visible activation. Projection work never blocks the input/render path, and there is no loading, raw-text fallback, or partially rendered prompt state. Apart from visibility, focus, and bell timing, prompt interaction and key behavior are unchanged.
- Before that first complete projection is ready, ordinary prompt/composer input is blocked: editing, cursor/navigation keys, `Tab`, paste, `Enter`, and `Esc` cannot mutate, submit, or cancel the invisible prompt. `Ctrl+C` remains available through global runtime/terminal handling but does not answer or cancel the invisible prompt. Existing ask key behavior begins unchanged when the prompt becomes visible/focused; same-ID reprojection of an already-visible prompt does not apply this readiness block.
- If an authoritative Markdown projection fails, including initial projection or visible reprojection, the TUI never remains running with an invisible blocked prompt or an indefinitely stale question. Debug mode logs bounded projection diagnostics with a stack trace and fails fast. Release mode logs the same diagnostics, displays a clear projection error, and cleanly exits/detaches the current TUI attachment without answering or cancelling the unresolved prompt. It does not retry automatically, rehydrate, ring prompt attention, or expose raw/partial question text.
- The active question is Markdown-rendered and wraps within the live region. Answer options and the active editor take viewport priority; question lines use the remaining capacity. If the question exceeds that capacity, the prompt preserves its beginning, ends the final visible question row with an ellipsis, and omits the remaining question rows. If no question row remains, the question is omitted rather than displacing options or the editor. The freeform editor retains its cursor-anchored viewport behavior within the rows allocated to it.
- Prompts queue FIFO: one current at a time; answering or external resolution activates the next. The current FIFO slot remains authoritative while its projection is pending. Same-ID updates refresh the current prompt in place without changing prompt input, delivery, or key semantics. (multi-client consistency owner: terminology :: Equal Full-Control Attach)
- Bell rings when a new prompt is shown; notification text is owned by tui-transcript :: Notifications.
- Submitting an answer snapshots an immutable payload. During answer delivery, the visible editor remains responsive and holds a separate editable retry draft; edits, cursor movement, option navigation, `Tab`, and paste affect only a future submission after delivery fails. Allow commentary has a preceding queued-input stage that temporarily locks the prompt until that queue operation finishes.
- Repeated `Enter` during delivery does not submit another answer and surfaces a brief nonblocking sending notice. A terminal delivery failure keeps the prompt, selection, and retry draft actionable. A new submission snapshots that draft as a new payload. Canonical prompt resolution discards the draft.
- Answer delivery retries non-terminal failures with finite exponential backoff while preserving one submission identity. A deadline is terminal immediately and returns the prompt to an actionable state; it is not retried automatically.
- After the last prompt resolves, focus returns to the main composer and activity returns to running/idle.
- The status-line spinner pauses while a prompt waits. (owner: tui-status-line :: Activity Indicator)

## Prompt Kinds

- **Question with options**: numbered options plus an appended "Freeform answer" option. A recommended option is marked (star + recommended suffix) and the selection marker is distinct from the recommendation marker. Exact glyphs are presentation, not contract.
- **Pure freeform question** (no options): goes straight to text entry.
- **Approval prompt**: options carry typed approval decisions; no freeform-selection option; optional commentary attaches to the chosen decision. Denial commentary travels only with the approval answer; allow commentary is queued before the approval answer. (owner: core-runtime-tools :: Ask Question)

## Keys

- `Up`/`Down` move the option cursor; `Enter` submits the selection (option number, approval decision, or freeform text).
- `Tab` toggles between the option picker and freeform text entry; for approvals, freeform is commentary for the currently selected decision. Toggling back preserves the typed draft (non-approval prompts), shown dimmed under the options.
- Freeform entry uses the shared editing key set (owner: tui-chat-core :: Editing Model) and supports the shared clipboard-paste contract (owner: tui-chat-core :: Clipboard Paste).
- Choosing the "Freeform answer" option with empty text enters freeform mode; submitting it with empty text is rejected with an error notice + bell.
- `Esc` cancels any active answer delivery, then cancels the prompt (the agent receives a typed cancellation); `Ctrl+C` cancels any active answer delivery and the prompt, then routes to global runtime Ctrl+C handling (interrupt while busy).
- There is no digit-jump on prompt options.

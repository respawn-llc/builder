# TUI Ask/Approval Prompts

The interactive prompt UI for agent questions (`ask_question`) and approval requests: surface placement, prompt lifecycle, prompt kinds, and keys.

Excludes: ask transcript entries and notification text (tui-transcript), prompt tool semantics (core-runtime-tools :: Tools), editing key semantics (tui-chat-core :: Editing Model).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Surface And Lifecycle

- The prompt renders in the ongoing live region (mutable band), not as an alt-screen overlay; it takes input focus while active. Ask entries in the transcript are owned by tui-transcript (collapsed = question only).
- The active question is Markdown-rendered and wraps within the live region. Answer options take viewport priority; question lines use the remaining capacity and follow the live region's existing collapse/truncation behavior. Pure freeform prompts retain the existing cursor-anchored viewport behavior.
- Prompts queue FIFO: one active at a time; answering (or external resolution) activates the next. A prompt resolved from another attached client disappears here too — including from the queue. Updates to the active prompt (same prompt ID) refresh it in place. (multi-client consistency owner: terminology :: Equal Full-Control Attach)
- Bell rings when a new prompt is shown; notification text is owned by tui-transcript :: Notifications.
- Submitting an answer never freezes input: the UI stays responsive throughout answer delivery. After the last prompt resolves, focus returns to the main composer and activity returns to running/idle.
- The status-line spinner pauses while a prompt waits. (owner: tui-status-line :: Activity Indicator)

## Prompt Kinds

- **Question with options**: numbered options plus an appended "Freeform answer" option. A recommended option is marked (star + recommended suffix) and the selection marker is distinct from the recommendation marker. Exact glyphs are presentation, not contract.
- **Pure freeform question** (no options): goes straight to text entry.
- **Approval prompt**: options carry typed approval decisions; no freeform-selection option; optional commentary attaches to the chosen decision. Approval answers submitted while the runtime is busy travel through the queued-input path, not a side channel.

## Keys

- `Up`/`Down` move the option cursor; `Enter` submits the selection (option number, approval decision, or freeform text).
- `Tab` toggles between the option picker and freeform text entry; for approvals, freeform is commentary for the currently selected decision. Toggling back preserves the typed draft (non-approval prompts), shown dimmed under the options.
- Freeform entry uses the shared editing key set (owner: tui-chat-core :: Editing Model) and supports the shared clipboard-paste contract (owner: tui-chat-core :: Clipboard Paste).
- Choosing the "Freeform answer" option with empty text enters freeform mode; submitting it with empty text is rejected with an error notice + bell.
- `Esc` cancels the prompt (the agent receives a typed cancellation); `Ctrl+C` cancels it AND routes to global runtime Ctrl+C handling (interrupt while busy).
- There is no digit-jump on prompt options.

## Known Drift (Go TUI, frozen)

- Go freezes prompt input while an answer is in flight (`answerPending`); spec is never-frozen input.

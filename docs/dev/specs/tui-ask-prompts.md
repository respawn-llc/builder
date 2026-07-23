# TUI Ask/Approval Prompts

## Surface And Lifecycle

- The active prompt appears in the Ongoing Mode Mutable Band and takes input focus. It does not open an Alternate Screen.
- A collapsed transcript entry for a Question shows only the Question.
- The active question is Markdown-rendered and wraps within the live region. Answer options take viewport priority; question lines use the remaining capacity and follow the live region's existing collapse/truncation behavior. Pure freeform prompts retain the existing cursor-anchored viewport behavior.
- Prompts use FIFO order and show one active prompt at a time. Resolving the active prompt shows the next one.
- A prompt resolved from another attached client disappears from the active view or pending list.
- An update to the active prompt refreshes it in place.
- The terminal bell rings when a new prompt appears.
- Submitting an answer fixes the submitted content for that attempt. The editor remains responsive while Kent delivers it. Further edits affect only a later retry if delivery fails.
- Allow commentary is queued before the Approval answer. The prompt does not accept another action until that queue operation finishes.
- Repeated `Enter` during delivery does not submit another answer. It shows a brief nonblocking sending notice.
- A terminal delivery failure preserves the prompt, selection, and edited retry draft.
- Resolving the prompt discards the retry draft.
- Kent retries temporary delivery failures with finite exponential backoff while preserving one submission identity. A deadline failure is terminal immediately, is not retried, and returns the prompt to an actionable state.
- After the last prompt resolves, focus returns to the main composer and activity returns to running/idle.
- The status-line spinner pauses while a prompt waits.

## Prompt Kinds

- **Question with options**: numbered options plus an appended "Freeform answer" option. A recommended option is marked (star + recommended suffix) and the selection marker is distinct from the recommendation marker. Exact glyphs are presentation, not contract.
- **Pure freeform question** (no options): goes straight to text entry.
- **Approval prompt**: options carry Approval decisions and have no freeform-answer option. Optional commentary attaches to the chosen decision. Denial commentary travels only with the Approval answer. Allow commentary is queued before the Approval answer.

## Keys

- `Up`/`Down` move the option cursor; `Enter` submits the selection (option number, approval decision, or freeform text).
- `Tab` toggles between the option picker and freeform text entry; for Approvals, freeform is commentary for the selected decision. Toggling back preserves the typed draft for a non-Approval prompt and shows it dimmed below the options.
- Freeform entry uses the same editing and clipboard behavior as the main composer.
- Choosing the "Freeform answer" option with empty text enters freeform mode; submitting it with empty text is rejected with an error notice + bell.
- `Esc` cancels any active answer delivery, then cancels the prompt (the agent receives a typed cancellation); `Ctrl+C` cancels any active answer delivery and the prompt, then routes to global runtime Ctrl+C handling (interrupt while busy).
- There is no digit-jump on prompt options.

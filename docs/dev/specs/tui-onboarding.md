# TUI Onboarding (First-Time Setup Wizard)

## Trigger And Surface

- After the first successful authentication, a missing `config.toml` opens first-time setup before Session selection.
- The wizard is a bounded Alternate Screen surface.
- Text fields use the native terminal cursor.
- Every asynchronous operation shows a loading state.
- Back navigation follows the startup navigation history.

## Flow Model

- The wizard is an ordered list of steps, one screen each, of three kinds: single-choice, text input (shared editor), and multi-select. Steps show or hide dynamically based on choices so far and detected capabilities.
- Step graph (order + visibility conditions):
  1. **Theme** — dark/light with live preview as the cursor moves; keeping the detected default stays on auto-detection.
  2. **Entry** — "configure now" vs "defaults": choosing defaults finalizes immediately with a default config (preserving the chosen theme) and skips all remaining steps.
  3. **Model** — text input, pre-filled with the current default.
  4. **Context window** — only for models with a large-window variant; default (smaller) window is the recommended pre-selection.
  5. **Thinking level** — only for reasoning-capable models; level list + Disable + custom-value entry (custom opens a text sub-step; empty custom value rejected).
  6. **Verbosity** — only for verbosity-capable models.
  7. **Follow-up questions** — enable/disable the ask-question tool.
  8. **Supervisor** — off / after edits / always; when enabled, sub-steps for supervisor model (pre-filled with the primary model) and supervisor thinking (mirrors primary until explicitly diverged; custom entry as above).
  9. **Compaction mode** — Local always offered; Native only when the provider supports it; Manual-only.
  10. **Skills import** — only when importable items are detected from other providers; skills enablement is a multi-select. Remote onboarding does not offer slash-command import.
  11. **Review** — finish, or start over (returns to the first step with all selections preserved).
- Validation errors render inline on the current screen and block advancing; they never abort the wizard.

## Keys

- `Up`/`Down` (+`j`/`k`) move the cursor on choice/multi screens and scroll long content; `Enter`/`→` submit the screen; number keys `1-9` jump to an option (choice: select + submit; multi: toggle); `Space`/`Backspace` toggle on multi screens; `a` toggles all when a toggle-all exists.
- `←` and `Esc` step back one step. `Esc` on the first step cancels the wizard.
- After finalization starts, the spinner remains active and input cannot cancel or dismiss the operation. The wizard waits up to 30 seconds for a final result.

## Finalize And Cancel

- Finalizing shows a progress state. For custom setup, imports finish before Kent writes the configuration. A failed import rolls back the imported changes and returns to the wizard with an error. Kent never leaves partial setup state.
- The defaults path writes the default configuration.
- Config is written exactly once, at finalize. No step writes settings incrementally.
- Canceling before finalization aborts startup with a clear `setup canceled` error and writes nothing. The next launch opens first-time setup again.
- Finalization cannot be canceled after submission.
- Success or failure received within 30 seconds is authoritative.
- A deadline or connection loss reports an indeterminate outcome. Kent does not retry automatically or claim that it canceled the operation.

## Setup Data And Completion

- The wizard submits choices to Kent. Kent executes imports and writes `config.toml`; the TUI does not write files.
- Kent supplies model facts such as context windows, thinking, and verbosity support.
- Kent also supplies provider facts such as native compaction and importable skills or commands.
- One setup attempt uses one consistent set of these facts.
- Model facts include the complete built-in known-model list and each model's capabilities.
- Model facts include one fallback for non-empty unknown model names, so every client applies the same behavior.
- Provider capability facts cover both the current effective provider and explicit provider choices.
- An unknown explicit provider fails with an unsupported-provider error. Kent does not replace it with the current provider.
- Importable skills and slash commands include source paths when identity requires them.
- The TUI offers importable skills but does not offer slash-command import.
- Generated Kent skills appear with external provider skills in the enablement list.
- Onboarding facts are available before auth completion and before project or session attachment.
- The model list comes from Kent's built-in catalog. Setup does not perform live provider model discovery.
- Setup facts include import scanning. An import-scan failure lets setup continue without importing and appears as an explicit issue.
- A model's larger context window is represented as an optional large-window fact. If it is absent, the context-window choice is hidden.
- A client can provide a workspace root for import discovery. Kent validates it before use. If it is absent, Kent does not substitute another working directory.
- If a provided workspace root cannot be validated, the facts response includes a structured workspace-scope import error, reports global import facts, and omits workspace-local duplicate and skip checks.
- Import facts include both provider source roots and item source paths when available.
- Provider facts use stable identifiers. Each client supplies display names.
- Recommendations are structured facts such as identifiers, modes, counts, and paths. Each client supplies the wording.
- When no workspace root is provided, import facts include global external-provider imports and generated Kent skill candidates, but omit workspace-local duplicate and skip checks.
- Each setup attempt reads current capabilities.
- Reading setup facts does not execute imports, finalize choices, or write configuration.
- Kent supplies provider capabilities and known-model capabilities such as thinking, verbosity, vision input, reasoning summary, and context-window support.
- For a known model, the built-in model catalog decides verbosity support.
- For an unknown non-empty model, an explicit provider capability override decides support when present.
- Without an override, first-party OpenAI providers enable verbosity and other built-in providers disable it.
- Kent never infers verbosity support from a model-name prefix.

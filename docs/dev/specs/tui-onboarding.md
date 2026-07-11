# TUI Onboarding (First-Time Setup Wizard)

The first-time setup wizard: trigger, step graph, screen kinds, keys, validation, finalize/cancel semantics. Excludes: auth (tui-startup :: Auth Gate), config schema and option semantics (core-runtime-tools :: Configuration), what each setting does (owner specs).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Trigger And Surface

- After first successful auth, missing `config.toml` triggers first-time setup before session selection. (owner: core-runtime-tools :: Configuration; gate 3 in tui-startup :: Startup Sequence)
- The wizard is a bounded alt-screen startup surface under the startup surface architecture: shared render layer, native cursor for text entry, loading affordances, navigation-stack membership. (owner: tui-startup :: Surface Architecture)
- The wizard's rendering composes entirely through the typed render layer — layout, choice groups, previews, and input fields are typed components. Go's onboarding renderer (hand-assembled raw character/string frames) must not be replicated in any form. (RATIFIED 2026-07-03)

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
  10. **Skills/commands import** — only when importable items are detected from other providers; skills enablement is a multi-select.
  11. **Review** — finish, or start over (returns to the first step with all selections preserved).
- Validation errors render inline on the current screen and block advancing; they never abort the wizard.

## Keys

- `Up`/`Down` (+`j`/`k`) move the cursor on choice/multi screens and scroll long content; `Enter`/`→` submit the screen; number keys `1-9` jump to an option (choice: select + submit; multi: toggle); `Space`/`Backspace` toggle on multi screens; `a` toggles all when a toggle-all exists.
- `←` and `Esc` step back one step; `Esc` on the first step exits the wizard (cancel), per the navigation-stack rule (owner: tui-startup :: Surface Architecture).
- While finalizing (spinner), only cancel keys are accepted.

## Finalize And Cancel

- Finalizing shows a progress state (spinner + label). Custom path: imports execute first with rollback-on-failure, then the config is written; a failure returns to the wizard with the error displayed — never a silent exit, never partial state. Defaults path writes the default config.
- Config is written exactly once, at finalize. No step writes settings incrementally.
- Cancel aborts startup with a clear "setup canceled" error and writes nothing; the next launch re-enters first-time setup.

## Server-Side Finalize (Thin Client)

- The wizard collects choices and submits them to the server, which executes imports and writes `config.toml`; the client never touches the filesystem. (RATIFIED 2026-07-03 — server-owns-storage rule applied to onboarding; requires server API surface for finalize.)
- Model metadata (context windows, thinking/verbosity capability) and provider capabilities (native compaction, importable skills/commands) are server-supplied facts, not client-side lookups.
- The server supplies onboarding facts as one wizard-run snapshot. Model facts include Kent's full built-in known model list with each model's metadata and capabilities.
- The model facts snapshot also includes one fallback fact for non-empty unknown model names, so thin clients do not duplicate runtime fallback rules.
- Provider capability facts cover both the current effective provider and explicit provider choices.
- Unknown explicit provider choices fail the facts request with an unsupported-provider RPC error; the server never falls back to the current provider for an explicit unsupported provider.
- Importable skill and slash-command facts include server-host source paths when needed for identity, display, and finalize round-tripping. Generated Kent skill candidates are reported together with external provider candidates for the enablement multi-select.
- Onboarding facts are available before auth completion and before project or session attachment.
- The model list is Kent's built-in known model list; the onboarding facts flow does not perform live provider model discovery.
- A wizard-run facts snapshot includes import scanning. Import scanning failures are reported as facts that let setup continue without importing, not as silent skips or a hard failure of the whole facts request.
- A model's larger context window is represented as an optional large-window fact. If it is absent, the context-window choice is hidden.
- Clients may provide a workspace root for import discovery; the server validates it before using it. The workspace root is optional, and the server does not fall back to its process working directory or startup directory when the client omits it.
- If a provided workspace root cannot be validated, the facts response includes a structured workspace-scope import error, still reports global/server-user import facts, and omits workspace-local duplicate and skip checks.
- Import facts include both provider source roots and item source paths when available.
- Provider facts use stable provider identifiers; clients own provider display names.
- Server-supplied recommendations are structured facts such as identifiers, modes, counts, and paths; clients own display wording.
- When no workspace root is provided, import facts still include global/server-user external provider imports and generated Kent skill candidates, but omit workspace-local duplicate and skip checks.
- Capability facts are recomputed for each request; there is no server-side memoization or cache contract.
- This facts surface does not execute imports, finalize setup choices, or write configuration.
- Provider and model facts expose domain capability fields from the server's runtime source of truth, including provider runtime capability booleans and registered model capabilities such as thinking, verbosity, vision input, reasoning summary, and context-window support.
- Model-catalog verbosity support overrides provider behavior for known models. For unknown non-empty models, a resolved explicit provider verbosity capability override decides support; when no override applies, first-party OpenAI built-ins default to enabled and other built-ins default to disabled. The server never derives verbosity from model-name prefix matching.

## Known Drift (Go TUI, frozen)

- Go's onboarding renderer is hand-assembled raw character/string output — the banned string-block approach; the spec wizard is typed-render-layer only.
- Go's `Esc` anywhere in the wizard cancels the whole flow and aborts startup; spec is back-one-step with cancel only on the first step.
- Go writes `config.toml` and executes skill/command imports client-side and looks up model/provider capabilities locally; spec is server-side finalize with server-supplied facts.

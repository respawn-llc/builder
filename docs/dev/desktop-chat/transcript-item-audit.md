# Desktop Chat Transcript Item Audit

This working checklist enumerates every active transcript item, live line, and
suppression class currently handled by the server projection or TUI. It does not
make product decisions. Locked answers are copied to
`docs/dev/specs/desktop-chat.md`, which remains authoritative.

For each open item, decide:

1. whether it appears in the transcript or another surface;
2. its collapsed representation;
3. its expanded representation;
4. whether it starts collapsed or expanded.

The default desktop rule is detail-mode-like: committed items remain available
unless the user explicitly classifies the exact item type as bloat. Existing TUI
`O`, `OC`, and `D` classifications are evidence for collapse defaults, not
desktop visibility authority.

Legend:

- `O`: TUI ongoing/full.
- `OC`: TUI ongoing/collapsed.
- `D`: TUI detail-only.
- `X`: hidden by the current projection.
- `C`: committed/persisted row.
- `L`: live-only state.

## Locked Before This Audit

| Item | Desktop decision |
|---|---|
| Normal user message | Visible user island. Over ten rendered lines starts collapsed; one-way Expand reveals the full Markdown. |
| User message with fork target | Same user presentation. The single Edit action submits into a new session branch. |
| Final assistant message | Visible assistant island, always fully expanded, with Copy and authoritative committed timestamp. |
| Provisional assistant message | Appears on the first nonempty delta and streams through the shared Streamdown-backed renderer. |
| Provider Reasoning Trace | Dedicated borderless tool-style transcript item. Collapsed presentation uses a brain icon and a responsive first-line preview; expansion reveals the complete selectable plain text in a muted tone. |
| Empty pre-first-token assistant state | No synthetic transcript row. Runtime status owns the waiting state. |
| Empty user/assistant content | No conversational row. |
| Assistant no-op final | No committed conversational row. |

Reasoning Trace correction:

- Thinking Status is not a transcript item.
- Each server-provided Reasoning Trace is a separate durable item in server
  order.
- A live trace starts collapsed on its first nonempty update and updates in
  place.
- A provider-attempt reset removes that provisional trace without clearing
  Thinking Status.
- The collapsed row uses a brain icon and responsive first-line preview.
- Expansion shows complete selectable muted plain text, never Markdown.
- Committed expansion adds authoritative timestamp and Copy; live expansion has
  neither.

## A. Committed Conversational Items

| Item | Current classification | Audit status |
|---|---|---|
| Normal user text | `C`, default `O` | Locked |
| User text with rollback/fork identity | `C`, default `O` | Locked |
| Assistant commentary | Assistant output shown live by the TUI | Locked: ordinary assistant island, always expanded, no phase label; Copy/timestamp appear when durable |
| Assistant final | `C`, default `O` | Locked |
| Assistant final resolving a live stream | `C`, default `O` | Locked |
| Tool-call-only assistant turn | No assistant row; tool rows carry the turn | Locked by the empty-assistant rule |

## B. Injected Context And Execution Instructions

These currently collapse into developer-context notices.

| Item / discriminator | Current TUI default | Audit status |
|---|---|---|
| Loaded `AGENTS.md` context (`agents.md`) | `D` | Locked: visible flat row, collapsed by default with source path/compact label; expands to full Markdown |
| Loaded skill guidance (`skills`) | `D` | Locked: visible flat Skill guidance row, collapsed by default; expands to full Markdown |
| Subagent context (`subagents`) | `D` generic developer path | Locked: visible flat Subagent context row, collapsed by default; expands to full Markdown |
| Environment facts (`environment`) | `D` | Locked: visible flat Environment row, collapsed by default; expands to full structured facts/text |
| Future-agent handoff context (`handoff_future_message`) | `D` | Locked: visible flat Handoff context row, collapsed by default; expands to full Markdown |
| Headless-mode instructions (`headless_mode`) | `D` | Locked: visible flat Headless mode row, collapsed by default; expands to full instructions |
| Interactive-mode restoration (`headless_mode_exit`) | `D` | Locked: visible flat Interactive mode restored row, collapsed by default; expands to full transition details |
| Workflow execution instructions (`workflow_mode`) | `OC` | Locked: visible flat Workflow mode row, collapsed by default; expands to full instructions |
| Active-goal continuation context (`active_goal_continuation`) | `D` | Locked: visible flat Goal continuation row, collapsed by default; expands to full context |
| Manual-compaction user-message carryover (`manual_compaction_carryover`) | `D` | Locked for now: visible flat Compaction carryover row, collapsed by default; expands to the preserved user message |
| Custom/provider tool-call output encodings | Provider/storage and suspected legacy handling | Not a Desktop product item; out-of-scope audit is `KENT-303` |

## C. Session, Context, And Operator Notices

| Item / discriminator | Current TUI default | Audit status |
|---|---|---|
| Context compaction summary (`compaction_summary`) | `O` | Locked: visible flat Context compacted row, collapsed by default; expands to full summary Markdown |
| Context-pressure reminder (`compaction_soon_reminder`) | `D` | Locked: visible flat Context pressure reminder row, collapsed by default; expands to full instruction |
| Prompt-cache continuity warning (`cache_warning`) | Usually `O` warning | Locked: visible flat warning row expanded by default with full structured warning/diagnostic; compact form remains available |
| User interruption feedback (`interruption`) | `O` error style | Locked: hidden; the user action already communicates the interruption |
| Runtime/developer error feedback (`error_feedback`) | `O` error style | Locked: visible flat error row expanded by default with full diagnostic; compact form remains available |
| User-facing goal feedback (`goal`) | `O` | Locked: visible flat Goal row, collapsed by default; expands to full feedback |
| Entered managed worktree (`worktree_mode`) | `O` | Locked: visible flat Worktree row, collapsed by default with branch/path summary; expands to full workspace/effective-directory details |
| Returned to main workspace (`worktree_mode_exit`) | `O` | Locked: visible flat Main workspace row, collapsed by default with destination summary; expands to full workspace/effective-directory details |
| Background-process lifecycle/result notice (`background_notice`) | `OC`, grouped with tools | Locked: visible flat Background process row using tool-family presentation, collapsed by default; expands to full notice/process facts |
| Generic system notice (`system`) | Usually `O` | Locked: visible flat System row expanded by default with full text; compact form remains available |
| Generic warning notice (`warning`) | Usually `O` | Locked: visible flat warning row expanded by default with full text; compact form remains available |
| Legacy untyped notice | Persisted visibility | Locked: visible flat Legacy notice row expanded by default with full historical text; compact form remains available |
| Runtime diagnostic notice | Persisted/event visibility | Locked: visible flat Diagnostic row, collapsed by default; expands to full structured diagnostic detail |

## D. Reviewer And Supervisor Items

| Item | Current TUI default | Audit status |
|---|---|---|
| Reviewer running/completed status | Live status line / committed status may be `OC` | Locked: Thinking/status island only; no transcript row |
| Reviewer feedback | One authoritative reviewer suggestion list currently fans out into model-facing `reviewer_feedback` and optional operator-facing reviewer-suggestions text | Locked: every nonempty reviewer result persists/projects one structured visible flat Reviewer feedback row, collapsed by default and expandable to full Markdown, independent of `Reviewer.VerboseOutput`; the model-facing instruction is internal only |
| Reviewer error | `O` reviewer/error style | Locked: visible flat Reviewer error row expanded by default with full diagnostic; compact form remains available |

## E. Completed Tool Product States

These states are orthogonal to the tool presentation family in the next
section.

| Item | Current TUI default | Audit status |
|---|---|---|
| Successful completed result | Usually `OC` | Locked: the existing row stops its activity indicator and remains collapsed with a tool-specific input/result summary; expands to full input/output |
| Failed completed result | Usually `OC`, error style | Locked: the existing row remains collapsed with an error summary; expands manually to full failure output/diagnostic |
| Nonzero shell result | Failed outcome in the Shell presentation family | Locked with failed result |
| Backgrounded shell result | Tool group with `backgrounded` suffix | Locked: copy TUI—original tool row completes as Backgrounded; a separate collapsed Background process row appears later at chronological completion/kill position and is linked by typed process identity |
| Answered `ask_question` result | `OC` special question/answer tree | Locked: exactly one completed Ask Question row, expanded by default |

The following current representations are not independent Desktop product
types:

- materialized versus synthesized/joined result provenance;
- persisted tool-call request entries;
- already-materialized duplicate suppression;
- provider custom/function call-output formats;
- result repair bookkeeping.

Malformed missing-call-identity handling belongs to the integrity audit, not the
normal tool state union. Suspected dead/legacy duplication is tracked in
`KENT-303`.

## F. Tool Presentation Families

| Family | Current compact representation | Current expanded representation | Audit status |
|---|---|---|---|
| Generic/unknown tool | Tool metadata or summary | Input and output | Locked: starts collapsed with tool name plus compact input/result summary; expands to full structured input/output |
| Shell command (`exec_command` and aliases) | Command, continuation count, exit/background status | Syntax-highlighted command and output | Locked: starts collapsed with command plus running/exit/background status; expands to syntax-highlighted command and full selectable output |
| Shell input (`write_stdin`) | Shell row with plain input | Detailed input/output | Locked: separate chronological Shell input row, collapsed with target/input summary; expands to full sent input and result |
| Patch/edit | File summary and addition/removal counts | Structured diff and result | Locked: same as TUI—starts collapsed with operation, affected files, and addition/removal counts; expands to structured diff and result |
| Source result mode | Compact tool input | Syntax-highlighted source output | Locked: expanded form is inline syntax-highlighted selectable source with typed path/language facts |
| Plain result mode | Plain compact text | Plain detailed input/output | Locked: expanded form is full selectable plain text inline |
| Ask Question | Question and answer tree | Full tool input/output | Locked completed state: expanded by default |
| Web Search | Web-search-specific style | Full tool input/output | Locked: distinct typed Web Search row, collapsed by default with query/result summary; expands to full sources/results |

Shell dialects currently classified independently for rendering:

- POSIX shell;
- PowerShell;
- Windows Command Prompt.

## G. Live Tool Lifecycle

Desktop reuses the existing TUI contract: typed tool start and end facts share a
`ToolCallID` and directly produce one row. This audit chooses row presentation;
it does not introduce another reconciliation model or refactor the server tool
lifecycle.

| Item | Current TUI behavior | Audit status |
|---|---|---|
| In-flight generic tool | Pending-tool spinner line | Locked: appears immediately as one flat row, collapsed by default with tool-specific compact input and an activity indicator; the same row updates when it ends |
| In-flight shell | Pending command line | Locked by the shared in-flight rule |
| In-flight patch | Pending patch preview | Locked by the shared in-flight rule |
| In-flight Ask Question tool | Pending tool plus pending-prompt section | Locked by the shared in-flight rule plus the separate prompt-control surface |
| Hydrated in-flight tool | Reconstructs pending-tool section | Locked: reconstruct the same pending row presentation |
| Canceled tool | Removes pending tool; no committed result | Locked: remove the pending row entirely |
| Failed tool abort | Removes pending tool; abort diagnostic is not a tool transcript row | Locked: same as TUI—remove the pending row; Sonner and any authoritative durable error-feedback row own the wider failure |

## H. Questions And Approvals

| Item | Current TUI behavior | Audit status |
|---|---|---|
| Pending question | Live pending-prompt section | Locked: dedicated prompt surface attached to composer/status; no pending transcript row |
| Resolved question | Removed; answered Ask Question may appear as a tool row | Locked: exactly one completed Ask Question transcript row, expanded by default |
| Pending approval | Live pending-prompt section | Locked: dedicated prompt surface attached to composer/status; no pending transcript row |
| Resolved approval | Removed; no committed approval row | Locked: no separate approval-decision row; the associated tool call owns approval history |
| Tool-associated prompt | Appears in both pending-tool and pending-prompt state | Locked principle: one prompt control surface and one associated tool transcript item; no duplicate prompt transcript row |

## I. Queued And Steered User Work

| Item | Current TUI behavior | Audit status |
|---|---|---|
| Queued user message accepted | Visible queued/steered line | Locked: Pending Work surface inside composer/status only; no transcript row |
| Queued user message submitted | Removed | Locked: pending item disappears and the authoritative user-message row owns history; no Submitted marker |
| Queued user message discarded | Removed | Locked: disappears from Pending Work and creates no transcript row |
| Queued user message failed | Removed and restored to input | Locked: no transcript row; Pending Work/composer owns typed failure and recovery |
| Failure: session closing | Humanized input reconciliation | Locked with queued failure; no transcript row |
| Failure: terminal workflow completion | Humanized input reconciliation | Locked with queued failure; no transcript row |
| Failure: runtime unavailable | Humanized input reconciliation | Locked with queued failure; no transcript row |
| Failure: runtime stopped | Humanized input reconciliation | Locked with queued failure; no transcript row |

Operation reconciliation also classifies:

- committed;
- accepted;
- submitted;
- canceled but not committed;
- failed with restore;
- unknown;
- evicted.

These are live control facts, not committed transcript rows.

## J. Runtime And Status Facts

### Runtime lifecycle

- unavailable;
- registered/idle;
- starting;
- running;
- awaiting question or approval;
- draining;
- closing.

Locked:

- idle, starting, running, awaiting-prompt, draining, and closing appear only in
  the later-designed runtime/Thinking/status surface and create no transcript
  rows;
- unavailable appears only in the runtime/connection error surface and creates
  no transcript row.

### Active work kind

- user turn;
- workflow turn;
- goal loop;
- compaction;
- pre-submit compaction;
- user shell;
- background work;
- runtime maintenance.

Locked: active-work kind only drives the later-designed Thinking/status label,
icon, and controls. Work-kind transitions create no transcript rows.

### Step/run lifecycle

- started/running;
- finished/completed;
- finished/interrupted;
- finished/failed.

Locked: step/run lifecycle remains in runtime/status and creates no transcript
marker rows. Actual user, assistant, tool, and error-feedback rows own history.

### Reviewer lifecycle

- running;
- completed.

### Compaction lifecycle

- started;
- completed;
- failed with diagnostic.

Locked: compaction lifecycle appears only in Thinking/status/Context and creates
no lifecycle transcript rows. Failure uses Sonner and any authoritative durable
error-feedback row; the committed compaction summary remains the historical row.

### Goal lifecycle

- active;
- active and suspended;
- paused;
- complete.

Locked: goal lifecycle appears only in the Goal/status control surface and
creates no lifecycle transcript rows. Committed Goal feedback remains separate.

### Background activity

- backgrounded;
- completed;
- killed.

Locked: live background activity and process controls appear only in a new
dedicated contextual-sidebar destination and create no duplicate live transcript
rows. The chronological committed Background process rows remain separate.

### Other status facts

- context usage;
- session identity;
- session settings and lineage;
- worktree-transition outcome;
- operational diagnostic;
- prompt-history persistence failure;
- sleep-guard diagnostic.

Locked:

- live context usage appears only in the selected AI Elements Context
  control/counter below the input field and creates no transcript status rows;
- session name appears in chrome and creates no transcript status row;
- previous-session lineage appears only as the under-composer settings
  popover's `To parent chat` action and creates no transcript status row;
- parent-agent-session lineage is omitted from ordinary Chat and belongs only
  to Subagent UX;
- every setting is audited independently later;
- raw workflow active/run/task/workflow IDs are not TUI-visible Chat status
  facts and create no transcript/status item; workflow-linked Chat exposes only
  a typed Task short-ID/title navigation row.

Locked:

- transient notices/errors that the TUI emits through its status line map to
  Sonner; Desktop does not invent per-feature error surfaces;
- worktree-transition outcomes create no transcript row, update the initiating
  control, and use Sonner for transient outcome feedback;
- sleep-guard and prompt-history persistence failures are Sonner-only and
  create no transcript rows;
- input-operation reconciliation states are internal facts and appear nowhere.

## K. Current TUI Live Sections

This is the existing presentation inventory, not a proposed Desktop layout.

| Section | Current content |
|---|---|
| Runtime activity | Usually collapsed into status line |
| Queued or steered | Up to two live lines |
| Pending prompt | Question/approval line |
| Context usage | Usually collapsed into status line |
| Goal | Usually collapsed into status line |
| Status | One line |
| Input | Multiline composer |
| Picker | Active picker |
| Help | Help pane |
| Prompt history | Selected history item |
| Pending tools | One or more spinner lines |
| Run state | Declared; not independently appended |
| Input reconciliation | Represented through input/status paths |
| Session status | Represented through status line |
| Session identity | Represented through status line/chrome |
| Compaction | Represented through status line |

## L. Integrity And Suppression Classes

| Item | Current behavior | Audit status |
|---|---|---|
| Recoverable malformed user/assistant/tool/notice row | Forced `O`, expandable fallback | Not a product item: impossible contract violation; no row-specific fallback UX |
| Unrecoverable malformed user/assistant/tool/notice row | Forced `D`, non-expandable placeholder | Not a product item: impossible contract violation; no placeholder UX |
| Empty unknown developer message | Detail diagnostic row | Locked: one expanded flat Diagnostic row with unknown type/source metadata and integrity explanation |
| Empty known developer message | Suppressed | Locked: no row |
| Unknown visibility discriminator | Validation/render failure | Architecture invariant |
| Explicit hidden visibility (`X`) | Removed before committed projection | Locked: honor persisted hidden intent and omit it |

Malformed-row compatibility branches remain implementation/legacy debt, not
Desktop product variants. Debug must fail fast. Release uses the separately
designed transcript contract-failure recovery path and never fabricates content,
role placeholders, or fallback rows.

## M. Shared Flat-Row Mechanics

- Every expandable flat row uses one full-width disclosure header with a leading
  semantic Lucide icon, localized type label, compact summary, trailing
  status/actions, and `ChevronRight`/`ChevronDown`.
- Clicking unused header space reversibly toggles the body below. Row-specific
  actions remain independent controls.
- Expansion is row-local component state. Virtualizer unmount resets the row to
  its audited type default; there is no destination map or persistence.
- Committed timestamps appear only while a flat row is expanded and follow the
  shared relative/absolute timestamp policy. Pending live rows never fabricate a
  timestamp.
- Expansion uses shared vertical disclosure motion. Reduced motion switches
  instantly.
- Flat rows are transparent at rest with no enclosing border/card and no
  hairline separators. A subtle full-row hover/focus wash provides interaction
  feedback.
- Row action buttons appear only while the row is expanded.
- Warning/error/success semantics change only the leading icon color, exactly
  following the TUI logic. Labels, content, borders, and row backgrounds remain
  neutral.
- Expanded bodies use the full available 1200px transcript-row width with no
  nested island. Content renderers add only their necessary internal padding.
- Expanded context, notice, Reviewer feedback, and Diagnostic rows expose one
  Copy action for the original full Markdown/source/text payload.
- Expanded generic tool rows expose one Copy all action combining full input
  and output.
- Expanded Shell command rows expose one Copy all action combining command and
  output/result.
- Expanded `write_stdin` rows expose one Copy action for returned terminal
  output only. Sent `chars`, process/session identity, timing arguments, and
  other call metadata are never copied.
- Expanded Patch/Edit rows expose one Copy action for the patch/diff only. The
  tool result is never included.
- Expanded completed Ask Question rows expose one Copy action containing the
  question, selected answer text rather than its index when present, and user
  commentary when present.
- Expanded Web Search rows expose no Copy action; source links and ordinary text
  selection remain available.
- Expanded-row buttons use the shared icon-only Lucide tooltip-button treatment
  with accessible names.
- Successful Copy actions acknowledge through Sonner rather than changing the
  button glyph.
- A backgrounded Shell row expands to the exact full model-visible tool result.
  Its later Background process completion/kill row expands to the exact full
  model-visible notice/result at that chronological position. Desktop does not
  copy the TUI bug where expansion reveals nothing.
- Copying TUI means neither background row has a row-local Open process action.
  The separate process sidebar destination is reached through its own entry
  point.
- Combined Copy payloads concatenate the selected raw sections in display order
  separated by blank lines, with no headings and no rendered-DOM conversion.

## Contract Gaps Exposed By The Audit

- The current notice DTO conflates context, diagnostics, lifecycle notices,
  reviewer output, and process notices.
- Materialized and synthesized tool results are indistinguishable to clients.
- Running and completed tool states are separate projections without one
  committed row identity beyond the tool-call ID.
- Pending prompts are live-only and have no committed prompt-row counterpart.
- Background activity and its committed notice are only weakly linked.
- Reviewer substantive output currently uses duplicate/misaligned backend labels
  that must normalize to one typed Reviewer feedback contract.
- Several live DTOs are status facts, not transcript items; Desktop needs a
  deliberate projection rather than treating every feed message as a row.

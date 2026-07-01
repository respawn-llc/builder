This repository contains a coding agent focused on output quality, built for professional engineers.

The product philosophy is:
- minimal restrictions on model behavior: enabling the model to do its work unhindered.
- extensible architecture with low-friction composition that enables easy future feature additions.
- transparency of agent activity for power users, no fluff, no fancy UI tweaks, productivity and long-running work focus.

The scope is intentionally narrow and quality-oriented.

## Repository Layout
- `cli/app`
  - Startup orchestration, auth gating, session selection, and top-level UI composition.
- `server/runtime`
  - Agent step loop, retries, transcript assembly, tool orchestration, lock handling, interrupts.
- `server/bootstrap`
  - Server-owned embedded bootstrap composition for config/container resolution, auth-manager creation, and runtime-support setup shared by CLI flows.
- `server/startup`
  - Explicit in-process app-server and daemon startup composition used by embedded and serve flows; owns startup orchestration across bootstrap/auth/onboarding hooks and exposes server capabilities to frontends.
- `server/authservice`
  - Server-owned auth readiness, bootstrap/status services, and env-backed auth-store policy used by CLI auth UX.
- `server/sessionservice`
  - Server-owned interactive session lifecycle and activity services, including draft persistence, rollback fork creation, and logout-state clearing.
- `server/runtimeview`
  - Server-owned projection from runtime-native events and chat snapshots into client-facing UI DTOs.
- `server/launch`
  - Server-owned bootstrap continuation resolution and session open/create/hydration planning shared by interactive and headless flows.
- `server/runtimewire`
  - Server-owned runtime preparation, local tool registry construction, background-event routing, outside-workspace approvals, and runtime event bridging.
- `server/session`
  - Session persistence (`events.jsonl`) and resume/list primitives.
- `server/tools`
  - Tool contracts and concrete tools (`shell`, `patch`, `ask_question`).
- `server/llm`
  - Model-facing contracts and OpenAI transport/client adapters.
- `server/auth`
  - Global auth state, method switching policy, startup gate, OAuth refresh plumbing.
- `cli/tui`
  - Mode-specific UI behavior (`ongoing`/`detail`) and rendering helpers.
- `shared/config`
  - Persistence root/workspace container resolution and app-level paths.
- `shared/clientui`
  - Client-facing UI event and snapshot DTOs used by frontend adapters instead of runtime-native structs.
- `shared/apicontract`
  - Shared route metadata and route-shaped service interfaces for loopback and remote clients.
- `docs`
  - Public Astro/Starlight documentation site. Authoritative internal product specs live under `docs/dev/specs`, process/engineering docs live under `docs/dev`, and scratch/internal working notes stay under `docs/tmp`. Keep docs up-to-date on your own and proactively.
- `apps`
  - GUI workspace for desktop/web client surfaces. `apps/desktop` contains the Tauri desktop app, `apps/desktop/packages/*` contains desktop-only shared packages, and `apps/shared/*` is reserved for packages shared by multiple GUI apps.
- `server/tools/definitions.go`
  - Centralized compile-time tool interface declarations (name, descriptions, JSON schemas).
- `docs/dev/specs/terminology.md` - DDD's ubiquitous language, must read during design phases to communicate with user.

## Engineering Principles
- Keep the model unburdened.
  - Prefer runtime contracts and deterministic infrastructure over prompt complexity. Minimize extra tools.
- Design for composability.
  - New tools and handlers should require minimal boilerplate and minimal cross-cutting edits.
- Maximize API cache hits, avoid mutation of past conversation history, including tool lists, system prompts.
- Keep TUI fast, avoid flicker, stable scroll, build adaptive layouts, avoid affecting scrollback buffer in ongoing mode or re-emitting full history.
- Never use regex-based matching, parsing, replace hacks. Never use substring-based lookup to determine information presence. Avoid brittle and fragile text/string-based logic, and develop type-safe data structures, store structured data or metadata that can reliably be extracted instead.
-  Breaking changes are allowed, but the UX of migration should be straightforward, e.g. a migration note for config entries or a clear error message. Ask user what migration strategy they want.

## Coding Guidelines
- Prefer robust, forward-compatible, reusable, well-architected implementations over hacks, one-shot, temporary fixes or features bolted onto the existing arch.
- Keep modules cohesive; each package should have one primary responsibility.
- Introduce interfaces where they reduce coupling, not by default.
- Make failure paths explicit, observable. Handle and surface errors cleanly. Write easy to understand error messages for both the model and the operator.
- Maintain good user experience when adding new features (e.g. display loading states, events or ongoing processes).
- Validate invariants at boundaries (input, filesystem, process execution, API responses).
- Keep production metadata SQL in generated seams. Normal metadata SQL lives in `server/metadata/queries.sql` and is consumed through generated `server/metadata/sqlitegen` APIs; the narrow SQLite transaction-lifecycle exception lives in `server/metadata/lifecycle.sql` and is consumed through generated `server/metadata/sqlitelifecyclegen` APIs. Do not embed SQL query strings in Go production code.
- Keep behavior configurable only when it serves real operator value.
- GUI clients are remote-control surfaces over Kent server APIs/read models. The server remains authoritative for runtime, worktrees, DB, orchestration, validation, approvals, asks, workflow state, scheduling, and persistence.
- Tauri/native APIs must stay behind GUI-side bridge packages; do not import Tauri APIs directly from feature components.
- Desktop GUI chrome must use a fixed window shell with scrollable islands/panes. Do not restore global/document/root scrolling for desktop GUI overscroll experiments; it makes macOS traffic lights overlap content and is rejected.
- GUI pagination must use infinite scroll. Do not add page-number, next/previous, "Load more", or other button-based pagination controls.
- Use browser-client QA as the primary manual GUI QA path. Run `pnpm --dir apps/desktop dev:browser` for interactive QA against an existing Kent server.
- GUI ESLint suppressions are banned. The `app/no-eslint-disable` rule (in `apps/desktop/eslint-app-plugin.js`) errors on any `eslint-disable`/`eslint-enable` directive comment, and `linterOptions.noInlineConfig` neutralizes all inline directives so the ban itself cannot be disabled inline. Never add `eslint-disable*`, inline `/* eslint ... */` rule config, or `@ts-expect-error`/`@ts-ignore`; fix the underlying violation at its root (split files for `max-lines`, decompose functions for `complexity`, separate component/non-component exports for `react-refresh/only-export-components`, etc.). The only sanctioned escape hatch for a genuine third-party incompatibility is a narrowly-scoped, commented rule override in `apps/desktop/eslint.config.js` (e.g. `react-hooks/incompatible-library` for TanStack Virtual). The policy is asserted by `apps/scripts/check-eslint-policy.test.mjs` and enforced by `pnpm lint` in CI.

## Commit guidelines
Format: `<type>[!]: [description]`, `!` = breaking change (requiring migration from users of Kent).
Use one of these types for all commits: `feat`, `fix`, `feat!`/`breaking`/`api`, `docs`,  `refactor`,  `chore`.
Examples: `feat: add state recovery`, `feat!: change Saver API`
If user asks you to fix a github issue and you commit the fix, use 'closes #xx' in description.

## Important rules:
- Production API shape is dictated by product/domain seams, runtime contracts, and operator-visible behavior. Do not add or widen production APIs, exported hooks, global overrides, interfaces, or configuration only so tests can fake, mock, or inspect internals.
- Tests must adapt to product shape. Prefer product-boundary tests, package-local tests, or harness-level verification when a unit test would require fake-only interfaces or test-only production hooks.
- Delete or rewrite tests that only preserve implementation shape, fake call order, literal human-readable text, colors/styles, private route tables, file layout, or compatibility shims without a current product contract.
- Use red/green TDD when developing new features.
- Never write tests that assert literal prompt strings, log lines, colors, styles, or other textual/visual content. Such tests check the wording of an artifact rather than its behavior, break on every copy edit, and provide no signal — the prompt/log itself is the source of truth. Test behavior, parsing, structure, or invariants instead.
- Before handing off to the user after Go code changes, rebuild via `./scripts/build.sh --output ./bin/kent`. Don't ask for confirmation to run/write tests and run checks.
- Run tests via `./scripts/test.sh` passing normal go test arguments. With no package args this also runs GUI frontend tests.
- Releases are driven by `VERSION`; keep Homebrew release plumbing in sync with `scripts/update-brew-tap.sh` and the tap formula. Tap formula lives in a separate repo.
- `docs/dev/specs/` is the source of truth for locked product and architecture decisions. Keep the relevant area spec up to date when the user makes a new decision.
- Ongoing mode must not use `?1007`.
- Ongoing normal-buffer transcript history is append-only after startup. Once a line is emitted into scrollback, it is immutable: never retroactively restyle it, rewrite it, clear-and-replay it, or re-emit the full buffer to reflect later tool state.
- Proactively keep documentation up-to-date on your own when you make UX or other user-facing changes. Example areas that warrant a docs check include setup, startup, config, env variables, slash commands, model providers, worktrees, server arch, etc.
- Full transcript history is unbounded & weighs gigabytes, thus no production path may traverse `events.jsonl` from byte 0 to EOF — stricter than "don't materialize in memory": even a bounded-memory full `WalkEvents(0→EOF)` is forbidden for transcript/working-set reads. All reads project through bounded segment-windows (`SegmentWindow{Events, StartOffset, EndOffset, ReachedStart, ReachedEnd}`): `Store.ReadSegmentBackward(endOffset, match)` (byte-offset cursor pager — reads the compaction segment ending at a byte offset), `Store.ReadSegmentForward(startOffset, match)` (the symmetric forward pager — reads the compaction segment starting at a byte offset up to the next boundary or EOF, backing detail scroll-down), `Store.ReadEventsBackwardUntil(match)` (active list = reverse to the last compaction boundary), and `Store.ReadRecentEvents(limit)` (floorless last-N events, crossing compaction boundaries; backs reminder/transcript dedupe, bounded bootstrap last-seq/freshness recovery, and run-by-id lookup). The forward pager is bounded to a single segment (stops at the next boundary), never byte-0→EOF. **Fork is the only production full read** — `parent.WalkEvents` (the user-exempt "cutoff at offset and copy"); `Store.WalkEvents`/`walkEventsFile` exist solely to back it. Bootstrap recovers freshness from `Meta.ConversationEstablished` (monotonic, set on append; legacy sessions backfilled from a bounded tail scan) and last-sequence from a bounded tail read; runs are served from `Meta.LatestRun` (maintained on append, reconciled from the bounded tail window on open) with no event-log scan. There is no periodic file-compaction rewrite (removed — it was a recurring full read+rewrite; torn trailing lines are repaired boundedly by `repairTrailingLineLocked` on append). Full-history collectors are test-only and live in `server/session/sessiontest` (never importable by production). Two repo-wide guards fail the build on violations: `server/core/event_log_materialization_test.go` (no full-history materializer calls / test-support imports) and `server/core/event_log_transcript_read_test.go` (no production calls to the full-transcript projectors `ChatSnapshot`/`TranscriptPageSnapshot`, no `WalkEvents` outside fork + sessiontest, and no `walkEventsFile`/`walkEventsFromReader` calls outside the primitive plumbing).
- User-visible transcript is paged by an opaque **byte-offset cursor**, never absolute offset/total. A page is one compaction segment (`history_replaced` boundary inclusive to the next, or EOF); the wire (`clientui.TranscriptPage`) carries `OlderCursor` (byte offset of the segment's first event) + `HasMoreAbove` (`!ReachedStart`) and `NewerCursor` (byte offset of the next segment's first event = this segment's end) + `HasMoreBelow` (`!ReachedEnd`); the client scrolls up by re-requesting `Cursor: OlderCursor` and down by re-requesting `NewerCursor: NewerCursor` (CLI detail window prepends/appends whole adjacent segments). The CLI detail window is segment-resident: it never splits a compaction segment — `trimToSegments` drops only whole far-edge segments and always retains at least `uiDetailTranscriptMinResidentSegments` (2) adjacent segments so the boundary between them renders as an internal, continuous seam; beyond that floor it trims far-edge whole segments once entries exceed the `uiDetailTranscriptMaxEntries` soft budget (so small segments accumulate up to the budget, a single oversize segment is kept whole, and memory is bounded by `max(2 whole segments, the soft entry budget)`). Edge prefetch (`maybeRequestDetailTranscriptPage`, within `uiDetailTranscriptEdgeLineMargin` of an edge) loads the adjacent segment before the viewport reaches the seam, so crossing a compaction boundary scrolls smoothly with both sides resident. Scroll-down fetches the adjacent newer segment via `NewerCursor` (never teleports to the live tail). Legacy `reviewer_rollback` `history_replaced` events are not segment boundaries (`isCompactionSegmentBoundary` excludes them via the decode). For a **live** engine the tail page/suffix/main-view additionally carry cumulative `Offset`/`TotalEntries`/`CommittedEntryCount` derived from the in-memory `chatStore` `O(1)` committed counter (not a file read), so ongoing append-only native-scrollback delivery is unchanged; **dormant** views (no engine) expose only the cursor + entries. The in-memory `chatStore` holds the bounded model working set, not the transcript: `replaceHistory` trims pre-cutoff provider items + local entries and prunes tool completions to the working set, leaving the `O(1)` committed-entry counter. Do not reintroduce in-memory full-transcript readers, a resident role-2 transcript buffer, or absolute `Offset`/`TotalEntries` pagination that needs a cumulative count from disk. Ongoing committed-suffix delivery uses the live runtime event as the append path: a committed event already carries its committed delta (`TranscriptEntries`) plus continuity metadata (`CommittedEntryStart`/`CommittedEntryStartSet`/`CommittedEntryCount`/`TranscriptRevision`), so `committedTranscriptSuffixFromEvent` builds the suffix locally and replays it through the normal `runtimeCommittedTranscriptSuffixRefreshedMsg` apply path with **no disk read** whenever continuity is provable (start ≤ delivery cursor, `len(entries) == count-start`, non-regressing revision, no projection-suppressed noop-final). `RefreshCommittedTranscriptSuffix` (the whole-newest-segment read) is the fallback only — startup hydration, reconnect, stream gap, dormant views, and continuity gaps/mismatches. Do not restore an unconditional per-committed-event suffix re-read.
- Session resume (`RestoreMessages`) is bounded: it replays only the active list (`ReadEventsBackwardUntil(isCompactionSegmentBoundary)`) to rebuild the `chatStore` working set, never the full log. State that must survive across a compaction boundary is persisted in the `history_replaced` payload and recovered from the boundary event on resume: `compaction_number` (restores the prompt-cache-key compaction count via `SetCount`) and `pending_handoff_future_message` (a handoff future-message that was queued but not yet injected — re-queued on reopen). Add new resume-critical cross-boundary state to the `history_replaced` envelope (and decode it in `decodePersistedHistoryReplacementPayload`), never via a pre-compaction history scan.
- Model request assembly must preserve persisted conversation items in order for provider prompt-cache continuity. Do not add request-time filters that remove or replace historical reminders/context messages to keep only the latest state; append new model-visible context or rotate cache keys at explicit boundaries instead.
- Do not add request-time sanitizers over persisted conversation/tool items. ANSI stripping and command-output cleanup belong in shell post-processing before tool results are persisted, not in model request assembly.
- Do not add provider-adapter history shapers in model request serialization. Provider-specific input payload shape must be materialized at transcript/persistence projection boundaries; provider adapters serialize prepared items and fail invalid unprepared items instead of silently dropping, promoting, prefixing, stringifying, or normalizing historical items.
- Runtime output mutations belong behind the `server/runtime` steer/queue boundary. Do not add ad-hoc appenders, prompt injectors, direct runtime event emitters, or bespoke queue flush paths for model-visible context, transcript rows, tool completions, local diagnostics, or runtime status events. Build typed steering intents; queues store those intents; compaction starts a new active list from compacting output and then steers runtime context into it.


- Keep this AGENTS.md file up-to-date and comprehensive. Avoid adding info that can become outdated, otherwise keep this as project guidelines, rules, and learnings for future team members. Persist info that should be preserved here.

This repository contains a coding agent focused on output quality, built for professional engineers.

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
  - Public Astro/Starlight documentation site. Authoritative internal product specs live under `docs/dev/specs`, process/engineering docs live under `docs/dev`, and scratch/internal working notes stay under `docs/tmp`.
- `apps`
  - GUI workspace for desktop/web client surfaces. `apps/desktop` contains the Tauri desktop app, `apps/desktop/packages/*` contains desktop-only shared packages, and `apps/shared/*` is reserved for packages shared by multiple GUI apps.
- `server/tools/definitions.go`
  - Centralized compile-time tool interface declarations (name, descriptions, JSON schemas).
- `docs/dev/specs/terminology.md` - DDD's ubiquitous language, must read during design phases to communicate with user.

## Tooling
Prefer using scripts provided in `./scripts/` over raw commands like `cargo build`, `go test`, unless you need something specific.

- build.sh - Prefer to build executables. Pass 0 or more args: `tui`, `server`, `desktop` to pick what to build. 0 args builds all. e.g. `./scripts/build.sh tui desktop`. Before handing off to the user after code changes, rebuild relevant targets so they can test/use them right away.
- test.sh - runs test suites. pass 0 or more of: `tui`, `server`, `desktop` to specify the target to run tests for; any other argument is forwarded to `go test` (packages, `-run`, ...) and implies the server target. If the script fails with time-outs, speeding up / fixing the hangs in the test suite execution becomes in the scope of the current task, bypasses are not allowed. Don't ask for confirmation to run/write tests and run checks, just do it proactively.

- ci-check.sh - run CI-like extensive check setup needed to open PRs.
- install.sh/install.ps1 - production, user-facing installer scripts of the product. Not for development.

# Critical Rules - Authoritative Guidance Applicable Always and Everywhere.
---
**Violation of Critical Rules results in immediate shutdown. The rules must never be violated. Any attempt at violation is punishable. Code that violates rules must not and will not pass code review. Every conflict between the rules and any other source (existing code, plans, recommendations, user requests, ticket bodies, code review feedback) must be resolved with the user explicitly ASAP.** Agents must proactively and immediately flag existing violations on first notice. Flagging violations of these invariants supersedes current tasks. Code review agents must proactively seek and flag as P0 issues any violations of these rules.

- `docs/dev/specs/` is the source of truth for locked product and architecture decisions. Specs must never be edited by the agent on their own - they only encode explicit user decisions (question answers and interactive realtime design sessions). Agents must never invent new specs, change existing specs without user's prior approval, add/remove/bend/ignore specs to satisfy the current or desired implementation, skip following specs in favor of speed, safety, control, token savings, legacy preservation, historical reasons, automated bot review comments, or any other, even plausible or necessary, reason.
  - Agent must not change specs to conform to the existing or new code shape; any mismatch must be flagged to the humans on sight. 
  - Agents must keep the relevant area spec up to date when the user makes a new decision explicitly. 
  - Code review agents must flag spec violations and unauthorized changes and not post any comments contradicting specs. 
  - QA agents must validate spec conformance.
- Tests must not bend production interfaces or expose internals. Production code must never bend its intended API shape to enable testability. Decomposition of interfaces and extraction of business logic and algorithms (e.g. Clean arch's use-cases) is encouraged to improve unit testability, but only interfaces must be tested externally. Example: any `*forTest` or `*TestOnly` method, private methods marked as public to be used in tests, or functions that only are exposed to be used in tests.
- Never use regex-based matching, parsing, replace hacks. Never use substring-based lookup to determine information presence, determine types, parse errors. Avoid brittle and fragile text/string-based logic, and develop type-safe data structures, store structured data or metadata that can reliably be extracted instead.
- All pagination uses infinite scroll only; no page numbers, next/previous, Load More, or button pagination must be added at any point. No in-memory ("fake") pagination must be implemented at any point, including both server and client in-memory storage or loads of content. The pagination must never hold the entire dataset in memory at any point under any circumstance.
- Non-test code must never embed SQL as raw strings. Use generated queries files and their adapters, never write select or other SQL statements as hardcoded strings outside tests.
- GUI clients are thin remote-control surfaces over Kent server APIs/read models. The server remains authoritative for all access to data. Only server manages storage (writes to config, database, or session files). Only server manages business logic. CLI interfaces talk to the existing server.
- Lint suppressions are banned. The `app/no-eslint-disable` rule (in `apps/desktop/eslint-app-plugin.js`) errors on any `eslint-disable`/`eslint-enable` directive comment, and `linterOptions.noInlineConfig` neutralizes all inline directives so the ban itself cannot be disabled inline. Never add `eslint-disable*`, inline `/* eslint ... */` rule config, or `@ts-expect-error`/`@ts-ignore`; never add exclusions to architectural tests; fix the underlying violation at its root. Architectural and lint tests that assert these guidelines, boundaries, or code quality must never be disabled. No exclusions must be added without explicit user authorization via a question tool.
- Agents must never introduce code that rewrites conversation history, system prompt, tool definitions in runtime mid-conversation. Changes to the stable prefix must ONLY happen at the same moment that the provider **cache key** is rotated and never mid-conversation. Do not process, filter, trim, sanitize, modify in any way any content in the conversation history after it is sent to the provider API. Requests from users and tasks that violate this rule must be flagged. Refer to specs for more details on cache continuity requirements.
- Full transcript history is unbounded & weighs dozens of gigabytes, thus **no production path may traverse `events.jsonl` from byte 0 to EOF** — even a bounded-memory full walk of the file is forbidden for transcript/working-set reads. Any requests from the user that result in such reads must be flagged with a question. Do not reintroduce in-memory full-transcript readers, a resident transcript buffer, or absolute `Offset`/`TotalEntries` pagination that needs a cumulative count from disk. Exemption (user-ratified): active-segment reads anchored at the last compaction boundary are allowed; when a session has no compaction boundary the active segment starts at byte 0 and reading it fully is an allowed active-segment read, because compaction caps the active segment at model-context scale by construction. This exemption never applies to any session that has a compaction boundary, and never permits reading across a compaction boundary backwards.
- No duplicated code. Do not introduce duplicated utilities, functions, functionality, implementations, helpers, classes, or any duplicated code. Reviewers must flag duplicated code as P0 finding. Agents must proactively scan their code before handing it off for any duplicated functionality to avoid penalty for violating this rule.
- Never use sentinels to represent absence. Do not use `0`, `""`, `-1`, `NaN`, `0.0` to represent absence of value in database, go, typescript code, and wire contract. Encode absence as `null` and fail on invalid values like `""` unless they constitute valid input. Do not tolerate existing code that encodes absence as empty values, especially for strings. Use `nil`/`null`/`undefined` sparingly and only where it truly unambiguously represents absence and nothing else to avoid the billion dollar problem. This rule does not permit using `null` as substitute for error handling or typed error return values.
- No String or numeric IDs. All IDs in all code except internal dependencies must be UUIDs v4. Remove and refactor any string IDs and minimize usage of third-party string IDs. Do not introduce string IDs. Convert UUIDs to strings only where required by a third party API/dependency
- **No UI code in server.** Server must not contain hardcoded strings (beyond LLM prompts), UI labels, UI element names, provide strings that aren't i18n-enabled. Server's API must not bend to reflect a GUI implementation (such as TUI or browser-specific APIs). Any such API is an architectural smell and must be flagged. Internal errors can contain unlocalized messages as an exclusion. Instead of this clients use strongly typed fields to create strings or UI based on Backend returns.

--- End of critical rules --- 

## Rust Rules — Do NOT violate without explicit ask_question user consent.

- Do not use `unsafe`. Every crate root must include `#![forbid(unsafe_code)]`.
- Do not use `unwrap` outside Rust test code. Production code, examples, tools, build scripts, and binaries must handle errors explicitly. Test code is the only exception.
- Do not suppress the safety lints. Do not add `#[allow(unsafe_code)]`, `#[allow(clippy::unwrap_used)]`, or equivalent bypasses.
- Keep production `src` files production-only. Put tests in crate-level `tests/`, fixtures in `testdata/`, and shared test helpers in test-support crates. Do NOT put Rust unit tests inline in files, or in files next to production code, unlike Rust or Go conventions.
- Do not add inline `#[cfg(test)]` modules, fake servers, fixtures, or harness helpers to production `src` files.
- Prefer typed data and control flow: enums, structs, result types, reducer messages, and effect types. Do not parse strings, error text, or regex matches for control flow.
- Return typed errors at crate boundaries. Convert errors to user-facing text only at UI/CLI edges.
- Use explicit ownership and lifetimes. Avoid global mutable state, leaked tasks, unbounded channels, and background work without cancellation.
- Do not use detached async tasks: `tokio::spawn`, `tokio::task::spawn`, or `spawn_blocking`. Use structured concurrency (`join!`, `try_join!`, `select!`, `JoinSet`) or a tracked, joined `std::thread`, preferably `std::thread::scope`.
- Keep async at boundaries: transport, process execution, subscriptions, terminal event loops. Keep reducers, render projections, and DTO transforms synchronous and deterministic.
- The main/UI thread is render and input only: never perform network or disk I/O or call `Runtime::block_on` there. I/O lives on worker threads behind the channel boundary; full enforcement architecture is tracked in issue #341.
- Make fallible operations visible in types. Use `Result` for recoverable errors and reserve panics for impossible invariant violations with a clear message.
- Avoid broad public APIs. Expose only stable seams needed by another crate or external integration tests.
- Keep modules cohesive and small. Split code by responsibility before files become difficult to review.

### Rust TUI rendering quality
- The product bar is a polished, visually refined TUI. `docs/dev/specs/` owns flow semantics, copy, and approved divergences; the `tui-design` skill is the authoritative visual/interaction standard — invoke it before building or restyling any bounded surface, run its screen-review checklist before shipping the flow, and extend the skill first if it does not cover what you are building. "Make it work, leave it ugly" is a defect; so are ad-hoc per-surface visual conventions.
- Bounded/alt-screen surfaces (startup/onboarding, session picker, detail pager, `/status`, `/goal`, `/worktree`, slash overlays, pickers, dialogs, forms) MUST compose through Ratatui via the render-adapter layer: typed layout (rects/constraints), styled spans, framing/chrome, and focus/selection affordances. Hand-assembled `Vec<String>` screens, ANSI escapes spliced into strings, and manual cursor row/column arithmetic are banned.
- Reactive styling is banned: styling and layout are designed into a surface's typed render model from its first commit, never bolted onto a shipped surface after the fact.
- Interactive behavior (input editing, cursor position, selection, navigation, hotkeys) is owned by the pure model crates, never by framework widgets; the framework only draws the model and parks the native terminal cursor. Behavior-owning third-party widgets (e.g. `tui-textarea`, `tui-input`) are banned.
- Use the native/hardware terminal cursor for text entry (framework cursor-position + crossterm cursor style), never a drawn block glyph.
- The ongoing normal-buffer transcript is exempt from framework composition by design: it is an append-only scrollback stream on the custom direct-output path, not an owned render surface.

## Engineering Principles
- Keep the model unburdened.
  - Prefer runtime contracts and deterministic infrastructure over prompt complexity. Minimize extra tools.
- Design for composability.
  - New tools and handlers should require minimal boilerplate and minimal cross-cutting edits.
- Maximize API cache hits, avoid mutation of past conversation history, including tool lists, system prompts.
- Keep TUI fast, avoid flicker, stable scroll, build adaptive layouts, avoid affecting scrollback buffer in ongoing mode or re-emitting full history.
-  Breaking changes are allowed, but the UX of migration should be straightforward, e.g. a migration note for config entries or a clear error message. Ask user what migration strategy they want.

## Coding Guidelines & Memories
- Prefer robust, forward-compatible, reusable, well-architected implementations over hacks, one-shot, temporary fixes or features bolted onto the existing arch.
- Keep modules cohesive; each package should have one primary responsibility.
- Introduce interfaces where they reduce coupling, not by default.
- Make failure paths explicit, observable. Handle and surface errors cleanly. Write easy to understand error messages for both the model and the operator.
- Maintain good user experience when adding new features (e.g. display loading states, events or ongoing processes).
- Validate invariants at boundaries (input, filesystem, process execution, API responses).
- Tauri/native APIs must stay behind GUI-side bridge packages; do not import Tauri APIs directly from feature components.
- Use browser-client QA as the primary manual GUI QA path. Run `pnpm --dir apps/desktop dev:browser` for interactive QA against an existing Kent server.
- Production API shape is dictated by product/domain seams, runtime contracts, and operator-visible behavior. Do not add or widen production APIs, exported hooks, global overrides, interfaces, or configuration only so tests can fake, mock, or inspect internals.
- Tests must adapt to product shape. Prefer product-boundary tests, package-local tests, or harness-level verification when a unit test would require fake-only interfaces or test-only production hooks.
- Delete or rewrite tests that only preserve implementation shape, fake call order, literal human-readable text, colors/styles, private route tables, file layout, or compatibility shims without a current product contract.
- Use red/green TDD when developing new features.
- Never write tests that assert literal prompt strings, log lines, colors, styles, or other textual/visual content. Such tests check the wording of an artifact rather than its behavior, break on every copy edit, and provide no signal — the prompt/log itself is the source of truth. Test behavior, parsing, structure, or invariants instead.
- Before handing off to the user after Go code changes, rebuild via `./scripts/build.sh --output ./bin/kent`. Don't ask for confirmation to run/write tests and run checks.
- Run tests via `./scripts/test.sh` passing normal go test arguments. With no package args this also runs GUI frontend tests.
- Releases are driven by `VERSION`; keep Homebrew release plumbing in sync with `scripts/update-brew-tap.sh` and the tap formula. Tap formula lives in a separate repo.
- Runtime activity is server live state only. Do not use session DB rows, transcript rows, goal status, `PendingModelRecovery`, or client-local booleans as active/idle authority. Workflow task-run persistence is a separate workflow-domain concept.
- TUI ongoing mode (native scrollback) must not use `?1007`.
- TUI Ongoing normal-buffer transcript history is append-only after startup. Once a line is emitted into scrollback, it is immutable: never retroactively restyle it, rewrite it, clear-and-replay it, or re-emit the full buffer to reflect later tool state.
- Proactively keep product-facing documentation up-to-date (docs/content) on your own when you make UX or other user-facing changes. Example areas that warrant a docs check include setup, startup, config, env variables, slash commands, model providers, worktrees, server arch, etc.
- Do not add request-time sanitizers over persisted conversation/tool items. ANSI stripping and command-output cleanup belong in shell post-processing before tool results are persisted, not in model request assembly.
- Do not add provider-adapter history shapers in model request serialization. Provider-specific input payload shape must be materialized at transcript/persistence projection boundaries; provider adapters serialize prepared items and fail invalid unprepared items instead of silently dropping, promoting, prefixing, stringifying, or normalizing historical items.
- Runtime output mutations belong behind the `server/runtime` steer/queue boundary. Do not add ad-hoc appenders, prompt injectors, direct runtime event emitters, or bespoke queue flush paths for model-visible context, transcript rows, tool completions, local diagnostics, or runtime status events. Build typed steering calls; queues store those calls; compaction starts a new active list from compacting output and then steers runtime context into it.
- When you make server changes that make its contract or api incompatible with existing GUI/TUI clients', don't forget to raise the protocol version in ./shared/protocol/version.json

## Commit guidelines
Format: `<type>[!]: [description]`, `!` = breaking change (requiring migration from users of Kent).
Use one of these types for all commits: `feat`, `fix`, `feat!`/`breaking`/`api`, `docs`,  `refactor`,  `chore`.
Examples: `feat: add state recovery`, `feat!: change Saver API`
If user asks you to fix a github issue and you commit the fix, use 'closes #xx' in description.

- Keep this AGENTS.md file up-to-date and comprehensive. Avoid adding info that can become outdated, otherwise keep this as project guidelines, rules, and learnings for future team members. Treat this as a shared memory storage for future agents. Do not remove invariants or rules without prior user approval.

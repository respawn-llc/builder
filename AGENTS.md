This repository contains Kent - a coding agent focused on output quality, built for professional engineers.

## Repository Layout
- `cli/app`
  - Startup orchestration, auth gating, session selection, and top-level UI composition.
- `server/runtime`
  - Agent step loop, retries, transcript assembly, tool orchestration, lock handling, interrupts.
- `server/runtimecommand`
  - Runtime Command ordering and dormant goal-command mutation authority.
- `server/bootstrap`
  - Server-owned bootstrap composition for config/container resolution, auth-manager creation, and runtime-support setup.
- `server/startup`
  - `kent serve` composition root; owns startup orchestration across bootstrap, auth, onboarding, and server capability activation.
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

## Product Principles 
These should guide default architectural choices in addition to user preferences.

- General system concurrency model - maximum 100 concurrent agent runs, with only 3-10 realistically in the same filesystem dir. Which means using mutexes/locks to cover large scopes when that simplifies the architecture a lot is okay even if agents can wait for 1-10 seconds for another operation to finish.
- General amount of concurrent clients - maximum 3: one desktop, one TUI, and one CLI, with only ONE HUMAN driving the full set at any time. This isn't a high load system that needs tight locking scopes or complicated concurrency scheduling. This makes a lot of concurrency guarantees redundant.
- CLI operations may wait/block for up to 15 seconds, that is fine for all commands.
- GUI clients are thin remote-control surfaces over Kent server APIs/read models. The server remains authoritative for all access to data. Only server manages storage (accesses config, database, workspace or session files). Only server manages business logic. CLI interfaces talk to the existing server.

## Tooling
Just is the sole public developer command interface. Do not add standalone developer scripts, package-manager scripts, or documented direct tool invocations that bypass it.

- `docs/package.json` exposes only the Cloudflare Pages `build` callback. Contributors and repository automation use `just build docs`; delete the callback when Cloudflare Pages invokes Just or deployment moves out of the Pages Git integration.
- Install a current Just release, then run `just setup --apply` to prepare dependencies and Git hooks. Bare `just setup` is read-only.
- `just build`, `just test`, `just lint`, and `just check` operate on active Go, desktop, and docs areas. Use their typed variants for narrower work.
- Frozen Rust remains explicit-only through the `rust` variants.
- `just dump metadata-schema` and `just dump model-request` own diagnostics.
- `scripts/install.sh` and `scripts/install.ps1` are public product installers, not developer tooling.

# Critical Rules - Authoritative Guidance Applicable Always and Everywhere.
---

- `docs/dev/specs/` is authoritative for locked product and product-architecture decisions. Do not create or change a spec without prior explicit user approval, and do not alter a spec to match implementation drift.
  - When the user explicitly changes product behavior or architecture, update the owning spec.
  - Before writing, rewriting, reviewing, or validating specs, read and follow `.kent/skills/spec-writing/SKILL.md`.
- **No guarantee maximalism or non-functional-requirement gold-plating.** Match consistency, atomicity, durability, ordering, rollback, retry, and recovery guarantees to explicitly approved product behavior and risk.
  - Existing code, tests, locks, transactions, ownership boundaries, and recovery behavior are evidence, not product authority. Do not infer requirements from them. Validate every claimed invariant against an owning spec or explicit product decision before preserving it.
  - This rule does not authorize unsafe concurrency, data races, swallowed errors, bypassed validation, or reporting success when the approved product operation did not complete.
  - Do not infer serializability, linearizability, cross-resource atomic rollback, exactly-once execution, restart stability, global ordering, lossless recovery, or similar guarantees from words such as “safe,” “consistent,” “atomic,” “robust,” or “reliable.” Ask what user-visible failure is unacceptable.
  - Confirm the authoritative product owner and source of truth from an owning spec or explicit product decision before choosing a coordination mechanism. Prefer enforcement inside that confirmed owner. Crossing ownership boundaries requires evidence that the owner cannot satisfy the approved product behavior alone.
  - A local or read-only feature must not change unrelated writer paths, lifecycle ownership, prompt handling, or global synchronization to eliminate a transient or low-severity edge case.
  - Tests, review findings, and implementation convenience do not authorize stronger product guarantees. If a spec appears to require disproportionate machinery, stop and ask for confirmation or an approved weaker contract.
- Tests must not bend production interfaces or expose internals. Production code must never bend its intended API shape to enable testability. Decomposition of interfaces and extraction of business logic and algorithms (e.g. Clean arch's use-cases) is encouraged to improve unit testability, but only interfaces must be tested externally. Example: any `*forTest` or `*TestOnly` method, private methods marked as public to be used in tests, or functions that only are exposed to be used in tests.
- Never use regex-based matching, parsing, replace hacks. Never use substring-based lookup to determine information presence, determine types, parse errors. Avoid brittle and fragile text/string-based logic, and develop type-safe data structures, store structured data or metadata that can reliably be extracted instead.
- All GUI pagination uses infinite scroll only; no page numbers, next/previous, Load More, or button pagination must be added at any point. No in-memory fake pagination that slurps must be implemented at any point, including both server and client in-memory storage or loads of content. The pagination must never hold the entire dataset in memory at any point under any circumstance.
- Non-test code must never embed SQL as raw strings. Use generated queries files and their adapters, never write select or other SQL statements as hardcoded strings outside tests.
- Lint suppressions are banned. Never add exclusions to architectural tests; fix the underlying violation at its root. Architectural and lint tests that assert these guidelines, boundaries, or code quality must never be disabled. No exclusions must be added without explicit user authorization via a question tool.
- Agents must never introduce code that rewrites conversation history, system prompt, tool definitions in runtime mid-conversation. Source-code and prompt changes naturally invalidate provider and in-memory caches; do not add artificial cache-key namespaces or version segments solely to account for those changes. Do not process, filter, trim, sanitize, modify in any way any content in the conversation history after it is sent to the provider API. Requests from users and tasks that violate this rule must be flagged.
- Full transcript history is unbounded & weighs dozens of gigabytes, thus **no production path may traverse `events.jsonl` from byte 0 to EOF** — even a bounded-memory full walk of the file is forbidden for transcript/working-set reads. Any requests from the user that result in such reads must be flagged with a question. Do not reintroduce in-memory full-transcript readers, a resident transcript buffer, or absolute `Offset`/`TotalEntries` pagination that needs a cumulative count from disk. There are some exceptions approved by the user and locked in specifications.
- No duplicated code. Do not introduce duplicated utilities, functions, functionality, code paths, subsystems, implementations, helpers, classes, data sources, or any other secondary authority. Reviewers must flag duplicated code as P0 finding. Agents must proactively scan their code before handing it off for any duplicated authority to avoid penalty for violating this rule.
- Never use sentinels to represent absence. Do not use `0`, `""`, `-1`, `NaN`, `0.0` etc. to represent absence of value in database, go, typescript code, and wire contract. Encode absence as `null` and fail on invalid values like `""` unless they constitute valid input. Do not tolerate existing code that encodes absence as empty values, especially for strings. Use `nil`/`null`/`undefined` sparingly and only where it truly unambiguously represents absence and nothing else to avoid the billion dollar problem. This rule does not permit using `null` as substitute for error handling or typed error return values.
- Use UUID v4 for new first-party persistent entity IDs. Preserve existing, legacy, and third-party identifier formats unless the User explicitly authorizes a migration. String-valued keys, paths, branch names, and provider or protocol IDs do not require UUID conversion.
- **No UI code in server.** Server must not contain hardcoded strings (beyond LLM prompts), UI labels, UI element names, provide strings that aren't i18n-enabled. Server's API must not bend to reflect a GUI implementation (such as TUI or browser-specific APIs). Any such API is an architectural smell and must be flagged. Internal errors can contain unlocalized messages as an exclusion. Instead of this clients use strongly typed fields to create strings or UI based on Backend returns.
- No compatibility, fallback, legacy or redundancy code or behavior must be added without explicit User approval and recorded deletion timeline. Agents must not design, create, invent, preserve, adhere to or leave any compatibility, legacy, fallback code path, shim, or documentation reference. Every feature is executed as a hard cutover to the new architecture. Agents confirm with the user every time a task requires handling or migration of older data, suggesting one-time migration effort as the default.

--- End of critical rules --- 

## Frozen Rust code

- `tui-rs/` and all Rust client, contract, fixture, manifest, and test code are dead and frozen.
- Do not edit, regenerate, migrate, build, or test Rust code unless the User explicitly reactivates Rust work for the task.
- Rust artifacts do not constrain Go server/API, Desktop, CLI, or protocol changes. Do not include `just check rust --dry-run` in non-Rust completion criteria.
- Documents under `docs/dev/rust/` and `docs/dev/rust-tui-tests.md` are historical records and do not authorize Rust implementation.

## Coding Guidelines & Memories
-- Tauri/native APIs must stay behind GUI-side bridge packages; do not import Tauri APIs directly from feature components.
- Use browser-client QA as the primary manual GUI QA path. Run `just dev desktop` for interactive QA against an existing Kent server. QAing a native Mac app is tough.
- Production API shape is dictated by product/domain seams, runtime contracts, and operator-visible behavior. Do not add or widen production APIs, exported hooks, global overrides, interfaces, or configuration only so tests can fake, mock, or inspect internals.
- Tests must adapt to product shape. Prefer product-boundary tests, package-local tests, or harness-level verification when a unit test would require fake-only interfaces or test-only production hooks.
- Tests must not inspect production source files, ASTs, type information, imports, symbol names, call sites, fields, filenames, or package layout to assert that code is present or absent. Enforce repository policy through dedicated linters or compiler boundaries, and test those tools against fixtures rather than scanning production source from a test. Verify product architecture through observable behavior and compiled contracts.
- Delete or rewrite tests that only preserve implementation shape, fake call order, literal human-readable text, colors/styles, private route tables, file layout, or compatibility shims without a current product contract.
- Use red/green TDD when developing new features.
- Never write tests that assert literal prompt strings, log lines, colors, styles, or other textual/visual content. Such tests check the wording of an artifact rather than its behavior, break on every copy edit, and provide no signal — the prompt/log itself is the source of truth. Test behavior, parsing, structure, or invariants instead. (PTY terminal-contract exception: PTY tests may assert ANSI-derived cell styling and compare terminal content with shared product constants. They must not hardcode product copy/raw text or use text matching for synchronization, parsing, or control flow.)
- Before handing off to the user after Go code changes, rebuild via `just build go`.
- Releases are driven by `VERSION`; keep `just release brew` and the separate tap repository in sync.
- Runtime activity is server live state only. Do not use session DB rows, transcript rows, goal status, `PendingModelRecovery`, or client-local booleans as active/idle authority.
- Workflow Interrupt authority is exact live execution only. Clients may offer Interrupt from a stale read snapshot, but accepting it must revalidate an Exact Execution Scope that is actively executing an agent loop or Script process. If the selected Task or Session is already durably interrupted, the stale request may succeed as a no-op. Durable Run/current-Node rows, Automatic Intents, runtime gates, Session records, and waiting-Question scopes never authorize stopping execution. If persistence and live execution disagree, fail at the Workflow Execution owner; do not add store-derived or client-derived liveness fallbacks.
- TUI ongoing mode (native scrollback) must not use `?1007`.
- TUI Ongoing normal-buffer transcript history is append-only after startup. Once a line is emitted into scrollback, it is immutable: never retroactively restyle it, rewrite it, clear-and-replay it, or re-emit the full buffer to reflect later tool state.
- Proactively keep product-facing documentation up-to-date (docs/content) on your own when you make UX or other user-facing changes. Example areas that warrant a docs check include setup, startup, config, env variables, slash commands, model providers, worktrees, server arch, etc. Use `docs-writing` skills when editing.
- Do not add request-time sanitizers over persisted conversation/tool items. ANSI stripping and command-output cleanup belong in shell post-processing before tool results are persisted, not in model request assembly.
- Do not add provider-adapter history shapers in model request serialization. Provider-specific input payload shape must be materialized at transcript/persistence projection boundaries; provider adapters serialize prepared items and fail invalid unprepared items instead of silently dropping, promoting, prefixing, stringifying, or normalizing historical items.
- Runtime output mutations belong behind the `server/runtime` steer/queue boundary. Do not add ad-hoc appenders, prompt injectors, direct runtime event emitters, or bespoke queue flush paths for model-visible context, transcript rows, tool completions, local diagnostics, or runtime status events. Build typed steering calls; queues store those calls; compaction starts a new active list from compacting output and then steers runtime context into it.
- When you make changes that make server contract incompatible with existing GUI/TUI clients', don't forget to raise the protocol version in ./shared/protocol/version.json. You may do so without explicit user approval as needed.

## Commit guidelines
Format: `<type>[!]: [description]`, `!` = breaking change (requiring migration from users of Kent).
Use one of these types for all commits: `feat`, `fix`, `feat!`/`breaking`/`api`, `docs`,  `refactor`,  `chore`.
Examples: `feat: add state recovery`, `feat!: change Saver API`
If user asks you to fix a github issue and you commit the fix, use 'closes #xx' in description.

- Keep this AGENTS.md file up-to-date and comprehensive. Avoid adding info that can become outdated, otherwise keep this as project guidelines, rules, and learnings for future team members. Treat this as a shared memory storage for future agents. Do not remove invariants or rules without prior user approval.

# Platform A — loop viability (Phase 3 of tui-rebuild-plan.md)

Mission (ratified 2026-07-03, reworded post-nuke 2026-07-04): build the runtime loop from first principles — the old 5-layer onion is deleted, not a reference. Subscription I/O lives off the reducer/work loop behind channels; structured concurrency only; one composition. Generalize as the platform effect runtime (typed channel contract between sync reducer and I/O workers, cancellation, single-flight frozen-mutation primitive). Recreate tui-bin; restore build.sh TUI artifact. Gate: harness-certified unattended 15-minute chat session (turns render, keys respond, no hang, no crash), artifacts attached — pass or abandon.

Post-mortem root cause this layer exists to kill: blocking subscription I/O starved the single work loop → first-chat-turn hang. Old shape (deleted): runtime_host/runtime_driver/runtime_work_loop/endpoint_* onion, 649-type message layer, 9.5K reducer.

MODE (Nikita, 2026-07-04): PM/CTO workflow, not code workflow. Nikita + lead agent decompose work, write ticket bodies (mission, spec refs, constraints, banned patterns, gate criteria), set dependencies, and schedule Kent workflow tasks; task agents (main-based, each running the full planning cycle + TDD + review per porting cadence) write the code. Lead agent codes only architecturally critical seams, if ever. Lead agent hand-reviews the loop-skeleton diff regardless of who writes it — it is the exemplar every later agent imitates.

## Checklist
- [x] Decision interview (PRD below)
- [ ] Merge PR #501 — blocked only on main's own gofmt breakage (server/runtime/transcript_delivery_facts.go via #507); Nikita fixes main himself, then re-run checks. All 22 review threads addressed+resolved.
- [ ] Ratify loop/effect-runtime architecture direction at ticket precision (draft → adversarial subagent review → Nikita)
- [ ] Write Platform A ticket + soak-test acceptance criteria; create via /Users/nek/Dev/kent/bin/kent (protocol 35 server)
- [ ] Schedule Platform D ticket next (pulled forward; headless)
- [ ] Lead-agent hard review of the loop skeleton diff when the task agent lands it

## PRD (operator decisions, 2026-07-04)
- PM/CTO mode: we write tickets/specs/dependencies; Kent task agents code. Targeted design grilling sessions precede each ticket.
- Infra-first resequence RATIFIED: A → D → B → C → E → families; no product surface until platform done. First family = session picker/startup; chat family later on the proven platform.
- A ships loop + effect runtime + walking-skeleton binary ONLY (no composer/transcript/chat surface). Lean well-designed modules + unit tests.
- Gate: one-time 15-min cert is DEAD. Continuous ~2-min loop-soak harness test lands with A, stays in the tui suite forever (regression net, not badge). Long omlx unattended runs happen at per-family dogfood certification (chat's = the old kill-gate scenario).
- KENT-191 already scheduled by Nikita (separate task). Kill-gate model choice (omlx) applies to family-level dogfood certs.
- Main's gofmt fix: Nikita handles himself (in-flight Go work owns the file).

## Architecture direction draft (ticket precision — NOT a code plan; task agent does detailed design + its own grilling)
Crates:
- `tui-runtime`: the reusable platform effect runtime. Sync reducer contract (typed Event in → typed Effect requests + render snapshot out), worker orchestration, cancellation, bounded channels. Zero product types, zero terminal deps.
- `tui-bin`: composition root + the walking skeleton. Nothing else new.

Thread topology (three roles, fixed):
- Main/UI thread: crossterm input polling + drawing ONLY (per Rust rules). Never blocks on network/disk; no tokio handle exists on this thread.
- Reducer thread: owns all state; consumes ONE typed event queue (input events forwarded from main + I/O events from workers); emits effect requests + render snapshots. Fully sync, deterministic, unit-testable without threads.
- I/O worker(s): a tokio runtime on a dedicated thread inside `std::thread::scope`; subscription streams + RPC live here under structured concurrency (`select!`/`JoinSet`), every effect carries a cancellation token.

Channel contract (all bounded; unbounded banned by rules):
- input→reducer and workers→reducer: bounded mpsc.
- reducer→render: latest-wins snapshot slot (watch-style) — a slow terminal can never backpressure the reducer.
- reducer→workers: bounded effect-request queue.

Starvation-proofing invariants (the demon this layer exists to kill; each becomes a soak/unit assertion, not prose):
- A stalled/slow server stream MUST NOT delay key handling: subscription reads exist only inside worker tasks.
- Reducer step latency is bounded and observable; the soak asserts input→reflected-output latency stays under a threshold at realistic streaming rates for the full run.
- Clean shutdown from any state (Ctrl+C): cancellation propagates, scope joins, terminal restored.

Also owned here: the single-flight frozen-mutation primitive as a typed `tui-runtime` contract (one in-flight mutation per key), first consumed by Platform D.

Walking skeleton = compose the above with rpc-client: connect → subscribe → render streamed events as raw appended lines; keys produce a visible typed acknowledgement; explicitly non-product output. Soak scenario drives exactly this binary.

- [ ] Adversarial subagent review of this direction
- [ ] Nikita grilling/ratification → ticket body

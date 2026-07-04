# Rust TUI Demolition + Merge-to-Main

Operator decisions (2026-07-04, chat):
- Merge rust-harness → main so Kent workflow tasks (which can only base off main) can drive the rebuild. Nothing ships to production until dogfood threshold.
- Nuke, don't operate: condemned structures are deleted outright so task agents never see the bad exemplar on main. Platform A reworded from "de-block the existing loop" to "build the loop from first principles"; kill gate unchanged.
- Sequencing: demolition lands ON rust-harness first, then ONE PR to main (main never carries the onion).
- Keepers (ratified in tui-rebuild-plan.md): tui-core (green tests), client-contracts, rpc-client. Condemned: tui-bin 5-layer loop onion (runtime_host/runtime_driver/runtime_work_loop/endpoint_*), tui-app 649-type message layer + 9.5K reducer.rs.
- Open carve-out (decide in recon): does tui-bin's working startup-selection path survive (as BUI-181 harness live target), or is it entangled with the onion and dies too? Default: nothing survives that we wouldn't want imitated.

## Checklist
- [ ] Recon: tui-rs workspace layout, crate deps, what depends on condemned code, startup-path entanglement, CI/policy coupling (manifest-check, file-size baselines)
- [ ] Decide cut line (carve-out ruling; ask user only if genuinely ambiguous)
- [ ] Design: post-demolition minimal state (what compiles, what the binary does, guard baselines)
- [ ] Adversarial review of the cut plan (subagent)
- [ ] Execute demolition commit(s) on rust-harness
- [ ] Green: cargo build/test workspace + ./scripts/ci-check.sh rust-relevant stages
- [ ] Update docs/dev/rust/tui-rebuild-plan.md (Platform A rewording + demolition recorded)
- [ ] Code review subagent on the diff
- [ ] Push rust-harness, open PR to main, hand off to operator for merge
- [ ] Cleanup + next steps note

## Recon findings
- Workspace: crates/{client-contracts, rpc-client, test-support, tui-app, tui-bin, tui-core} + tools/manifest-check. Deps: tui-bin → tui-app → (tui-core, contracts, rpc-client); rpc-client → contracts (+test-support dev-dep); tui-core standalone.
- LOC: tui-app 53,416 (132 files), tui-bin 47,433 (117 files), tui-core 12,791 (34 files + 27 test files), contracts 4,041, rpc-client 2,765.
- tui-app and tui-bin have ZERO crate-level tests. Only tui-core/contracts/rpc-client are tested — the "keeper = green with tests" criterion condemns both big crates wholesale.
- Style check (worktree_surface.rs sample): `error_text: String`, `selected_id: String` — sentinel/string-ID violations of current Rust rules; port-era exemplar quality confirmed bad. The reducer/message-layer/onion are the hearts of their crates — narrow excision leaves 90K LOC of orphaned untested code; not a real option.
- ratatui_kit lives INSIDE tui-bin (per-surface files, port-era) — dies with the crate.
- Coupling to fix on deletion: workspace Cargo.toml members + workspace.dependencies + Cargo.lock; scripts/test.sh (builds -p tui-app -p tui-bin explicitly, line ~224); scripts/build.sh (tui target copies target/debug/tui-bin to output, line ~81); scripts/build_test.go (asserts binary-copy behavior, fake cargo writes tui-bin); lint_policy.rs shrink-only baseline table has 34 rows for condemned crates (prune; stale rows harmless but wrong).
- tui-rs/goal.md: untracked, still-valid process rules for the rebuild (references gitignored plan path — keep untracked).
- BUI-181 impact: whole-crate nuke leaves NO binary on rust-harness tip; harness certifies against its own branch (61142d72, pre-nuke binary) until Platform A ships the new loop.

## Cut line (RULED 2026-07-04: whole-crate nuke; operator: "cut anything you don't like, or what smells, idgaf")
Delete crates/tui-app and crates/tui-bin ENTIRELY (~100,849 LOC). Keep tui-core, client-contracts, rpc-client, test-support, tools/manifest-check, build-support (integration harness used by keepers). No stub binary — Platform A creates tui-bin fresh. Quality bar (operator): "Rust tui will be impeccable. Code is dirt cheap, architecture and product quality are priceless."

## Execution checklist
- [x] git rm -r tui-rs/crates/tui-app tui-rs/crates/tui-bin (253 files)
- [x] tui-rs/Cargo.toml: removed 2 members + 2 workspace.dependencies self-entries + pruned 9 unused third-party deps (base64, getrandom, sha2, yaml_serde, rustix, proc-macro2, unicode-normalization, ureq, vte). RULED 2026-07-04: ratatui + crossterm STAY in workspace.dependencies — settled stack, pinned for Platform A/B.
- [ ] Squash rewrite (operator ask): rebase branch onto origin/main, reset --soft, re-commit as specs + already-demolished foundation so condemned code never enters main history; verify tree byte-identical to reviewed state; then push + PR.
- [x] Cargo.lock minimally pruned (restored original, cargo check re-resolved: −681 lines pure removal; full generate-lockfile rejected as over-broad)
- [x] lint_policy.rs: pruned the 34 baseline rows
- [x] scripts/test.sh: dropped `cargo build -p tui-app -p tui-bin`
- [x] scripts/build.sh: --tui-output flag + usage + binary-copy logic removed; run_tui_build = workspace build only
- [x] scripts/build_test.go: rewritten to the one surviving contract (locked workspace cargo build args)
- [x] CONTRIBUTING.md build.sh paragraph updated
- [x] git rm docs/dev/rust-tui-replacement-readiness.md
- [x] docs/dev/rust-tui-tests.md: example switched to tui-core; quick-xml advisory ignores documented
- [x] UNPLANNED: fresh RUSTSEC db broke cargo-deny (RUSTSEC-2026-0194/0195, quick-xml via syntect→plist→quick-xml, fix unreachable: latest plist pins quick-xml 0.39). Ignores added to deny.toml + new guard test ignored_quick_xml_advisories_stay_scoped_to_syntect_transitives + doc note. Pre-existing breakage, not caused by demolition.
- [x] Green: ./scripts/test.sh tui AND go test ./scripts/ AND gofmt/cargo fmt
- [x] tui-rebuild-plan.md: Platform A reworded (build from first principles; recreate tui-bin + restore binary artifact output); demolition ruling recorded
- [x] Committed d2340807 on rust-harness (264 files, +25/−101,984)
- [x] Review findings addressed: P0 clippy (3 lints in tui-core fixed by refactor — EntryLayout extraction deduped the triplicated prefix/width computation, visual.rs 797→795 under its shrink-only baseline); P2 fixture: route_contract.rs + tui-route-contract.json DELETED (parity pin test of Go route table, banned test class); WON'T-FIX: rpc-client/tui-core workspace.dependencies entries stay (settled first-party stack, same ruling as ratatui/crossterm), deny.toml comments stay out (no-comments rule, rationale in docs).
- [x] Squash rewrite complete: origin/main moved mid-rewrite (PR #492 landed the spec suite via BUI-163; PR #497 landed the PTY harness — Go-side, zero refs to deleted Rust artifacts). Final shape: rust-harness = origin/main + single commit 2f81c932 "feat: implement rust tui foundation"; tree verified byte-identical to the gated merge tree; specs commit dropped (already on main). Backup: demolition-backup branch (pre-rewrite lineage).
- [x] Pushed; PR #501 open (https://github.com/respawn-llc/kent/pull/501); operator merges.

## Second-pass cuts (RULED 2026-07-04, post-PR review by operator)
- [x] tui-core nuked whole-crate (~12.8K src + ~5.6K tests/golden): transcript encoded port-era dual-truth rendering (hardcoded divider const + width fn), tests were literal-output pins (banned class); operator chose full-crate over keeping input/. Platform C rebuilds the text-editing core + width fixture from scratch.
- [x] Workspace deps pruned: syntect, pulldown-cmark, unicode-segmentation, unicode-general-category, unicode-width (all tui-core-only).
- [x] deny.toml: all four RUSTSEC ignores removed (syntect chain left the tree); tools/manifest-check/tests/dependency_policy.rs deleted whole (existed only for those chains); docs/dev/rust-tui-tests.md advisory paragraphs replaced with the standing pair-an-ignore-with-a-guard policy.
- [x] lint_policy baseline down to 2 rows (routes.rs, api.rs); manifest-check test fixtures renamed tui-core→sample-crate so the fossil name isn't an exemplar.
- [x] scripts/test.sh: dropped `-p tui-core`; Cargo.lock −230 lines.
- [x] rpc-client testdata/payloads corpus eliminated (subagent): hand-copied server JSON = duplicate wire-shape truth; minimal json! payloads inlined at 14 call sites (shared fragments as test-file helpers per no-duplication rule), read_contract_fixture helpers deleted, 4 phase_six fossil test names renamed; `#![recursion_limit = "256"]` added to rpc-client tests/integration.rs (macro expansion limit for the consolidated json! literal, test-only, not a lint suppression).
- [x] Green: ./scripts/test.sh tui exit 0; rpc-client 100/100; manifest-check 11/11 + repo policy OK; cargo-deny green with zero ignores; clippy --workspace --all-targets -D warnings clean; amend + force-push PR #501.
- OPEN FLAG for operator: docs/dev/specs/tui-chat-core.md names the deleted tui-core width-cursor fixture as authority — spec wording change needs explicit approval.
False positives left alone: cli/app kent-clipboard-*.png filenames, launch_planner_test "kent-cli" dir. AGENTS.md porting-cadence/rendering sections left untouched this pass (rule edits need separate approval; tui-app mention is forward-looking).

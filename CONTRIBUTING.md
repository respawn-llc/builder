# Contributing

Kent is intentionally narrow and opinionated. We value changes that improve reliability, output quality, reviewability, and long-term maintainability. The best contributions are focused, technically coherent, and aligned with the product direction.

## Start With an Issue

External contributions should begin with an issue before a pull request is opened.

This helps avoid wasted work and gives maintainers a chance to confirm scope, approach, and fit. Once the issue has been triaged, a PR is welcome.

Changes are less likely to be accepted if they add broad configurability, plugin-style surface area, extra UI chrome, or product direction that conflicts with the repository's design principles.

## Product Boundaries

Kent is intentionally narrow. Feature proposals are expected to improve reliability, output quality, observability, long-running work, or composability without adding avoidable model burden.

These directions are part of the current product boundary and are unlikely to be accepted:

- **Native in-process subagent orchestration.** Use separate headless Kent runs through `kent run`, named subagent roles, shell scripts, tmux, or background shells. Keeping side agents as normal Kent processes makes them scriptable, inspectable, resumable, and easy to kill.
- **Plan mode as a dedicated product surface.** Frontier models can already plan, revise, and ask questions. Kent should not add a UI mode that constrains the model or encourages ceremonial planning output.
- **MCP as a first-class integration surface.** Kent prefers a small model-facing tool set and normal CLI programs. If a capability can be exposed as a command-line tool or script, that is usually the better integration path.
- **Extra UI chrome or vibe-coding surfaces.** Kent's UI should stay focused on terminal-native engineering work: steering, inspection, review, session control, and long-running execution.
- **Runtime toolset or model switching for active sessions.** Changing these mid-session can invalidate prompt caches and alter the model contract. Prefer per-session config, subagent roles, or new sessions.
- **Microcompaction.** Compaction should preserve continuity and cache behavior. Tiny frequent rewrites add cost and risk without enough benefit.
- **Built-in sandboxing as a trust boundary.** Sandboxing should be done with real isolation such as containers, VMs, or remote environments. Kent may support workflows that run inside those environments, but the CLI itself should not pretend that a fragile local sandbox is a security boundary.
- **A dedicated WebFetch tool.** Use shell-accessible tools or scripts that return raw Markdown, such as Jina Reader wrappers. This keeps web access transparent and avoids another model-facing tool.
- **Anthropic, Gemini, or Antigravity subscription usage.** These will not be supported unless their terms allow third-party harnesses. API-key or compatible-provider work should still fit the normal provider capability model.

This list is not a substitute for design review. If a proposal appears to conflict with a boundary but solves an important reliability or quality problem, open an issue and describe the tradeoff clearly before writing code.

## Development Setup

Current prerequisites:

- Go `1.25`
- Node `22` and `pnpm` `10` for docs work in `docs/`
- Current Node `25+` where available and pnpm through Corepack/packageManager policy for GUI work in `apps/`
- Rust toolchain for Tauri native builds in `apps/desktop`

If you want the repository pre-push hook locally, enable it with:

```bash
git config core.hooksPath .githooks
```

## Before Opening a Pull Request

For code changes, run:

```bash
./scripts/ci-check.sh all
```

`scripts/build.sh` builds repo targets. With no positional targets it builds `server`, `tui`, and `desktop`; pass targets to narrow the build, for example `scripts/build.sh server --output ./bin/kent` or `scripts/build.sh tui desktop`. Use `--skip-frontend` or `KENT_SKIP_FRONTEND=1` only for infrastructure contexts that intentionally do not need frontend validation.

`scripts/test.sh` tests repo targets. With no positional targets it tests `server`, `tui`, and `desktop`; pass targets to narrow the run, for example `scripts/test.sh server` or `scripts/test.sh tui desktop`. It does not pass arguments through to `go test`; use `go test ...` directly for targeted Go package runs.

For manual Go test runs outside the full check, use:

```bash
./scripts/test.sh
```

This keeps successful runs silent while still printing the captured test log on failure.

The script applies a 120s wall-clock cap by default. To reproduce CI's test
behavior locally, disable that script-level cap while keeping Go's own package
timeouts:

```bash
KENT_TEST_DISABLE_WALL_CLOCK_CAP=1 ./scripts/test.sh server
```

If you changed docs under `docs/`, also run:

```bash
cd docs
pnpm install --frozen-lockfile
pnpm test
pnpm build
```

If you changed GUI code under `apps/`, also run frontend-specific checks:

```bash
cd apps
pnpm install --frozen-lockfile
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

Native Tauri builds additionally require the Rust toolchain and platform-specific WebView/build prerequisites.

## Pull Request Expectations

Please keep pull requests small enough to review in one pass and make sure they are tied to a previously triaged issue.

A strong PR usually:

- solves one clear problem
- includes tests for behavior changes
- updates user-facing documentation when needed
- keeps `AGENTS.md` accurate when project guidance changes
- avoids unrelated cleanup in the same change set

Draft PRs are fine when they are clearly marked and linked to the issue.

## Questions

If you want to work on something and there is no issue yet, open one first. If an issue already exists, use that thread to discuss the work.

## AI Code Policy

AI-generated code is absolutely acceptable, **provided it matches the quality standards** of the repo.

Fully or mostly AI-generated PRs with no or little human review that do not adhere to the current project architecture or guidance & constraints (in short, "Slop") will be closed without notice or discussion with the author of the PR being blocked and reported.

Additionally:
- Do **not** leave "co-authored by <agent>" attributions
- Disclose the use of AI in the PR authorship
- Do **not** introduce additional AI configuration files such as `.cursorrules` to the project in an unrelated PR. Editing AGENTS.md is fine and encouraged.
- Do not leave elaborate AI-generated PR descriptions. Prompt the agent to leave succinct, readable, human-like descriptions that are to the point.

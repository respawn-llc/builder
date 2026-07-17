GUI workspace for Kent desktop/web client surfaces.

## Layout

- `desktop/` contains the Tauri desktop app.
- `desktop/packages/*` contains packages only used by the desktop app.
- `shared/*` is reserved for packages shared by multiple GUI apps. Do not add packages here until there is a second real consumer.

## Stack

- Use React, TypeScript, Vite, Vitest, pnpm workspaces, and Tauri v2.
- Keep Kent.s Go server authoritative for runtime, worktrees, DB, orchestration, validation, approvals, asks, and workflow state.
- Treat GUI as a remote-control client over Kent API/read-model contracts.

## Desktop TypeScript Ownership

- `desktop/src/app/**` owns shell composition, including startup under `app/startup/**`, routes, sidebars, providers, native-window controllers, and the developer showcase.
- `desktop/src/app-facade/**` is the feature-facing shell seam for app services, navigation, query keys, status, sidebar contracts, and native helpers. It may depend on public API/UI/native seams, but never on shell or features.
- Each `desktop/src/features/<feature>/**` directory is an isolated feature module. Features may depend on `@/app-facade`, `@/api`, `@/ui`, `@/i18n`, and public `@/shared/<capability>` entrypoints, but never on another feature, API internals/composition, shell, or the native package.
- Each `desktop/src/shared/<capability>/**` directory is an app-local reusable capability with its own entrypoint. Shared capabilities may use public app-facade, API, UI, vendor, and shared-capability seams; they do not depend on features or shell.
- `desktop/src/ui/**` owns generic visual primitives and presentation utilities and does not depend on other desktop production owners.
- `desktop/src/api/**` owns adapted models, raw schemas, transport, sockets, and client implementation. `@/api` is feature-safe; `@/api/composition` is restricted to shell startup and test support.
- `desktop/src/test-support/**` owns reusable test-only harnesses through capability entrypoints. Only categorized tests may import it, and feature-internal fixtures remain with their feature.
- `desktop/packages/native-bridge/**`, `desktop/src/i18n/**`, `desktop/src/vendor/**`, `desktop/src/types/**`, `desktop/test/**`, and `desktop/tooling/**` are explicit leaf owners.

Cross-owner imports use the target owner's public `@/…` entrypoint. Relative imports stay within one owner. The native package uses `@app/native-bridge`; vendor aliases must match their declared adapter paths exactly. Entrypoints remain narrow, declarative, and side-effect free.

Architecture lint applies to production, tests, type imports, re-exports, dynamic imports, CommonJS calls, and literal Vitest module APIs. Tests receive no general ownership exemption. Categorized tests may use relative imports only for `src-tauri/tauri.conf.json` and `src-tauri/capabilities/default.json`.

Boundary enforcement is fail-closed: every desktop TypeScript file and local dependency must be classified. Do not add grandfather lists, inline suppressions, deep-import shortcuts, or compatibility barrels. Any exception requires explicit approval, an adjacent configuration rationale, and focused policy-fixture coverage.

## Checks

- From `apps/`, run `pnpm install --frozen-lockfile`, `pnpm lint`, `pnpm typecheck`, `pnpm test`, and relevant build scripts after GUI code changes.
- `pnpm lint` enforces the architecture policy. `scripts/ci-check.sh all` runs the same workspace command, and pre-push delegates to that CI script.
- Use browser-client QA as the primary manual GUI QA path. Run `pnpm --dir apps/desktop dev:browser` for interactive QA against an existing Kent server.
- Tauri native builds require Rust toolchain plus platform-specific WebView/build dependencies.
- Commit `apps/desktop/src-tauri/gen/schemas/*.json` when Tauri regenerates them; they are generated, but keeping them in the repo avoids dirty editor/schema state on clean clones.
- Frontend dependency policy is enforced by `apps/dependency-policy.json` and `apps/scripts/check-dependency-policy.mjs`. New direct dependencies are blocked until they are added to the allowlist intentionally.
- TypeScript policy is enforced by `apps/scripts/check-typescript-policy.mjs`. Explicit `any`, including `as any`, is forbidden across the whole `apps/` workspace.
- `apps/pnpm-workspace.yaml` enforces `minimumReleaseAge: 10080` and `onlyBuiltDependencies: []`; do not bypass these without explicit maintainer approval.
- If `pnpm install` fails only because a toolchain transitive package is younger than 7 days, keep direct app deps strict and add a narrow `minimumReleaseAgeExclude` only after explicit review. Do not use age exemptions for direct app/runtime dependencies.

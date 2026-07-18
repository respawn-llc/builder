# KENT-278 Step 7 TUI design review

Reviewed July 18, 2026 against `.kent/skills/tui-design/SKILL.md`.

## Deterministic manual harness

Ran:

```sh
go test -v ./cli/app -run '^TestSessionPickerUpdateHeaderReviewHarness$' -count=1
```

The package-local harness renders `pending`, `available`, and `check-failed`
picker states at 120 columns and the 40-column minimum supported geometry. It
uses no live endpoint and applies the existing typed picker completion message.
The reviewer inspected the rendered wide and narrow frames, including the
selected session after the header height changed.

## Checklist evidence

- **Opens instantly — pass.** The pending frame renders the static picker shell
  before a result exists; Step 6's independent async command remains unchanged.
- **Narrow widths — pass.** The 40-column available and failed frames retain the
  version and a full update row. Only overflowing detail truncates with the
  existing ANSI-aware pipeline; no rendered line exceeded its terminal width.
- **Title, values, metadata hierarchy — pass.** `Kent v…` is first, the
  available/failed update row is immediately beneath it, then repository,
  authentication/model, and server metadata follow.
- **Semantic, restrained colors — pass.** Manual review used true color:
  available uses the existing Success token and failed uses the Error token;
  ordinary metadata retains the normal/muted hierarchy.
- **Repeated-row alignment — pass.** Session title/timestamp rows remain aligned
  in the harness at both widths after the extra header row changes the body
  budget.
- **Faint text only for secondary information — pass.** Preview and timestamp
  remain secondary; update status is full-strength and bold enough to scan.
- **Scrolling and selected-row visibility — pass.** The harness selects a row
  below the initial viewport. After the update row arrives, the selected row
  remains in the visible window; the available/failed header costs exactly one
  visible body line without producing an empty viewport.
- **Alt-screen suitability — pass.** The picker remains a full-screen navigation
  destination and continues to use the existing alternate-screen path.
- **Main scrollback restoration — pass.** This change does not alter the
  existing picker terminal enter/close path (`?1049`); normal-buffer transcript
  history remains outside the picker.
- **Cached/loading honesty — pass.** Pending intentionally shows no update row,
  rather than claiming a current version. The only persistent rendered states
  are the server-provided available or check-failed outcomes.

All ten screen-review checklist items apply to this surface; none is
non-applicable.

## Approved presentation checks

- Copy is `Update available: v<latest>` or `Update check failed: <cause>`.
- Success/Error styling is semantic; wording remains legible without color.
- The typed result remains visible for the picker lifetime after it arrives; a
  later picker open performs the existing Step 6 request and normally receives
  the server cache.
- The row is immediately below the Kent version, easy to scan, and does not
  create a second notice/status surface.

## Final integrated revalidation

Revalidated July 18, 2026 after the legacy main-session and `/status` update
lanes were removed.

- Built `./bin/kent-update-qa` with version `dev`, started it in isolated
  configured-server mode, and used the same binary as the TUI client. The
  reachable server normalized `dev` to `0.0.0` and rendered the available row.
- Opened the picker at 120 columns, then reopened it on the same server at 40
  columns. The cached available result reappeared immediately on both opens;
  the narrow frame stayed within 40 columns and retained the complete
  available row.
- Launched a persisted session directly with `--session`; its main UI showed
  no update presentation. `TestStatusOverlayRefreshDefersRuntimeAndAuthReadsWithoutUpdateStatusCall`
  separately verifies the `/status` surface makes no update-status call, so
  the removed lane cannot reappear through status collection.
- Re-ran the deterministic 120/40-column harness for pending, available, and
  failed states. It remains the error-row oracle; live server configuration is
  not used to manufacture a failure.

All ten screen-review checklist items remain applicable and pass. There are no
non-applicable items: the picker opens immediately, the row remains readable
and semantically styled at narrow widths, selection/scroll geometry remains
stable, alternate-screen ownership and normal scrollback restoration are
unchanged, and pending versus cached outcomes remain honest.

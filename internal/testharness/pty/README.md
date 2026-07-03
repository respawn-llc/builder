# PTY Harness

`internal/testharness/pty` launches terminal programs in a pseudo-terminal, captures timestamped bytes, analyzes them through a terminal screen model, and checks typed terminal operations.

Use the facade package for scenario tests:

1. Build or select a fixture command.
2. Run it with `RunCommand` and explicit `Dimensions`.
3. Analyze the returned `Capture` with `Analyze`.
4. Resolve phase-marker windows with `ResolveOperationWindows`.
5. Run assertions over operation windows and regions.
6. Write artifacts with `WriteArtifacts` when a failure needs raw bytes, escaped bytes, chunks, operations, final screen text, and diagnostics.

Terminal byte interpretation is owned by the analyzer. Do not use regex, substring matching, stderr text, wall-clock sleeps, or rendered terminal copy as synchronization or parsing sources.

Use synthetic byte streams and tiny fixture commands for harness self-tests. Live Kent TUI scenarios use the dedicated fixture binary and must not assert product wording, style, color, cursor placement, or the legacy ongoing scrollback implementation.

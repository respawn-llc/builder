# KENT-570 review fixes

- [x] Restore OAuth `Generate` to non-streaming JSON semantics and observe only its response headers.
- [x] Restrict provider turn-state observation and diagnostics to resolved ChatGPT OAuth dispatches.
- [x] Replace optional string sentinels with structural optionals/presence values.
- [x] Replace the handwritten metadata parser with the approved ordered `json.Decoder.Token` walk.
- [x] Remove exported state-inspection APIs used only by tests and keep identity verification package-local/product-boundary.
- [x] Remove Desktop visual-tone assertions while preserving behavior, localization, action, and redaction coverage.
- [x] Publish a redacted six-operation authenticated verification report in the task completion record.
- [x] Run focused deterministic tests, Desktop tests, server tests, and the required Go build.
- [x] Commit the complete review-fix round and transition KENT-570 back to review.

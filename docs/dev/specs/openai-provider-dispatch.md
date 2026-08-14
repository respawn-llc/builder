# OpenAI Provider Dispatch

## Dispatch Identity

- Every OpenAI-family generation, streaming-generation, and compaction dispatch sends `originator`, `User-Agent`, and `session-id`, including a dispatch to an explicit custom OpenAI-compatible endpoint without authentication.
- `session-id` is the dispatch's Kent Session identity. It must be nonempty, must have no leading or trailing space or horizontal tab, and must be valid as an outbound HTTP header value.
- Kent rejects a missing or invalid dispatch Session identity before authentication resolution, credential refresh, or any network request.
- Authenticated dispatches retain their `Authorization` and ChatGPT account-identity headers. An explicit custom OpenAI-compatible endpoint may remain anonymous and sends the common identity headers without `Authorization`.
- Kent sends no `session_id` header.
- Token counting, model-context resolution, and offline request inspection send no Session identity, Codex dispatch metadata, routing hint, or provider turn state.

## ChatGPT Codex Routing

- Only ChatGPT Codex OAuth dispatches send Codex turn metadata, `x-codex-routing-hint`, or `x-codex-turn-state`.
- Codex turn metadata is one object at `client_metadata["x-codex-turn-metadata"]`.
- Codex turn metadata contains `session_id`, `thread_id`, `turn_id`, and `window_id`. It contains `request_kind` only when the operation has an approved request kind.
- A main-agent or subagent dispatch maps its own Kent Session ID to `session_id` and `thread_id`, its live Agent Turn Run ID to `turn_id`, and `<Session ID>:<compaction generation>` to `window_id`.
- Each subagent sends only its own Session and Agent Turn identities.
- A Reviewer dispatch maps `<Kent Session ID>/supervisor` to `session_id` and `thread_id`, copies the enclosing main Agent Turn Run ID to `turn_id`, and maps `<Kent Session ID>/supervisor:<main compaction generation>` to `window_id`.
- Ordinary main-agent, subagent, and Reviewer generation uses request kind `turn`. Responses compaction uses `compaction`. Local generation-based compaction omits request kind.
- Inline compaction uses the enclosing Agent Turn Run ID. Standalone compaction uses its live exclusive Run ID.
- A compaction dispatch uses the pre-compaction window. The compaction generation advances only after successful compaction.
- `x-codex-routing-hint` is `model=<selected model>` and adds `;tier=<effective service tier>` only when the request uses a service tier.
- The routing model must be nonempty, must have no leading or trailing space or horizontal tab, must contain no semicolon, and must produce a valid outbound HTTP header value.
- Kent rejects an invalid routing model before the provider request. The routing tier must equal the service tier sent in the request.

## ChatGPT Codex Turn State

- ChatGPT Codex provider turn state is opaque retry-local routing state. Kent never trims, decodes, canonicalizes, joins, logs, persists, or presents its value.
- Kent observes one `x-codex-turn-state` value from the initial HTTP response headers.
- A provider turn-state value must be nonempty, must have no leading or trailing space or horizontal tab, and must be valid as an outbound HTTP header value.
- A bounded retry of the same unchanged logical dispatch replays the accepted value exactly as `x-codex-turn-state`.
- Every changed-payload request starts with empty provider turn state.
- Provider turn-state absence or invalidity never changes the original model success, provider error, stream error, or cancellation result.
- Invalid state produces one redacted operator warning and one best-effort live `provider_turn_state_invalid` operational diagnostic per logical dispatch.
- The warning and diagnostic must not contain provider state, credentials, account data, request or response bodies, model output, or encrypted payloads.
- The diagnostic is not persisted or replayed and may be missed by an absent, disconnected, or overflowed client.
- Clients own all human-facing provider-state diagnostic copy.

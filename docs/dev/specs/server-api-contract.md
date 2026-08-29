# Server API Contract

## Authority And Compatibility

- Protobuf schemas are the sole editable authority for the Kent server API.
- Generated Protobuf messages are the API-boundary contract for official Go and TypeScript clients.
- Kent carries serialized Protobuf messages in typed binary WebSocket envelopes. It does not expose gRPC or Connect transport semantics.
- Kent's protocol version is the sole compatibility authority for API schema changes.
- Every contract change must increment the protocol version.
- Clients and servers must use the same protocol version.
- Protobuf packages must not introduce an independent `v1` or `v2` compatibility authority.
- Kent promises no source, wire, or backward schema compatibility across protocol versions.
- Protocol capability flags must not negotiate route or schema availability.
- Onboarding model, provider, and import capability facts remain ordinary product data rather than protocol compatibility gates.
- The Protobuf schema must remain suitable for standard code generation without changing API ownership.
- The Protobuf schema is platform-neutral. No client or server owns it.
- Applications compile generated contract code into their artifacts and never load `.proto` files at runtime.

## Operations And Transport

- Protobuf services and typed Kent method options own operation request types, response types, operation kind, and unary connection policy.
- Generated descriptors are the sole operation identity and route-metadata authority.
- Active operation names use strict lower-snake `<package>.<service>.<method>` form with no service elision and no `kent` prefix.
- Each nonempty package segment must start with an ASCII lowercase letter and contain only ASCII lowercase letters, digits, or underscores.
- Service and method identifiers convert from PascalCase to lower snake case by splitting before an uppercase letter when a lowercase letter or digit precedes it, and before the final uppercase letter of an uppercase run when a lowercase letter follows it.
- Digits remain in their surrounding token.
- `APIStatus` becomes `api_status`, `UUID` becomes `uuid`, `HTTP2Server` becomes `http2_server`, and `MaterializeWorkspaceChat` becomes `materialize_workspace_chat`.
- Both Go and TypeScript must apply the same operation-name validation and conversion.
- The generated request and response contract must not contain a generic application request ID.
- Multiplexed call and result envelopes may carry opaque connection-local correlation values.
- Connection-local correlation values must not reach service or domain code, persistence, replay, memoization, or reconciliation.
- Notifications and events must not carry connection-local correlation values.
- Every unary method must declare either multiplexed or dedicated connection policy. An unspecified policy is invalid.
- Server Update Status, Workflow Task Search, Runtime Submit User Turn, Runtime Submit User Shell Command, Runtime Compact Context, Runtime Interrupt, Runtime Live Stop, Runtime Live Wait, and Runtime Live Watch use dedicated unary connections.
- Subscription, progress, and notification connection behavior derives from operation kind.
- A dedicated unary call owns one WebSocket and performs the applicable handshake, authentication, and attachment steps on that connection.
- Caller cancellation closes a dedicated unary connection and stops only that caller's waiting and delivery.
- Closing a caller connection must not cancel, pause, retry, replay, authorize, or otherwise change server-owned work.
- The transport must not add a cancel-frame variant for server-owned work.
- Call, notification, subscription, progress, authentication, attachment, and cancellation semantics remain owned by their operation specifications.
- Subscription and progress operations use the terminal completion and failure behavior defined by their owning operation specifications. Transport framing adds no generic terminal product outcome.

## Message Design

- Every route-reachable named scalar must be classified as open or validated text, identifier, or closed enum.
- Closed enums must declare every supported value.
- Message-local rules must be declared in the Protobuf contract and enforced through generated validation.
- Rules that require server state must remain with their server-owned domain boundary.
- Unclassified scalars and validators are invalid API contract state.
- Kent-owned variant payloads must use typed Protobuf messages or explicit `oneof` branches.
- Transcript events, handshake outcomes, workspace selections, API error details, and Attention variants must not use generic dynamic payloads.
- Session and Prompt Attention and Workflow-owned Attention must each have one typed representation under their owning domain.
- Externally authored provider, model, and tool content remains server-only in its provider-native representation.
- Client-facing operation messages must not wrap provider-owned JSON.
- Operation messages must not contain unclassified `bytes`, `Any`, generic maps, or raw JSON.
- A transport envelope may contain serialized Protobuf bytes only when its operation descriptor declares the exact payload type.
- Optional values represent semantic absence through Protobuf presence and generated optional platform types.
- Sentinel values and explicit JSON-style null entries must not represent absence.
- Missing repeated and map fields represent empty collections.
- Optional wrapper messages are reserved for semantics where absence differs from present-empty.
- Operations with no request content or no success data must use `google.protobuf.Empty`.
- API instants must use `google.protobuf.Timestamp`.
- API elapsed amounts must use `google.protobuf.Duration`.
- Raw numeric time representations are permitted only when an external system owns that representation.
- Existing first-party identifiers retain their owning domain format. The Protobuf API does not migrate the persistence or domain identity model.
- First-party UUID v4 identifiers retain canonical textual wire form.
- Third-party and provider identifiers remain validated string fields unless their owning contract defines another representation.
- Generated TypeScript uses the standard Protobuf `bigint` representation for `int64` and `uint64`.
- JavaScript-facing 64-bit values must remain within the JavaScript safe-integer range.

## Results And Errors

- Unary results, progress final results, and subscription-start acknowledgements must use an operation-specific top-level `oneof` with exactly one `success` or `error` branch.
- A present error branch is an operation failure.
- Each operation error must contain a nonempty stable code and a typed detail `oneof`.
- Known error codes must declare and validate their required detail variant.
- A missing outcome, empty error code, or known code with missing or incorrect detail is malformed.
- A client must surface an unknown nonempty error code as a generic error while preserving available unknown fields.
- Subscription-start failure occurs before acknowledgement and remains distinct from later events and terminal completion.
- Client-visible wording is client-owned and derived from stable codes and typed details.
- The shared `internal_failure` code represents otherwise-unclassified operational failures.
- Onboarding rollback errors use one typed primary failure plus structured rollback facts containing operation and cause.
- `background_process_active` remains a public error because Session workspace retargeting exposes that blocker.
- Clients must not stringify errors or depend on server-authored human-readable messages.

## Validation And Decoding

- External request values must be validated once at the earliest server request boundary before downstream server logic consumes them.
- Validation owned by another shared authority, such as a database constraint used by several writers, must remain with that authority.
- Clients must run generated Protobuf constraints on server responses and events.
- Platforms must centralize generated validation at their transport boundary.
- Generated clients must preserve unknown Protobuf fields.
- Generated clients must reject unknown enum values.
- Malformed Protobuf bytes, unknown operations, wrong frame direction, invalid envelopes, and known-operation validation failures must reject only the affected frame after a connection is established.
- Kent must keep the established connection available for unrelated valid traffic after a rejected frame.
- Kent must return or publish a typed protocol or validation failure when the frame can be correlated safely.
- Protocol-version mismatch and invalid handshake or authentication establishment must reject setup and close the connection.
- Kent must not add strike counters, rate limits, or repeated-invalid-frame disconnect state solely for contract validation.
- Debug builds fail fast when Kent attempts to emit a generated message that violates its declared contract.
- Malformed peer input is external data and must be rejected without crashing in debug and production.
- Production must surface an internal contract error and must not send an invalid frame.

## Startup Availability

- Handshake, authentication, readiness, onboarding capability facts, and onboarding finalization remain available before server activation completes.
- Operations that require activation must fail with a typed onboarding-required or activation-failed result while activation is unavailable.
- Handshake messages must not contain route capability flags.

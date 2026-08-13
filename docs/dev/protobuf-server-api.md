# Protobuf Server API Architecture

This document owns the implementation architecture and task boundaries for the Server API Protobuf migration. The external contract is defined in [Server API Contract](specs/server-api-contract.md).

## Source And Generation

- [`api/proto/`](../../api/proto/) is the only editable Server API schema tree.
- The schema tree is organized by Kent API domain, never by consuming application.
- Generated Go and TypeScript live outside `api/proto/` in platform-specific generated packages.
- Generated Go lives under `shared/protoapi/gen/<domain>`.
- Generated TypeScript lives in `apps/desktop/packages/server-api-contract`. `apps/shared` remains reserved until a second GUI app consumes a TypeScript package.
- Generated output is checked in and compiled into each application. Normal builds do not run generation or load `.proto` files.
- Buf pins and runs official Go Protobuf generation, Protobuf-ES, and Protovalidate generation for Go and TypeScript.
- `scripts/generate-protobuf.sh` generates every target. Its check mode regenerates complete output trees in isolation and rejects deleted, extra, stale, or edited generated files.
- `scripts/ci-check.sh` runs freshness and contract checks. Normal build, test, and package-install commands do not invoke generation.
- Permanent safeguards are limited to Buf lint/descriptors, generated package tests/compilation, complete-tree regeneration comparison, and the schema/protocol-version changed-path check. Final diff review verifies inactive adoption and frozen Rust.
- Rust remains frozen. A future in-repository Kotlin client generates from the same schema tree without a separate schema copy or registry.

## Schema Conventions

- Protobuf packages represent stable Kent API domains and do not contain `v1` or `v2` version segments.
- Services represent domain resources. Methods represent actions.
- Active operation names derive as strict lower-snake `<package>.<service>.<method>` with no service elision and no `kent` prefix. Package segments are lower-snake. Service and method identifiers split before an uppercase letter preceded by a lowercase letter or digit, and before the final uppercase letter of an uppercase run followed by a lowercase letter; tokens are lowercased and joined with `_`, with digits retained. Examples: `APIStatus` → `api_status`, `HTTP2Server` → `http2_server`, and `MaterializeWorkspaceChat` → `materialize_workspace_chat`.
- Private legacy names are intentionally normalized during each breaking vertical. Until a method migrates, its descriptor carries a typed migration-only `legacy_wire_name` for exact coverage association. It is never an accepted binary name or runtime alias. Each vertical removes the option from migrated methods; final legacy deletion removes the option definition.
- Typed method options store Kent transport facts that cannot be derived from the method signature—operation kind, authentication stage, attachment/scope policy, direction, event/completion association, and unary connection policy—plus temporary legacy-name migration provenance.
- Every unary method declares exactly one typed connection policy: multiplexed or dedicated. An unspecified value is invalid. Subscription, progress, and notification connection behavior derives from operation kind.
- A dedicated unary call owns one WebSocket connection. Caller cancellation closes that operation-owned connection without closing the client's multiplexed control connection or canceling unrelated control traffic. The migration does not add a correlated cancel frame.
- Descriptors are the route and operation authority. No generated or handwritten parallel route table is allowed for migrated operations.
- Generation emits aggregate Go and TypeScript descriptor registries whose imports/exports derive from the schema file graph. Thin handwritten indexes consume those aggregates and contain no domain, file, or operation list.
- Coverage joins the exact registered legacy-operation set to descriptors through co-located `legacy_wire_name` options and compares route metadata/connection policy. It separately validates unique normalized active names. No copied route inventory is committed, and no route count is a permanent constant.
- The authoritative handshake is projected to the approved post-capability-negotiation contract: client capability advertisement, server `CapabilityFlags`, and the server identity capabilities field are intentionally absent from Protobuf. Structured projection is keyed to those exact Go declarations. The onboarding `CapabilityFacts` operation and provider/model/import capability facts remain covered and present.
- The same predecessor projection applies KENT-345's approved no-generic-request-identity contract by exact Go declaration identity across server requests/responses, client read models/transcript events, `RuntimeClientRequestID`, custom-wire members, and exclusively dependent validation/helper branches. It does not match names or tags broadly. Queue Item, Setup Operation, Run, Agent Step, Session, Resource Generation, and connection-local envelope correlation remain covered.
- A bounded migration-only check recursively compares ordinary reachable Go wire fields with descriptors. Ordinary association converts the Go field identifier with the same lower-snake algorithm and checks its JSON tag as legacy evidence; semantic tag/name differences require explicit classification. The check covers presence, scalar width/signedness, aliases, collections, nested messages, and approved standard type mappings. Every reachable custom-marshaled type, wire-significant unexported state, closed union, intentional rename, or Protobuf reshape has a focused exhaustive domain fixture. Vertical ownership tests replace these checks at cutover.
- Bounded `go/packages`/`go/types`/AST analysis enumerates typed constants for every reachable named scalar. Each scalar is classified as open/validated text, identifier, or closed enum; closed enums have exact discovered-member-to-Protobuf coverage, including explicit intentional renames.
- The same structured analysis discovers reachable `Validate`/`ValidateRPC` implementations, follows directly called package-local validation helpers, and fingerprints their canonical typed-AST closure. Human review maps message-local behavior to Protovalidate boundary fixtures or records stateful/shared-choke-point ownership. A changed legacy branch/helper invalidates sign-off and forces review; the tooling does not claim to infer predicate semantics or mechanically prove predicate completeness.
- Every unary and progress operation returns an operation-specific top-level union containing exactly success or error.
- Every subscription returns a typed start acknowledgement or declared pre-ack failure before events begin. Events and terminal completion remain separate.
- Each operation error has a required non-empty stable string code and a typed detail union. Known codes require their declared detail. A present error with an unknown code is a generic failure, including when a future detail variant is retained only as unknown fields; absent outcomes, empty codes, and known codes with missing/wrong details are malformed. Client-visible wording remains client-owned.
- Error variants are inspected while each domain schema is authored, using owning specs and concrete request/error paths. Unproven client-visible associations return to product design and are recorded only in the owning spec and Protobuf declaration. No whole-server error-analysis subsystem or duplicate error inventory is added.
- Permanent descriptor tests validate schema integrity. KENT-555 replaces whole-legacy parity checks with per-vertical migrated/unmigrated partition checks as legacy routes begin disappearing.
- Subscription and progress terminal behavior is preserved from the owning operation; schema conversion does not add another completion model.
- Empty requests and success values use `google.protobuf.Empty`.
- Instants use `google.protobuf.Timestamp`. Elapsed amounts use `google.protobuf.Duration`.
- Singular semantic absence uses Protobuf presence. Missing repeated or map fields mean empty.
- JavaScript-facing 64-bit fields are constrained to the JavaScript safe-integer range and use Protobuf-ES's standard TypeScript `bigint` representation.
- Provider-owned identifiers remain validated strings. Existing first-party IDs preserve their formats and UUIDs preserve canonical text.
- Kent-owned dynamic variants use explicit messages and `oneof`.
- Provider-, model-, and tool-authored content stays server-only in its native representation.
- Operation-message schemas may not use `Any`, raw JSON, opaque bytes, or unclassified generic maps.

## Validation And Failure Policy

- Protovalidate owns constraints decidable from one external message.
- The server validates a request once at the earliest request boundary.
- Clients validate every response, event, and completion before application code consumes it.
- Stateful and shared-store rules remain with their existing server owner.
- Generated decoders accept and preserve unknown fields.
- Generated validation rejects unknown enum values and declared constraint violations.
- A malformed frame, unknown operation, wrong-direction frame, or invalid operation message rejects that frame while an established connection continues processing unrelated traffic.
- A protocol-version mismatch or invalid connection establishment rejects the connection.
- Peer input never triggers a debug panic. Kent-generated output that violates the schema panics in debug; production surfaces the internal error and does not send the invalid frame.
- The migration adds no repeated-invalid-frame counter, rate limiter, or disconnect state machine.

## Envelope And Correlation

- Binary WebSocket frames contain an explicit envelope union for calls, results, notifications/events, and transport failures.
- The envelope carries serialized Protobuf message bytes whose exact type comes from the operation descriptor.
- Descriptor-typed envelope payload bytes do not authorize `bytes` as an unclassified field inside operation messages.
- A multiplexed call/result may carry an opaque connection-local correlation value.
- Descriptor-marked dedicated unary calls do not require envelope correlation for cancellation: each uses an operation-owned connection, and cancellation closes only that connection.
- Correlation never appears in an operation message, reaches service/domain code, identifies product work, or participates in persistence, replay, memoization, or reconciliation.
- Notifications and events do not carry call correlation.
- Generic application request IDs are absent from the Protobuf contract.
- The envelope has no generic cancel-frame variant. Replacing operation-owned connections with correlated cancellation would be a separate product/concurrency design.
- Kent preserves its call, notification, subscription, progress, authentication, attachment, cancellation, and concurrency behavior without adopting gRPC or Connect.

## Generated Boundary

- Generated messages are the wire boundary, not the default domain or persistence model.
- A server-owned type and mapping remain only where the server owns meaning or behavior that differs from the wire representation.
- Equivalent handwritten wire DTOs, schemas, validators, error decoders, method constants, and route entries are deleted when their operation migrates.
- Generated directories contain no handwritten adapters or policy.
- The active `shared/apicontract` interfaces and route table remain unchanged during the foundation task and disappear by operation only when an active vertical migrates.
- The active `shared/rpcwire` text-frame adapter remains unchanged during the foundation task. The first active vertical extends it with disjoint binary-frame support.

## Migration Invariants

- Every operation has exactly one active encoding and one contract authority.
- A migrated operation accepts only binary Protobuf.
- An unmigrated operation accepts only its existing JSON contract.
- No operation accepts both encodings and no compatibility or fallback decoder exists.
- One temporary dispatcher may distinguish disjoint binary and text WebSocket frames.
- Each complete domain slice migrates server, Go clients, and Desktop together and increments the protocol version.
- Each domain slice selects unary transport from the descriptor. For every migrated dedicated unary operation, transport tests cancel that call and prove its operation-owned connection closes while a concurrent multiplexed control call remains unaffected.
- The final deletion removes JSON-RPC framing, handwritten wire authorities, and the temporary dispatcher.
- Generated output and mechanically repetitive schema boilerplate are excluded from the 10,000-production-line task cap. Handwritten executable and tooling logic is counted.
- `shared/protocol/version.json` remains the only editable protocol-version value. CI rejects schema changes that do not change it in the same change set.
- The foundation task increments `shared/protocol/version.json` when it introduces `api/proto/`, even though active transport adoption occurs in later tasks.

## Task Chain

1. **KENT-192 — Protobuf foundation:** define the complete inactive schema, generation, descriptors, validation/error conventions, generated Go/TypeScript packages, freshness guards, and envelope contract. Active API signatures and transport remain unchanged.
2. **KENT-554 — Capability deletion:** delete handshake route-capability negotiation and activation while the active API remains JSON.
3. **KENT-555 — Bootstrap API:** activate Protobuf for connection/bootstrap, authentication/onboarding, Project, and Session launch/catalog operations. This task depends on merged KENT-345.
4. **KENT-557 — Worktree API:** migrate Worktree reads, mutations, and setup subscription.
5. **KENT-558 — Session API:** migrate interactive Session, Runtime, transcript, prompts/approvals, process, attention, and Run Prompt operations.
6. **KENT-559 — Workflow API:** migrate Workflow definition, graph, project-link, and label operations.
7. **KENT-556 — Task API:** migrate Workflow Task reads, lifecycle, dependencies, comments/activity, attention, and subscriptions.
8. **KENT-560 — Legacy deletion:** prove complete descriptor ownership and delete the JSON contract and temporary migration dispatcher.

KENT-553 separately evaluates role-specific provider ID types. It does not block this chain.

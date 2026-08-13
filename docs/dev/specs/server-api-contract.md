# Server API Contract

## Contract Source

- Kent's Protobuf definitions are the authoritative Server API contract.
- The contract contains every supported operation, request, result, event, completion, validation rule, and client-visible error variant.
- Clients generate platform-native contract code from the same Protobuf definitions.
- Client and server applications compile generated contract code into their binaries or bundles. They do not load Protobuf definitions at runtime.
- A Kotlin client generates from the same contract as Go and TypeScript clients.
- The Server API uses generated Protobuf messages rather than JSON payloads.

## Compatibility

- The Kent protocol version is the only compatibility version for the Server API.
- A client and server must use the same protocol version.
- Every Server API contract change must increase the protocol version.
- Kent does not provide backward or forward compatibility between different protocol versions.
- Protocol packages do not define independent API versions.

## Operations

- Each operation belongs to one domain package and one service.
- An operation's wire name derives as strict lower-snake `<package>.<service>.<method>`. Service is never omitted. Acronyms remain one lowercased word and word boundaries are separated with underscores. Existing private JSON operation names are not preserved as aliases during Protobuf migration.
- A successful operation with no response data returns an empty Protobuf message.
- A singular optional field uses Protobuf presence to distinguish absence from a present value.
- A missing repeated or map field means an empty collection.
- Instants use Protobuf timestamps. Elapsed amounts use Protobuf durations.
- Integer values exposed to JavaScript clients must remain within JavaScript's safe integer range.
- Generated TypeScript represents 64-bit integer fields as `bigint`.

## Transport

- Kent carries Protobuf messages in binary WebSocket frames.
- The Server API supports calls, notifications, subscriptions, progress updates, authentication, attachment, and cancellation.
- Each unary operation declares whether calls share a multiplexed connection or use an operation-owned dedicated connection.
- Canceling a dedicated unary call closes only its operation-owned connection. It does not close the client's multiplexed connection or cancel unrelated calls.
- A connection-local correlation value may match concurrent calls and results on one WebSocket connection.
- Connection-local correlation is not part of an operation's request or result, does not identify product work, and is not persisted or replayed.
- The envelope may carry serialized message bytes whose exact type is declared by the operation.
- A malformed frame, unknown operation, wrong-direction frame, or invalid operation message rejects that frame without closing an established connection.
- A protocol-version mismatch or invalid connection establishment rejects the connection.
- The Server API does not define a generic cancel frame. Connection-owned cancellation behavior is part of the operation descriptor.
- Kent does not expose gRPC or Connect transport semantics.

## Validation

- The server validates each external request against the generated contract before the operation consumes it.
- A client validates each response, event, and completion against the generated contract before application code consumes it.
- Every closed scalar domain declares all supported values in the contract.
- Every rule decidable from one message, including scalar bounds and cross-field requirements, is declared in the contract.
- Rules that require server state or a shared authoritative store remain with that server owner.
- An unknown field is accepted and preserved.
- An unknown enum value is rejected.
- A contract violation in one frame does not prevent an established connection from processing unrelated valid frames.

## Results And Errors

- Each unary result, progress final result, and subscription-start acknowledgement contains exactly one success or error branch.
- An error branch contains a required stable string code and typed detail. Known codes require their declared detail.
- A non-empty unknown error code is still a generic failure. Future unknown detail fields are preserved even when the client cannot interpret them.
- A missing result branch, empty error code, or known code without its required detail is a contract violation.
- A subscription returns a typed start result before events begin. A successful start acknowledges the subscription; a failed start returns one of that operation's supported errors.
- Notifications, events, and post-ack subscription completion use separate frames and messages. They do not use success/error result envelopes.
- Error variants use stable Kent error codes and typed details.
- Clients own user-visible error wording.
- A client surfaces an unknown failure as a generic error.
- A transport failure remains distinct from an operation result.

## Dynamic Content

- Kent-owned message variants use explicit typed messages.
- Provider-, model-, and tool-authored content remains outside the client Server API contract.
- Operation messages do not expose Kent-owned payloads as generic maps, raw JSON, opaque bytes, or unclassified dynamic values.

## Identifiers

- Existing Kent identifier formats remain unchanged unless the operation's owning product contract changes them.
- First-party UUID values use their canonical textual form.
- Provider-owned identifiers preserve the provider-defined string value.
- Generic application request identifiers are not part of operation messages.

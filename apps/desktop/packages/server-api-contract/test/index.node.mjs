import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  DurationSchema,
  EmptySchema,
  TimestampSchema,
} from "@bufbuild/protobuf/wkt";
import { createValidator } from "@bufbuild/protovalidate";

import * as publicContract from "../dist/index.js";
import {
  activeOperationName,
  classifyOperationResult,
  descriptorPaths,
  decodeGeneratedMessage,
  encodeGeneratedMessage,
  file,
  files,
  isOperationResultDescriptor,
  marshalEnvelope,
  OperationOutcome,
  operationByName,
  operationFromDescriptor,
  operations,
  pascalCaseToLowerSnake,
  unmarshalEnvelope,
  validateGeneratedMessage,
  validatePackageName,
  validateKentMethodOptions,
} from "../dist/index.js";
import {
  schema_kent_api_attention_attention as attention,
  schema_kent_api_process_process as process,
  schema_kent_api_prompt_prompt as prompt,
  schema_kent_api_run_prompt_run_prompt as runPrompt,
  schema_kent_api_shared_foundation as foundation,
  schema_kent_api_server_server as server,
  schema_kent_api_transcript_transcript as transcript,
} from "../dist/index.js";
import {
  schema_fixture_method_policy_fixture as methodPolicyFixture,
  schema_fixture_newer_forward_error_fixture as forwardErrorFixture,
  schema_fixture_schema_conventions_fixture as schemaConventionsFixture,
} from "../dist/gen/test-registry/registry.js";
const {
  AuthenticationStage,
  CallSchema,
  Direction,
  EnvelopeSchema,
  FoundationImportsSchema,
  kent_method,
  NotificationEventSchema,
  OperationKind,
  ResultSchema,
  ScopePolicy,
  TransportFailureCode,
  TransportFailureSchema,
  UnaryConnection,
} = foundation;
const {
  CreateErrorSchema,
  CreateResultSchema,
  CreateSuccessSchema,
  InvalidInputDetailsSchema,
  NamingService,
  ResourceConflictDetailsSchema,
  WatchAcknowledgementSchema,
  WatchCompletionSchema,
  WatchEventSchema,
  WatchStartResultSchema,
} = methodPolicyFixture;
const {
  FutureCreateErrorSchema,
  FutureCreateResultSchema,
  FutureConflictDetailsSchema,
} = forwardErrorFixture;
const {
  ConventionState,
  SchemaConventionsFixtureSchema,
} = schemaConventionsFixture;
const { QuestionSchema } = prompt;
const {
  QueuedMessageStateSchema,
  QueuedMessageStatus,
} = transcript;
const { OutputChunkSchema } = process;
const {
  GetReadinessResultSchema,
  GetReadinessSuccessSchema,
  ReadinessSchema,
} = server;
const {
  Kind: AttentionKind,
  NotificationIDSchema: AttentionNotificationIDSchema,
  NotificationSchema: AttentionNotificationSchema,
  ApprovalStateSchema: AttentionApprovalStateSchema,
} = attention;
const {
  ProgressEventSchema,
  SessionStartedSchema,
} = runPrompt;

const validator = createValidator();
const javaScriptSafeIntegerMaximum = 9007199254740991n;
const packageRoot = path.dirname(path.dirname(fileURLToPath(import.meta.url)));

test("new schema slice executes generated validation in TypeScript", () => {
  assert.equal(prompt.QuestionSchema, QuestionSchema);
  assert.equal(transcript.QueuedMessageStateSchema, QueuedMessageStateSchema);
  assert.equal(process.OutputChunkSchema, OutputChunkSchema);
  assert.equal(attention.NotificationSchema, AttentionNotificationSchema);
  assert.equal(runPrompt.ProgressEventSchema, ProgressEventSchema);
  const timestamp = create(TimestampSchema, { seconds: 1700000000n });
  const validUUID = "123e4567-e89b-42d3-a456-426614174000";

  validateGeneratedMessage(QuestionSchema, create(QuestionSchema, {
    promptId: "prompt-1",
    sessionId: "session-1",
    stepId: validUUID,
    question: "Choose",
    suggestions: ["first", "second"],
    recommendedOptionIndex: 2,
    createdAt: timestamp,
  }));
  assert.throws(() => validateGeneratedMessage(QuestionSchema, create(QuestionSchema, {
    promptId: "prompt-1",
    sessionId: "session-1",
    stepId: validUUID,
    question: "Choose",
    suggestions: ["first"],
    recommendedOptionIndex: 2,
    createdAt: timestamp,
  })));

  validateGeneratedMessage(QueuedMessageStateSchema, create(QueuedMessageStateSchema, {
    queueItemId: validUUID,
    status: QueuedMessageStatus.ACCEPTED,
    text: "queued",
  }));
  assert.throws(() => validateGeneratedMessage(QueuedMessageStateSchema, create(QueuedMessageStateSchema, {
    queueItemId: validUUID,
    status: QueuedMessageStatus.ACCEPTED,
  })));

  assert.throws(() => validateGeneratedMessage(OutputChunkSchema, create(OutputChunkSchema, {
    processId: "process-1",
    offsetBytes: 4n,
    nextOffsetBytes: 3n,
  })));

  assert.throws(() => validateGeneratedMessage(AttentionNotificationSchema, create(AttentionNotificationSchema, {
    id: create(AttentionNotificationIDSchema, {
      kind: AttentionKind.QUESTION,
      uuid: validUUID,
    }),
    occurredAt: timestamp,
    revision: 1n,
    sessionId: "session-1",
    state: {
      case: "approval",
      value: create(AttentionApprovalStateSchema),
    },
  })));

  validateGeneratedMessage(ProgressEventSchema, create(ProgressEventSchema, {
    payload: {
      case: "sessionStarted",
      value: create(SessionStartedSchema, { sessionId: validUUID }),
    },
  }));
});

function validSchemaConventionsFixture() {
  return create(SchemaConventionsFixtureSchema, {
    optionalLabel: "present",
    empty: create(EmptySchema),
    occurredAt: create(TimestampSchema, { seconds: 1700000000n }),
    elapsed: create(DurationSchema, { seconds: 2n }),
    state: ConventionState.READY,
    providerId: "openai",
    uuidV4: "550e8400-e29b-41d4-a716-446655440000",
    signedSafe: javaScriptSafeIntegerMaximum,
    unsignedSafe: javaScriptSafeIntegerMaximum,
  });
}

function assertConventionValid(message) {
  const result = validator.validate(SchemaConventionsFixtureSchema, message);
  assert.equal(result.kind, "valid", result.kind === "invalid" ? result.error.message : undefined);
}

function assertConventionInvalid(message) {
  const result = validator.validate(SchemaConventionsFixtureSchema, message);
  assert.equal(result.kind, "invalid");
}

test("Protovalidate matches the shared Go/TypeScript parity corpus", async () => {
  const corpusPath = path.resolve(
    packageRoot,
    "../../../../shared/protoapi/testdata/protovalidate-parity.json",
  );
  const cases = JSON.parse(await readFile(corpusPath, "utf8"));

  for (const parityCase of cases) {
    const message = create(SchemaConventionsFixtureSchema, {
      optionalLabel: parityCase.optional_label ?? undefined,
      optionalPresence: parityCase.optional_presence ?? undefined,
      empty: parityCase.empty_present ? create(EmptySchema) : undefined,
      occurredAt: create(TimestampSchema, {
        seconds: BigInt(parityCase.occurred_at_seconds),
      }),
      elapsed: create(DurationSchema, {
        seconds: BigInt(parityCase.elapsed_seconds),
      }),
      state: parityCase.state,
      providerId: parityCase.provider_id,
      uuidV4: parityCase.uuid_v4,
      signedSafe: BigInt(parityCase.signed_safe),
      unsignedSafe: BigInt(parityCase.unsigned_safe),
    });
    const result = validator.validate(SchemaConventionsFixtureSchema, message);
    assert.equal(
      result.kind === "valid",
      parityCase.valid,
      `${parityCase.name}: ${result.kind === "invalid" ? result.error.message : "valid"}`,
    );
  }
});

test("schema conventions accept valid values and optional absence", () => {
  const message = validSchemaConventionsFixture();
  message.optionalLabel = undefined;
  assertConventionValid(message);
});

test("schema conventions preserve absent and present-empty values distinctly", () => {
  for (const [value, present] of [
    [undefined, false],
    ["", true],
  ]) {
    const message = validSchemaConventionsFixture();
    message.optionalPresence = value;
    const decoded = fromBinary(
      SchemaConventionsFixtureSchema,
      toBinary(SchemaConventionsFixtureSchema, message),
    );
    assert.equal(decoded.optionalPresence !== undefined, present);
    if (present) {
      assert.equal(decoded.optionalPresence, "");
    }
    assertConventionValid(decoded);
  }
});

test("schema conventions reject a present empty optional value", () => {
  const message = validSchemaConventionsFixture();
  message.optionalLabel = "";
  assertConventionInvalid(message);
});

test("schema conventions require Empty, Timestamp, and Duration messages", () => {
  for (const field of ["empty", "occurredAt", "elapsed"]) {
    const message = validSchemaConventionsFixture();
    message[field] = undefined;
    assertConventionInvalid(message);
  }

  const invalidTimestamp = validSchemaConventionsFixture();
  invalidTimestamp.occurredAt = create(TimestampSchema, { seconds: 253402300800n });
  assertConventionInvalid(invalidTimestamp);

});

test("schema conventions allow structurally valid negative durations", () => {
  const message = validSchemaConventionsFixture();
  message.elapsed = create(DurationSchema, { nanos: -1 });
  assertConventionValid(message);
});

test("schema conventions reject unspecified and undefined enum values", () => {
  for (const state of [ConventionState.UNSPECIFIED, 99]) {
    const message = validSchemaConventionsFixture();
    message.state = state;
    assertConventionInvalid(message);
  }
});

test("schema conventions validate provider strings and canonical UUID v4 text", () => {
  for (const providerId of ["", " ", " openai", "openai "]) {
    const message = validSchemaConventionsFixture();
    message.providerId = providerId;
    assertConventionInvalid(message);
  }

  for (const uuidV4 of [
    "",
    "550e8400-e29b-11d4-a716-446655440000",
    "550E8400-E29B-41D4-A716-446655440000",
    "550e8400-e29b-41d4-7716-446655440000",
    "{550e8400-e29b-41d4-a716-446655440000}",
  ]) {
    const message = validSchemaConventionsFixture();
    message.uuidV4 = uuidV4;
    assertConventionInvalid(message);
  }
});

test("schema conventions keep standard Protobuf-ES bigint within JS safe range", () => {
  const minimum = validSchemaConventionsFixture();
  minimum.signedSafe = -javaScriptSafeIntegerMaximum;
  minimum.unsignedSafe = 0n;
  assert.equal(typeof minimum.signedSafe, "bigint");
  assert.equal(typeof minimum.unsignedSafe, "bigint");
  assertConventionValid(minimum);

  const signedAbove = validSchemaConventionsFixture();
  signedAbove.signedSafe = javaScriptSafeIntegerMaximum + 1n;
  assertConventionInvalid(signedAbove);

  const signedBelow = validSchemaConventionsFixture();
  signedBelow.signedSafe = -javaScriptSafeIntegerMaximum - 1n;
  assertConventionInvalid(signedBelow);

  const unsignedAbove = validSchemaConventionsFixture();
  unsignedAbove.unsignedSafe = javaScriptSafeIntegerMaximum + 1n;
  assertConventionInvalid(unsignedAbove);
});

test("generated message codec preserves unknown fields", () => {
  const encoded = toBinary(
    SchemaConventionsFixtureSchema,
    validSchemaConventionsFixture(),
  );
  const withUnknownField = new Uint8Array([...encoded, 0xa0, 0x06, 0x07]);

  const decoded = decodeGeneratedMessage(
    SchemaConventionsFixtureSchema,
    withUnknownField,
  );

  assert.deepEqual(
    encodeGeneratedMessage(SchemaConventionsFixtureSchema, decoded),
    withUnknownField,
  );
});

test("generated message codec rejects unknown enum values", () => {
  const message = validSchemaConventionsFixture();
  message.state = 99;
  const encoded = toBinary(SchemaConventionsFixtureSchema, message);

  assert.throws(() =>
    decodeGeneratedMessage(SchemaConventionsFixtureSchema, encoded),
  );
});

test("generated message codec executes Protovalidate constraints", () => {
  const message = validSchemaConventionsFixture();
  message.providerId = "";
  const encoded = toBinary(SchemaConventionsFixtureSchema, message);

  assert.throws(() =>
    decodeGeneratedMessage(SchemaConventionsFixtureSchema, encoded),
  );
  assert.throws(() =>
    encodeGeneratedMessage(SchemaConventionsFixtureSchema, message),
  );
  assert.throws(() =>
    validateGeneratedMessage(SchemaConventionsFixtureSchema, message),
  );
});

test("operation names use the locked state machine", () => {
  const fixtures = new Map([
    ["APIStatus", "api_status"],
    ["UUID", "uuid"],
    ["HTTP2Server", "http2_server"],
    ["MaterializeWorkspaceChat", "materialize_workspace_chat"],
    ["CreateTarget", "create_target"],
  ]);
  for (const [input, expected] of fixtures) {
    assert.equal(pascalCaseToLowerSnake(input), expected);
  }
  assert.equal(
    activeOperationName("workflow.task", "HTTP2Service", "MaterializeWorkspaceChat"),
    "workflow.task.http2_service.materialize_workspace_chat",
  );
});

test("operation names reject invalid packages and identifiers", () => {
  for (const packageName of [
    "",
    "Workflow",
    "workflow.Task",
    "workflow.2task",
    "workflow.-task",
    "workflow..task",
    "workflow.",
    "workfløw",
  ]) {
    assert.throws(() => validatePackageName(packageName));
  }
  for (const identifier of ["", "workflow", "Work_flow", "Work-Flow", "Wørkflow"]) {
    assert.throws(() => pascalCaseToLowerSnake(identifier));
  }
});

test("the operation index reads typed method options", () => {
  const indexed = operations();
  assert.ok(indexed.length > 0);
  assert.ok(indexed.every((operation) =>
    operation.descriptor.parent.file.proto.package.startsWith("kent.api.")
  ));
});

test("active lookup excludes legacy wire names", () => {
  assert.ok(operationByName("kent.api.server.server_service.get_readiness"));
  assert.equal(operationByName("server.GetReadiness"), undefined);
  assert.equal(operationByName("fixture.naming_service.http2_server"), undefined);
});

test("method policy rejects missing and invalid options", () => {
  const base = {
    kind: OperationKind.UNARY,
    authenticationStage: AuthenticationStage.SERVER,
    scopePolicy: ScopePolicy.NONE,
    direction: Direction.CLIENT_TO_SERVER,
    unaryConnection: UnaryConnection.MULTIPLEXED,
  };
  for (const [name, options] of [
    ["kind", { ...base, kind: OperationKind.UNSPECIFIED }],
    ["kind", { ...base, kind: 99 }],
    ["authentication", { ...base, authenticationStage: AuthenticationStage.UNSPECIFIED }],
    ["authentication", { ...base, authenticationStage: 99 }],
    ["scope policy", { ...base, scopePolicy: ScopePolicy.UNSPECIFIED }],
    ["scope policy", { ...base, scopePolicy: 99 }],
    ["direction", { ...base, direction: Direction.UNSPECIFIED }],
    ["direction", { ...base, direction: 99 }],
    ["unary connection", { ...base, unaryConnection: UnaryConnection.UNSPECIFIED }],
    ["unary connection", { ...base, unaryConnection: 99 }],
    ["non-unary", { ...base, kind: OperationKind.NOTIFICATION }],
    [
      "unary operation",
      {
        ...base,
        event: {
          $typeName: "kent.api.shared.OperationAssociation",
          package: "fixture",
          service: "NamingService",
          method: "WatchEvent",
        },
      },
    ],
    [
      "subscription",
      {
        ...base,
        kind: OperationKind.SUBSCRIPTION,
        unaryConnection: UnaryConnection.UNSPECIFIED,
      },
    ],
    [
      "progress",
      {
        ...base,
        kind: OperationKind.PROGRESS,
        unaryConnection: UnaryConnection.UNSPECIFIED,
      },
    ],
  ]) {
    assert.throws(
      () => validateKentMethodOptions(options),
      (error) => error instanceof Error && error.message.length > 0,
      name,
    );
  }

  assert.throws(
    () => operationFromDescriptor(NamingService.method.hTTP2Server, undefined),
    /method options/,
  );
  assert.ok(kent_method);
});

test("the handwritten index enumerates the generated aggregate", () => {
  const descriptors = [...files()];
  assert.ok(descriptors.length > 0);

  for (const descriptor of descriptors) {
    assert.equal(file(`${descriptor.name}.proto`), descriptor);
  }
});

test("descriptor paths report the complete sorted schema set", async () => {
  const got = descriptorPaths();
  assert.deepEqual(got, [...got].sort());

  const schemaRoot = path.resolve(packageRoot, "../../../../api/proto/kent/api");
  const want = await protobufPaths(schemaRoot);
  assert.deepEqual(got, want.map((entry) => `kent/api/${entry}`));
});

async function protobufPaths(root, relative = "") {
  const entries = await readdir(path.join(root, relative), { withFileTypes: true });
  const paths = [];
  for (const entry of entries) {
    const entryRelative = path.join(relative, entry.name);
    if (entry.isDirectory()) {
      paths.push(...await protobufPaths(root, entryRelative));
    } else if (entry.isFile() && path.extname(entry.name) === ".proto") {
      paths.push(entryRelative.split(path.sep).join("/"));
    }
  }
  paths.sort();
  return paths;
}

test("the package entry point exposes generated domain schemas", () => {
  assert.equal(foundation.FoundationImportsSchema, FoundationImportsSchema);
  assert.equal(publicContract.schema_fixture_method_policy_fixture, undefined);
});

test("the generated test registry exposes fixture schemas separately", () => {
  assert.equal(methodPolicyFixture.NamingService, NamingService);
  assert.equal(forwardErrorFixture.FutureCreateResultSchema, FutureCreateResultSchema);
  assert.equal(
    schemaConventionsFixture.SchemaConventionsFixtureSchema,
    SchemaConventionsFixtureSchema,
  );
});

test("binary envelopes round-trip every frame variant", () => {
  const correlation = "connection-call-1";
  const readinessResultPayload = toBinary(
    GetReadinessResultSchema,
    create(GetReadinessResultSchema, {
      outcome: {
        case: "success",
        value: create(GetReadinessSuccessSchema, {
          readiness: create(ReadinessSchema, {
            ready: true,
            serverId: "server-1",
            serverVersion: "1.0.0",
            serverBuild: "build-1",
            protocolVersion: "1",
          }),
        }),
      },
    }),
  );
  const outputChunkPayload = toBinary(
    OutputChunkSchema,
    create(OutputChunkSchema, {
      processId: "process-1",
      offsetBytes: 0n,
      nextOffsetBytes: 1n,
    }),
  );
  const frames = [
    {
      case: "call",
      value: create(CallSchema, {
        operation: "kent.api.server.server_service.get_readiness",
        correlation,
        payload: new Uint8Array(),
      }),
    },
    {
      case: "result",
      value: create(ResultSchema, {
        operation: "kent.api.server.server_service.get_readiness",
        correlation,
        payload: readinessResultPayload,
      }),
    },
    {
      case: "notificationEvent",
      value: create(NotificationEventSchema, {
        operation: "kent.api.process.output_service.event",
        payload: outputChunkPayload,
      }),
    },
    {
      case: "transportFailure",
      value: create(TransportFailureSchema, {
        code: TransportFailureCode.MALFORMED_ENVELOPE,
        correlation,
      }),
    },
    {
      case: "transportFailure",
      value: create(TransportFailureSchema, {
        code: TransportFailureCode.UNKNOWN_OPERATION,
      }),
    },
  ];

  for (const frame of frames) {
    const envelope = create(EnvelopeSchema, { frame });
    const encoded = marshalEnvelope(envelope);
    const decoded = unmarshalEnvelope(encoded);
    assert.deepEqual(toBinary(EnvelopeSchema, decoded), encoded);
  }
});

test("binary envelopes reject operation frame and direction mismatches", () => {
  const mismatches = [
    {
      case: "call",
      value: create(CallSchema, {
        operation: "kent.api.process.output_service.event",
        payload: toBinary(
          OutputChunkSchema,
          create(OutputChunkSchema, {
            processId: "process-1",
            nextOffsetBytes: 1n,
          }),
        ),
      }),
    },
    {
      case: "result",
      value: create(ResultSchema, {
        operation: "kent.api.process.output_service.event",
        payload: new Uint8Array(),
      }),
    },
    {
      case: "notificationEvent",
      value: create(NotificationEventSchema, {
        operation: "kent.api.server.server_service.get_readiness",
        payload: new Uint8Array(),
      }),
    },
  ];
  for (const frame of mismatches) {
    const envelope = create(EnvelopeSchema, { frame });
    assert.throws(() => marshalEnvelope(envelope));
    assert.throws(() => unmarshalEnvelope(toBinary(EnvelopeSchema, envelope)));
  }
});

test("binary envelopes reject malformed variants", () => {
  const malformed = [
    create(EnvelopeSchema),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, { payload: Uint8Array.of(1) }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, {
          operation: "kent.api.server.server_service.get_readiness",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, {
          operation: "kent.api.server.server_service.get_readiness",
          correlation: "",
          payload: Uint8Array.of(1),
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "result",
        value: create(ResultSchema, {
          operation: "kent.api.server.server_service.get_readiness",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "notificationEvent",
        value: create(NotificationEventSchema, {
          operation: "kent.api.process.output_service.event",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "transportFailure",
        value: create(TransportFailureSchema),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "transportFailure",
        value: create(TransportFailureSchema, { code: 99 }),
      },
    }),
  ];
  for (const envelope of malformed) {
    assert.throws(() => marshalEnvelope(envelope));
  }
  assert.throws(() => unmarshalEnvelope(Uint8Array.of(255)));

  const descriptor = EnvelopeSchema;
  assert.deepEqual(
    descriptor.oneofs[0]?.fields.map((field) => field.name),
    ["call", "result", "notification_event", "transport_failure"],
  );
  assert.equal(fromBinary(EnvelopeSchema, toBinary(EnvelopeSchema, malformed[0])).frame.case, undefined);
});

test("binary envelope decoding rejects semantically malformed wire bytes", () => {
  const malformed = [
    create(EnvelopeSchema),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, { payload: Uint8Array.of(1) }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, {
          operation: "kent.api.server.server_service.get_readiness",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "call",
        value: create(CallSchema, {
          operation: "kent.api.server.server_service.get_readiness",
          correlation: "",
          payload: Uint8Array.of(1),
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "result",
        value: create(ResultSchema, { payload: Uint8Array.of(1) }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "result",
        value: create(ResultSchema, {
          operation: "kent.api.server.server_service.get_readiness",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "notificationEvent",
        value: create(NotificationEventSchema, { payload: Uint8Array.of(1) }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "notificationEvent",
        value: create(NotificationEventSchema, {
          operation: "kent.api.process.output_service.event",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "transportFailure",
        value: create(TransportFailureSchema, {
          code: TransportFailureCode.UNKNOWN_OPERATION,
          correlation: "",
        }),
      },
    }),
    create(EnvelopeSchema, {
      frame: {
        case: "transportFailure",
        value: create(TransportFailureSchema, { code: 99 }),
      },
    }),
  ];

  for (const envelope of malformed) {
    const encoded = toBinary(EnvelopeSchema, envelope);
    assert.throws(() => unmarshalEnvelope(encoded));
  }
});

test("binary envelopes allow present zero-byte Empty payloads only", () => {
  const emptyPayloadFrames = [
    {
      case: "call",
      value: create(CallSchema, {
        operation: "kent.api.server.server_service.get_readiness",
        payload: new Uint8Array(),
      }),
    },
  ];
  for (const frame of emptyPayloadFrames) {
    const emptyPayload = create(EnvelopeSchema, { frame });
    const encoded = marshalEnvelope(emptyPayload);
    assert.deepEqual(toBinary(EnvelopeSchema, unmarshalEnvelope(encoded)), encoded);

    const rawEncoded = toBinary(EnvelopeSchema, emptyPayload);
    assert.deepEqual(toBinary(EnvelopeSchema, unmarshalEnvelope(rawEncoded)), rawEncoded);
  }

  const absentPayload = create(EnvelopeSchema, {
    frame: {
      case: "call",
      value: create(CallSchema, {
        operation: "kent.api.server.server_service.get_readiness",
      }),
    },
  });
  assert.throws(() => marshalEnvelope(absentPayload));

  const nonEmptyMessagePayload = create(EnvelopeSchema, {
    frame: {
      case: "notificationEvent",
      value: create(NotificationEventSchema, {
        operation: "kent.api.process.output_service.event",
        payload: new Uint8Array(),
      }),
    },
  });
  assert.throws(() => marshalEnvelope(nonEmptyMessagePayload));
  assert.throws(() => unmarshalEnvelope(toBinary(EnvelopeSchema, nonEmptyMessagePayload)));
});

test("operation results distinguish success, known typed failures, and unknown generic failures", () => {
  const success = create(CreateResultSchema, {
    outcome: {
      case: "success",
      value: create(CreateSuccessSchema, { resourceId: "resource-1" }),
    },
  });
  const classifiedSuccess = classifyOperationResult(CreateResultSchema, success);
  assert.equal(classifiedSuccess.kind, OperationOutcome.SUCCESS);
  assert.equal(classifiedSuccess.success.$typeName, "fixture.CreateSuccess");

  const knownFailure = create(CreateResultSchema, {
    outcome: {
      case: "error",
      value: create(CreateErrorSchema, {
        code: "resource_conflict",
        detail: {
          case: "resourceConflict",
          value: create(ResourceConflictDetailsSchema, { resourceId: "resource-1" }),
        },
      }),
    },
  });
  const classifiedKnown = classifyOperationResult(CreateResultSchema, knownFailure);
  assert.equal(classifiedKnown.kind, OperationOutcome.KNOWN_FAILURE);
  assert.equal(classifiedKnown.failure.code, "resource_conflict");
  assert.equal(classifiedKnown.failure.detail?.$typeName, "fixture.ResourceConflictDetails");

  const otherKnownFailure = create(CreateResultSchema, {
    outcome: {
      case: "error",
      value: create(CreateErrorSchema, {
        code: "invalid_input",
        detail: {
          case: "invalidInput",
          value: create(InvalidInputDetailsSchema, { field: "name" }),
        },
      }),
    },
  });
  const classifiedOtherKnown = classifyOperationResult(
    CreateResultSchema,
    otherKnownFailure,
  );
  assert.equal(classifiedOtherKnown.kind, OperationOutcome.KNOWN_FAILURE);
  assert.equal(classifiedOtherKnown.failure.code, "invalid_input");
  assert.equal(classifiedOtherKnown.failure.detail?.$typeName, "fixture.InvalidInputDetails");

  const unknownFailure = create(CreateResultSchema, {
    outcome: {
      case: "error",
      value: create(CreateErrorSchema, {
        code: "future_failure",
        detail: {
          case: "invalidInput",
          value: create(InvalidInputDetailsSchema, { field: "name" }),
        },
      }),
    },
  });
  const classifiedUnknown = classifyOperationResult(CreateResultSchema, unknownFailure);
  assert.equal(classifiedUnknown.kind, OperationOutcome.GENERIC_FAILURE);
  assert.equal(classifiedUnknown.failure.code, "future_failure");
  assert.equal(classifiedUnknown.failure.detail?.$typeName, "fixture.InvalidInputDetails");
});

test("operation results preserve a future unknown error detail", () => {
  const future = create(FutureCreateResultSchema, {
    outcome: {
      case: "error",
      value: create(FutureCreateErrorSchema, {
        code: "future_failure",
        detail: {
          case: "futureConflict",
          value: create(FutureConflictDetailsSchema, {
            resourceId: "resource-1",
            generation: 7,
          }),
        },
      }),
    },
  });
  const encoded = toBinary(FutureCreateResultSchema, future);
  const current = fromBinary(CreateResultSchema, encoded);

  const classified = classifyOperationResult(CreateResultSchema, current);
  assert.equal(classified.kind, OperationOutcome.GENERIC_FAILURE);
  assert.equal(classified.failure.code, "future_failure");
  assert.equal(classified.failure.detail, undefined);
  assert.deepEqual(toBinary(CreateResultSchema, current), encoded);
});

test("operation results reject missing outcomes, codes, and known required details", () => {
  const malformed = [
    create(CreateResultSchema),
    create(CreateResultSchema, {
      outcome: {
        case: "error",
        value: create(CreateErrorSchema, {
          detail: {
            case: "invalidInput",
            value: create(InvalidInputDetailsSchema, { field: "name" }),
          },
        }),
      },
    }),
    create(CreateResultSchema, {
      outcome: {
        case: "error",
        value: create(CreateErrorSchema, { code: "invalid_input" }),
      },
    }),
    create(CreateResultSchema, {
      outcome: {
        case: "error",
        value: create(CreateErrorSchema, {
          code: "invalid_input",
          detail: {
            case: "resourceConflict",
            value: create(ResourceConflictDetailsSchema, { resourceId: "resource-1" }),
          },
        }),
      },
    }),
  ];
  for (const result of malformed) {
    assert.throws(() => classifyOperationResult(CreateResultSchema, result));
  }
});

test("subscription start result is separate from post-ack events and completion", () => {
  const start = create(WatchStartResultSchema, {
    outcome: {
      case: "success",
      value: create(WatchAcknowledgementSchema),
    },
  });
  assert.equal(
    classifyOperationResult(WatchStartResultSchema, start).kind,
    OperationOutcome.SUCCESS,
  );
  assert.equal(isOperationResultDescriptor(WatchEventSchema), false);
  assert.equal(isOperationResultDescriptor(WatchCompletionSchema), false);
  assert.equal(NamingService.method.watch.output, WatchStartResultSchema);
  assert.equal(NamingService.method.watchEvent.input, WatchEventSchema);
  assert.equal(NamingService.method.watchComplete.input, WatchCompletionSchema);
});

test("transport failures remain distinct without string parsing", () => {
  assert.equal(isOperationResultDescriptor(TransportFailureSchema), false);
  assert.throws(() =>
    classifyOperationResult(
      TransportFailureSchema,
      create(TransportFailureSchema, {
        code: TransportFailureCode.INVALID_PAYLOAD,
      }),
    ),
  );
});

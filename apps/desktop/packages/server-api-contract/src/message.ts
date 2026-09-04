import {
  createRegistry,
  fromJson,
  fromBinary,
  toJson,
  toBinary,
  type DescMessage,
  type JsonValue,
  type MessageShape,
} from "@bufbuild/protobuf";
import { createValidator } from "@bufbuild/protovalidate";
import { canonical_name } from "./gen/kent/api/prompt_command/validation_pb.js";
import { scoped_session_id } from "./gen/kent/api/session_launch/validation_pb.js";
import { canonical_uuid_v4, nonblank, trimmed, workflow_key } from "./gen/kent/api/shared/validation_pb.js";

const validator = createValidator({
  registry: createRegistry(
    canonical_name,
    scoped_session_id,
    canonical_uuid_v4,
    nonblank,
    trimmed,
    workflow_key,
  ),
});

type JsonInput =
  string | number | boolean | null | readonly JsonInput[] | Readonly<{ [key: string]: JsonInput }>;

export function decode<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  bytes: Uint8Array,
): MessageShape<Descriptor> {
  const message = decodeUnvalidated(descriptor, bytes);
  validate(descriptor, message);
  return message;
}

export function decodeUnvalidated<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  bytes: Uint8Array,
): MessageShape<Descriptor> {
  return fromBinary(descriptor, bytes, {
    readUnknownFields: true,
  });
}

export function encode<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  message: MessageShape<Descriptor>,
): Uint8Array {
  validate(descriptor, message);
  return toBinary(descriptor, message, {
    writeUnknownFields: true,
  });
}

export function validate<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  message: MessageShape<Descriptor>,
): void {
  const result = validator.validate(descriptor, message);
  if (result.kind !== "valid") {
    throw result.error;
  }
}

export function decodeJson<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  json: JsonInput,
): MessageShape<Descriptor> {
  const message = fromJson(descriptor, mutableJsonValue(json), {
    ignoreUnknownFields: false,
  });
  validate(descriptor, message);
  return message;
}

export function encodeJson<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  message: MessageShape<Descriptor>,
): JsonValue {
  validate(descriptor, message);
  return toJson(descriptor, message, {
    alwaysEmitImplicit: true,
    useProtoFieldName: true,
  });
}

function mutableJsonValue(value: JsonInput): JsonValue {
  if (value === null) return null;
  if (Array.isArray(value)) return value.map(mutableJsonValue);
  if (value instanceof Object) {
    return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, mutableJsonValue(entry)]));
  }
  return value;
}

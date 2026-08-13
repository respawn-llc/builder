import type {
  DescField,
  DescMessage,
  Message,
} from "@bufbuild/protobuf";
import {
  getOption,
  hasOption,
  ScalarType,
} from "@bufbuild/protobuf";
import {
  isReflectMessage,
  reflect,
  type ReflectMessage,
} from "@bufbuild/protobuf/reflect";

import {
  kent_result_field,
  type KentResultFieldOptions,
  ResultFieldRole,
} from "./gen/kent/api/shared/foundation_pb.js";
import { validateGeneratedMessage } from "./message.js";

export const OperationOutcome = {
  SUCCESS: "success",
  KNOWN_FAILURE: "known_failure",
  GENERIC_FAILURE: "generic_failure",
} as const;

export type OperationOutcome =
  (typeof OperationOutcome)[keyof typeof OperationOutcome];

export type OperationFailure = Readonly<{
  code: string;
  detail?: Message;
}>;

export type OperationResult =
  | Readonly<{
      kind: typeof OperationOutcome.SUCCESS;
      success: Message;
    }>
  | Readonly<{
      kind:
        | typeof OperationOutcome.KNOWN_FAILURE
        | typeof OperationOutcome.GENERIC_FAILURE;
      failure: OperationFailure;
    }>;

type OperationErrorConvention = Readonly<{
  code: DescField;
  detail: NonNullable<DescField["oneof"]>;
  detailByCode: ReadonlyMap<string, DescField>;
}>;

type OperationResultConvention = Readonly<{
  outcome: NonNullable<DescField["oneof"]>;
  success: DescField;
  failure: DescField;
  error: OperationErrorConvention;
}>;

export function classifyOperationResult(
  descriptor: DescMessage,
  message: Message,
): OperationResult {
  const convention = inspectOperationResult(descriptor);
  validateGeneratedMessage(descriptor, message);
  const reflected = reflect(descriptor, message);
  const selected = reflected.oneofCase(convention.outcome);
  if (selected === undefined) {
    throw new Error(`${descriptor.typeName} has no outcome`);
  }
  if (selected === convention.success) {
    return {
      kind: OperationOutcome.SUCCESS,
      success: messageField(reflected, convention.success).message,
    };
  }
  if (selected !== convention.failure) {
    throw new Error(
      `${descriptor.typeName} selected an undeclared outcome field ${selected.name}`,
    );
  }
  return classifyOperationFailure(
    messageField(reflected, convention.failure),
    convention.error,
  );
}

export function isOperationResultDescriptor(descriptor: DescMessage): boolean {
  try {
    inspectOperationResult(descriptor);
    return true;
  } catch {
    return false;
  }
}

function inspectOperationResult(descriptor: DescMessage): OperationResultConvention {
  let outcome: DescField["oneof"];
  let success: DescField | undefined;
  let failure: DescField | undefined;
  for (const field of descriptor.fields) {
    const options = resultFieldOptions(field);
    if (options === undefined) {
      throw new Error(`${descriptor.typeName} field ${field.name} has no result role`);
    }
    const classified = classifyOutcomeField(descriptor, field, options);
    if (classified === "success") {
      success = uniqueResultField(descriptor, "success", success, field);
    } else {
      failure = uniqueResultField(descriptor, "error", failure, field);
    }
    outcome = resultOutcomeOneof(descriptor, field, options, outcome);
  }
  if (
    outcome === undefined ||
    success === undefined ||
    failure === undefined ||
    descriptor.fields.length !== 2 ||
    outcome.fields.length !== 2
  ) {
    throw new Error(
      `${descriptor.typeName} must declare exactly one success and one error field`,
    );
  }
  if (failure.fieldKind !== "message") {
    throw new Error(`${descriptor.typeName} error field must be a message`);
  }
  return {
    outcome,
    success,
    failure,
    error: inspectOperationError(failure.message),
  };
}

function classifyOutcomeField(
  descriptor: DescMessage,
  field: DescField,
  options: KentResultFieldOptions,
): "success" | "error" {
  if (options.role === ResultFieldRole.SUCCESS) {
    return "success";
  }
  if (options.role === ResultFieldRole.ERROR) {
    return "error";
  }
  throw new Error(
    `${descriptor.typeName} field ${field.name} has invalid top-level result role`,
  );
}

function uniqueResultField(
  descriptor: DescMessage,
  role: "success" | "error",
  existing: DescField | undefined,
  field: DescField,
): DescField {
  if (existing !== undefined) {
    throw new Error(`${descriptor.typeName} declares multiple ${role} fields`);
  }
  return field;
}

function resultOutcomeOneof(
  descriptor: DescMessage,
  field: DescField,
  options: KentResultFieldOptions,
  outcome: DescField["oneof"],
): NonNullable<DescField["oneof"]> {
  if (options.errorCode !== undefined) {
    throw new Error(
      `${descriptor.typeName} top-level field ${field.name} must not declare an error code`,
    );
  }
  if (field.fieldKind !== "message" || field.oneof === undefined) {
    throw new Error(
      `${descriptor.typeName} field ${field.name} must be a message in the outcome oneof`,
    );
  }
  if (outcome !== undefined && outcome !== field.oneof) {
    throw new Error(
      `${descriptor.typeName} success and error fields must share one outcome oneof`,
    );
  }
  return field.oneof;
}

function inspectOperationError(descriptor: DescMessage): OperationErrorConvention {
  let code: DescField | undefined;
  let detail: DescField["oneof"];
  const detailByCode = new Map<string, DescField>();
  for (const field of descriptor.fields) {
    const options = resultFieldOptions(field);
    if (options === undefined) {
      throw new Error(`${descriptor.typeName} field ${field.name} has no error role`);
    }
    if (options.role === ResultFieldRole.ERROR_CODE) {
      code = inspectErrorCodeField(descriptor, field, options, code);
    } else if (options.role === ResultFieldRole.ERROR_DETAIL) {
      detail = inspectErrorDetailField(
        descriptor,
        field,
        options,
        { detail, detailByCode },
      );
    } else {
      throw new Error(
        `${descriptor.typeName} field ${field.name} has invalid error role`,
      );
    }
  }
  if (code === undefined || detail?.fields.length !== detailByCode.size) {
    throw new Error(
      `${descriptor.typeName} must declare one error code and one typed detail oneof`,
    );
  }
  return { code, detail, detailByCode };
}

function inspectErrorCodeField(
  descriptor: DescMessage,
  field: DescField,
  options: KentResultFieldOptions,
  existing: DescField | undefined,
): DescField {
  if (
    existing !== undefined ||
    field.fieldKind !== "scalar" ||
    field.scalar !== ScalarType.STRING ||
    field.oneof !== undefined ||
    options.errorCode !== undefined
  ) {
    throw new Error(
      `${descriptor.typeName} field ${field.name} is not a valid error code field`,
    );
  }
  return field;
}

function inspectErrorDetailField(
  descriptor: DescMessage,
  field: DescField,
  options: KentResultFieldOptions,
  convention: Readonly<{
    detail: DescField["oneof"];
    detailByCode: Map<string, DescField>;
  }>,
): NonNullable<DescField["oneof"]> {
  if (field.fieldKind !== "message" || field.oneof === undefined) {
    throw new Error(
      `${descriptor.typeName} field ${field.name} is not a typed detail oneof field`,
    );
  }
  const errorCode = options.errorCode;
  if (errorCode === undefined || errorCode.length === 0) {
    throw new Error(
      `${descriptor.typeName} detail field ${field.name} requires a non-empty error code`,
    );
  }
  if (convention.detailByCode.has(errorCode)) {
    throw new Error(
      `${descriptor.typeName} declares duplicate detail code ${errorCode}`,
    );
  }
  if (convention.detail !== undefined && convention.detail !== field.oneof) {
    throw new Error(
      `${descriptor.typeName} detail fields must share one detail oneof`,
    );
  }
  convention.detailByCode.set(errorCode, field);
  return field.oneof;
}

function classifyOperationFailure(
  failure: ReflectMessage,
  convention: OperationErrorConvention,
): OperationResult {
  const codeValue = scalarStringField(failure, convention.code);
  if (codeValue.length === 0) {
    throw new Error(`${failure.desc.typeName} error code is required`);
  }
  const selectedDetail = failure.oneofCase(convention.detail);
  const requiredDetail = convention.detailByCode.get(codeValue);
  if (requiredDetail === undefined) {
    return {
      kind: OperationOutcome.GENERIC_FAILURE,
      failure: {
        code: codeValue,
        ...(selectedDetail === undefined
          ? {}
          : { detail: messageField(failure, selectedDetail).message }),
      },
    };
  }
  if (selectedDetail === undefined) {
    throw new Error(
      `${failure.desc.typeName} known error code ${codeValue} requires detail ${requiredDetail.name}`,
    );
  }
  if (selectedDetail !== requiredDetail) {
    throw new Error(
      `${failure.desc.typeName} known error code ${codeValue} requires detail ${requiredDetail.name}, got ${selectedDetail.name}`,
    );
  }
  return {
    kind: OperationOutcome.KNOWN_FAILURE,
    failure: {
      code: codeValue,
      detail: messageField(failure, selectedDetail).message,
    },
  };
}

function scalarStringField(message: ReflectMessage, field: DescField): string {
  if (field.fieldKind !== "scalar" || field.scalar !== ScalarType.STRING) {
    throw new Error(`${field.toString()} must be a string field`);
  }
  return message.get(field);
}

function messageField(
  message: ReflectMessage,
  field: DescField,
): ReflectMessage {
  if (field.fieldKind !== "message") {
    throw new Error(`${field.toString()} must be a message field`);
  }
  const value = message.get(field);
  if (!isReflectMessage(value, field.message)) {
    throw new Error(`${field.toString()} is required`);
  }
  return value;
}

function resultFieldOptions(
  field: DescField,
): KentResultFieldOptions | undefined {
  return hasOption(field, kent_result_field)
    ? getOption(field, kent_result_field)
    : undefined;
}

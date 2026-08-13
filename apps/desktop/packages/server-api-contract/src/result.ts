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
import { reflect, type ReflectMessage } from "@bufbuild/protobuf/reflect";

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
    switch (options.role) {
      case ResultFieldRole.SUCCESS:
        if (success !== undefined) {
          throw new Error(`${descriptor.typeName} declares multiple success fields`);
        }
        success = field;
        break;
      case ResultFieldRole.ERROR:
        if (failure !== undefined) {
          throw new Error(`${descriptor.typeName} declares multiple error fields`);
        }
        failure = field;
        break;
      default:
        throw new Error(
          `${descriptor.typeName} field ${field.name} has invalid top-level result role`,
        );
    }
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
    if (outcome === undefined) {
      outcome = field.oneof;
    } else if (outcome !== field.oneof) {
      throw new Error(
        `${descriptor.typeName} success and error fields must share one outcome oneof`,
      );
    }
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

function inspectOperationError(descriptor: DescMessage): OperationErrorConvention {
  let code: DescField | undefined;
  let detail: DescField["oneof"];
  const detailByCode = new Map<string, DescField>();
  for (const field of descriptor.fields) {
    const options = resultFieldOptions(field);
    if (options === undefined) {
      throw new Error(`${descriptor.typeName} field ${field.name} has no error role`);
    }
    switch (options.role) {
      case ResultFieldRole.ERROR_CODE:
        if (
          code !== undefined ||
          field.fieldKind !== "scalar" ||
          field.scalar !== ScalarType.STRING ||
          field.oneof !== undefined ||
          options.errorCode !== undefined
        ) {
          throw new Error(
            `${descriptor.typeName} field ${field.name} is not a valid error code field`,
          );
        }
        code = field;
        break;
      case ResultFieldRole.ERROR_DETAIL: {
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
        if (detailByCode.has(errorCode)) {
          throw new Error(
            `${descriptor.typeName} declares duplicate detail code ${errorCode}`,
          );
        }
        if (detail === undefined) {
          detail = field.oneof;
        } else if (detail !== field.oneof) {
          throw new Error(
            `${descriptor.typeName} detail fields must share one detail oneof`,
          );
        }
        detailByCode.set(errorCode, field);
        break;
      }
      default:
        throw new Error(
          `${descriptor.typeName} field ${field.name} has invalid error role`,
        );
    }
  }
  if (
    code === undefined ||
    detail === undefined ||
    detail.fields.length !== detailByCode.size
  ) {
    throw new Error(
      `${descriptor.typeName} must declare one error code and one typed detail oneof`,
    );
  }
  return { code, detail, detailByCode };
}

function classifyOperationFailure(
  failure: ReflectMessage,
  convention: OperationErrorConvention,
): OperationResult {
  const codeValue = failure.get(convention.code);
  if (typeof codeValue !== "string" || codeValue.length === 0) {
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

function messageField(
  message: ReflectMessage,
  field: DescField,
): ReflectMessage {
  if (field.fieldKind !== "message") {
    throw new Error(`${field.toString()} must be a message field`);
  }
  const value = message.get(field);
  if (value.message === undefined) {
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

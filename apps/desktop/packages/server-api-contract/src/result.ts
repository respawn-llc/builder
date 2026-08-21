import {
  getOption,
  hasOption,
  ScalarType,
  type DescField,
  type DescMessage,
  type DescOneof,
  type Message,
} from "@bufbuild/protobuf";
import { reflect } from "@bufbuild/protobuf/reflect";
import {
  kent_result_field,
  ResultFieldRole,
  type KentResultFieldOptions,
} from "./gen/kent/api/shared/foundation_pb.js";
import { validate } from "./message.js";

export type ResultFailureClassification = Readonly<{
  kind: "known" | "generic";
  code: string;
}>;

export function classifyResultFailure(
  resultDescriptor: DescMessage,
  failure: Message,
): ResultFailureClassification {
  const errorDescriptor = inspectResultError(resultDescriptor);
  validate(errorDescriptor.message, failure);
  const convention = inspectError(errorDescriptor.message);
  const reflected = reflect(errorDescriptor.message, failure);
  const code = reflected.get(convention.code);
  const selectedDetail = reflected.oneofCase(convention.detail);
  const requiredDetail = convention.detailByCode.get(code);
  if (requiredDetail === undefined) {
    return { kind: "generic", code };
  }
  if (selectedDetail === undefined) {
    throw new Error(
      `${errorDescriptor.message.typeName} known error code ${code} requires detail ${requiredDetail.name}`,
    );
  }
  if (selectedDetail !== requiredDetail) {
    throw new Error(
      `${errorDescriptor.message.typeName} known error code ${code} requires detail ${requiredDetail.name}, got ${selectedDetail.name}`,
    );
  }
  return { kind: "known", code };
}

type FieldWithOptions = Readonly<{
  field: DescField;
  options: KentResultFieldOptions;
}>;

type StringField = DescField & {
  fieldKind: "scalar";
  scalar: ScalarType.STRING;
};

type ErrorConvention = Readonly<{
  code: StringField;
  detail: DescOneof;
  detailByCode: ReadonlyMap<string, DescField>;
}>;

function inspectResultError(resultDescriptor: DescMessage): DescField & { fieldKind: "message" } {
  const fields = resultDescriptor.fields.map(withResultOptions);
  if (
    fields.some(
      ({ options }) => options.role !== ResultFieldRole.SUCCESS && options.role !== ResultFieldRole.ERROR,
    )
  ) {
    throw new Error(`${resultDescriptor.typeName} has an invalid top-level result role`);
  }
  const success = singleRoleField(resultDescriptor, fields, ResultFieldRole.SUCCESS);
  const error = singleRoleField(resultDescriptor, fields, ResultFieldRole.ERROR);
  const successOutcome = resultOutcome(success);
  const errorOutcome = resultOutcome(error);
  if (
    resultDescriptor.fields.length !== 2 ||
    successOutcome !== errorOutcome ||
    successOutcome.fields.length !== 2
  ) {
    throw new Error(`${resultDescriptor.typeName} must declare exactly one shared success/error outcome`);
  }
  if (error.field.fieldKind !== "message") {
    throw new Error(`${error.field.toString()} must be a message`);
  }
  return error.field;
}

function inspectError(errorDescriptor: DescMessage): ErrorConvention {
  const fields = errorDescriptor.fields.map(withResultOptions);
  if (
    fields.some(
      ({ options }) =>
        options.role !== ResultFieldRole.ERROR_CODE && options.role !== ResultFieldRole.ERROR_DETAIL,
    )
  ) {
    throw new Error(`${errorDescriptor.typeName} has an invalid error result role`);
  }
  const code = errorCodeField(singleRoleField(errorDescriptor, fields, ResultFieldRole.ERROR_CODE));
  const detailFields = fields
    .filter(({ options }) => options.role === ResultFieldRole.ERROR_DETAIL)
    .map(errorDetailField);
  const detail = detailFields[0]?.oneof;
  if (
    detail === undefined ||
    detailFields.length === 0 ||
    detailFields.some(({ oneof }) => oneof !== detail) ||
    detail.fields.length !== detailFields.length
  ) {
    throw new Error(`${errorDescriptor.typeName} must declare one typed detail oneof`);
  }
  const detailByCode = new Map<string, DescField>();
  for (const { code: fieldCode, field } of detailFields) {
    if (detailByCode.has(fieldCode)) {
      throw new Error(`${errorDescriptor.typeName} declares duplicate detail code ${fieldCode}`);
    }
    detailByCode.set(fieldCode, field);
  }
  return { code, detail, detailByCode };
}

function withResultOptions(field: DescField): FieldWithOptions {
  if (!hasOption(field, kent_result_field)) {
    throw new Error(`${field.toString()} has no result role`);
  }
  return { field, options: getOption(field, kent_result_field) };
}

function singleRoleField(
  descriptor: DescMessage,
  fields: readonly FieldWithOptions[],
  role: ResultFieldRole,
): FieldWithOptions {
  const matches = fields.filter(({ options }) => options.role === role);
  if (matches.length !== 1 || matches[0] === undefined) {
    throw new Error(`${descriptor.typeName} must declare exactly one field for result role ${String(role)}`);
  }
  return matches[0];
}

function resultOutcome({ field, options }: FieldWithOptions): DescOneof {
  if (options.errorCode !== undefined) {
    throw new Error(`${field.toString()} must not declare an error code`);
  }
  if (field.fieldKind !== "message" || field.oneof === undefined) {
    throw new Error(`${field.toString()} must be a message in the outcome oneof`);
  }
  return field.oneof;
}

function errorCodeField({ field, options }: FieldWithOptions): StringField {
  if (
    field.fieldKind !== "scalar" ||
    field.scalar !== ScalarType.STRING ||
    field.oneof !== undefined ||
    options.errorCode !== undefined
  ) {
    throw new Error(`${field.toString()} is not a valid error code field`);
  }
  return field;
}

function errorDetailField({ field, options }: FieldWithOptions): Readonly<{
  code: string;
  field: DescField;
  oneof: DescOneof;
}> {
  if (
    field.fieldKind !== "message" ||
    field.oneof === undefined ||
    options.errorCode === undefined ||
    options.errorCode.length === 0
  ) {
    throw new Error(`${field.toString()} is not a typed error detail field`);
  }
  return { code: options.errorCode, field, oneof: field.oneof };
}

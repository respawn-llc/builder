import { getOption, hasOption } from "@bufbuild/protobuf";
import type { DescMethod } from "@bufbuild/protobuf";
import type { GenFile } from "@bufbuild/protobuf/codegenv2";

import {
  AuthenticationStage,
  Direction,
  kent_method,
  type KentMethodOptions,
  type OperationAssociation as OperationAssociationOption,
  OperationKind,
  ScopePolicy,
  UnaryConnection,
} from "./gen/kent/api/shared/foundation_pb.js";
import {
  descriptorPaths as generatedDescriptorPaths,
  fileDescriptors,
} from "./gen/registry/registry.js";

type GeneratedDescriptorFile = (typeof fileDescriptors)[number];
type GeneratedMethod = GeneratedDescriptorFile["services"][number]["methods"][number];

export type OperationAssociation = Readonly<{
  activeName: string;
  descriptor: DescMethod;
}>;

export type Operation = Readonly<{
  activeName: string;
  legacyWireName?: string;
  descriptor: DescMethod;
  options: KentMethodOptions;
  event?: OperationAssociation;
  completion?: OperationAssociation;
}>;

function isAsciiUpper(code: number): boolean {
  return code >= 65 && code <= 90;
}

function isAsciiLower(code: number): boolean {
  return code >= 97 && code <= 122;
}

function isAsciiDigit(code: number): boolean {
  return code >= 48 && code <= 57;
}

export function validatePackageName(packageName: string): void {
  if (packageName.length === 0) {
    throw new Error("package is empty");
  }
  let atSegmentStart = true;
  for (let index = 0; index < packageName.length; index += 1) {
    const code = packageName.charCodeAt(index);
    if (code === 46) {
      if (atSegmentStart) {
        throw new Error(`package segment at code unit ${String(index)} is empty`);
      }
      atSegmentStart = true;
      continue;
    }
    if (atSegmentStart) {
      if (!isAsciiLower(code)) {
        throw new Error(
          `package segment at code unit ${String(index)} must start with an ASCII lowercase letter`,
        );
      }
      atSegmentStart = false;
      continue;
    }
    if (!isAsciiLower(code) && !isAsciiDigit(code) && code !== 95) {
      throw new Error(`invalid package character at code unit ${String(index)}`);
    }
  }
  if (atSegmentStart) {
    throw new Error("package has an empty trailing segment");
  }
}

export function pascalCaseToLowerSnake(identifier: string): string {
  if (identifier.length === 0) {
    throw new Error("identifier is empty");
  }
  if (!isAsciiUpper(identifier.charCodeAt(0))) {
    throw new Error("identifier must start with an ASCII uppercase letter");
  }
  const result: string[] = [];
  for (let index = 0; index < identifier.length; index += 1) {
    const code = identifier.charCodeAt(index);
    validateIdentifierCode(code, index);
    appendIdentifierBoundary(result, identifier, index, code);
    result.push(String.fromCharCode(isAsciiUpper(code) ? code + 32 : code));
  }
  return result.join("");
}

function validateIdentifierCode(code: number, index: number): void {
  if (!isAsciiUpper(code) && !isAsciiLower(code) && !isAsciiDigit(code)) {
    throw new Error(`invalid identifier character at code unit ${String(index)}`);
  }
}

function appendIdentifierBoundary(
  result: string[],
  identifier: string,
  index: number,
  code: number,
): void {
  if (!isAsciiUpper(code) || index === 0) {
    return;
  }
  const previous = identifier.charCodeAt(index - 1);
  const hasFollowingLower =
    index + 1 < identifier.length && isAsciiLower(identifier.charCodeAt(index + 1));
  if (
    isAsciiLower(previous) ||
    isAsciiDigit(previous) ||
    (isAsciiUpper(previous) && hasFollowingLower)
  ) {
    result.push("_");
  }
}

export function activeOperationName(
  packageName: string,
  service: string,
  method: string,
): string {
  validatePackageName(packageName);
  return `${packageName}.${pascalCaseToLowerSnake(service)}.${pascalCaseToLowerSnake(method)}`;
}

export function validateKentMethodOptions(options: KentMethodOptions): void {
  validateAuthenticationStage(options.authenticationStage);
  validateScopePolicy(options.scopePolicy);
  validateDirection(options.direction);
  validateOperationKind(options);
}

function validateAuthenticationStage(stage: AuthenticationStage): void {
  switch (stage) {
    case AuthenticationStage.NONE:
    case AuthenticationStage.PRE_SERVER:
    case AuthenticationStage.SERVER:
      return;
    case AuthenticationStage.UNSPECIFIED:
      throw new Error("authentication stage is required");
    default:
      throw new Error(`authentication stage ${String(stage)} is invalid`);
  }
}

const validScopePolicies: ReadonlySet<ScopePolicy> = new Set([
  ScopePolicy.NONE,
  ScopePolicy.ATTACH_PROJECT,
  ScopePolicy.ATTACH_SESSION,
  ScopePolicy.PROJECT_VIEW,
  ScopePolicy.PROJECT_WORKSPACE,
  ScopePolicy.PROJECT_WORKSPACE_BINDING,
  ScopePolicy.SESSION_ACTIVE_PROJECT,
  ScopePolicy.SESSION_ACTIVE_PROJECT_IF_SET,
  ScopePolicy.SESSION_ATTACHED_PROJECT,
  ScopePolicy.ATTACHED_SESSION,
  ScopePolicy.GOAL_SESSION,
  ScopePolicy.RUNTIME_LIVE_SESSION_REQUIRED,
  ScopePolicy.RUNTIME_LIVE_SESSION_OPTIONAL,
  ScopePolicy.PROCESS_ACTIVE_PROJECT,
  ScopePolicy.PROCESS_LIST_ACTIVE_PROJECT,
  ScopePolicy.NOTIFICATION,
]);

function validateScopePolicy(policy: ScopePolicy): void {
  if (policy === ScopePolicy.UNSPECIFIED) {
    throw new Error("scope policy is required");
  }
  if (!validScopePolicies.has(policy)) {
    throw new Error(`scope policy ${String(policy)} is invalid`);
  }
}

function validateDirection(direction: Direction): void {
  switch (direction) {
    case Direction.CLIENT_TO_SERVER:
    case Direction.SERVER_TO_CLIENT:
      return;
    case Direction.UNSPECIFIED:
      throw new Error("direction is required");
    default:
      throw new Error(`direction ${String(direction)} is invalid`);
  }
}

function validateOperationKind(options: KentMethodOptions): void {
  switch (options.kind) {
    case OperationKind.UNARY:
      validateUnaryOperation(options);
      break;
    case OperationKind.SUBSCRIPTION:
      validateSubscriptionOperation(options);
      break;
    case OperationKind.PROGRESS:
      validateProgressOperation(options);
      break;
    case OperationKind.NOTIFICATION:
      validateNotificationOperation(options);
      break;
    case OperationKind.UNSPECIFIED:
      throw new Error("operation kind is required");
    default:
      throw new Error(`operation kind ${String(options.kind)} is invalid`);
  }
}

function validateUnaryOperation(options: KentMethodOptions): void {
  switch (options.unaryConnection) {
    case UnaryConnection.MULTIPLEXED:
    case UnaryConnection.DEDICATED:
      break;
    case UnaryConnection.UNSPECIFIED:
      throw new Error("unary connection is required for unary operation");
    default:
      throw new Error(`unary connection ${String(options.unaryConnection)} is invalid`);
  }
  if (options.event !== undefined || options.completion !== undefined) {
    throw new Error("unary operation must not declare event or completion association");
  }
}

function validateSubscriptionOperation(options: KentMethodOptions): void {
  validateNonUnaryConnection(options);
  if (options.event === undefined || options.completion === undefined) {
    throw new Error("subscription operation requires event and completion associations");
  }
}

function validateProgressOperation(options: KentMethodOptions): void {
  validateNonUnaryConnection(options);
  if (options.event === undefined || options.completion !== undefined) {
    throw new Error("progress operation requires only an event association");
  }
}

function validateNotificationOperation(options: KentMethodOptions): void {
  validateNonUnaryConnection(options);
  if (options.event !== undefined || options.completion !== undefined) {
    throw new Error("notification operation must not declare event or completion association");
  }
}

function validateNonUnaryConnection(options: KentMethodOptions): void {
  if (options.unaryConnection !== UnaryConnection.UNSPECIFIED) {
    throw new Error("non-unary operation must not declare unary connection");
  }
}

export function operationFromDescriptor(
  descriptor: GeneratedMethod,
  options: KentMethodOptions | undefined,
): Operation {
  if (options === undefined) {
    throw new Error(`${descriptor.toString()} method options are required`);
  }
  validateKentMethodOptions(options);
  const activeName = activeOperationName(
    descriptor.parent.file.proto.package,
    descriptor.parent.name,
    descriptor.name,
  );
  const legacyWireName =
    options.legacyWireName === undefined
      ? undefined
      : requiredLegacyWireName(descriptor, options.legacyWireName);
  return {
    activeName,
    ...(legacyWireName === undefined ? {} : { legacyWireName }),
    descriptor,
    options,
  };
}

export function operations(): readonly Operation[] {
  const descriptors: GeneratedMethod[] = [];
  for (const descriptorFile of fileDescriptors) {
    for (const service of descriptorFile.services) {
      descriptors.push(...service.methods);
    }
  }

  const byDeclaration = new Map(
    descriptors.map((descriptor) => [methodDeclarationName(descriptor), descriptor]),
  );
  const seenActiveNames = new Set<string>();
  const indexed = descriptors.map((descriptor) => {
    const options = hasOption(descriptor, kent_method)
      ? getOption(descriptor, kent_method)
      : undefined;
    const operation = operationFromDescriptor(descriptor, options);
    if (seenActiveNames.has(operation.activeName)) {
      throw new Error(`duplicate active operation name ${operation.activeName}`);
    }
    seenActiveNames.add(operation.activeName);
    return operation;
  });
  const associated = indexed.map((operation): Operation => ({
    ...operation,
    ...(operation.options.event === undefined
      ? {}
      : { event: resolveAssociation(operation.options.event, byDeclaration) }),
    ...(operation.options.completion === undefined
      ? {}
      : { completion: resolveAssociation(operation.options.completion, byDeclaration) }),
  }));
  associated.sort((left, right) => left.activeName.localeCompare(right.activeName));
  return associated;
}

export function operationByName(activeName: string): Operation | undefined {
  return operations().find((operation) => operation.activeName === activeName);
}

function requiredLegacyWireName(descriptor: DescMethod, legacyWireName: string): string {
  if (legacyWireName.length === 0) {
    throw new Error(`${descriptor.toString()} legacy wire name must not be empty`);
  }
  return legacyWireName;
}

function resolveAssociation(
  declaration: OperationAssociationOption,
  descriptors: ReadonlyMap<string, DescMethod>,
): OperationAssociation {
  validatePackageName(declaration.package);
  pascalCaseToLowerSnake(declaration.service);
  pascalCaseToLowerSnake(declaration.method);
  const declarationName = `${declaration.package}.${declaration.service}.${declaration.method}`;
  const descriptor = descriptors.get(declarationName);
  if (descriptor === undefined) {
    throw new Error(`method declaration ${declarationName} does not exist`);
  }
  return {
    activeName: activeOperationName(
      descriptor.parent.file.proto.package,
      descriptor.parent.name,
      descriptor.name,
    ),
    descriptor,
  };
}

function methodDeclarationName(descriptor: DescMethod): string {
  return `${descriptor.parent.typeName}.${descriptor.name}`;
}

const filesByPath = new Map(
  fileDescriptors.map((descriptor) => [`${descriptor.name}.proto`, descriptor]),
);

export function files(): Iterable<GenFile> {
  return fileDescriptors;
}

export function file(path: string): GenFile | undefined {
  return filesByPath.get(path);
}

export function descriptorPaths(): readonly string[] {
  return generatedDescriptorPaths;
}

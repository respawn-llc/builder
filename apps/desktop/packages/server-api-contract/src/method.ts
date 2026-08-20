import { getOption, hasOption, type DescMethod } from "@bufbuild/protobuf";
import {
  AuthenticationStage,
  Direction,
  kent_method,
  type KentMethodOptions,
  OperationKind,
  ScopePolicy,
  UnaryConnection,
} from "./gen/kent/api/shared/foundation_pb.js";

export type Operation = Readonly<{
  name: string;
  legacyWireName?: string;
  descriptor: DescMethod;
  options: KentMethodOptions;
}>;

export type UnaryConnectionPolicy = "multiplexed" | "dedicated";

export function unaryConnectionPolicy(operation: Operation): UnaryConnectionPolicy {
  switch (operation.options.unaryConnection) {
    case UnaryConnection.MULTIPLEXED:
      return "multiplexed";
    case UnaryConnection.DEDICATED:
      return "dedicated";
    case UnaryConnection.UNSPECIFIED:
      throw new Error(`${operation.name} has no unary connection policy`);
  }
}

export function operationFromDescriptor(descriptor: DescMethod): Operation {
  if (!hasOption(descriptor, kent_method)) {
    throw new Error(`${descriptor.toString()} method options are required`);
  }
  const options = getOption(descriptor, kent_method);
  validateMethodOptions(options);
  const service = descriptor.parent;
  const packageLength = service.typeName.length - service.name.length - 1;
  if (packageLength <= 0 || service.typeName[packageLength] !== ".") {
    throw new Error(`${service.toString()} has no package`);
  }
  const name = activeOperationName(service.typeName.slice(0, packageLength), service.name, descriptor.name);
  const legacyWireName =
    options.legacyWireName === undefined
      ? undefined
      : requiredLegacyWireName(descriptor, options.legacyWireName);
  return {
    name,
    ...(legacyWireName === undefined ? {} : { legacyWireName }),
    descriptor,
    options,
  };
}

export function activeOperationName(packageName: string, service: string, method: string): string {
  validatePackageName(packageName);
  return `${packageName}.${pascalCaseToLowerSnake(service)}.${pascalCaseToLowerSnake(method)}`;
}

function requiredLegacyWireName(descriptor: DescMethod, legacyWireName: string): string {
  if (legacyWireName.length === 0) {
    throw new Error(`${descriptor.toString()} legacy wire name must not be empty`);
  }
  return legacyWireName;
}

function validateMethodOptions(options: KentMethodOptions): void {
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
      return;
    case OperationKind.SUBSCRIPTION:
      validateSubscriptionOperation(options);
      return;
    case OperationKind.PROGRESS:
      validateProgressOperation(options);
      return;
    case OperationKind.NOTIFICATION:
      validateNotificationOperation(options);
      return;
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
  requireNonUnaryConnection(options);
  if (options.event === undefined || options.completion === undefined) {
    throw new Error("subscription operation requires event and completion associations");
  }
}

function validateProgressOperation(options: KentMethodOptions): void {
  requireNonUnaryConnection(options);
  if (options.event === undefined || options.completion !== undefined) {
    throw new Error("progress operation requires only an event association");
  }
}

function validateNotificationOperation(options: KentMethodOptions): void {
  requireNonUnaryConnection(options);
  if (options.event !== undefined || options.completion !== undefined) {
    throw new Error("notification operation must not declare event or completion association");
  }
}

function requireNonUnaryConnection(options: KentMethodOptions): void {
  if (options.unaryConnection !== UnaryConnection.UNSPECIFIED) {
    throw new Error("non-unary operation must not declare unary connection");
  }
}

function validatePackageName(packageName: string): void {
  if (packageName.length === 0) {
    throw new Error("package is empty");
  }
  let atSegmentStart = true;
  for (let index = 0; index < packageName.length; index += 1) {
    const character = packageName.charCodeAt(index);
    if (character === 46) {
      if (atSegmentStart) {
        throw new Error(`package segment at index ${String(index)} is empty`);
      }
      atSegmentStart = true;
      continue;
    }
    if (atSegmentStart) {
      if (!isAsciiLower(character)) {
        throw new Error(
          `package segment at index ${String(index)} must start with an ASCII lowercase letter`,
        );
      }
      atSegmentStart = false;
      continue;
    }
    if (!isAsciiLower(character) && !isAsciiDigit(character) && character !== 95) {
      throw new Error(`invalid package character at index ${String(index)}`);
    }
  }
  if (atSegmentStart) {
    throw new Error("package has an empty trailing segment");
  }
}

function pascalCaseToLowerSnake(identifier: string): string {
  if (identifier.length === 0) {
    throw new Error("identifier is empty");
  }
  if (!isAsciiUpper(identifier.charCodeAt(0))) {
    throw new Error("identifier must start with an ASCII uppercase letter");
  }
  const result: string[] = [];
  for (let index = 0; index < identifier.length; index += 1) {
    const character = identifier.charCodeAt(index);
    validateIdentifierCharacter(character, index);
    if (shouldInsertSeparator(identifier, index, character)) {
      result.push("_");
    }
    result.push(String.fromCharCode(lowerAscii(character)));
  }
  return result.join("");
}

function validateIdentifierCharacter(character: number, index: number): void {
  if (!isAsciiUpper(character) && !isAsciiLower(character) && !isAsciiDigit(character)) {
    throw new Error(`invalid identifier character at index ${String(index)}`);
  }
}

function shouldInsertSeparator(identifier: string, index: number, character: number): boolean {
  if (!isAsciiUpper(character) || index === 0) {
    return false;
  }
  const previous = identifier.charCodeAt(index - 1);
  const hasFollowingLower = index + 1 < identifier.length && isAsciiLower(identifier.charCodeAt(index + 1));
  return isAsciiLower(previous) || isAsciiDigit(previous) || (isAsciiUpper(previous) && hasFollowingLower);
}

function lowerAscii(character: number): number {
  return isAsciiUpper(character) ? character + 32 : character;
}

function isAsciiUpper(character: number): boolean {
  return character >= 65 && character <= 90;
}

function isAsciiLower(character: number): boolean {
  return character >= 97 && character <= 122;
}

function isAsciiDigit(character: number): boolean {
  return character >= 48 && character <= 57;
}

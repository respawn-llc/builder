import { getOption, hasOption, type DescFile, type DescMethod } from "@bufbuild/protobuf";
import {
  Direction,
  kent_method,
  OperationKind,
  type KentMethodOptions,
  type OperationAssociation,
  UnaryConnection,
} from "./gen/kent/api/shared/foundation_pb.js";

export type UnaryConnectionPolicy = "multiplexed" | "dedicated";

export function operationName(method: DescMethod): string {
  const packageName = method.parent.file.proto.package;
  if (packageName.length === 0) {
    throw new Error(`${method.parent.toString()} has no package`);
  }
  return `${packageName}.${lowerSnake(method.parent.name)}.${lowerSnake(method.name)}`;
}

function methodOptions(method: DescMethod): KentMethodOptions {
  if (!hasOption(method, kent_method)) {
    throw new Error(`${method.toString()} method options are required`);
  }
  return getOption(method, kent_method);
}

export function unaryConnectionPolicy(method: DescMethod): UnaryConnectionPolicy {
  switch (methodOptions(method).unaryConnection) {
    case UnaryConnection.MULTIPLEXED:
      return "multiplexed";
    case UnaryConnection.DEDICATED:
      return "dedicated";
    case UnaryConnection.UNSPECIFIED:
      throw new Error(`${operationName(method)} has no unary connection policy`);
  }
}

export type SubscriptionAssociations = Readonly<{
  event: DescMethod;
  completion: DescMethod;
}>;

export function subscriptionAssociations(method: DescMethod): SubscriptionAssociations {
  const operation = operationName(method);
  const options = methodOptions(method);
  if (options.kind !== OperationKind.SUBSCRIPTION || options.direction !== Direction.CLIENT_TO_SERVER) {
    throw new Error(`${operation} is not a client-to-server subscription`);
  }
  const event = resolveAssociation(method.parent.file, options.event, "event", operation);
  const completion = resolveAssociation(method.parent.file, options.completion, "completion", operation);
  requireNotification(event, "event", operation);
  requireNotification(completion, "completion", operation);
  return { event, completion };
}

function resolveAssociation(
  file: DescFile,
  association: OperationAssociation | undefined,
  kind: "event" | "completion",
  operation: string,
): DescMethod {
  if (association === undefined) {
    throw new Error(`${operation} has no ${kind} association`);
  }
  const matches: DescMethod[] = [];
  const visited = new Set<DescFile>();
  const visit = (candidate: DescFile) => {
    if (visited.has(candidate)) return;
    visited.add(candidate);
    if (candidate.proto.package === association.package) {
      for (const service of candidate.services) {
        if (service.name !== association.service) continue;
        for (const method of service.methods) {
          if (method.name === association.method) matches.push(method);
        }
      }
    }
    for (const dependency of candidate.dependencies) visit(dependency);
  };
  visit(file);
  if (matches.length !== 1 || matches[0] === undefined) {
    throw new Error(`${operation} ${kind} association does not resolve to one method`);
  }
  return matches[0];
}

function requireNotification(method: DescMethod, kind: "event" | "completion", subscription: string): void {
  const options = methodOptions(method);
  if (options.kind !== OperationKind.NOTIFICATION || options.direction !== Direction.SERVER_TO_CLIENT) {
    throw new Error(`${subscription} ${kind} association is not a server-to-client notification`);
  }
}

function lowerSnake(identifier: string): string {
  const result: string[] = [];
  for (let index = 0; index < identifier.length; index += 1) {
    const character = identifier.charCodeAt(index);
    if (shouldInsertSeparator(identifier, index, character)) {
      result.push("_");
    }
    result.push(String.fromCharCode(toLowerAscii(character)));
  }
  return result.join("");
}

function shouldInsertSeparator(identifier: string, index: number, character: number): boolean {
  if (!isAsciiUpper(character) || index === 0) {
    return false;
  }
  const previous = identifier.charCodeAt(index - 1);
  const hasFollowingLower = index + 1 < identifier.length && isAsciiLower(identifier.charCodeAt(index + 1));
  return isAsciiLower(previous) || isAsciiDigit(previous) || (isAsciiUpper(previous) && hasFollowingLower);
}

function toLowerAscii(character: number): number {
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

import { getOption, hasOption, type DescMethod } from "@bufbuild/protobuf";
import { kent_method, type KentMethodOptions, UnaryConnection } from "./gen/kent/api/shared/foundation_pb.js";

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

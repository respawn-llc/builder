import { z } from "zod";

export type JsonPrimitive = string | number | boolean | null;
export type JsonArray = readonly JsonValue[];
export type JsonObject = Readonly<{
  [key: string]: JsonValue;
}>;
export type JsonValue = JsonPrimitive | JsonArray | JsonObject;

export const jsonValueSchema: z.ZodType<JsonValue> = z.lazy(() =>
  z.union([
    z.string(),
    z.number(),
    z.boolean(),
    z.null(),
    z.array(jsonValueSchema),
    z.record(z.string(), jsonValueSchema),
  ]),
);

export const emptyJsonObject: JsonObject = {};

export function stringRecordToJson(value: Readonly<Record<string, string>>): JsonObject {
  return Object.fromEntries(Object.entries(value));
}

export function compactJsonObject(value: Readonly<Record<string, JsonValue | undefined>>): JsonObject {
  return Object.fromEntries(
    Object.entries(value).filter((entry): entry is [string, JsonValue] => entry[1] !== undefined),
  );
}

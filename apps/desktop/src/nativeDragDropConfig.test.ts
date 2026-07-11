import { describe, expect, it } from "vitest";
import { z } from "zod";

import tauriConfig from "../src-tauri/tauri.conf.json";

const objectSchema = z.record(z.string(), z.unknown());
const mainWindowConfigSchema = objectSchema.and(z.object({ title: z.literal("Kent") }));

describe("Tauri drag/drop config", () => {
  it("keeps the native drag/drop handler disabled so HTML5 board DnD works", async () => {
    const config = parseObject(tauriConfig);
    const app = readObject(config, "app");
    const windows = readArray(app, "windows");
    const mainWindow = windows.find(isMainWindowConfig);

    expect(mainWindow).toBeDefined();
    expect(mainWindow?.dragDropEnabled).toBe(false);
  });
});

function parseObject(value: unknown): Readonly<Record<string, unknown>> {
  const parsed = objectSchema.safeParse(value);
  if (!parsed.success || Array.isArray(value)) {
    throw new Error("Tauri config root must be an object.");
  }
  return parsed.data;
}

function readObject(
  value: Readonly<Record<string, unknown>>,
  key: string,
): Readonly<Record<string, unknown>> {
  const item = value[key];
  const parsed = objectSchema.safeParse(item);
  if (!parsed.success || Array.isArray(item)) {
    throw new Error(`Tauri config field ${key} must be an object.`);
  }
  return parsed.data;
}

function readArray(value: Readonly<Record<string, unknown>>, key: string): readonly unknown[] {
  const item = value[key];
  if (!Array.isArray(item)) {
    throw new Error(`Tauri config field ${key} must be an array.`);
  }
  return item;
}

function isMainWindowConfig(value: unknown): value is Readonly<Record<string, unknown>> {
  return mainWindowConfigSchema.safeParse(value).success;
}

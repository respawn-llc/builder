// @vitest-environment node
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { z } from "zod";
import config from "./vite.config";

it("injects the shared protocol version", () => {
  const protocol = z
    .object({ version: z.string().min(1) })
    .parse(
      JSON.parse(readFileSync(new URL("../../../shared/protocol/version.json", import.meta.url), "utf8")),
    );
  expect(config.define?.__KENT_PROTOCOL_VERSION__).toBe(JSON.stringify(protocol.version));
});

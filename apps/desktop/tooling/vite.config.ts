/// <reference lib="dom" />
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { BrowserCommandContext } from "vitest/node";
import { configDefaults, defineConfig } from "vitest/config";
import { z } from "zod";

const protocolVersionDefinition = z
  .object({ version: z.string().min(1) })
  .parse(JSON.parse(readFileSync(new URL("../../../shared/protocol/version.json", import.meta.url), "utf8")));

export type ReorderPointerCommandInput = Readonly<{
  sourceSelector: string;
  destination:
    | Readonly<{ kind: "source" }>
    | Readonly<{
        kind: "target";
        placement: "center" | "gap";
        selector: string;
      }>;
}>;

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    pointerDrag: (input: ReorderPointerCommandInput) => Promise<void>;
  }
}

type PointerBox = Readonly<{
  height: number;
  width: number;
  x: number;
  y: number;
}>;

type PointerFrame = Readonly<{
  locator(selector: string): Readonly<{
    boundingBox(): Promise<PointerBox | null>;
  }>;
}>;

type PointerCommandRuntimeContext = Readonly<{
  frame(): Promise<PointerFrame>;
  page: Readonly<{
    mouse: Readonly<{
      down(): Promise<void>;
      move(x: number, y: number): Promise<void>;
      up(): Promise<void>;
    }>;
  }>;
}>;

const pointerCommandRuntimeContextSchema = z.custom<PointerCommandRuntimeContext>(
  (value) => value instanceof Object,
);

export const pointerDrag = async (
  { provider, sessionId }: BrowserCommandContext,
  input: ReorderPointerCommandInput,
): Promise<void> => {
  const runtimeContext = pointerCommandRuntimeContextSchema.parse(provider.getCommandsContext(sessionId));
  const testFrame = await runtimeContext.frame();
  const sourceBox = await resolveBox(testFrame, input.sourceSelector, "source");
  const destinationBox =
    input.destination.kind === "source"
      ? sourceBox
      : await resolveBox(testFrame, input.destination.selector, "destination");
  const destination = projectPointerDestination(destinationBox, input.destination);
  const sourceX = sourceBox.x + sourceBox.width / 2;
  const sourceY = sourceBox.y + sourceBox.height / 2;
  await runtimeContext.page.mouse.move(sourceX, sourceY);
  await runtimeContext.page.mouse.down();
  await runtimeContext.page.mouse.move(sourceX, sourceY + 7);
  await runtimeContext.page.mouse.move(destination.x, destination.y);
  await runtimeContext.page.mouse.up();
};

async function resolveBox(frame: PointerFrame, selector: string, role: string): Promise<PointerBox> {
  const box = await frame.locator(selector).boundingBox();
  if (box === null) {
    throw new Error(`Cannot locate pointer-drag ${role}: ${selector}`);
  }
  return box;
}

function projectPointerDestination(
  box: PointerBox,
  destination: ReorderPointerCommandInput["destination"],
): Readonly<{ x: number; y: number }> {
  if (destination.kind === "source") {
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  }
  return {
    x: box.x + box.width / 2,
    y: destination.placement === "center" ? box.y + box.height / 2 : box.y - 4,
  };
}

export default defineConfig({
  plugins: [tailwindcss(), react()],
  clearScreen: false,
  define: {
    __KENT_PROTOCOL_VERSION__: JSON.stringify(protocolVersionDefinition.version),
  },
  build: {
    // Desktop bundle loads from local disk; keep warning for real growth without treating current MVP size as web risk.
    chunkSizeWarningLimit: 2_048,
    manifest: true,
    // Never ship JS source maps in release artifacts (they expose original
    // source). Matches Vite's default; pinned explicitly to prevent regressions.
    sourcemap: false,
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("../src", import.meta.url)),
      "@app/native-bridge": fileURLToPath(new URL("../packages/native-bridge/src/index.ts", import.meta.url)),
      "@app/ui-kit": fileURLToPath(new URL("../packages/ui-kit/src/index.ts", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 1420,
    strictPort: true,
    watch: {
      ignored: ["**/src-tauri/**"],
    },
  },
  test: {
    environment: "jsdom",
    exclude: [...configDefaults.exclude, "eslint-fixtures/**", "**/*.browser.test.*"],
    globals: true,
    maxWorkers: 2,
    setupFiles: ["./test/setup.ts"],
  },
});

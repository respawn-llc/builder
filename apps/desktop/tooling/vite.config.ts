import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { configDefaults, defineConfig } from "vitest/config";
import { z } from "zod";

const protocolVersionDefinition = z
  .object({ version: z.string().min(1) })
  .parse(JSON.parse(readFileSync(new URL("../../../shared/protocol/version.json", import.meta.url), "utf8")));

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
    alias: [
      {
        find: "@app/server-api-contract/gen",
        replacement: fileURLToPath(new URL("../packages/server-api-contract/src/gen", import.meta.url)),
      },
      {
        find: "@app/server-api-contract",
        replacement: fileURLToPath(new URL("../packages/server-api-contract/src/index.ts", import.meta.url)),
      },
      { find: "@", replacement: fileURLToPath(new URL("../src", import.meta.url)) },
      {
        find: "@app/native-bridge",
        replacement: fileURLToPath(new URL("../packages/native-bridge/src/index.ts", import.meta.url)),
      },
      {
        find: "@app/ui-kit",
        replacement: fileURLToPath(new URL("../packages/ui-kit/src/ReorderableList.tsx", import.meta.url)),
      },
    ],
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
    exclude: [...configDefaults.exclude, "eslint-fixtures/**"],
    globals: true,
    maxWorkers: 2,
    setupFiles: ["./test/setup.ts"],
  },
});

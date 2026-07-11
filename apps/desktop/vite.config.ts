import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [tailwindcss(), react()],
  clearScreen: false,
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
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      "@app/native-bridge": fileURLToPath(
        new URL("./packages/native-bridge/src/index.ts", import.meta.url),
      ),
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
    globals: true,
    maxWorkers: 4,
    setupFiles: ["./test/setup.ts"],
  },
});

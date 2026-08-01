import { env } from "node:process";
import { mergeConfig, defineConfig } from "vitest/config";
import { playwright } from "@vitest/browser-playwright";
import baseConfig, { pointerDrag } from "./vite.config";

const chromiumExecutablePath = env.KENT_PLAYWRIGHT_CHROMIUM_PATH;
const browserProvider =
  chromiumExecutablePath === undefined
    ? playwright()
    : playwright({ launchOptions: { executablePath: chromiumExecutablePath } });

const browserConfig = mergeConfig(
  baseConfig,
  defineConfig({
    test: {
      include: ["packages/ui-kit/src/**/*.browser.test.tsx"],
      browser: {
        enabled: true,
        commands: { pointerDrag },
        headless: true,
        provider: browserProvider,
        instances: [{ browser: "chromium" }],
      },
    },
  }),
);

browserConfig.test.exclude = browserConfig.test.exclude?.filter(
  (pattern) => pattern !== "**/*.browser.test.*",
);

export default browserConfig;

import { mergeConfig, defineConfig } from "vitest/config";
import { playwright } from "@vitest/browser-playwright";
import baseConfig from "./vite.config";

const chromiumExecutablePath = process.env.KENT_PLAYWRIGHT_CHROMIUM_PATH;
const browserProvider =
  chromiumExecutablePath === undefined
    ? playwright()
    : playwright({ launchOptions: { executablePath: chromiumExecutablePath } });

const dragPointerTo = async ({ provider, sessionId }, sourceSelector, destination) => {
  const { page, frame } = provider.getCommandsContext(sessionId);
  const testFrame = await frame();
  const source = testFrame.locator(sourceSelector);
  const box = await source.boundingBox();
  if (box === null) {
    throw new Error(`Cannot locate pointer-drag source: ${sourceSelector}`);
  }
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x, y + 7);
  await page.mouse.move(destination.x, destination.y);
  await page.mouse.up();
};

const dragToAdjacent = async ({ provider, sessionId }, sourceSelector, targetSelector) => {
  const { frame } = provider.getCommandsContext(sessionId);
  const target = (await frame()).locator(targetSelector);
  const targetBox = await target.boundingBox();
  if (targetBox === null) {
    throw new Error(`Cannot locate pointer-drag destination: ${targetSelector}`);
  }
  await dragPointerTo({ provider, sessionId }, sourceSelector, {
    x: targetBox.x + targetBox.width / 2,
    y: targetBox.y + targetBox.height / 2,
  });
};

const dragToGap = async ({ provider, sessionId }, sourceSelector, targetSelector) => {
  const { frame } = provider.getCommandsContext(sessionId);
  const target = (await frame()).locator(targetSelector);
  const targetBox = await target.boundingBox();
  if (targetBox === null) {
    throw new Error(`Cannot locate pointer-drag destination: ${targetSelector}`);
  }
  await dragPointerTo({ provider, sessionId }, sourceSelector, {
    x: targetBox.x + targetBox.width / 2,
    y: targetBox.y - 4,
  });
};

const activatePointerInSource = async ({ provider, sessionId }, selector) => {
  const { page, frame } = provider.getCommandsContext(sessionId);
  const source = (await frame()).locator(selector);
  const box = await source.boundingBox();
  if (box === null) {
    throw new Error(`Cannot locate pointer-drag source: ${selector}`);
  }
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x, y + 7);
  await page.mouse.up();
};

const browserConfig = mergeConfig(
  baseConfig,
  defineConfig({
    test: {
      include: ["packages/ui-kit/src/**/*.browser.test.tsx"],
      browser: {
        enabled: true,
        commands: { activatePointerInSource, dragToAdjacent, dragToGap },
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

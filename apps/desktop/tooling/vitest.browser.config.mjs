import { env } from "node:process";
import { mergeConfig, defineConfig } from "vitest/config";
import { playwright } from "@vitest/browser-playwright";
import baseConfig from "./vite.config";

const pointerDrag = async ({ frame, page }, input) => {
  const testFrame = await frame();
  const sourceBox = await resolveBox(testFrame, input.sourceSelector, "source");
  const destinationBox =
    input.destination.kind === "source"
      ? sourceBox
      : await resolveBox(testFrame, input.destination.selector, "destination");
  const destination = projectPointerDestination(destinationBox, input.destination);
  const sourceX = sourceBox.x + sourceBox.width / 2;
  const sourceY = sourceBox.y + sourceBox.height / 2;
  await page.mouse.move(sourceX, sourceY);
  await page.mouse.down();
  await page.mouse.move(sourceX, sourceY + 7);
  await page.mouse.move(destination.x, destination.y);
  await page.mouse.up();
};

async function resolveBox(frame, selector, role) {
  const box = await frame.locator(selector).boundingBox();
  if (box === null) {
    throw new Error(`Cannot locate pointer-drag ${role}: ${selector}`);
  }
  return box;
}

function projectPointerDestination(box, destination) {
  if (destination.kind === "source") {
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  }
  return {
    x: box.x + box.width / 2,
    y: destination.placement === "center" ? box.y + box.height / 2 : box.y - 4,
  };
}

const chromiumExecutablePath = env.KENT_PLAYWRIGHT_CHROMIUM_PATH;
const browserProvider =
  chromiumExecutablePath === undefined
    ? playwright({ launchOptions: { channel: "chromium" } })
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

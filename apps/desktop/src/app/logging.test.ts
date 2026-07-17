import { createBrowserNativeBridge } from "@app/native-bridge";

import type { AppLogger } from "@/app-facade";
import { createGuiLogger } from "./logging";

describe("GUI logger", () => {
  it("implements the feature-safe logger port while retaining shell-owned entry inspection", async () => {
    const concreteLogger = createGuiLogger(createBrowserNativeBridge());
    const logger: AppLogger = concreteLogger;

    await logger.append("info", "ready", { token: "secret" });

    expect(concreteLogger.entries()).toContainEqual(
      expect.objectContaining({
        level: "info",
        message: "ready",
        context: { token: "[redacted]" },
      }),
    );
  });
});

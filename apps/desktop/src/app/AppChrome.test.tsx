import { createBrowserNativeBridge, type NativePlatform } from "@app/native-bridge";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";

import { App } from "../App";
import { createTestServices, startupRoutes } from "../testSupport/appServices";

describe("AppChrome debug theme toggle", () => {
  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  it("hides the in-memory theme toggle outside debug desktop builds", async () => {
    render(<App services={createTestServices(startupRoutes)} />);

    expect(await screen.findByTestId("home-route-root")).toBeInTheDocument();
    expect(screen.queryByLabelText("Toggle theme")).not.toBeInTheDocument();
  });

  it("toggles the in-memory theme override in debug desktop builds", async () => {
    render(
      <App
        services={createTestServices(startupRoutes, createBrowserNativeBridge(), {
          debugThemeOverrideEnabled: true,
        })}
      />,
    );

    const toggle = await screen.findByLabelText("Toggle theme");
    expect(toggle).toHaveAttribute("data-testid", "app-chrome-debug-theme-toggle");

    fireEvent.click(toggle);
    expect(document.documentElement).toHaveAttribute("data-theme", "light");

    fireEvent.click(toggle);
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });

  it("renders exactly one platform-specific top treatment", async () => {
    const cases: readonly Readonly<{ platform: NativePlatform; effect: "blur" | "fade" }>[] = [
      { platform: "windows", effect: "blur" },
      { platform: "macos", effect: "fade" },
      { platform: "linux", effect: "fade" },
      { platform: "browser", effect: "fade" },
      { platform: "unknown", effect: "fade" },
    ];

    for (const testCase of cases) {
      const view = render(
        <App services={createTestServices(startupRoutes, createBrowserNativeBridge({ platform: testCase.platform }))} />,
      );
      await screen.findByTestId("home-route-root");
      const treatments = screen.getAllByTestId("app-chrome-top-treatment");
      expect(treatments).toHaveLength(1);
      expect(treatments[0]).toHaveAttribute("data-effect", testCase.effect);
      view.unmount();
    }
  });
});

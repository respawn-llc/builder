import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { createBrowserNativeBridge } from "@app/native-bridge";
import { appI18n } from "@/i18n";
import { flushQueuedWork } from "@/test-support/scheduling";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { AppRoot } from "./AppRoot";

describe("application chrome Search", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("opens the shared host with global scope from Home", async () => {
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.search",
        result: { mode: "literal", groups: [], next_offset: null },
      },
    ]);

    render(<AppRoot services={services} />);
    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByTestId("app-chrome-search"));
    fireEvent.change(screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") }), {
      target: { value: "search" },
    });

    await waitFor(() => {
      expect(services.transport.dedicatedCalls.at(-1)?.params).toMatchObject({ query: "search" });
    });
    expect(services.transport.dedicatedCalls.at(-1)?.params).not.toHaveProperty("project_ids");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
  });

  it.each([
    { label: "macOS", platform: "macos" as const },
    { label: "other platforms", platform: "linux" as const },
  ])("places Search at the outer edge on $label", async ({ platform }) => {
    const services = createTestServices(startupRoutes, undefined, { platform });
    render(<AppRoot services={services} />);
    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
    });
    await flushQueuedWork();

    const navigation = screen.getByTestId("app-chrome-navigation");
    const controls = within(navigation).getAllByTestId(/app-chrome-(search|home)/);
    expect(controls[0]).toHaveAttribute(
      "data-testid",
      platform === "macos" ? "app-chrome-home" : "app-chrome-search",
    );
    expect(controls.at(-1)).toHaveAttribute(
      "data-testid",
      platform === "macos" ? "app-chrome-search" : "app-chrome-home",
    );
    expect(within(navigation).getByTestId("app-chrome-search")).toBeInTheDocument();
  });

  it("uses macOS navigation order for a Mac-hosted browser client", async () => {
    const originalPlatform = window.navigator.platform;
    Object.defineProperty(window.navigator, "platform", {
      configurable: true,
      value: "MacIntel",
    });
    try {
      const services = createTestServices(startupRoutes);
      render(<AppRoot services={services} />);
      await waitFor(() => {
        expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
      });

      const controls = within(screen.getByTestId("app-chrome-navigation")).getAllByTestId(
        /app-chrome-(search|home)/,
      );
      expect(controls[0]).toHaveAttribute("data-testid", "app-chrome-home");
      expect(controls.at(-1)).toHaveAttribute("data-testid", "app-chrome-search");
    } finally {
      Object.defineProperty(window.navigator, "platform", {
        configurable: true,
        value: originalPlatform,
      });
    }
  });

  it("keeps Search adjacent to visible history when an update is available", async () => {
    const browserBridge = createBrowserNativeBridge({ platform: "linux" });
    const nativeBridge = {
      ...browserBridge,
      capabilities: { ...browserBridge.capabilities, updater: true },
      updates: {
        ...browserBridge.updates,
        supported: async () => true,
        check: async () => ({
          available: true as const,
          currentVersion: "1.0.0",
          notes: null,
          publishedAt: null,
          version: "1.1.0",
        }),
      },
    };
    const services = createTestServices(
      [...startupRoutes, { method: "workflow.list", result: { next_offset: null, workflows: [] } }],
      nativeBridge,
    );
    window.history.replaceState(null, "", "/workflows");
    render(<AppRoot services={services} />);
    await waitFor(() => {
      expect(screen.getByTestId("app-update-chip")).toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId("app-chrome-home"));
    const navigation = screen.getByTestId("app-chrome-navigation");
    await waitFor(() => {
      expect(within(navigation).getByTestId("app-chrome-history-buttons")).toBeInTheDocument();
    });

    const controls = within(navigation).getAllByTestId(
      /app-chrome-search|app-chrome-history-buttons|app-update-chip/,
    );
    expect(controls[0]).toHaveAttribute("data-testid", "app-chrome-search");
    expect(controls[1]).toHaveAttribute("data-testid", "app-chrome-history-buttons");
    expect(controls[2]).toHaveAttribute("data-testid", "app-update-chip");
  });
});

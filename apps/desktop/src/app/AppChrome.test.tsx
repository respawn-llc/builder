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
    const search = within(navigation).getByTestId("app-chrome-search");
    const home = within(navigation).getByTestId("app-chrome-home");
    expect(appearsBefore(platform === "macos" ? home : search, platform === "macos" ? search : home)).toBe(
      true,
    );
    expect(search).toBeInTheDocument();
  });

  it("uses macOS navigation order for a Mac-hosted browser client", async () => {
    const browserBridge = createBrowserNativeBridge();
    const services = createTestServices(
      startupRoutes,
      {
        ...browserBridge,
        capabilities: { ...browserBridge.capabilities, hostPlatform: "macos" },
      },
    );
    render(<AppRoot services={services} />);
    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
    });

    const navigation = screen.getByTestId("app-chrome-navigation");
    const search = within(navigation).getByTestId("app-chrome-search");
    const home = within(navigation).getByTestId("app-chrome-home");
    expect(appearsBefore(home, search)).toBe(true);
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

    const search = within(navigation).getByTestId("app-chrome-search");
    const history = within(navigation).getByTestId("app-chrome-history-buttons");
    const update = within(navigation).getByTestId("app-update-chip");
    const historyButtons = within(history).getAllByRole("button");
    const updateButtons = within(update).getAllByRole("button");
    const controls = within(navigation).getAllByRole("button");
    expect(controls[0]).toBe(search);
    expect(controls[1]).toBe(historyButtons[0]);
    expect(controls[2]).toBe(historyButtons[1]);
    expect(controls[3]).toBe(updateButtons[0]);
    expect(controls[4]).toBe(updateButtons[1]);
  });
});

function appearsBefore(first: Element, second: Element): boolean {
  return (first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
}

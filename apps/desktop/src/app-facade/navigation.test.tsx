import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from "@tanstack/react-router";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { useAppNavigation, type AppNavigationResult } from "./navigation";

describe("useAppNavigation", () => {
  it("returns a typed failed outcome when close-project navigation rejects", async () => {
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/"] }),
      routeTree: createRootRoute(),
    });
    const navigationError = new Error("navigation failed");
    vi.spyOn(router, "navigate").mockRejectedValue(navigationError);
    const services = createTestServices([]);
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
      <RouterContextProvider router={router}>
        <TestAppProviders services={services}>{children}</TestAppProviders>
      </RouterContextProvider>
    );
    const { result } = renderHook(() => useAppNavigation(), { wrapper });
    let outcome: AppNavigationResult | undefined;

    await act(async () => {
      outcome = await result.current.closeProjectTask("project-1", "workflow-1");
    });

    if (outcome?.status !== "failed") {
      throw new Error("Expected a typed failed navigation outcome.");
    }
    expect(outcome.error).toBe(navigationError);
  });
});

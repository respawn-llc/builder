import { QueryClient } from "@tanstack/react-query";

import { completeProjectDeletion } from "./projectDeletionEvents";

describe("completeProjectDeletion", () => {
  function deferred(): Readonly<{
    promise: Promise<void>;
    resolve(): void;
  }> {
    let resolve!: () => void;
    const promise = new Promise<void>((nextResolve) => {
      resolve = nextResolve;
    });
    return { promise, resolve };
  }

  it("keeps unrelated sidebar survivors when the route stays in place", async () => {
    const closeSidebar = vi.fn();
    const navigateHome = vi.fn(async () => Promise.resolve());
    const invalidateSidebar = vi.fn(() => ({ kind: "discarded" as const }));

    await completeProjectDeletion({
      closeSidebar,
      invalidateSidebar,
      navigateHome,
      projectID: "project-1",
      pushDeletedToast: vi.fn(),
      queryClient: new QueryClient(),
      isProjectRouteCurrent: () => false,
    });

    expect(closeSidebar).not.toHaveBeenCalled();
    expect(navigateHome).not.toHaveBeenCalled();
  });

  it("closes when Project invalidation removes the final survivor", async () => {
    const closeSidebar = vi.fn();
    const invalidateSidebar = vi.fn(() => ({ kind: "closed" as const }));

    await completeProjectDeletion({
      closeSidebar,
      invalidateSidebar,
      navigateHome: vi.fn(async () => Promise.resolve()),
      projectID: "project-1",
      pushDeletedToast: vi.fn(),
      queryClient: new QueryClient(),
      isProjectRouteCurrent: () => false,
    });

    expect(closeSidebar).toHaveBeenCalledOnce();
  });

  it("closes and navigates home when the deleted Project owns the route", async () => {
    const closeSidebar = vi.fn();
    const navigateHome = vi.fn(async () => Promise.resolve());

    await completeProjectDeletion({
      closeSidebar,
      invalidateSidebar: vi.fn(() => ({ kind: "discarded" as const })),
      navigateHome,
      projectID: "project-1",
      pushDeletedToast: vi.fn(),
      queryClient: new QueryClient(),
      isProjectRouteCurrent: () => true,
    });

    expect(closeSidebar).toHaveBeenCalledOnce();
    expect(navigateHome).toHaveBeenCalledOnce();
  });

  it("rechecks the route after cleanup before deciding to navigate", async () => {
    const queryClient = new QueryClient();
    const cleanup = deferred();
    vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async () => {
      await cleanup.promise;
    });
    const closeSidebar = vi.fn();
    const navigateHome = vi.fn(async () => Promise.resolve());
    let routeIsCurrent = true;
    const completion = completeProjectDeletion({
      closeSidebar,
      invalidateSidebar: vi.fn(() => ({ kind: "discarded" as const })),
      isProjectRouteCurrent: () => routeIsCurrent,
      navigateHome,
      projectID: "project-1",
      pushDeletedToast: vi.fn(),
      queryClient,
    });

    routeIsCurrent = false;
    cleanup.resolve();
    await completion;

    expect(closeSidebar).not.toHaveBeenCalled();
    expect(navigateHome).not.toHaveBeenCalled();
  });

  it("navigates when the route becomes the deleted Project during cleanup", async () => {
    const queryClient = new QueryClient();
    const cleanup = deferred();
    vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async () => {
      await cleanup.promise;
    });
    const closeSidebar = vi.fn();
    const navigateHome = vi.fn(async () => Promise.resolve());
    let routeIsCurrent = false;
    const completion = completeProjectDeletion({
      closeSidebar,
      invalidateSidebar: vi.fn(() => ({ kind: "discarded" as const })),
      isProjectRouteCurrent: () => routeIsCurrent,
      navigateHome,
      projectID: "project-1",
      pushDeletedToast: vi.fn(),
      queryClient,
    });

    routeIsCurrent = true;
    cleanup.resolve();
    await completion;

    expect(closeSidebar).toHaveBeenCalledOnce();
    expect(navigateHome).toHaveBeenCalledOnce();
  });
});

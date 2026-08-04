import { QueryClient } from "@tanstack/react-query";

import { completeProjectDeletion } from "./projectDeletionEvents";

describe("completeProjectDeletion", () => {
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
      routeMatchesProject: false,
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
      routeMatchesProject: false,
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
      routeMatchesProject: true,
    });

    expect(closeSidebar).toHaveBeenCalledOnce();
    expect(navigateHome).toHaveBeenCalledOnce();
  });
});

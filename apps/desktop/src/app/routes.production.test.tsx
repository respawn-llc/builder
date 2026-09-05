import { render, screen } from "@testing-library/react";
import { RouterProvider } from "@tanstack/react-router";
import { expect, it, vi } from "vitest";

import { sessionChatRoutePath } from "@/app-facade";
import { createAppRouter } from "./routes";

vi.mock("@/shared/feature-flags", () => ({ desktopChatEnabled: false }));
vi.mock("./routeComponents", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  return {
    ChatRoute: () => <div data-testid="chat-route" />,
    HomeShellRoute: () => null,
    ProjectRoute: () => null,
    ProjectTasksRoute: () => null,
    RootRoute: () => <Outlet />,
    TaskRoute: () => null,
    WorkflowEditorShellRoute: () => null,
    WorkflowLibraryShellRoute: () => null,
  };
});

it("leaves the development-gated Session Chat URL without Chat content in production", async () => {
  const router = createAppRouter();

  await router.navigate({
    to: sessionChatRoutePath,
    params: { projectId: "project-1", sessionId: "session-1" },
  });
  render(<RouterProvider router={router} />);

  expect(screen.queryByTestId("chat-route")).not.toBeInTheDocument();
});

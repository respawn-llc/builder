import { render, screen } from "@testing-library/react";
import { RouterProvider } from "@tanstack/react-router";
import { afterEach, expect, it, vi } from "vitest";

import { createAppRouter } from "./routes";

vi.mock("./routeComponents", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  return {
    HomeShellRoute: () => null,
    ProjectRoute: () => null,
    ProjectTasksRoute: () => <div data-testid="standalone-project-tasks-route" />,
    RootRoute: () => <Outlet />,
    TaskRoute: () => null,
    WorkflowEditorShellRoute: () => null,
    WorkflowLibraryShellRoute: () => null,
  };
});

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

it("rejects malformed present workflow selectors while preserving omission", () => {
  const validate = createAppRouter().routesById["/projects/$projectId"].options.validateSearch;
  if (!(validate instanceof Function)) {
    throw new Error("project route search validation is unavailable");
  }
  expect(validate({})).toEqual({ taskId: "", workflowId: undefined });
  expect(validate({ workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0" })).toEqual({
    taskId: "",
    workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0",
  });
  expect(() => validate({ workflowId: "workflow-1" })).toThrow();
});

it("normalizes omitted Home project selection", () => {
  const validate = createAppRouter().routesById["/"].options.validateSearch;
  if (!(validate instanceof Function)) {
    throw new Error("Home route search validation is unavailable");
  }
  expect(validate({})).toEqual({});
  expect(validate({ projectId: "project-1" })).toEqual({ projectId: "project-1" });
});

it("opens the standalone Project Task List route from its public URL", async () => {
  window.history.replaceState(null, "", "/projects/project-1/tasks");
  render(<RouterProvider router={createAppRouter()} />);

  expect(await screen.findByTestId("standalone-project-tasks-route")).toBeInTheDocument();
});

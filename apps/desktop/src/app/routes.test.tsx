import { expect, it } from "vitest";
import { createAppRouter } from "./routes";

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

it("always exposes the standalone Project Task List route", () => {
  expect(createAppRouter().routesById["/projects/$projectId/tasks"]).toBeDefined();
});

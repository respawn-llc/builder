import { expect, it } from "vitest";
import { LegacyEmptyTaskSelectorError } from "./projectRouteErrors";
import { createAppRouter } from "./routes";

it("rejects malformed present workflow selectors while preserving omission", () => {
  const validate = createAppRouter().routesById["/projects/$projectId"].options.validateSearch;
  if (!(validate instanceof Function)) {
    throw new Error("project route search validation is unavailable");
  }
  expect(validate({})).toEqual({ taskId: undefined, workflowId: undefined });
  expect(validate({ workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0" })).toEqual({
    taskId: undefined,
    workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0",
  });
  expect(() => validate({ taskId: "" })).toThrow(LegacyEmptyTaskSelectorError);
  expect(() => validate({ workflowId: "workflow-1" })).toThrow();
});

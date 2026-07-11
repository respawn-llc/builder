import { createRoute, type AnyRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import type { WorkflowDeleteImpact } from "../api";
import { WorkflowDeleteWindowRoute } from "../features/workflow-editor/WorkflowDeleteButton";
import { InvalidNativeDialogRoute } from "./InvalidNativeDialogRoute";

export const workflowDeleteNativeDialogPath = "/native-dialog/workflow-delete";

const optionalSearchString = z
  .union([z.string(), z.number().transform((value) => value.toString())])
  .catch("");

const workflowDeleteSearchSchema = z.object({
  active_run_count: optionalSearchString,
  blocked_task_count: optionalSearchString,
  default_replacement_project_count: optionalSearchString,
  link_count: optionalSearchString,
  project_count: optionalSearchString,
  runnable_run_count: optionalSearchString,
  task_count: optionalSearchString,
  version: optionalSearchString,
  workflow_id: optionalSearchString,
});

export function createWorkflowDeleteWindowRoute(rootRoute: AnyRootRoute) {
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: workflowDeleteNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => workflowDeleteSearchSchema.parse(search),
    component() {
      const rawSearch: unknown = route.useSearch();
      const search = workflowDeleteSearchSchema.parse(rawSearch);
      const impact = workflowDeleteImpactFromSearch(search);
      if (impact === null) {
        return <InvalidNativeDialogRoute />;
      }
      return <WorkflowDeleteWindowRoute impact={impact} />;
    },
  });
  return route;
}

function workflowDeleteImpactFromSearch(
  search: z.infer<typeof workflowDeleteSearchSchema>,
): WorkflowDeleteImpact | null {
  const workflowID = search.workflow_id.trim();
  if (workflowID.length === 0) {
    return null;
  }
  const version = parseSearchCount(search.version);
  const projectCount = parseSearchCount(search.project_count);
  const linkCount = parseSearchCount(search.link_count);
  const taskCount = parseSearchCount(search.task_count);
  const defaultReplacementProjectCount = parseSearchCount(search.default_replacement_project_count);
  const activeRunCount = parseSearchCount(search.active_run_count);
  const runnableRunCount = parseSearchCount(search.runnable_run_count);
  const blockedTaskCount = parseSearchCount(search.blocked_task_count);
  if (
    version === null ||
    projectCount === null ||
    linkCount === null ||
    taskCount === null ||
    defaultReplacementProjectCount === null ||
    activeRunCount === null ||
    runnableRunCount === null ||
    blockedTaskCount === null
  ) {
    return null;
  }
  return {
    activeRunCount,
    blockedTaskCount,
    defaultReplacementProjectCount,
    linkCount,
    projectCount,
    runnableRunCount,
    taskCount,
    version,
    workflowID,
  };
}

function parseSearchCount(value: string): number | null {
  const count = Number(value);
  if (!Number.isSafeInteger(count) || count < 0) {
    return null;
  }
  return count;
}

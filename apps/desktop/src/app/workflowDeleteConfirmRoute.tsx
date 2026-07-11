import { createRoute, type AnyRootRoute } from "@tanstack/react-router";
import { z } from "zod";

import { WorkflowDeleteConfirmationWindowRoute } from "../features/workflow-editor/WorkflowDeleteConfirmationWindow";
import { workflowDeleteConfirmationWindowTargetFromSearch } from "../features/workflow-editor/workflowDeleteConfirmationModel";

export const workflowDeleteConfirmNativeDialogPath = "/native-dialog/workflow-delete-confirm";

const optionalSearchString = z.string().catch("");

const workflowDeleteConfirmSearchSchema = z.object({
  edgeCount: optionalSearchString,
  nodeCount: optionalSearchString,
  operation: optionalSearchString,
  promptCount: optionalSearchString,
  requestID: optionalSearchString,
  transitionGroupCount: optionalSearchString,
});

export function createWorkflowDeleteConfirmWindowRoute(rootRoute: AnyRootRoute) {
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: workflowDeleteConfirmNativeDialogPath,
    validateSearch: (search: Record<string, unknown>) => workflowDeleteConfirmSearchSchema.parse(search),
    component() {
      const rawSearch: unknown = route.useSearch();
      const search = workflowDeleteConfirmSearchSchema.parse(rawSearch);
      return <WorkflowDeleteConfirmationWindowRoute {...workflowDeleteConfirmationWindowTargetFromSearch(search)} />;
    },
  });
  return route;
}

import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { errorMessage } from "@/api";
import { type WorkflowProjectEvent } from "@/api";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { useStatusController } from "@/app-facade";

export type WorkflowEditorData = ReturnType<typeof useWorkflowEditorData>;

export function useWorkflowEditorData(rawProjectID: string, workflowID: string) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const connection = useConnectionSnapshot();
  const queryClient = useQueryClient();
  const { dismiss, push } = useStatusController();
  // A blank or whitespace-only project id is not a real project context (e.g. the
  // editor opened from the global workflow library). Normalizing it here keeps every
  // project-scoped query, subscription, and the link gate off, so the editor never
  // issues a project-scoped RPC with an empty `project_id`.
  const projectID = rawProjectID.trim();
  const linksQuery = useQuery({
    queryKey: queryKeys.projectWorkflowLinks(projectID),
    queryFn: async () => api.listProjectWorkflowLinks(projectID),
    enabled: projectID.length > 0,
  });
  const activeLink = linksQuery.data?.find(
    (link) => link.projectID === projectID && link.workflowID === workflowID,
  );
  const projectContext = projectID.length > 0;
  const linked = !projectContext || activeLink !== undefined;
  const workflowQuery = useQuery({
    queryKey: queryKeys.workflowDefinition(workflowID),
    queryFn: async () => api.getWorkflow(workflowID),
    enabled: linked,
  });
  const validationQuery = useQuery({
    queryKey: queryKeys.workflowValidation(workflowID, "execution"),
    queryFn: async () => api.validateWorkflow(workflowID, "execution"),
    enabled: linked,
  });

  useEffect(() => {
    if (workflowID.length === 0 || connection.phase !== "connected") {
      return;
    }
    async function refresh(notify: boolean): Promise<void> {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.projectWorkflowLinks(projectID) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.boardWorkflowRoot(projectID, workflowID) }),
        queryClient.invalidateQueries({
          queryKey: queryKeys.boardNodeCardsWorkflowRoot(projectID, workflowID),
        }),
        queryClient.invalidateQueries({ queryKey: queryKeys.workflowDefinition(workflowID) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.workflowValidation(workflowID, "execution") }),
      ]);
      if (notify) {
        push({
          id: "workflow-editor-updated",
          tone: "neutral",
          title: t("workflowEditor.updated"),
        });
      }
    }
    const subscriptions = [
      api.subscribeWorkflow(workflowID, {
        onOpen() {
          dismiss("workflow-editor-workflow-subscription-failed");
          void refresh(false);
        },
        onEvent(event) {
          if (shouldRefreshWorkflowDefinition(event, workflowID)) {
            void refresh(shouldNotifyWorkflowEditorRefresh(event, projectID, workflowID));
          }
        },
        onComplete() {
          return;
        },
        onError(error) {
          push({
            body: errorMessage(error),
            durationMs: Infinity,
            id: "workflow-editor-workflow-subscription-failed",
            tone: "danger",
            title: t("workflowEditor.subscriptionFailed"),
          });
        },
      }),
    ];
    if (projectID.length > 0) {
      subscriptions.push(
        api.subscribeProject(projectID, {
          onOpen() {
            dismiss("workflow-editor-project-subscription-failed");
            void refresh(false);
          },
          onEvent(event) {
            if (shouldRefreshWorkflowLink(event, projectID, workflowID)) {
              void refresh(shouldNotifyWorkflowEditorRefresh(event, projectID, workflowID));
            }
          },
          onComplete() {
            return;
          },
          onError(error) {
            push({
              body: errorMessage(error),
              durationMs: Infinity,
              id: "workflow-editor-project-subscription-failed",
              tone: "danger",
              title: t("workflowEditor.subscriptionFailed"),
            });
          },
        }),
      );
    }
    return () => {
      for (const subscription of subscriptions) {
        subscription.close();
      }
    };
  }, [api, connection.generation, connection.phase, dismiss, projectID, push, queryClient, t, workflowID]);

  return {
    activeLink,
    linked,
    linksQuery,
    projectContext,
    validationQuery,
    workflowQuery,
  };
}

export function shouldRefreshWorkflowEditor(
  event: WorkflowProjectEvent,
  projectID: string,
  workflowID: string,
): boolean {
  return (
    shouldRefreshWorkflowDefinition(event, workflowID) ||
    shouldRefreshWorkflowLink(event, projectID, workflowID)
  );
}

export function shouldNotifyWorkflowEditorRefresh(
  event: WorkflowProjectEvent,
  projectID: string,
  workflowID: string,
): boolean {
  if (
    event.resource === "workflow" &&
    event.workflowID === workflowID &&
    workflowDefinitionActions.has(event.action)
  ) {
    return event.action !== "deleted";
  }
  if (
    event.resource === "workflow_link" &&
    event.projectID === projectID &&
    workflowLinkActions.has(event.action) &&
    event.workflowID === workflowID
  ) {
    return event.action !== "unlinked";
  }
  return false;
}

export function shouldRefreshWorkflowDefinition(event: WorkflowProjectEvent, workflowID: string): boolean {
  return (
    event.resource === "workflow" &&
    event.workflowID === workflowID &&
    workflowDefinitionActions.has(event.action)
  );
}

export function shouldRefreshWorkflowLink(
  event: WorkflowProjectEvent,
  projectID: string,
  workflowID: string,
): boolean {
  return (
    event.resource === "workflow_link" &&
    event.projectID === projectID &&
    workflowLinkActions.has(event.action) &&
    event.workflowID === workflowID
  );
}

const workflowDefinitionActions = new Set([
  "updated",
  "deleted",
  "graph_saved",
]);

const workflowLinkActions = new Set(["linked", "default_changed", "unlinked"]);

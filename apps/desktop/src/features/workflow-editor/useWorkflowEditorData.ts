import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { queryKeys } from "../../app/queryKeys";
import { useAppServices } from "../../app/useAppServices";
import { useConnectionSnapshot } from "../../app/useConnectionSnapshot";
import { useStatusController } from "../../app/useStatusController";
import { workflowProjectEvent } from "../../app/workflowProjectEvents";

export type WorkflowEditorData = ReturnType<typeof useWorkflowEditorData>;

export function useWorkflowEditorData(rawProjectID: string, workflowID: string) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const connection = useConnectionSnapshot();
  const queryClient = useQueryClient();
  const { push } = useStatusController();
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
        queryClient.invalidateQueries({ queryKey: queryKeys.board(projectID, workflowID) }),
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
          void refresh(false);
        },
        onEvent(_method, params) {
          if (shouldRefreshWorkflowDefinition(params, workflowID)) {
            void refresh(shouldNotifyWorkflowEditorRefresh(params, projectID, workflowID));
          }
        },
        onComplete() {
          return;
        },
        onError() {
          void refresh(false);
        },
      }),
    ];
    if (projectID.length > 0) {
      subscriptions.push(
        api.subscribeProject(projectID, {
          onOpen() {
            void refresh(false);
          },
          onEvent(_method, params) {
            if (shouldRefreshWorkflowLink(params, projectID, workflowID)) {
              void refresh(shouldNotifyWorkflowEditorRefresh(params, projectID, workflowID));
            }
          },
          onComplete() {
            return;
          },
          onError() {
            void refresh(false);
          },
        }),
      );
    }
    return () => {
      for (const subscription of subscriptions) {
        subscription.close();
      }
    };
  }, [api, connection.generation, connection.phase, projectID, push, queryClient, t, workflowID]);

  return {
    activeLink,
    linked,
    linksQuery,
    projectContext,
    validationQuery,
    workflowQuery,
  };
}

export function shouldRefreshWorkflowEditor(params: unknown, projectID: string, workflowID: string): boolean {
  return (
    shouldRefreshWorkflowDefinition(params, workflowID) ||
    shouldRefreshWorkflowLink(params, projectID, workflowID)
  );
}

export function shouldNotifyWorkflowEditorRefresh(
  params: unknown,
  projectID: string,
  workflowID: string,
): boolean {
  const event = workflowProjectEvent(params);
  if (event === null) {
    return false;
  }
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
    (event.workflowID === workflowID || event.changedIDs.includes(workflowID))
  ) {
    return event.action !== "unlinked";
  }
  return false;
}

export function shouldRefreshWorkflowDefinition(params: unknown, workflowID: string): boolean {
  const event = workflowProjectEvent(params);
  if (event === null) {
    return false;
  }
  return (
    event.resource === "workflow" &&
    event.workflowID === workflowID &&
    workflowDefinitionActions.has(event.action)
  );
}

export function shouldRefreshWorkflowLink(params: unknown, projectID: string, workflowID: string): boolean {
  const event = workflowProjectEvent(params);
  if (event === null) {
    return false;
  }
  return (
    event.resource === "workflow_link" &&
    event.projectID === projectID &&
    workflowLinkActions.has(event.action) &&
    (event.workflowID === workflowID || event.changedIDs.includes(workflowID))
  );
}

const workflowDefinitionActions = new Set([
  "updated",
  "node_added",
  "node_updated",
  "node_group_added",
  "node_group_updated",
  "node_group_deleted",
  "transition_group_added",
  "transition_group_updated",
  "edge_added",
  "edge_updated",
  "deleted",
  "graph_saved",
]);

const workflowLinkActions = new Set(["linked", "default_changed", "unlinked"]);

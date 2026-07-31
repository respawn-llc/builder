import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { ProjectWorkflowLink, WorkflowRecord } from "@/api";
import { errorMessage } from "@/api";
import { queryKeys } from "@/app-facade";
import { useSidebar } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { WorkflowActionsContextMenu, useWorkflowPages } from "@/shared/workflow-library";
import {
  Button,
  EmptyState,
  ErrorState,
  ItemContent,
  ItemTitle,
  LoadingState,
  VirtualizedInfiniteList,
} from "@/ui";
import { WorkflowCreateForm } from "./WorkflowCreateForm";

export function LinkWorkflowSidebar({
  creating,
  onCreated,
  onLinked,
  projectID,
  selectedWorkflowID,
}: Readonly<{
  creating: boolean;
  onCreated: (workflowID: string) => void;
  onLinked: (workflowID: string) => void;
  projectID: string;
  selectedWorkflowID?: string | undefined;
}>) {
  const { t } = useTranslation();
  if (creating) {
    return (
      <WorkflowCreateForm
        onCreated={(result) => {
          onCreated(result.workflow.id);
        }}
        projectID={projectID}
      />
    );
  }
  return (
    <LinkWorkflowPicker
      onLinked={onLinked}
      projectID={projectID}
      selectedWorkflowID={selectedWorkflowID}
      title={t("workflowLibrary.linkWorkflow")}
    />
  );
}

function LinkWorkflowPicker({
  onLinked,
  projectID,
  selectedWorkflowID,
  title,
}: Readonly<{
  onLinked: (workflowID: string) => void;
  projectID: string;
  selectedWorkflowID?: string | undefined;
  title: string;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const normalizedProjectID = projectID.trim();
  const workflowsQuery = useWorkflowPages();
  const linksQuery = useQuery({
    queryKey: queryKeys.projectWorkflowLinks(normalizedProjectID),
    queryFn: async () => api.listProjectWorkflowLinks(normalizedProjectID),
    enabled: normalizedProjectID.length > 0,
  });
  const workflows = useMemo(
    () => workflowsQuery.data?.pages.flatMap((page) => page.workflows) ?? [],
    [workflowsQuery.data],
  );
  const linkedByWorkflowID = useMemo(
    () => projectLinksByWorkflowID(linksQuery.data ?? []),
    [linksQuery.data],
  );
  const linkMutation = useMutation({
    mutationFn: async (workflowID: string) => {
      if (normalizedProjectID.length === 0) {
        throw new Error("Cannot link a workflow without a project.");
      }
      return api.linkWorkflowToProject({ projectID: normalizedProjectID, workflowID });
    },
    onSuccess: async (link) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.allProjectWorkflowLinks });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflows });
      onLinked(link.workflowID);
    },
  });

  if (workflowsQuery.isPending || linksQuery.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} title={title} />;
  }
  if (workflowsQuery.isError) {
    return (
      <ErrorState
        body={errorMessage(workflowsQuery.error)}
        fullPage={false}
        onRetry={() => void workflowsQuery.refetch()}
        retryLabel={t("app.retry")}
        title={t("workflowLibrary.loadFailed")}
      />
    );
  }
  if (linksQuery.isError) {
    return (
      <ErrorState
        body={errorMessage(linksQuery.error)}
        fullPage={false}
        onRetry={() => void linksQuery.refetch()}
        retryLabel={t("app.retry")}
        title={t("workflowEditor.linkLoadFailed")}
      />
    );
  }

  const list = (
    <VirtualizedInfiniteList
      className="h-full min-h-0 overflow-auto"
      empty={
        <EmptyState
          body={t("workflowLibrary.emptyBody")}
          fullPage={false}
          title={t("workflowLibrary.emptyTitle")}
        />
      }
      estimateSize={() => 92}
      getItemKey={(workflow) => workflow.id}
      hasNextPage={workflowsQuery.hasNextPage}
      isFetchingNextPage={workflowsQuery.isFetchingNextPage}
      items={workflows}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void workflowsQuery.fetchNextPage()}
      renderItem={(workflow) => (
        <WorkflowLinkRow
          linked={linkedByWorkflowID.get(workflow.id)}
          linking={linkMutation.isPending}
          onLink={() => void linkMutation.mutateAsync(workflow.id)}
          projectID={projectID}
          selected={workflow.id === selectedWorkflowID}
          workflow={workflow}
        />
      )}
    />
  );
  if (linkMutation.isError) {
    return (
      <div className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-3)]">
        <ErrorState
          body={errorMessage(linkMutation.error)}
          fullPage={false}
          reveal={false}
          title={t("workflowLibrary.linkFailed")}
        />
        {list}
      </div>
    );
  }
  return <div className="h-full min-h-0">{list}</div>;
}

function WorkflowLinkRow({
  linked,
  linking,
  onLink,
  projectID,
  selected,
  workflow,
}: Readonly<{
  linked: ProjectWorkflowLink | undefined;
  linking: boolean;
  onLink: () => void;
  projectID: string;
  selected: boolean;
  workflow: WorkflowRecord;
}>) {
  const { t } = useTranslation();
  const { openSidebar } = useSidebar();
  const row = (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-[var(--space-2)] rounded-md border border-[var(--color-outline)] bg-[var(--color-island-1)] px-[var(--space-3)] py-[var(--space-3)]">
      <ItemContent>
        <ItemTitle>{workflow.name}</ItemTitle>
        <span className="text-sm text-[var(--color-muted)]">
          {linked?.isDefault === true
            ? t("workflowLibrary.defaultLinked")
            : linked !== undefined
              ? t("workflowLibrary.linked")
              : selected
                ? t("workflowLibrary.selected")
                : t("workflowLibrary.reusableDefinition")}
        </span>
      </ItemContent>
      <Button disabled={linking} onClick={onLink} variant={linked === undefined ? "primary" : "secondary"}>
        {linked === undefined ? t("workflowLibrary.link") : t("workflowLibrary.select")}
      </Button>
    </div>
  );
  return (
    <WorkflowActionsContextMenu
      onEdit={() => {
        void openSidebar({ kind: "workflowEditor", mode: "overlay", projectID, workflowID: workflow.id });
      }}
      workflowID={workflow.id}
    >
      {row}
    </WorkflowActionsContextMenu>
  );
}

function projectLinksByWorkflowID(
  links: readonly ProjectWorkflowLink[],
): ReadonlyMap<string, ProjectWorkflowLink> {
  return new Map(links.map((link) => [link.workflowID, link]));
}

import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import {
  projectEditInfiniteQueryOptions,
  useAppServices,
  useProjectWorkspaceTab,
} from "@/app-facade";
import { BoardRoute } from "@/features/board";
import { ProjectSessionsBrowser } from "@/features/sessions";
import { ErrorState, IslandTabs, LoadingState } from "@/ui";

export function ProjectWorkspaceRoute({
  projectId,
  selectedTaskId,
  workflowId,
}: Readonly<{
  projectId: string;
  selectedTaskId: string;
  workflowId: string | undefined;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const { selectedTab, selectTab } = useProjectWorkspaceTab();
  const query = useInfiniteQuery(projectEditInfiniteQueryOptions(api, projectId));
  const project = query.data?.pages[0];

  if (query.isPending && project === undefined) {
    return (
      <LoadingState
        appearanceDelayMs={0}
        chromePadding
        title={t("projectWorkspace.loading")}
      />
    );
  }
  if (query.isError || project === undefined) {
    return (
      <ErrorState
        body={query.isError ? errorMessage(query.error) : t("projectWorkspace.missing")}
        chromePadding
        onRetry={() => void query.refetch()}
        retryLabel={t("app.retry")}
        title={t("states.error")}
      />
    );
  }

  return (
    <section className="flex h-full min-h-0 flex-col" data-testid="project-workspace">
      <header className="sticky top-0 z-10 grid gap-[var(--space-3)] px-[var(--space-4)] pt-[var(--space-4)]">
        <div className="min-w-0">
          <span className="font-mono text-xs text-[var(--color-muted)]">
            {project.projectKey}
          </span>
          <h1 className="m-0 truncate text-xl">{project.displayName}</h1>
        </div>
        <IslandTabs
          ariaLabel={t("projectWorkspace.tabs")}
          className="grid-cols-2"
          items={[
            { label: t("projectWorkspace.workflows"), value: "workflows" },
            { label: t("projectWorkspace.sessions"), value: "sessions" },
          ]}
          onValueChange={selectTab}
          value={selectedTab}
        />
      </header>
      <div className="min-h-0 flex-1">
        {selectedTab === "workflows" ? (
          <BoardRoute
            projectId={projectId}
            selectedTaskId={selectedTaskId}
            workflowId={workflowId}
          />
        ) : (
          <ProjectSessionsBrowser projectID={projectId} />
        )}
      </div>
    </section>
  );
}

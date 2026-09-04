import { useEffect, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { SessionCatalogSummary, SessionCategory } from "@/api";
import { errorMessage, isProjectMissingError } from "@/api";
import {
  clearLastProjectRoute,
  formatRelativeTime,
  mainSessionCatalogInfiniteQueryOptions,
  type ProjectContentTab,
  queryKeys,
  subagentSessionCatalogInfiniteQueryOptions,
  useSessionChatCatalogReturn,
  useAppNavigation,
  useAppServices,
  readLastProjectRoute,
  writeLastProjectContentTab,
  type SidebarMode,
} from "@/app-facade";
import {
  directionalBoundary,
  EmptyState,
  homeListCardListMaxWidthClassName,
  HomeListCard,
  InfiniteListBoundary,
  IslandTabs,
  VirtualizedInfiniteList,
} from "@/ui";
import { OverlappingCrossfade } from "./OverlappingCrossfade";
import { ProjectTasksSurface } from "./ProjectTasksSurface";
import { createProjectTasksViewMemory } from "./projectTasksViewMemory";

export function HomeProjectContent({
  projectID,
  sessionsVisible,
  sidebarMode,
}: Readonly<{
  projectID: string;
  sessionsVisible: boolean;
  sidebarMode: SidebarMode;
}>) {
  const { api } = useAppServices();
  const navigation = useAppNavigation();
  const catalogReturn = useSessionChatCatalogReturn(projectID);
  const [taskListViewMemory] = useState(createProjectTasksViewMemory);
  useEffect(() => {
    catalogReturn?.consume();
  }, [catalogReturn]);
  const projectQuery = useQuery({
    queryKey: queryKeys.projectEdit(projectID),
    queryFn: async () => api.getProjectEdit(projectID),
  });
  useEffect(() => {
    if (!isProjectMissingError(projectQuery.error)) return;
    clearLastProjectRoute(projectID);
    void navigation.selectHomeProject(null);
  }, [navigation, projectID, projectQuery.error]);
  return sessionsVisible ? (
    <ProjectContentTabs
      catalogReturn={catalogReturn?.category ?? null}
      projectID={projectID}
      sidebarMode={sidebarMode}
      taskListViewMemory={taskListViewMemory}
    />
  ) : (
    <ProjectTasksSurface projectID={projectID} sidebarMode={sidebarMode} viewMemory={taskListViewMemory} />
  );
}

function ProjectContentTabs({
  catalogReturn,
  projectID,
  sidebarMode,
  taskListViewMemory,
}: Readonly<{
  catalogReturn: SessionCategory | null;
  projectID: string;
  sidebarMode: SidebarMode;
  taskListViewMemory: ReturnType<typeof createProjectTasksViewMemory>;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const [tab, setTab] = useState<ProjectContentTab>(() => {
    if (catalogReturn === "main") return "sessions";
    if (catalogReturn === "subagent") return "subagents";
    const lastProjectRoute = readLastProjectRoute();
    if (lastProjectRoute?.kind === "home_project" && lastProjectRoute.projectId === projectID) {
      return lastProjectRoute.contentTab ?? "tasks";
    }
    return "tasks";
  });
  useEffect(() => {
    writeLastProjectContentTab(projectID, tab);
  }, [projectID, tab]);
  const mainSessionsQuery = useInfiniteQuery({
    ...mainSessionCatalogInfiniteQueryOptions(api, projectID),
    enabled: tab === "sessions",
  });
  const subagentSessionsQuery = useInfiniteQuery({
    ...subagentSessionCatalogInfiniteQueryOptions(api, projectID),
    enabled: tab === "subagents",
  });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="px-[var(--space-4)] pt-[var(--space-4)]">
        <IslandTabs
          ariaLabel={t("home.prototype.projectContent")}
          className="grid-cols-3"
          items={[
            { label: t("home.prototype.tasks"), value: "tasks" },
            { label: t("home.prototype.sessions"), value: "sessions" },
            { label: t("home.prototype.subagents"), value: "subagents" },
          ]}
          onValueChange={(value) => {
            setTab(value);
          }}
          value={tab}
        />
      </div>
      <div className="min-h-0 flex-1">
        <OverlappingCrossfade contentKey={tab}>
          {tab === "tasks" ? (
            <ProjectTasksSurface
              projectID={projectID}
              sidebarMode={sidebarMode}
              viewMemory={taskListViewMemory}
            />
          ) : (
            <SessionList
              category={tab === "sessions" ? "main" : "subagent"}
              projectID={projectID}
              query={tab === "sessions" ? mainSessionsQuery : subagentSessionsQuery}
            />
          )}
        </OverlappingCrossfade>
      </div>
    </div>
  );
}

function SessionList({
  category,
  projectID,
  query,
}: Readonly<{
  category: "main" | "subagent";
  projectID: string;
  query: Readonly<{
    data:
      | Readonly<{
          pages: readonly Readonly<{ sessions: readonly SessionCatalogSummary[] }>[];
        }>
      | undefined;
    error: Error | null;
    fetchNextPage: () => Promise<unknown>;
    hasNextPage: boolean;
    isError: boolean;
    isFetchingNextPage: boolean;
    isPending: boolean;
    refetch: () => Promise<unknown>;
  }>;
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const sessions = query.data?.pages.flatMap((page) => page.sessions) ?? [];
  const initialBoundary = directionalBoundary({
    failed: query.isError,
    loading: query.isPending,
    loadingLabel: t("states.loading"),
    message: query.isError ? errorMessage(query.error) : "",
    onRetry: () => {
      void query.refetch();
    },
    retryLabel: t("app.retry"),
  });
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={
        initialBoundary === undefined ? (
          <EmptyState
            body={t("home.prototype.noSessionsBody")}
            fullPage={false}
            title={t("home.prototype.noSessionsTitle")}
          />
        ) : (
          <InfiniteListBoundary direction="initial" state={initialBoundary} />
        )
      }
      estimateSize={() => 96}
      getItemKey={(session) => session.id}
      hasNextPage={query.hasNextPage}
      isFetchingNextPage={query.isFetchingNextPage}
      items={sessions}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => {
        void query.fetchNextPage();
      }}
      paddingEnd={16}
      paddingStart={16}
      renderItem={(session) => (
        <HomeListCard
          ariaLabel={session.name ?? session.firstPromptPreview ?? session.id}
          onClick={() => {
            void navigation.openSessionChat({
              catalogOrigin: { category },
              projectID,
              sessionID: session.id,
            });
          }}
        >
          <span className="truncate text-sm text-[var(--color-muted)]">
            {formatRelativeTime(session.updatedAt)}
          </span>
          <strong className="truncate">{session.name ?? session.firstPromptPreview ?? session.id}</strong>
          <span className="truncate text-sm text-[var(--color-muted)]">
            {session.firstPromptPreview ?? t("home.prototype.noPromptPreview")}
          </span>
        </HomeListCard>
      )}
    />
  );
}

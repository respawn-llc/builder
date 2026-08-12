import { useState } from "react";
import { useInfiniteQuery, useQueries, useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ProjectSummary, SessionCatalogSummary } from "@/api";
import { errorMessage } from "@/api";
import {
  formatRelativeTime,
  mainSessionCatalogInfiniteQueryOptions,
  queryKeys,
  subagentSessionCatalogInfiniteQueryOptions,
  useAppNavigation,
  useAppServices,
  useOwnedSidebarRoots,
  type SidebarMode,
} from "@/app-facade";
import { WorkflowRow } from "@/shared/workflow-library";
import {
  directionalBoundary,
  EmptyState,
  homeListCardListMaxWidthClassName,
  HomeListCard,
  InfiniteListBoundary,
  IslandTabs,
  VirtualizedInfiniteList,
} from "@/ui";

type ProjectPrototypeTab = "boards" | "sessions" | "subagents";

export function ProjectPrototypeDetail({
  disabled,
  onLinkWorkflow,
  project,
  sidebarMode,
}: Readonly<{
  disabled: boolean;
  onLinkWorkflow: () => void;
  project: ProjectSummary;
  sidebarMode: SidebarMode;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const [tab, setTab] = useState<ProjectPrototypeTab>("boards");
  const linksQuery = useQuery({
    queryKey: queryKeys.projectWorkflowLinks(project.id),
    queryFn: async () => api.listProjectWorkflowLinks(project.id),
  });
  const linkedWorkflowQueries = useQueries({
    queries: (linksQuery.data ?? []).map((link) => ({
      queryKey: queryKeys.workflowDefinition(link.workflowID),
      queryFn: async () => api.getWorkflow(link.workflowID),
    })),
  });
  const mainSessionsQuery = useInfiniteQuery({
    ...mainSessionCatalogInfiniteQueryOptions(api, project.id),
    enabled: tab === "sessions",
  });
  const subagentSessionsQuery = useInfiniteQuery({
    ...subagentSessionCatalogInfiniteQueryOptions(api, project.id),
    enabled: tab === "subagents",
  });
  const linkedWorkflowRows = (linksQuery.data ?? []).map((link, index) => ({
    link,
    query: linkedWorkflowQueries[index],
  }));
  const linksBoundary = directionalBoundary({
    failed: linksQuery.isError,
    loading: linksQuery.isPending,
    loadingLabel: t("states.loading"),
    message: linksQuery.isError ? errorMessage(linksQuery.error) : "",
    onRetry: () => {
      void linksQuery.refetch();
    },
    retryLabel: t("app.retry"),
  });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="px-[var(--space-4)] pt-[var(--space-4)]">
        <IslandTabs
          ariaLabel={t("home.prototype.projectContent")}
          className="grid-cols-3"
          items={[
            {
              action: {
                ariaLabel: t("workflowLibrary.linkWorkflow"),
                children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
                disabled,
                onClick: onLinkWorkflow,
              },
              label: t("home.prototype.boards"),
              value: "boards",
            },
            {
              action: {
                ariaLabel: t("home.prototype.newChatUnavailable"),
                children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
                disabled: true,
                onClick: unavailablePrototypeAction,
              },
              label: t("home.prototype.sessions"),
              value: "sessions",
            },
            { label: t("home.prototype.subagents"), value: "subagents" },
          ]}
          onValueChange={setTab}
          value={tab}
        />
      </div>
      <div className="min-h-0 flex-1">
        {tab === "boards" ? (
          <VirtualizedInfiniteList
            className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
            empty={
              linksBoundary === undefined ? (
                <EmptyState
                  body={t("home.prototype.noBoardsBody")}
                  fullPage={false}
                  title={t("home.prototype.noBoardsTitle")}
                />
              ) : (
                <InfiniteListBoundary direction="initial" state={linksBoundary} />
              )
            }
            estimateSize={() => 96}
            getItemKey={(row) => row.link.id}
            hasNextPage={false}
            isFetchingNextPage={false}
            items={linkedWorkflowRows}
            loadingLabel={t("app.loadingMore")}
            onLoadMore={unavailablePrototypeAction}
            paddingEnd={16}
            paddingStart={16}
            renderItem={({ link, query }) =>
              query === undefined || query.isPending ? (
                <InfiniteListBoundary
                  direction="initial"
                  state={{ state: "loading", label: t("states.loading") }}
                />
              ) : query.isError ? (
                <InfiniteListBoundary
                  direction="initial"
                  state={{
                    state: "error",
                    message: errorMessage(query.error),
                    onRetry: () => {
                      void query.refetch();
                    },
                    retryLabel: t("app.retry"),
                  }}
                />
              ) : (
                <WorkflowRow
                  contextActions={{
                    onEdit: () => {
                      open({
                        kind: "workflowSettings",
                        mode: sidebarMode,
                        workflowID: query.data.workflow.id,
                      });
                    },
                  }}
                  onOpen={() => {
                    void navigation.openProject(project.id, link.workflowID);
                  }}
                  workflow={query.data.workflow}
                />
              )
            }
          />
        ) : (
          <SessionPrototypeList query={tab === "sessions" ? mainSessionsQuery : subagentSessionsQuery} />
        )}
      </div>
    </div>
  );
}

function SessionPrototypeList({
  query,
}: Readonly<{
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
          onClick={unavailablePrototypeAction}
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

function unavailablePrototypeAction(): void {
  return;
}

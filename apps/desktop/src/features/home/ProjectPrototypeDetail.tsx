import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { SessionCatalogSummary } from "@/api";
import { errorMessage } from "@/api";
import {
  formatRelativeTime,
  mainSessionCatalogInfiniteQueryOptions,
  subagentSessionCatalogInfiniteQueryOptions,
  useAppServices,
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
import { ProjectTasksSurface } from "./ProjectTasksSurface";

type ProjectPrototypeTab = "tasks" | "sessions" | "subagents";

export function ProjectPrototypeDetail({
  projectID,
  sidebarMode,
}: Readonly<{
  projectID: string;
  sidebarMode: SidebarMode;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const [tab, setTab] = useState<ProjectPrototypeTab>("tasks");
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
        {tab === "tasks" ? (
          <ProjectTasksSurface projectID={projectID} sidebarMode={sidebarMode} />
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

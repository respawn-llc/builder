import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { SessionCatalogSummary, SessionCategory } from "@/api";
import { errorMessage } from "@/api";
import { formatRelativeTime } from "@/app-facade";
import {
  autoLoadAvailable,
  cx,
  directionalBoundary,
  EmptyState,
  ErrorState,
  homeListCardListMaxWidthClassName,
  IslandTabs,
  islandSurfaceClassName,
  LoadingState,
  useElementHeight,
  VirtualizedInfiniteList,
} from "@/ui";
import { useProjectSessionsData, type ProjectSessionsData } from "./useProjectSessionsData";

export function ProjectSessionsBrowser({ projectID }: Readonly<{ projectID: string }>) {
  const { t } = useTranslation();
  const [category, setCategory] = useState<SessionCategory>("main");
  const controlsRef = useRef<HTMLDivElement | null>(null);
  const controlsHeight = useElementHeight(controlsRef);
  const data = useProjectSessionsData(projectID, category);

  return (
    <div className="relative h-full min-h-0" data-testid="project-sessions-browser">
      <IslandTabs
        ariaLabel={t("sessions.categories")}
        className="pointer-events-none absolute inset-x-0 top-0 z-10 grid grid-cols-2 gap-[var(--space-3)] px-[var(--space-4)] pt-[var(--space-4)] pb-[var(--space-3)]"
        containerRef={controlsRef}
        items={[
          { label: t("sessions.main"), value: "main" },
          { label: t("sessions.subagents"), value: "subagent" },
        ]}
        onValueChange={setCategory}
        value={category}
      />
      <div className="h-full min-h-0" key={`${projectID}:${category}`}>
        <SessionsContent
          category={category}
          controlsHeight={controlsHeight}
          data={data}
          projectID={projectID}
        />
      </div>
    </div>
  );
}

function SessionsContent({
  category,
  controlsHeight,
  data,
  projectID,
}: Readonly<{
  category: SessionCategory;
  controlsHeight: number;
  data: ProjectSessionsData;
  projectID: string;
}>) {
  const { t } = useTranslation();
  if (data.kind === "loading") {
    return (
      <div className="h-full min-h-0" style={{ paddingTop: controlsHeight }}>
        <LoadingState
          appearanceDelayMs={0}
          fullPage={false}
          reveal={false}
          title={t("states.loading")}
        />
      </div>
    );
  }
  if (data.kind === "error") {
    return (
      <div className="h-full min-h-0" style={{ paddingTop: controlsHeight }}>
        <ErrorState
          body={errorMessage(data.error)}
          fullPage={false}
          onRetry={data.retry}
          reveal={false}
          retryLabel={t("app.retry")}
          title={t("states.error")}
        />
      </div>
    );
  }

  const directionalMessage =
    data.error === null ? t("sessions.loadFailed") : errorMessage(data.error);
  const previousBoundary = directionalBoundary({
    failed: data.newerFailed,
    loading: data.loadingNewer,
    loadingLabel: t("app.loadingMore"),
    message: directionalMessage,
    onRetry: data.loadNewer,
    retryLabel: t("app.retry"),
  });
  const nextBoundary = directionalBoundary({
    failed: data.olderFailed,
    loading: data.loadingOlder,
    loadingLabel: t("app.loadingMore"),
    message: directionalMessage,
    onRetry: data.loadOlder,
    retryLabel: t("app.retry"),
  });
  return (
    <VirtualizedInfiniteList
      ariaLabel={t("sessions.list")}
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={
        <EmptyState
          body={t("sessions.emptyBody")}
          fullPage={false}
          title={t("sessions.emptyTitle")}
        />
      }
      estimateSize={() => 76}
      getItemKey={(session) => session.id}
      hasNextPage={autoLoadAvailable(data.hasOlder, nextBoundary)}
      hasPreviousPage={autoLoadAvailable(data.hasNewer, previousBoundary)}
      isFetchingNextPage={data.loadingOlder}
      isFetchingPreviousPage={data.loadingNewer}
      items={data.rows}
      loadingLabel={t("app.loadingMore")}
      loadMoreKey={data.loadMoreKey}
      nextBoundary={nextBoundary}
      onLoadMore={data.loadOlder}
      onLoadPrevious={data.loadNewer}
      paddingEnd={16}
      paddingStart={controlsHeight}
      previousBoundary={previousBoundary}
      previousLoadKey={data.previousLoadKey}
      renderItem={(session) => <SessionRow session={session} />}
      rowSpacing="compact"
      testId={`session-list-${projectID}-${category}`}
    />
  );
}

function SessionRow({ session }: Readonly<{ session: SessionCatalogSummary }>) {
  const trimmedName = session.name?.trim();
  const title = trimmedName ?? session.id;
  const preview = session.firstPromptPreview?.trim();
  return (
    <article
      className={cx(
        "grid min-w-0 gap-[var(--space-1)] rounded-[var(--radius-l)] p-[var(--space-3)] text-[var(--color-on-island)]",
        islandSurfaceClassName(1),
      )}
      data-testid="session-row"
    >
      <div className="flex min-w-0 items-baseline gap-[var(--space-3)]">
        <strong className="min-w-0 flex-1 truncate">{title}</strong>
        <time className="shrink-0 text-xs text-[var(--color-muted)]">
          {formatRelativeTime(session.updatedAt)}
        </time>
      </div>
      {preview === undefined || preview.length === 0 ? null : (
        <p className="m-0 truncate text-sm text-[var(--color-muted)]">{preview}</p>
      )}
    </article>
  );
}

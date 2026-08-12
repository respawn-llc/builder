import { useMemo, useRef, type Ref } from "react";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ProjectSummary } from "@/api";
import { errorMessage } from "@/api";
import { useAppNavigation } from "@/app-facade";
import { WorkflowCard, useWorkflowPages } from "@/shared/workflow-library";
import {
  EmptyState,
  ErrorState,
  homeListCardListMaxWidthClassName,
  IslandTabs,
  LoadingState,
  useElementHeight,
  VirtualizedInfiniteList,
} from "@/ui";
import { ProjectRow } from "./ProjectRow";
import type { useProjectPages } from "./useHomeData";

export type HomePrimaryTab = "projects" | "workflows";

export type HomePrimaryPaneProps = Readonly<{
  activeTab: HomePrimaryTab;
  disabled: boolean;
  onChooseWorkspace: () => void;
  onCreateWorkflow: () => void;
  onTabChange: (tab: HomePrimaryTab) => void;
  projectItems: readonly ProjectSummary[];
  projectsQuery: ReturnType<typeof useProjectPages>;
}>;

export function HomePrimaryPane({
  activeTab,
  disabled,
  onChooseWorkspace,
  onCreateWorkflow,
  onTabChange,
  projectItems,
  projectsQuery,
}: HomePrimaryPaneProps) {
  const { t } = useTranslation();
  const controlsRef = useRef<HTMLDivElement | null>(null);
  const controlsHeight = useElementHeight(controlsRef);
  return (
    <div className="relative h-full min-h-0" data-testid="home-primary-pane">
      <HomePrimaryTabs
        activeTab={activeTab}
        controlsRef={controlsRef}
        disabled={disabled}
        onChooseWorkspace={onChooseWorkspace}
        onCreateWorkflow={onCreateWorkflow}
        onTabChange={onTabChange}
      />
      <div className="h-full min-h-0" key={activeTab}>
        <h2 className="sr-only" id="home-primary-pane-title">
          {activeTab === "projects" ? t("home.projectsPane") : t("workflowLibrary.homeIslandTitle")}
        </h2>
        <div className="route-transition-frame h-full min-h-0" data-testid="home-primary-scroll-layer">
          {activeTab === "projects" ? (
            <ProjectList controlsHeight={controlsHeight} items={projectItems} query={projectsQuery} />
          ) : (
            <HomeWorkflowList controlsHeight={controlsHeight} />
          )}
        </div>
      </div>
    </div>
  );
}

function HomePrimaryTabs({
  activeTab,
  controlsRef,
  disabled,
  onChooseWorkspace,
  onCreateWorkflow,
  onTabChange,
}: Readonly<{
  activeTab: HomePrimaryTab;
  controlsRef: Ref<HTMLDivElement>;
  disabled: boolean;
  onChooseWorkspace: () => void;
  onCreateWorkflow: () => void;
  onTabChange: (tab: HomePrimaryTab) => void;
}>) {
  const { t } = useTranslation();
  return (
    <IslandTabs
      ariaLabel={t("home.projectsPane")}
      className="pointer-events-none absolute inset-x-0 top-0 z-10 grid grid-cols-2 gap-[var(--space-3)] px-[var(--space-4)] pt-[var(--space-4)] pb-[var(--space-3)]"
      containerRef={controlsRef}
      data-testid="home-primary-controls"
      items={[
        {
          action: {
            ariaLabel: t("home.newProject"),
            children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
            disabled,
            onClick: onChooseWorkspace,
          },
          label: t("home.projectsPane"),
          testId: "home-primary-projects-tab-island",
          value: "projects",
        },
        {
          action: {
            ariaLabel: t("workflowLibrary.createWorkflow"),
            children: <Plus aria-hidden="true" size={18} strokeWidth={1.5} />,
            disabled,
            onClick: onCreateWorkflow,
          },
          label: t("workflowLibrary.homeIslandTitle"),
          testId: "home-primary-workflows-tab-island",
          value: "workflows",
        },
      ]}
      onValueChange={onTabChange}
      value={activeTab}
    />
  );
}

function ProjectList({
  controlsHeight,
  items,
  query,
}: Readonly<{
  controlsHeight: number;
  items: readonly ProjectSummary[];
  query: ReturnType<typeof useProjectPages>;
}>) {
  const { t } = useTranslation();
  if (query.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (query.isError) {
    return <ErrorState body={errorMessage(query.error)} reveal={false} title={t("states.error")} />;
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={<HomeInlineEmptyState body={t("home.emptyBody")} />}
      estimateSize={() => 96}
      getItemKey={(project) => project.id}
      hasNextPage={query.hasNextPage}
      isFetchingNextPage={query.isFetchingNextPage}
      items={items}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void query.fetchNextPage()}
      paddingEnd={16}
      paddingStart={controlsHeight}
      renderItem={(project) => <ProjectRow project={project} />}
    />
  );
}

function HomeWorkflowList({ controlsHeight }: Readonly<{ controlsHeight: number }>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const workflowsQuery = useWorkflowPages();
  const workflows = useMemo(
    () => workflowsQuery.data?.pages.flatMap((page) => page.workflows) ?? [],
    [workflowsQuery.data],
  );
  if (workflowsQuery.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (workflowsQuery.isError) {
    return (
      <ErrorState
        body={errorMessage(workflowsQuery.error)}
        reveal={false}
        title={t("workflowLibrary.loadFailed")}
      />
    );
  }
  return (
    <VirtualizedInfiniteList
      className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${homeListCardListMaxWidthClassName}`}
      empty={
        <EmptyState
          body={t("workflowLibrary.emptyBody")}
          fullPage={false}
          title={t("workflowLibrary.emptyTitle")}
        />
      }
      estimateSize={() => 96}
      getItemKey={(workflow) => workflow.id}
      hasNextPage={workflowsQuery.hasNextPage}
      isFetchingNextPage={workflowsQuery.isFetchingNextPage}
      items={workflows}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={() => void workflowsQuery.fetchNextPage()}
      paddingEnd={16}
      paddingStart={controlsHeight}
      renderItem={(workflow) => (
        <WorkflowCard
          onOpen={() => {
            void navigation.openWorkflowEditor({ workflowID: workflow.id });
          }}
          workflow={workflow}
        />
      )}
    />
  );
}

function HomeInlineEmptyState({ body }: Readonly<{ body: string }>) {
  return (
    <div className="rounded-[var(--radius-l)] border border-dashed border-[var(--color-outline)] p-[var(--space-4)] text-[var(--color-muted)]">
      <p>{body}</p>
    </div>
  );
}

import { type ReactNode, useMemo } from "react";
import { Check, Folder, Plus, Workflow } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ProjectSummary } from "@/api";
import { errorMessage } from "@/api";
import { useAppNavigation, useOwnedSidebarRoots, type SidebarMode } from "@/app-facade";
import { WorkflowRow, useWorkflowPages } from "@/shared/workflow-library";
import { cx, directionalBoundary, EmptyState, InfiniteListBoundary, VirtualizedInfiniteList } from "@/ui";
import { OverlappingCrossfade } from "./OverlappingCrossfade";
import { ProjectRow } from "./ProjectRow";
import type { useProjectPages } from "./useHomeData";

export type HomeSidebarCategory = "projects" | "workflows";

export function HomeSidebar({
  disabled,
  onCategorySelect,
  onChooseWorkspace,
  onCreateWorkflow,
  onProjectSelect,
  projectItems,
  projectsQuery,
  sidebarMode,
  selectedCategory,
  selectedProjectID,
}: Readonly<{
  disabled: boolean;
  onCategorySelect: (category: HomeSidebarCategory) => void;
  onChooseWorkspace: () => void;
  onCreateWorkflow: () => void;
  onProjectSelect: (projectID: string) => void;
  projectItems: readonly ProjectSummary[];
  projectsQuery: ReturnType<typeof useProjectPages>;
  sidebarMode: SidebarMode;
  selectedCategory: HomeSidebarCategory;
  selectedProjectID: string | null;
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const workflowsQuery = useWorkflowPages("", selectedCategory === "workflows");
  const workflows = useMemo(
    () => workflowsQuery.data?.pages.flatMap((page) => page.workflows) ?? [],
    [workflowsQuery.data],
  );
  const activeQuery = selectedCategory === "projects" ? projectsQuery : workflowsQuery;
  const initialBoundary = directionalBoundary({
    failed: activeQuery.isError,
    loading: activeQuery.isPending,
    loadingLabel: t("states.loading"),
    message: activeQuery.isError ? errorMessage(activeQuery.error) : "",
    onRetry: () => {
      void activeQuery.refetch();
    },
    retryLabel: t("app.retry"),
  });
  const empty =
    initialBoundary === undefined ? (
      <EmptyState
        body={selectedCategory === "projects" ? t("home.emptyBody") : t("workflowLibrary.emptyBody")}
        fullPage={false}
        title={selectedCategory === "projects" ? t("home.emptyTitle") : t("workflowLibrary.emptyTitle")}
      />
    ) : (
      <InfiniteListBoundary direction="initial" state={initialBoundary} />
    );
  return (
    <div className="flex h-full min-h-0 select-none flex-col px-[calc(var(--space-3)/2)]">
      <div className="relative z-20 grid shrink-0 gap-[var(--space-2)] pt-[var(--space-3)]">
        <CategoryRow
          actionLabel={t("home.newProject")}
          disabled={disabled}
          icon={<Folder size={18} strokeWidth={1.5} />}
          label={t("home.projectsPane")}
          onAction={onChooseWorkspace}
          onSelect={() => {
            onCategorySelect("projects");
          }}
          selected={selectedCategory === "projects"}
        />
        <CategoryRow
          actionLabel={t("workflowLibrary.createWorkflow")}
          disabled={disabled}
          icon={<Workflow size={18} strokeWidth={1.5} />}
          label={t("workflowLibrary.homeIslandTitle")}
          onAction={onCreateWorkflow}
          onSelect={() => {
            onCategorySelect("workflows");
          }}
          selected={selectedCategory === "workflows"}
        />
      </div>
      <div className="relative -mt-[var(--space-3)] min-h-0 flex-1">
        <OverlappingCrossfade contentKey={selectedCategory}>
          {selectedCategory === "projects" ? (
            <VirtualizedInfiniteList
              className="home-sidebar-scroll h-full min-h-0 overflow-auto hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch]"
              empty={empty}
              estimateSize={() => 54}
              getItemKey={(project) => project.id}
              hasNextPage={projectsQuery.hasNextPage}
              isFetchingNextPage={projectsQuery.isFetchingNextPage}
              items={projectItems}
              loadingLabel={t("app.loadingMore")}
              onLoadMore={() => {
                void projectsQuery.fetchNextPage();
              }}
              paddingEnd={24}
              paddingStart={24}
              rowSpacing="compact"
              renderItem={(project) => (
                <ProjectRow
                  onSelect={() => {
                    onProjectSelect(project.id);
                  }}
                  project={project}
                  selected={project.id === selectedProjectID}
                  sidebarMode={sidebarMode}
                />
              )}
            />
          ) : (
            <VirtualizedInfiniteList
              className="home-sidebar-scroll h-full min-h-0 overflow-auto hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch]"
              empty={empty}
              estimateSize={() => 54}
              getItemKey={(workflow) => workflow.id}
              hasNextPage={workflowsQuery.hasNextPage}
              isFetchingNextPage={workflowsQuery.isFetchingNextPage}
              items={workflows}
              loadingLabel={t("app.loadingMore")}
              onLoadMore={() => {
                void workflowsQuery.fetchNextPage();
              }}
              paddingEnd={24}
              paddingStart={24}
              rowSpacing="compact"
              renderItem={(workflow) => (
                <WorkflowRow
                  contextActions={{
                    onEdit: () => {
                      open({ kind: "workflowSettings", mode: sidebarMode, workflowID: workflow.id });
                    },
                  }}
                  onOpen={() => {
                    void navigation.openWorkflowEditor({ workflowID: workflow.id });
                  }}
                  workflow={workflow}
                />
              )}
            />
          )}
        </OverlappingCrossfade>
      </div>
    </div>
  );
}

function CategoryRow({
  actionLabel,
  disabled,
  icon,
  label,
  onAction,
  onSelect,
  selected,
}: Readonly<{
  actionLabel: string;
  disabled: boolean;
  icon: ReactNode;
  label: string;
  onAction: () => void;
  onSelect: () => void;
  selected: boolean;
}>) {
  return (
    <div
      className={cx(
        "relative flex min-w-0 items-center gap-[var(--space-2)] rounded-[var(--radius-m)] px-[calc(var(--space-3)/2)] py-[var(--space-1)] pr-[calc(40px+var(--space-3)/2)] transition-colors",
        selected
          ? "bg-[color-mix(in_srgb,var(--color-on-island)_12%,transparent)]"
          : "hover:bg-[color-mix(in_srgb,var(--color-on-island)_4%,transparent)]",
      )}
    >
      <button
        className="flex min-w-0 flex-1 items-center gap-[var(--space-2)] text-left"
        onClick={onSelect}
        type="button"
      >
        <CategoryIcon icon={icon} selected={selected} />
        <strong className="min-w-0 truncate">{label}</strong>
      </button>
      <button
        aria-label={actionLabel}
        className="absolute right-[calc(var(--space-3)/2)] top-1/2 grid h-10 w-10 -translate-y-1/2 place-items-center justify-items-end rounded-full text-[var(--color-on-island)] disabled:opacity-55"
        disabled={disabled}
        onClick={onAction}
        type="button"
      >
        <Plus aria-hidden="true" size={14} strokeWidth={1.5} />
      </button>
    </div>
  );
}

function CategoryIcon({ icon, selected }: Readonly<{ icon: ReactNode; selected: boolean }>) {
  return (
    <span className="relative h-[18px] w-[18px] shrink-0">
      <span
        aria-hidden="true"
        className={cx("absolute inset-0 transition-opacity", selected ? "opacity-0" : "opacity-100")}
      >
        {icon}
      </span>
      <Check
        aria-hidden="true"
        className={cx("absolute left-0.5 top-0.5 transition-opacity", selected ? "opacity-100" : "opacity-0")}
        size={14}
        strokeWidth={2}
      />
    </span>
  );
}

import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAppNavigation } from "@/app-facade";
import {
  autoLoadAvailable,
  InfiniteListBoundary,
  InteractiveChip,
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
} from "@/ui";
import type { ProjectTaskWorkflowItem } from "./projectTaskWorkflows";
import { ProjectSortChrome } from "./ProjectSortChrome";
import type { ProjectTaskSort } from "./projectTaskSorting";

export function ProjectWorkflowStrip({
  hasNextPage,
  hasPreviousPage,
  initialBoundary,
  isFetchingNextPage,
  isFetchingPreviousPage,
  nextBoundary,
  onLinkWorkflow,
  onLoadNext,
  onLoadPrevious,
  onSortChange,
  previousBoundary,
  projectID,
  sort,
  workflows,
}: Readonly<{
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  initialBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  onLinkWorkflow: () => void;
  onLoadNext: () => void;
  onLoadPrevious: () => void;
  onSortChange(sort: ProjectTaskSort): void;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  projectID: string;
  sort: ProjectTaskSort;
  workflows: readonly ProjectTaskWorkflowItem[];
}>) {
  const { t } = useTranslation();
  const navigation = useAppNavigation();
  return (
    <VirtualizedInfiniteList
      className="shrink-0 overflow-x-auto py-[var(--space-3)] hide-scrollbar"
      empty={
        initialBoundary === undefined ? undefined : (
          <InfiniteListBoundary direction="initial" state={initialBoundary} />
        )
      }
      estimateSize={() => 160}
      getItemKey={(workflow) => workflow.id}
      hasNextPage={autoLoadAvailable(hasNextPage, nextBoundary)}
      hasPreviousPage={autoLoadAvailable(hasPreviousPage, previousBoundary)}
      header={
        <div className="flex shrink-0 items-center gap-[var(--space-2)]">
          <ProjectSortChrome onSortChange={onSortChange} sort={sort} />
          <InteractiveChip className="shrink-0" onClick={onLinkWorkflow}>
            <Plus aria-hidden="true" size={14} strokeWidth={1.8} />
            {t("workflowLibrary.linkWorkflow")}
          </InteractiveChip>
        </div>
      }
      isFetchingNextPage={isFetchingNextPage}
      isFetchingPreviousPage={isFetchingPreviousPage}
      itemRole="presentation"
      items={workflows}
      loadingLabel={t("app.loadingMore")}
      onLoadMore={onLoadNext}
      onLoadPrevious={onLoadPrevious}
      orientation="horizontal"
      paddingEnd={16}
      paddingStart={16}
      previousBoundary={previousBoundary}
      previousLoadItemKey={workflows.at(0)?.id}
      renderItem={(workflow) => (
        <InteractiveChip
          className="shrink-0"
          onClick={() => void navigation.openProject(projectID, workflow.id)}
          title={workflow.description}
        >
          {workflow.name}
        </InteractiveChip>
      )}
      role="presentation"
      nextBoundary={nextBoundary}
    />
  );
}

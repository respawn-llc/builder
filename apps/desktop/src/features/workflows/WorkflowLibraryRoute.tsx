import { Plus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowRecord } from "@/api";
import { errorMessage } from "@/api";
import { useAppNavigation } from "@/app-facade";
import { useSidebar } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { WorkflowCard, useWorkflowPages } from "@/shared/workflow-library";
import { Button, EmptyState, ErrorState, LoadingState, VirtualizedInfiniteList } from "@/ui";

const workflowLibraryItemMaxWidthClassName = "[&>*]:max-w-[1280px]";

export function WorkflowLibraryRoute() {
  const { t } = useTranslation();
  const { openSidebar } = useSidebar();
  const connection = useConnectionSnapshot();
  const workflowsQuery = useWorkflowPages();
  const createDisabled = connection.phase !== "connected";
  const workflows = useMemo(
    () => workflowsQuery.data?.pages.flatMap((page) => page.workflows) ?? [],
    [workflowsQuery.data],
  );
  const openCreateWorkflow = () => {
    void openSidebar({ kind: "workflowCreate", mode: "overlay" });
  };

  if (workflowsQuery.isPending) {
    return <LoadingState appearanceDelayMs={0} title={t("workflowLibrary.title")} />;
  }
  if (workflowsQuery.isError) {
    return (
      <ErrorState
        body={errorMessage(workflowsQuery.error)}
        chromePadding
        onRetry={() => void workflowsQuery.refetch()}
        retryLabel={t("app.retry")}
        title={t("workflowLibrary.loadFailed")}
      />
    );
  }
  if (workflows.length === 0) {
    return (
      <section className="h-full min-h-0" data-testid="workflow-library-route">
        <EmptyState
          action={
            <Button disabled={createDisabled} onClick={openCreateWorkflow} variant="primary">
              {t("workflowLibrary.createWorkflow")}
            </Button>
          }
          body={t("workflowLibrary.emptyBody")}
          title={t("workflowLibrary.emptyTitle")}
        />
      </section>
    );
  }

  return (
    <section className="h-full min-h-0" data-testid="workflow-library-route">
      <div className="island-glass grid h-full min-h-0 overflow-hidden rounded-[var(--radius-xl)]">
        <VirtualizedInfiniteList
          className={`h-full min-h-0 overflow-auto px-[var(--space-4)] hide-scrollbar contain-strict [-webkit-overflow-scrolling:touch] [&>*]:mx-auto [&>*]:w-full ${workflowLibraryItemMaxWidthClassName}`}
          estimateSize={() => 96}
          getItemKey={(workflow) => workflow.id}
          hasNextPage={workflowsQuery.hasNextPage}
          header={<WorkflowLibraryHeader disabled={createDisabled} onCreate={openCreateWorkflow} />}
          isFetchingNextPage={workflowsQuery.isFetchingNextPage}
          items={workflows}
          loadingLabel={t("app.loadingMore")}
          onLoadMore={() => void workflowsQuery.fetchNextPage()}
          paddingEnd={16}
          paddingStart={16}
          renderItem={(workflow) => <WorkflowLibraryCard workflow={workflow} />}
        />
      </div>
    </section>
  );
}

function WorkflowLibraryCard({ workflow }: Readonly<{ workflow: WorkflowRecord }>) {
  const navigation = useAppNavigation();
  const { openSidebar } = useSidebar();

  return (
    <WorkflowCard
      contextActions={{
        onEdit: () => {
          void openSidebar({ kind: "workflowEditor", mode: "overlay", workflowID: workflow.id });
        },
      }}
      onOpen={() => {
        void navigation.openWorkflowEditor({ workflowID: workflow.id });
      }}
      workflow={workflow}
    />
  );
}

function WorkflowLibraryHeader({
  disabled,
  onCreate,
}: Readonly<{ disabled: boolean; onCreate: () => void }>) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center justify-between gap-[var(--space-3)] pb-[var(--space-2)]">
      <h1 className="m-0 text-[1.15rem]" id="workflow-library-title">
        {t("workflowLibrary.title")}
      </h1>
      <button
        aria-label={t("workflowLibrary.createWorkflow")}
        className="grid h-9 w-9 place-items-center rounded-full border border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-on-island)] disabled:cursor-not-allowed disabled:opacity-55"
        disabled={disabled}
        onClick={onCreate}
        type="button"
      >
        <Plus aria-hidden="true" size={18} strokeWidth={1.6} />
      </button>
    </div>
  );
}

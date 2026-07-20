import { useCallback, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useOpenExternalLink } from "@/app-facade";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { ProjectLabelsProvider, TaskLabelAssignmentProvider, useProjectLabelCatalog } from "@/shared/labels";
import { ErrorState, LoadingState } from "@/ui";
import { TaskDetailContent } from "./TaskDetailContent";
import { useTaskActivity, useTaskAttention, useTaskComments, useTaskDetail } from "./useTaskDetailData";

export type TaskDetailSurfaceProps = Readonly<{
  taskId: string;
  enabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onMutated?: (() => void) | undefined;
}>;

export function TaskDetailSurface({ taskId, enabled, initialFocus, onMutated }: TaskDetailSurfaceProps) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const reportLabelError = useCallback(
    (error: unknown) => {
      push({
        body: errorMessage(error),
        durationMs: Infinity,
        id: "task-label-load-error",
        title: t("labels.loadFailed"),
        tone: "danger",
      });
    },
    [push, t],
  );
  const detail = useTaskDetail(taskId, enabled);
  const attention = useTaskAttention(taskId, enabled);
  const activity = useTaskActivity(taskId, enabled);
  const comments = useTaskComments(taskId, enabled);
  const openLink = useOpenExternalLink();

  if (detail.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (detail.isError) {
    return <ErrorState body={errorMessage(detail.error)} reveal={false} title={t("states.error")} />;
  }
  const content = (
    <ProjectLabelsProvider onBackgroundError={reportLabelError} projectID={detail.data.projectID}>
      <TaskDetailAssignmentScope taskID={detail.data.id}>
        <TaskDetailContent
          activity={activity}
          attention={attention}
          comments={comments}
          detail={detail.data}
          initialFocus={initialFocus}
          onMutated={onMutated}
          openLink={openLink}
        />
      </TaskDetailAssignmentScope>
    </ProjectLabelsProvider>
  );
  if (!attention.isError) {
    return content;
  }
  return (
    <div className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-2)]">
      <ErrorState
        body={errorMessage(attention.error)}
        fullPage={false}
        onRetry={() => {
          void attention.refetch();
        }}
        retryLabel={t("app.retry")}
        reveal={false}
        title={t("states.error")}
      />
      <div className="min-h-0">{content}</div>
    </div>
  );
}

function TaskDetailAssignmentScope({ children, taskID }: Readonly<{ children: ReactNode; taskID: string }>) {
  const catalog = useProjectLabelCatalog();
  return (
    <TaskLabelAssignmentProvider catalog={catalog.data ?? null} taskID={taskID}>
      {children}
    </TaskLabelAssignmentProvider>
  );
}

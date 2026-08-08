import { useCallback, useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskDetail, type TaskLabelAssignment } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { ProjectLabelsProvider, TaskLabelAssignmentProvider, useProjectLabelCatalog } from "@/shared/labels";
import { ErrorState, LoadingState } from "@/ui";
import { TaskDetailContent } from "./TaskDetailContent";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";
import { useTaskActivity, useTaskAttention, useTaskComments, useTaskDetail } from "./useTaskDetailData";

export type TaskDetailSurfaceProps = Readonly<{
  taskId: string;
  enabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onDeleteDismiss: TaskDetailDeleteDismissal;
  onMutated?: (() => void) | undefined;
}>;

export function TaskDetailSurface({
  taskId,
  enabled,
  initialFocus,
  onDeleteDismiss,
  onMutated,
}: TaskDetailSurfaceProps) {
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
  if (detail.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (detail.isError) {
    return <ErrorState body={errorMessage(detail.error)} reveal={false} title={t("states.error")} />;
  }
  const content = (
    <ProjectLabelsProvider onBackgroundError={reportLabelError} projectID={detail.data.projectID}>
      <TaskDetailAssignmentScope detail={detail.data}>
        <TaskDetailContent
          activity={activity}
          attention={attention}
          comments={comments}
          detail={detail.data}
          initialFocus={initialFocus}
          onDeleteDismiss={onDeleteDismiss}
          onMutated={onMutated}
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

function TaskDetailAssignmentScope({
  children,
  detail,
}: Readonly<{ children: ReactNode; detail: TaskDetail }>) {
  const catalog = useProjectLabelCatalog();
  const initialAssignment = useMemo<TaskLabelAssignment>(
    () => ({
      taskID: detail.id,
      labelIDs: detail.labelIDs,
    }),
    [detail.id, detail.labelIDs],
  );
  return (
    <TaskLabelAssignmentProvider
      catalog={catalog.data ?? null}
      initialAssignment={initialAssignment}
      taskID={detail.id}
      workflowID={detail.workflowID}
    >
      {children}
    </TaskLabelAssignmentProvider>
  );
}

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, rpcErrorCodes, RpcError, type TaskDetail, type TaskLabelAssignment } from "@/api";
import { useOpenExternalLink } from "@/app-facade";
import type { SidebarTaskDetailSnapshot, TaskDetailInitialFocus } from "@/app-facade";
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
  sidebarSnapshot?: SidebarTaskDetailSnapshot | undefined;
  onMissingTask?: (() => void) | undefined;
}>;

export function TaskDetailSurface({
  taskId,
  enabled,
  initialFocus,
  onMutated,
  sidebarSnapshot,
  onMissingTask,
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
  const openLink = useOpenExternalLink();
  const refetchDetail = detail.refetch;
  const refetchAttention = attention.refetch;
  const refetchActivity = activity.refetch;
  const refetchComments = comments.refetch;
  const restorationSnapshotKey =
    sidebarSnapshot === undefined ? null : JSON.stringify(sidebarSnapshot);
  const [completedRestorationSnapshotKey, setCompletedRestorationSnapshotKey] = useState<string | null>(null);
  useEffect(() => {
    if (restorationSnapshotKey === null) {
      return;
    }
    let canceled = false;
    void Promise.all([
      refetchDetail(),
      refetchAttention(),
      refetchActivity(),
      refetchComments(),
    ]).then(
      () => {
        if (!canceled) {
          setCompletedRestorationSnapshotKey(restorationSnapshotKey);
        }
      },
      () => {
        if (!canceled) {
          setCompletedRestorationSnapshotKey(restorationSnapshotKey);
        }
      },
    );
    return () => {
      canceled = true;
    };
  }, [
    refetchActivity,
    refetchAttention,
    refetchComments,
    refetchDetail,
    restorationSnapshotKey,
  ]);
  const missingTask = detail.isError && isWorkflowTaskNotFound(detail.error);
  useEffect(() => {
    if (missingTask) {
      onMissingTask?.();
    }
  }, [missingTask, onMissingTask]);

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
          onMutated={onMutated}
          openLink={openLink}
          restoredDataReady={
            (restorationSnapshotKey === null ||
              completedRestorationSnapshotKey === restorationSnapshotKey) &&
            !detail.isFetching &&
            !attention.isFetching &&
            !activity.isFetching &&
            !comments.isFetching
          }
          sidebarSnapshot={sidebarSnapshot}
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

function isWorkflowTaskNotFound(error: unknown): boolean {
  return error instanceof RpcError && error.code === rpcErrorCodes.workflowTaskNotFound;
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

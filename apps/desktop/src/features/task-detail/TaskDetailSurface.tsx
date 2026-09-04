import { useCallback } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, isTaskMissingError } from "@/api";
import type {
  SidebarDestination,
  SidebarMode,
  SidebarPageNavigator,
  SidebarRootController,
  TaskDetailInitialFocus,
} from "@/app-facade";
import { useSidebarBackWhen } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { ProjectLabelsProvider, TaskLabelAssignmentProvider } from "@/shared/labels";
import { ErrorState, LoadingState } from "@/ui";
import { TaskDetailContent } from "./TaskDetailContent";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";
import type { TaskDetailSessionChatEntry } from "./taskDetailSessionChat";
import { useTaskActivity, useTaskAttention, useTaskComments, useTaskDetail } from "./useTaskDetailData";

type TaskDetailSurfaceCommonProps = Readonly<{
  taskId: string;
  enabled: boolean;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onMutated?: (() => void) | undefined;
  openSessionChat?: TaskDetailSessionChatEntry | undefined;
  openSidebar?: SidebarRootController["open"] | undefined;
  retainedState?: unknown;
  sidebarDestination?: Extract<SidebarDestination, { kind: "taskDetail" }> | undefined;
  sidebarMode?: SidebarMode | undefined;
}>;

export type TaskDetailSurfaceProps = TaskDetailSurfaceCommonProps &
  (
    | Readonly<{
        navigator: SidebarPageNavigator;
        onDeleteDismiss?: undefined;
      }>
    | Readonly<{
        navigator?: undefined;
        onDeleteDismiss: TaskDetailDeleteDismissal;
      }>
  );

export function TaskDetailSurface({
  taskId,
  enabled,
  initialFocus,
  navigator,
  onDeleteDismiss,
  onMutated,
  openSessionChat,
  openSidebar,
  retainedState,
  sidebarDestination,
  sidebarMode,
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
  const deleteDismissal: TaskDetailDeleteDismissal =
    navigator === undefined ? onDeleteDismiss : async () => ({ kind: navigator.close() });
  const taskMissing = detail.isError && isTaskMissingError(detail.error);
  useSidebarBackWhen(taskMissing, navigator);
  if (detail.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (detail.isError) {
    if (taskMissing && navigator !== undefined) return null;
    return <ErrorState body={errorMessage(detail.error)} reveal={false} title={t("states.error")} />;
  }
  const content = (
    <ProjectLabelsProvider onBackgroundError={reportLabelError} projectID={detail.data.projectID}>
      <TaskLabelAssignmentProvider key={detail.data.id} taskID={detail.data.id}>
        <TaskDetailContent
          key={detail.data.id}
          activity={activity}
          attention={attention}
          comments={comments}
          detail={detail.data}
          initialFocus={initialFocus}
          navigator={navigator}
          onDeleteDismiss={deleteDismissal}
          onMutated={onMutated}
          openSessionChat={openSessionChat}
          openSidebar={openSidebar}
          retainedState={retainedState}
          sidebarDestination={sidebarDestination}
          sidebarMode={sidebarMode}
        />
      </TaskLabelAssignmentProvider>
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

import { useTranslation } from "react-i18next";

import { errorMessage } from "../../api/errors";
import { useOpenExternalLink } from "../../app/nativeHooks";
import { ErrorState, LoadingState } from "../../ui";
import { TaskDetailContent } from "./TaskDetailContent";
import { useTaskActivity, useTaskComments, useTaskDetail } from "./useTaskDetailData";

export type TaskDetailSurfaceProps = Readonly<{
  taskId: string;
  enabled: boolean;
  initialFocus?: "firstQuestion" | undefined;
  onMutated?: (() => void) | undefined;
}>;

export function TaskDetailSurface({
  taskId,
  enabled,
  initialFocus,
  onMutated,
}: TaskDetailSurfaceProps) {
  const { t } = useTranslation();
  const detail = useTaskDetail(taskId, enabled);
  const activity = useTaskActivity(taskId, enabled);
  const comments = useTaskComments(taskId, enabled);
  const openLink = useOpenExternalLink();

  if (detail.isPending) {
    return <LoadingState appearanceDelayMs={0} fullPage={false} reveal={false} title={t("states.loading")} />;
  }
  if (detail.isError) {
    return <ErrorState body={errorMessage(detail.error)} reveal={false} title={t("states.error")} />;
  }
  return (
    <TaskDetailContent
      activity={activity}
      comments={comments}
      detail={detail.data}
      initialFocus={initialFocus}
      onMutated={onMutated}
      openLink={openLink}
    />
  );
}

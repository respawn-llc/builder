import { useTranslation } from "react-i18next";

import type { TaskDependencyProgress } from "@/api";
import {
  ProgressChip,
  ProgressInteractiveChip,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
  type ProgressChipProps,
  type ProgressInteractiveChipProps,
} from "@/ui";

export type TaskDependencyProgressChipProps = Readonly<
  Omit<ProgressChipProps, "label" | "maximum" | "tone" | "value"> & {
    preview?: boolean | undefined;
    progress: TaskDependencyProgress;
  }
>;

export type TaskDependencyProgressInteractiveChipProps = Readonly<
  Omit<ProgressInteractiveChipProps, "label" | "maximum" | "tone" | "value"> & {
    progress: TaskDependencyProgress;
  }
>;

export function TaskDependencyProgressChip({
  preview = false,
  progress,
  tabIndex = 0,
  ...appearance
}: TaskDependencyProgressChipProps) {
  return (
    <TaskDependencyProgressChipPresentation
      appearance={{ ...appearance, tabIndex }}
      preview={preview}
      progress={progress}
    />
  );
}

export function TaskDependencyProgressInteractiveChip({
  progress,
  ...behavior
}: TaskDependencyProgressInteractiveChipProps) {
  return <TaskDependencyProgressChipPresentation behavior={behavior} progress={progress} />;
}

function TaskDependencyProgressChipPresentation({
  appearance,
  behavior,
  preview = false,
  progress,
}: Readonly<{
  appearance?: Omit<ProgressChipProps, "label" | "maximum" | "tone" | "value">;
  behavior?: Omit<ProgressInteractiveChipProps, "label" | "maximum" | "tone" | "value">;
  preview?: boolean | undefined;
  progress: TaskDependencyProgress;
}>) {
  const { t } = useTranslation();
  const complete = progress.satisfiedCount === progress.totalCount;
  const values = {
    completed: progress.satisfiedCount,
    total: progress.totalCount,
  };
  const tooltip = preview
    ? t("task.dependenciesProgressPreview", values)
    : t(complete ? "task.dependenciesProgressComplete" : "task.dependenciesProgress", values);
  const accessibleLabel = t(
    preview ? "task.dependenciesProgressPreviewAccessible" : "task.dependenciesProgressAccessible",
    values,
  );
  const progressProps = {
    label: accessibleLabel,
    maximum: progress.totalCount,
    tone: complete ? ("success" as const) : ("primary" as const),
    value: progress.satisfiedCount,
  };
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          {behavior === undefined ? (
            <ProgressChip {...progressProps} {...appearance} />
          ) : (
            <ProgressInteractiveChip {...progressProps} {...behavior} />
          )}
        </TooltipTrigger>
        <TooltipContent level={3} sideOffset={6}>
          {tooltip}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

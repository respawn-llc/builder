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
    progress: TaskDependencyProgress;
  }
>;

export type TaskDependencyProgressInteractiveChipProps = Readonly<
  Omit<ProgressInteractiveChipProps, "label" | "maximum" | "tone" | "value"> & {
    progress: TaskDependencyProgress;
  }
>;

export function TaskDependencyProgressChip({
  progress,
  tabIndex = 0,
  ...appearance
}: TaskDependencyProgressChipProps) {
  return (
    <TaskDependencyProgressChipPresentation appearance={{ ...appearance, tabIndex }} progress={progress} />
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
  progress,
}: Readonly<{
  appearance?: Omit<ProgressChipProps, "label" | "maximum" | "tone" | "value">;
  behavior?: Omit<ProgressInteractiveChipProps, "label" | "maximum" | "tone" | "value">;
  progress: TaskDependencyProgress;
}>) {
  const { t } = useTranslation();
  const complete = progress.satisfiedCount === progress.totalCount;
  const tooltip = t(complete ? "task.dependenciesProgressComplete" : "task.dependenciesProgress", {
    completed: progress.satisfiedCount,
    total: progress.totalCount,
  });
  const accessibleLabel = t("task.dependenciesProgressAccessible", {
    completed: progress.satisfiedCount,
    total: progress.totalCount,
  });
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

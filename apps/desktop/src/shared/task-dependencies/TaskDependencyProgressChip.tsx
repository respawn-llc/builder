import { useTranslation } from "react-i18next";

import type { TaskDependencyProgress } from "@/api";
import {
  ProgressInteractiveChip,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
  type ProgressInteractiveChipProps,
} from "@/ui";

export type TaskDependencyProgressChipProps = Readonly<
  Omit<ProgressInteractiveChipProps, "label" | "maximum" | "tone" | "value"> & {
    progress: TaskDependencyProgress;
  }
>;

export function TaskDependencyProgressChip({ progress, ...behavior }: TaskDependencyProgressChipProps) {
  const { t } = useTranslation();
  const complete = progress.satisfiedCount === progress.totalCount;
  const label = t(complete ? "task.dependenciesProgressComplete" : "task.dependenciesProgress", {
    completed: progress.satisfiedCount,
    total: progress.totalCount,
  });
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <ProgressInteractiveChip
            label={label}
            maximum={progress.totalCount}
            tone={complete ? "success" : "primary"}
            value={progress.satisfiedCount}
            {...behavior}
          />
        </TooltipTrigger>
        <TooltipContent level={3} sideOffset={6}>
          {label}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

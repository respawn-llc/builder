import type { MouseEvent, PointerEvent } from "react";

import type { TaskDependencyProgress } from "@/api";
import { ProgressInteractiveChip } from "@/ui";

export function BoardDependencyProgressChip({
  label,
  onActivate,
  progress,
}: Readonly<{
  label: string;
  onActivate(): void;
  progress: TaskDependencyProgress;
}>) {
  const complete = progress.satisfiedCount === progress.totalCount;
  return (
    <ProgressInteractiveChip
      label={label}
      maximum={progress.totalCount}
      onClick={(event: MouseEvent<HTMLButtonElement>) => {
        event.stopPropagation();
        onActivate();
      }}
      onDragStart={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onPointerDown={(event: PointerEvent<HTMLButtonElement>) => {
        event.stopPropagation();
      }}
      size="compact"
      tone={complete ? "success" : "primary"}
      value={progress.satisfiedCount}
    />
  );
}

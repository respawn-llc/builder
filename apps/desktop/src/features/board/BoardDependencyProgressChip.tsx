import type { MouseEvent, PointerEvent } from "react";

import type { TaskDependencyProgress } from "@/api";
import { TaskDependencyProgressInteractiveChip } from "@/shared/task-dependencies";

export function BoardDependencyProgressChip({
  onActivate,
  progress,
}: Readonly<{
  onActivate(): void;
  progress: TaskDependencyProgress;
}>) {
  return (
    <TaskDependencyProgressInteractiveChip
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
      progress={progress}
    />
  );
}

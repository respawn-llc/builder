import type { ReactNode } from "react";

export function TaskDetailBodyIslands({
  description,
  metadata,
}: Readonly<{
  description: ReactNode;
  metadata: ReactNode;
}>) {
  return (
    <div
      className="task-detail-body-split grid w-full min-w-0 max-w-full items-stretch gap-[var(--space-2)]"
      data-testid="task-detail-body-split"
    >
      <div
        className="task-detail-body-island-slot grid min-w-0"
        data-testid="task-detail-description-slot"
      >
        {description}
      </div>
      <div
        className="task-detail-body-island-slot grid min-w-0"
        data-testid="task-detail-metadata-slot"
      >
        {metadata}
      </div>
    </div>
  );
}

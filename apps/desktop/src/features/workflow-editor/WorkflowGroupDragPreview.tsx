import { IslandSurface } from "@/ui";

export type WorkflowGroupDragState = Readonly<{
  label: string;
  nodeID: string;
  targetGroupID: string | null;
  x: number;
  y: number;
}>;

export function WorkflowGroupDragPreview({ drag }: Readonly<{ drag: WorkflowGroupDragState }>) {
  return (
    <IslandSurface
      as="div"
      className="pointer-events-none fixed z-50 grid max-w-[260px] rounded-[var(--radius-l)] px-[var(--space-3)] py-[var(--space-2)] text-sm font-semibold text-[var(--color-on-island)]"
      data-drop-target={drag.targetGroupID === null ? "none" : "group"}
      data-testid="workflow-group-drag-preview"
      level={3}
      style={{
        borderColor: drag.targetGroupID === null ? "var(--color-outline)" : "var(--color-primary)",
        left: drag.x + 10,
        top: drag.y + 10,
      }}
    >
      <span className="truncate">{drag.label}</span>
    </IslandSurface>
  );
}

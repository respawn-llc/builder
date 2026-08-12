import { CircleDot } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { TaskStatusKind } from "@/api";
import { TaskStatusIcon } from "@/shared/task-status";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/ui";

const statusLegendGroups: readonly Readonly<{
  label: "Active" | "Backlog" | "Done";
  statuses: readonly TaskStatusKind[];
}>[] = [
  {
    label: "Active",
    statuses: ["waiting_question", "waiting_approval", "interrupted", "running", "queued", "active"],
  },
  { label: "Backlog", statuses: ["backlog"] },
  { label: "Done", statuses: ["done"] },
];

export function ProjectTaskStatusLegend() {
  const { t } = useTranslation();
  const legend = useMemo(
    () =>
      statusLegendGroups.map((group) => ({
        ...group,
        label: t(`home.prototype.statusGroups.${group.label.toLowerCase()}`),
      })),
    [t],
  );
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span
            aria-hidden="true"
            className="inline-grid cursor-help place-items-center"
            data-testid="project-task-status-legend-trigger"
          >
            <CircleDot size={15} strokeWidth={1.8} />
          </span>
        </TooltipTrigger>
        <TooltipContent
          className="grid max-w-sm items-start gap-[var(--space-2)] p-[var(--space-3)]"
          level={3}
        >
          {legend.map((group) => (
            <section className="grid gap-[var(--space-1)]" key={group.label}>
              <strong>{group.label}</strong>
              {group.statuses.map((status) => (
                <span className="grid grid-cols-[16px_auto] items-center gap-[var(--space-2)]" key={status}>
                  <TaskStatusIcon status={status} />
                  <span>
                    {t(`task.statusKinds.${status}`)} — {t(`home.prototype.statusDescriptions.${status}`)}
                  </span>
                </span>
              ))}
            </section>
          ))}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

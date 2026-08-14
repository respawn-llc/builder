import { CircleDot } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ProjectTaskGroupDefinition } from "@/api";
import { TaskStatusIcon } from "@/shared/task-status";
import { Spinner, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/ui";

export function ProjectTaskStatusLegend({
  definitions,
}: Readonly<{
  definitions: readonly ProjectTaskGroupDefinition[] | undefined;
}>) {
  const { t } = useTranslation();
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            aria-label={t("task.status")}
            className="inline-grid cursor-help place-items-center rounded-full bg-transparent p-0 text-inherit outline-none focus-visible:ring-1 focus-visible:ring-[var(--color-primary)]"
            type="button"
          >
            <CircleDot aria-hidden="true" size={15} strokeWidth={1.8} />
          </button>
        </TooltipTrigger>
        <TooltipContent
          className="grid max-w-sm items-start gap-[var(--space-2)] p-[var(--space-3)]"
          level={3}
          sideOffset={6}
        >
          {definitions === undefined ? (
            <span className="inline-flex items-center gap-[var(--space-2)]">
              <Spinner size="sm" />
              {t("states.loading")}
            </span>
          ) : (
            definitions.map((definition) => (
              <section className="grid gap-[var(--space-1)]" key={definition.group}>
                <strong>{t(`home.prototype.statusGroups.${definition.group}`)}</strong>
                {definition.statusKinds.map((status) => (
                  <span className="grid grid-cols-[16px_auto] items-center gap-[var(--space-2)]" key={status}>
                    <TaskStatusIcon status={status} />
                    <span>
                      {t(`task.statusKinds.${status}`)} — {t(`home.prototype.statusDescriptions.${status}`)}
                    </span>
                  </span>
                ))}
              </section>
            ))
          )}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

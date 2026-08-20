import { CircleDot } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ProjectTaskGroupDefinition } from "@/api";
import { TaskStatusIcon } from "@/shared/task-status";
import { Spinner, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/ui";

export function ProjectTaskStatusLegend({
  definitions,
  trigger,
}: Readonly<{
  definitions: readonly ProjectTaskGroupDefinition[] | undefined;
  trigger?: ReactNode | undefined;
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
            {trigger ?? <CircleDot aria-hidden="true" size={15} strokeWidth={1.8} />}
          </button>
        </TooltipTrigger>
        <TooltipContent
          className="grid max-w-xs items-start gap-[var(--space-1)] p-[var(--space-2)] text-xs"
          level={3}
          sideOffset={6}
        >
          {definitions === undefined ? (
            <span className="inline-flex items-center gap-[var(--space-2)]">
              <Spinner size="sm" />
              {t("states.loading")}
            </span>
          ) : (
            definitions.flatMap((definition) =>
              definition.statusKinds.map((status) => (
                <span className="grid grid-cols-[16px_auto] items-center gap-[var(--space-2)]" key={status}>
                  <TaskStatusIcon status={status} />
                  <span>{t(`task.statusKinds.${status}`)}</span>
                </span>
              )),
            )
          )}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

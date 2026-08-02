import { MessageSquareIcon } from "lucide-react";
import type { PointerEvent } from "react";
import { useTranslation } from "react-i18next";

import { type TaskSearchGroup, type TaskSearchHit, type TaskSearchLiteralHit } from "@/api";
import { TaskStatusIcon } from "@/shared/task-status";
import { cx, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/ui";

const visibleHitLimit = 3;

export type TaskSearchResultItem = Readonly<{
  key: string;
  group: TaskSearchGroup;
}>;

export function TaskSearchResultRow({
  active,
  id,
  onActivate,
  onPointerMove,
  result,
}: Readonly<{
  active: boolean;
  id: string;
  onActivate(): void;
  onPointerMove(event: PointerEvent<HTMLElement>): void;
  result: TaskSearchResultItem;
}>) {
  const { t } = useTranslation();
  const visibleHits = result.group.hits.slice(0, visibleHitLimit);
  const lastVisibleOrdinal = visibleHits.at(-1)?.ordinal ?? 0;
  const remainingHitCount = Math.max(0, result.group.totalHitCount - lastVisibleOrdinal);
  return (
    <div
      aria-selected={active}
      className={cx(
        "rounded-[var(--radius-l)] border px-[var(--space-3)] py-[var(--space-3)] transition-[background-color,border-color] duration-100 motion-reduce:transition-none",
        active
          ? "border-[color-mix(in_srgb,var(--color-primary)_48%,transparent)] bg-[color-mix(in_srgb,var(--color-primary)_11%,var(--color-island-1))]"
          : "border-transparent bg-transparent",
      )}
      id={id}
      onClick={onActivate}
      onMouseDown={(event) => {
        event.preventDefault();
      }}
      onPointerMove={onPointerMove}
      role="option"
    >
      <header className="grid gap-[var(--space-1)]">
        <span className="flex min-w-0 items-center gap-[var(--space-2)]">
          <TaskSearchStatusIcon
            label={t(`task.statusKinds.${result.group.status.kind}`)}
            status={result.group.status.kind}
          />
          <span className="shrink-0 font-mono text-xs font-medium tracking-wide text-[var(--color-on-island)]">
            {result.group.shortID}
          </span>
        </span>
        <strong className="task-card-title text-left text-base leading-snug font-semibold text-[var(--color-on-island)]">
          {result.group.title}
        </strong>
      </header>
      <div className="mt-[var(--space-2)] grid gap-[var(--space-1)]">
        {visibleHits.map((hit) => (
          <TaskSearchHitPreview hit={hit} key={hit.ordinal} />
        ))}
        {remainingHitCount > 0 ? (
          <span className="text-sm leading-relaxed text-[var(--color-muted)]">
            …{t("taskSearch.moreHits", { count: remainingHitCount })}
          </span>
        ) : null}
      </div>
    </div>
  );
}

function TaskSearchStatusIcon({
  label,
  status,
}: Readonly<{
  label: string;
  status: TaskSearchGroup["status"]["kind"];
}>) {
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span aria-label={label} className="inline-flex shrink-0" role="img">
            <TaskStatusIcon status={status} />
          </span>
        </TooltipTrigger>
        <TooltipContent level={3} sideOffset={6}>
          {label}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

function TaskSearchHitPreview({ hit }: Readonly<{ hit: TaskSearchHit }>) {
  const { t } = useTranslation();
  const comment = hit.source.kind === "comment";
  return (
    <div className="relative flex min-w-0 items-start gap-[var(--space-2)] text-sm leading-relaxed text-[var(--color-muted)]">
      {comment ? (
        <MessageSquareIcon
          aria-label={t("taskSearch.commentHit")}
          className="mt-0.5 shrink-0"
          role="img"
          size={14}
          strokeWidth={1.7}
        />
      ) : null}
      <span className="min-w-0">
        <TaskSearchHitText hit={hit} />
      </span>
    </div>
  );
}

function TaskSearchHitText({ hit }: Readonly<{ hit: TaskSearchHit }>) {
  if (!isLiteralHit(hit)) {
    return hit.fts5.snippet;
  }
  return (
    <>
      {hit.literal.leftTruncated ? "…" : null}
      {hit.literal.before}
      <mark className="bg-transparent font-bold text-[var(--color-on-island)]">{hit.literal.match}</mark>
      {hit.literal.after}
      {hit.literal.rightTruncated ? "…" : null}
    </>
  );
}

function isLiteralHit(hit: TaskSearchHit): hit is TaskSearchLiteralHit {
  return "literal" in hit;
}

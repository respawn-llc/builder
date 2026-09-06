import { ChevronRight } from "lucide-react";
import { useId, useState, type ReactNode } from "react";

import { cx } from "./classes";
import { useOpacityExit } from "./motion";
import "./TranscriptDisclosure.css";

export type TranscriptDisclosureIconTone = "neutral" | "warning" | "error" | "success";
export type TranscriptDisclosureSummaryMode = "single-line" | "multiline";

export type TranscriptDisclosureProps = Readonly<{
  actions?: ReactNode;
  body: ReactNode;
  collapseLabel: string;
  defaultExpanded: boolean;
  expandLabel: string;
  icon: ReactNode;
  iconTone?: TranscriptDisclosureIconTone | undefined;
  liveStatus?: ReactNode;
  summary: ReactNode;
  summaryMode?: TranscriptDisclosureSummaryMode | undefined;
  typeLabel?: ReactNode;
}>;

const iconToneClassNames: Readonly<Record<TranscriptDisclosureIconTone, string>> = {
  neutral: "transcript-disclosure-icon--neutral",
  warning: "transcript-disclosure-icon--warning",
  error: "transcript-disclosure-icon--error",
  success: "transcript-disclosure-icon--success",
};

export function TranscriptDisclosure({
  actions,
  body,
  collapseLabel,
  defaultExpanded,
  expandLabel,
  icon,
  iconTone = "neutral",
  liveStatus,
  summary,
  summaryMode = "single-line",
  typeLabel,
}: TranscriptDisclosureProps) {
  const bodyId = `transcript-disclosure-body-${useId()}`;
  const [expanded, setExpanded] = useState(defaultExpanded);
  const bodyPhase = useOpacityExit(expanded);

  return (
    <div className="transcript-disclosure-shell group/transcript-disclosure relative w-full border border-transparent bg-transparent hover:bg-[var(--color-island-1)] focus-within:bg-[var(--color-island-1)]">
      <TranscriptDisclosureHeader
        actions={actions}
        bodyId={bodyId}
        collapseLabel={collapseLabel}
        expanded={expanded}
        expandLabel={expandLabel}
        icon={icon}
        iconTone={iconTone}
        liveStatus={liveStatus}
        onToggle={() => {
          setExpanded((current) => !current);
        }}
        summary={summary}
        summaryMode={summaryMode}
        typeLabel={typeLabel}
      />
      {bodyPhase === "hidden" ? null : (
        <div
          aria-hidden={bodyPhase === "exiting" ? true : undefined}
          className={cx(
            "transcript-disclosure-body grid overflow-hidden",
            bodyPhase === "visible"
              ? "transcript-disclosure-body--visible"
              : "transcript-disclosure-body--exiting",
          )}
          id={bodyId}
          inert={bodyPhase === "exiting" ? true : undefined}
        >
          <div className="min-h-0 min-w-0 px-[var(--space-2)] pb-[var(--space-2)] text-sm text-[var(--color-on-background)]">
            {body}
          </div>
        </div>
      )}
    </div>
  );
}

function TranscriptDisclosureHeader({
  actions,
  bodyId,
  collapseLabel,
  expanded,
  expandLabel,
  icon,
  iconTone,
  liveStatus,
  onToggle,
  summary,
  summaryMode,
  typeLabel,
}: Readonly<{
  actions?: ReactNode;
  bodyId: string;
  collapseLabel: string;
  expanded: boolean;
  expandLabel: string;
  icon: ReactNode;
  iconTone: TranscriptDisclosureIconTone;
  liveStatus?: ReactNode;
  onToggle: () => void;
  summary: ReactNode;
  summaryMode: TranscriptDisclosureSummaryMode;
  typeLabel?: ReactNode;
}>) {
  return (
    <header
      className={cx(
        "relative grid min-h-9 grid-cols-[auto_auto_minmax(0,1fr)_auto_auto] gap-[var(--space-2)] px-[var(--space-2)] py-[var(--space-1)]",
        summaryMode === "multiline" ? "items-start" : "items-center",
      )}
    >
      <button
        aria-controls={bodyId}
        aria-expanded={expanded}
        aria-label={expanded ? collapseLabel : expandLabel}
        className="absolute inset-0 z-0 rounded-[var(--radius-s)] bg-transparent text-left outline-none focus-visible:ring-[2px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_55%,transparent)] focus-visible:ring-offset-[-1px]"
        onClick={onToggle}
        type="button"
      />
      <span
        aria-hidden="true"
        className={cx(
          "pointer-events-none relative z-0 grid size-5 shrink-0 place-items-center",
          iconToneClassNames[iconTone],
        )}
      >
        {icon}
      </span>
      {typeLabel === undefined ? (
        <span aria-hidden="true" className="pointer-events-none" />
      ) : (
        <span className="pointer-events-none relative z-0 min-w-0 truncate text-xs font-medium text-[var(--color-muted)]">
          {typeLabel}
        </span>
      )}
      <span
        className={cx(
          "pointer-events-none relative z-0 min-w-0 text-left text-sm text-[var(--color-on-background)]",
          summaryMode === "multiline" ? "transcript-disclosure-summary--multiline" : "truncate",
        )}
      >
        {summary}
      </span>
      <div className="pointer-events-none relative z-10 flex min-w-0 items-center justify-end gap-[var(--space-1)]">
        {liveStatus === undefined ? null : (
          <div className="pointer-events-auto flex min-w-0 items-center">{liveStatus}</div>
        )}
        {expanded && actions !== undefined ? (
          <div className="pointer-events-auto flex items-center">{actions}</div>
        ) : null}
      </div>
      <ChevronRight
        aria-hidden="true"
        className={cx(
          "transcript-disclosure-chevron pointer-events-none relative z-0 size-4 shrink-0 text-[var(--color-muted)]",
          expanded ? "rotate-90" : undefined,
        )}
      />
    </header>
  );
}

import { ArrowRight } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cx } from "@/ui";
import { workflowEdgeColor } from "./workflowGraphColors";

export type WorkflowEdgeRouteGraphicProps = Readonly<{
  className?: string | undefined;
  contextMode: string;
  hasError?: boolean | undefined;
  layout?: "balanced" | "compact" | undefined;
  neutralArrow?: boolean | undefined;
  sourceLabel: string;
  targetLabel: string;
}>;

export function WorkflowEdgeRouteGraphic({
  className,
  contextMode,
  hasError = false,
  layout = "balanced",
  neutralArrow = false,
  sourceLabel,
  targetLabel,
}: WorkflowEdgeRouteGraphicProps) {
  const { t } = useTranslation();
  return (
    <div
      aria-label={t("workflowEditor.edgeRouteAriaLabel", { source: sourceLabel, target: targetLabel })}
      className={cx(
        "min-w-0 items-center gap-[var(--space-2)]",
        layout === "balanced" && "grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]",
        layout === "compact" && "flex max-w-full",
        className,
      )}
      data-testid="workflow-edge-route-graphic"
      role="group"
    >
      <WorkflowEdgeRouteGraphicNode
        compact={layout === "compact"}
        label={sourceLabel}
        testID="workflow-edge-route-source"
      />
      <ArrowRight
        aria-hidden="true"
        className="size-5 shrink-0"
        data-testid="workflow-edge-route-arrow"
        size={20}
        strokeWidth={1.8}
        style={{ color: neutralArrow ? "var(--color-muted)" : workflowEdgeColor(contextMode, hasError) }}
      />
      <WorkflowEdgeRouteGraphicNode
        compact={layout === "compact"}
        label={targetLabel}
        testID="workflow-edge-route-target"
      />
    </div>
  );
}

function WorkflowEdgeRouteGraphicNode({
  compact,
  label,
  testID,
}: Readonly<{ compact: boolean; label: string; testID: string }>) {
  return (
    <span
      className={cx(
        "grid min-h-10 min-w-0 place-items-center px-[var(--space-2)] py-[var(--space-2)] text-center text-sm font-semibold text-[var(--color-on-island)]",
        compact && "max-w-[12rem] flex-shrink",
      )}
      data-testid={testID}
    >
      <span className="block max-w-full truncate">{label}</span>
    </span>
  );
}

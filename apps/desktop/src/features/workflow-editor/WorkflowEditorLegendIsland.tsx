import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CircleQuestionMark } from "lucide-react";

import { FloatingNoticeIsland, HelpHint } from "../../ui";
import { cx } from "../../ui/classes";

export function WorkflowEditorLegendIsland({
  positionStrategy,
}: Readonly<{ positionStrategy: "absolute" | "fixed" }>) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(true);
  return (
    <FloatingNoticeIsland
      collapsed={collapsed}
      collapseLabel={t("app.collapse")}
      expandedClassName="floating-notice-expanded grid min-h-[123px] max-h-[min(400px,calc(100vh-var(--space-2)*2))] w-[min(300px,calc(100vw-var(--space-2)*2))] gap-[6px] rounded-[var(--radius-x)] p-[var(--space-3)]"
      expandLabel={t("app.expand")}
      icon={
        <CircleQuestionMark
          aria-hidden="true"
          data-testid="workflow-legend-collapsed-help-icon"
          size={24}
          strokeWidth={1.7}
        />
      }
      level={3}
      onCollapsedChange={setCollapsed}
      positionClassName="left-[var(--space-2)] bottom-[var(--space-2)]"
      positionStrategy={positionStrategy}
      title={t("workflowEditor.legend")}
      tone="neutral"
    >
      {/*
        overflow-hidden clips each row's enlarged HelpHint hit area (a 40px
        pseudo-element overflowing ~16px rows) so it can't register as scrollable
        overflow in the island's auto-scroll content area and force a phantom
        scrollbar. Tooltips render in a portal, so they are unaffected.
      */}
      <div className="grid gap-[6px] overflow-hidden pt-[4px] text-sm leading-none text-[var(--color-on-island)]">
        <LegendRow
          help={t("workflowEditor.legendContinueSessionHelp")}
          label={t("workflowEditor.legendContinueSession")}
        >
          <EdgeLegendSwatch tone="neutral" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendFreshSessionHelp")}
          label={t("workflowEditor.legendFreshSession")}
        >
          <EdgeLegendSwatch tone="primary" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendCompactSessionHelp")}
          label={t("workflowEditor.legendCompactSession")}
        >
          <EdgeLegendSwatch tone="secondary" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendAgentNodeHelp")}
          label={t("workflowEditor.legendAgentNode")}
        >
          <NodeLegendSwatch tone="neutral" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendScriptNodeHelp")}
          label={t("workflowEditor.legendScriptNode")}
        >
          <NodeLegendSwatch shape="script" tone="neutral" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendTerminalStateHelp")}
          label={t("workflowEditor.legendTerminalState")}
        >
          <NodeLegendSwatch tone="success" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendStartingStateHelp")}
          label={t("workflowEditor.legendStartingState")}
        >
          <NodeLegendSwatch tone="primary" />
        </LegendRow>
        <LegendRow
          help={t("workflowEditor.legendMultiAgentJoinHelp")}
          label={t("workflowEditor.legendMultiAgentJoin")}
        >
          <NodeLegendSwatch shape="diamond" tone="secondary" />
        </LegendRow>
      </div>
    </FloatingNoticeIsland>
  );
}

function LegendRow({ children, help, label }: Readonly<{ children: ReactNode; help: string; label: string }>) {
  return (
    <div className="grid grid-cols-[26px_minmax(0,1fr)] items-center gap-[var(--space-2)]">
      <span className="grid h-3 place-items-center">{children}</span>
      <span className="inline-flex min-w-0 items-center gap-[var(--space-1)]">
        <span className="min-w-0 truncate">{label}</span>
        <HelpHint className="shrink-0" label={help} side="right" />
      </span>
    </div>
  );
}

function EdgeLegendSwatch({ tone }: Readonly<{ tone: "neutral" | "primary" | "secondary" }>) {
  return (
    <svg
      aria-hidden="true"
      className={edgeLegendToneClassName(tone)}
      data-testid="workflow-legend-edge-swatch"
      fill="none"
      height="6"
      viewBox="0 0 22 6"
      width="22"
    >
      <path
        d="M1 3H19"
        data-testid="workflow-legend-edge-line"
        stroke="currentColor"
        strokeLinecap="round"
        strokeWidth="1.25"
      />
      <path
        d="M17 1L20 3L17 5"
        data-testid="workflow-legend-edge-head"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.25"
      />
    </svg>
  );
}

function NodeLegendSwatch({
  shape = "box",
  tone,
}: Readonly<{ shape?: "box" | "diamond" | "script"; tone: "neutral" | "primary" | "secondary" | "success" }>) {
  const shapeClassName = nodeLegendShapeClassName(shape);
  return (
    <span
      aria-hidden="true"
      className={cx(
        "relative block border bg-[var(--color-island-1)]",
        shapeClassName,
        nodeLegendToneClassName(tone),
      )}
      data-testid="workflow-legend-node-swatch"
    >
      {shape === "script" ? (
        <>
          <span className="absolute top-[1.14px] bottom-[1.14px] left-[1.71px] w-[2px] rounded-[var(--radius-s)] bg-[var(--color-primary)]" />
          <span className="absolute top-[1.14px] right-[1.71px] bottom-[1.14px] w-[2px] rounded-[var(--radius-s)] bg-[var(--color-primary)]" />
        </>
      ) : null}
    </span>
  );
}

function nodeLegendShapeClassName(shape: "box" | "diamond" | "script"): string {
  if (shape === "diamond") {
    return "h-[10px] w-[10px] rotate-45 rounded-[2px]";
  }
  return "h-[9px] w-[14px] rounded-[2px]";
}

function edgeLegendToneClassName(tone: "neutral" | "primary" | "secondary"): string {
  if (tone === "primary") {
    return "text-[var(--color-primary)]";
  }
  if (tone === "secondary") {
    return "text-[var(--color-secondary)]";
  }
  return "text-[var(--color-muted)]";
}

function nodeLegendToneClassName(tone: "neutral" | "primary" | "secondary" | "success"): string {
  if (tone === "primary") {
    return "border-[var(--color-primary)] text-[var(--color-primary)]";
  }
  if (tone === "secondary") {
    return "border-[var(--color-secondary)] text-[var(--color-secondary)]";
  }
  if (tone === "success") {
    return "border-[var(--color-success)] text-[var(--color-success)]";
  }
  return "border-[var(--color-outline)] text-[var(--color-outline)]";
}

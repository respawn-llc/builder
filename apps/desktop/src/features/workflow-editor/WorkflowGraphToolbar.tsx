import { useReactFlow } from "@xyflow/react";
import { Fullscreen, Plus, ScanSearch, Settings, ZoomIn, ZoomOut } from "lucide-react";
import { type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import {
  IslandSurface,
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../../ui";
import { cx } from "../../ui/classes";
import { WorkflowNodeKindPicker } from "./WorkflowNodeKindPicker";
import type { CreatableWorkflowNodeKind } from "./workflowEditorGraphMutationTypes";

export function WorkflowGraphToolbar({
  onAddNode,
  onWorkflowInspect,
  positionStrategy = "fixed",
}: Readonly<{
  onAddNode: ((kind: CreatableWorkflowNodeKind) => void) | undefined;
  onWorkflowInspect: () => void;
  positionStrategy?: "absolute" | "fixed" | undefined;
}>) {
  const { t } = useTranslation();
  const instance = useReactFlow();
  const toolbar = (
    <IslandSurface
      as="div"
      className={cx(
        "workflow-editor-tools app-region-no-drag left-[var(--space-2)] z-30 grid gap-[var(--space-1)] rounded-[var(--radius-l)] p-[var(--space-1)]",
        positionStrategy === "fixed"
          ? "fixed top-[calc(var(--native-titlebar-height)+var(--space-2))]"
          : "absolute top-[var(--space-2)]",
      )}
      data-testid="workflow-editor-tools"
      level={3}
    >
      <AddNodeTool disabled={onAddNode === undefined} onAddNode={onAddNode} />
      <CanvasTool
        label={t("workflowEditor.inspectWorkflow")}
        onClick={onWorkflowInspect}
        tooltip={t("workflowEditor.editWorkflowTooltip")}
      >
        <Settings aria-hidden="true" size={18} strokeWidth={1.7} />
      </CanvasTool>
      <CanvasTool
        label={t("workflowEditor.resetZoom")}
        onClick={() => void instance.setViewport({ x: 0, y: 0, zoom: 1 })}
        // setViewport zoom 1 is the graph's actual-size / 100% zoom action.
        tooltip={t("workflowEditor.zoomActualSizeTooltip")}
      >
        <ScanSearch aria-hidden="true" size={18} strokeWidth={1.7} />
      </CanvasTool>
      <CanvasTool
        label={t("workflowEditor.zoomIn")}
        onClick={() => void instance.zoomIn()}
        tooltip={t("workflowEditor.zoomInTooltip")}
      >
        <ZoomIn aria-hidden="true" size={18} strokeWidth={1.7} />
      </CanvasTool>
      <CanvasTool
        label={t("workflowEditor.zoomOut")}
        onClick={() => void instance.zoomOut()}
        tooltip={t("workflowEditor.zoomOutTooltip")}
      >
        <ZoomOut aria-hidden="true" size={18} strokeWidth={1.7} />
      </CanvasTool>
      <CanvasTool
        label={t("workflowEditor.fitView")}
        onClick={() => void instance.fitView({ padding: 0.18 })}
        // Fit view resets the canvas framing to the workflow contents, not the window fullscreen state.
        tooltip={t("workflowEditor.fitView")}
      >
        <Fullscreen aria-hidden="true" size={18} strokeWidth={1.7} />
      </CanvasTool>
    </IslandSurface>
  );
  return positionStrategy === "fixed" ? createPortal(toolbar, document.body) : toolbar;
}

function AddNodeTool({
  disabled,
  onAddNode,
}: Readonly<{ disabled: boolean; onAddNode: ((kind: CreatableWorkflowNodeKind) => void) | undefined }>) {
  const { t } = useTranslation();
  return (
    <WorkflowNodeKindPicker
      disabled={disabled}
      onSelect={(kind) => {
        onAddNode?.(kind);
      }}
      trigger={
        <button
          aria-label={t("workflowEditor.addNode")}
          className="grid size-9 place-items-center rounded-[var(--radius-m)] border border-transparent bg-transparent text-[var(--color-on-island)] transition-colors hover:bg-[var(--color-island-1)] focus-visible:border-[var(--color-primary)] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50"
          disabled={disabled}
          title={t("workflowEditor.addNode")}
          type="button"
        >
          <Plus aria-hidden="true" size={18} strokeWidth={1.7} />
        </button>
      }
      triggerPolicy="toolbar"
    />
  );
}

function CanvasTool({
  children,
  label,
  onClick,
  tooltip,
}: Readonly<{ children: ReactNode; label: string; onClick: () => void; tooltip: string }>) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          aria-label={label}
          className="grid size-9 place-items-center rounded-[var(--radius-m)] border border-transparent bg-transparent text-[var(--color-on-island)] transition-colors hover:bg-[var(--color-island-1)] focus-visible:border-[var(--color-primary)] focus-visible:outline-none"
          onClick={onClick}
          type="button"
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent level={3} showArrow={false} side="right" sideOffset={6}>
        {tooltip}
      </TooltipContent>
    </Tooltip>
  );
}

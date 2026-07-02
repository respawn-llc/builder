import { useReactFlow } from "@xyflow/react";
import { Fullscreen, Plus, ScanSearch, Settings, ZoomIn, ZoomOut } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type FocusEvent, type KeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import {
  IslandSurface,
  Popover,
  PopoverContent,
  PopoverTrigger,
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../../ui";
import { cx } from "../../ui/classes";

type AddNodeMenuOpenOrigin = "focus" | "pointer";

interface CancelableEvent {
  preventDefault: () => void;
}

interface StoppableEvent extends CancelableEvent {
  stopPropagation: () => void;
}

export function WorkflowGraphToolbar({
  onAddNode,
  onWorkflowInspect,
  positionStrategy = "fixed",
}: Readonly<{
  onAddNode: ((kind: "agent" | "script" | "terminal") => void) | undefined;
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
}: Readonly<{ disabled: boolean; onAddNode: ((kind: "agent" | "script" | "terminal") => void) | undefined }>) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pointerInsideTriggerRef = useRef(false);
  const pointerInsideContentRef = useRef(false);
  const focusInsideMenuRef = useRef(false);
  const openOriginRef = useRef<AddNodeMenuOpenOrigin>("pointer");
  const suppressReturnedTriggerFocusRef = useRef(false);
  const cancelClose = useCallback(() => {
    if (closeTimerRef.current === null) {
      return;
    }
    clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);
  const closeMenu = useCallback(() => {
    cancelClose();
    pointerInsideTriggerRef.current = false;
    pointerInsideContentRef.current = false;
    focusInsideMenuRef.current = false;
    setOpen(false);
  }, [cancelClose]);
  const openMenu = useCallback((origin: AddNodeMenuOpenOrigin) => {
    if (disabled) {
      return;
    }
    openOriginRef.current = origin;
    cancelClose();
    setOpen(true);
  }, [cancelClose, disabled]);
  const scheduleClose = useCallback(() => {
    cancelClose();
    closeTimerRef.current = setTimeout(() => {
      closeTimerRef.current = null;
      if (pointerInsideTriggerRef.current || pointerInsideContentRef.current || focusInsideMenuRef.current) {
        return;
      }
      closeMenu();
    }, 120);
  }, [cancelClose, closeMenu]);
  useEffect(() => cancelClose, [cancelClose]);
  const handleClickOnlyInteraction = useCallback((event: StoppableEvent) => {
    event.preventDefault();
    event.stopPropagation();
  }, []);
  const handleTriggerClick = useCallback(
    (event: StoppableEvent) => {
      event.preventDefault();
      event.stopPropagation();
      focusInsideMenuRef.current = true;
      openMenu("focus");
    },
    [openMenu],
  );
  const preventDefault = useCallback((event: CancelableEvent) => {
    event.preventDefault();
  }, []);
  const handleOpenAutoFocus = useCallback(
    (event: CancelableEvent) => {
      if (openOriginRef.current === "pointer") {
        event.preventDefault();
      }
    },
    [],
  );
  const handleCloseAutoFocus = useCallback(
    (event: CancelableEvent) => {
      if (openOriginRef.current === "pointer") {
        event.preventDefault();
        return;
      }
      suppressReturnedTriggerFocusRef.current = true;
    },
    [],
  );
  const handleTriggerPointerEnter = useCallback(() => {
    pointerInsideTriggerRef.current = true;
    openMenu("pointer");
  }, [openMenu]);
  const handleTriggerPointerLeave = useCallback(() => {
    pointerInsideTriggerRef.current = false;
    scheduleClose();
  }, [scheduleClose]);
  const handleContentPointerEnter = useCallback(() => {
    pointerInsideContentRef.current = true;
    cancelClose();
  }, [cancelClose]);
  const handleContentPointerLeave = useCallback(() => {
    pointerInsideContentRef.current = false;
    scheduleClose();
  }, [scheduleClose]);
  const handleTriggerFocus = useCallback(() => {
    if (suppressReturnedTriggerFocusRef.current) {
      suppressReturnedTriggerFocusRef.current = false;
      return;
    }
    focusInsideMenuRef.current = true;
    openMenu("focus");
  }, [openMenu]);
  const handleContentFocus = useCallback(() => {
    focusInsideMenuRef.current = true;
    cancelClose();
  }, [cancelClose]);
  const handleTriggerBlur = useCallback(() => {
    suppressReturnedTriggerFocusRef.current = false;
    focusInsideMenuRef.current = false;
    scheduleClose();
  }, [scheduleClose]);
  const closeUnlessFocusStaysInside = useCallback(
    (event: FocusEvent<HTMLElement>) => {
      if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) {
        return;
      }
      focusInsideMenuRef.current = false;
      scheduleClose();
    },
    [scheduleClose],
  );
  const handleTriggerKeyDown = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>) => {
      if (event.key !== "Enter" && event.key !== " ") {
        return;
      }
      preventDefault(event);
    },
    [preventDefault],
  );
  return (
    <Popover
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          closeMenu();
        }
      }}
      open={open}
    >
      <PopoverTrigger asChild>
        <button
          aria-label={t("workflowEditor.addNode")}
          className="grid size-9 place-items-center rounded-[var(--radius-m)] border border-transparent bg-transparent text-[var(--color-on-island)] transition-colors hover:bg-[var(--color-island-1)] focus-visible:border-[var(--color-primary)] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50"
          disabled={disabled}
          onClick={handleTriggerClick}
          onBlur={handleTriggerBlur}
          onFocus={handleTriggerFocus}
          onKeyDown={handleTriggerKeyDown}
          onPointerDown={handleClickOnlyInteraction}
          onPointerEnter={handleTriggerPointerEnter}
          onPointerLeave={handleTriggerPointerLeave}
          title={t("workflowEditor.addNode")}
          type="button"
        >
          <Plus aria-hidden="true" size={18} strokeWidth={1.7} />
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[220px] gap-[var(--space-1)] p-[var(--space-2)]"
        level={3}
        onCloseAutoFocus={handleCloseAutoFocus}
        onOpenAutoFocus={handleOpenAutoFocus}
        onBlur={closeUnlessFocusStaysInside}
        onFocus={handleContentFocus}
        onPointerEnter={handleContentPointerEnter}
        onPointerLeave={handleContentPointerLeave}
        side="right"
      >
        <CanvasMenuButton
          label={t("workflowEditor.addAgentNode")}
          onClick={() => {
            closeMenu();
            onAddNode?.("agent");
          }}
        />
        <CanvasMenuButton
          label={t("workflowEditor.addScriptNode")}
          onClick={() => {
            closeMenu();
            onAddNode?.("script");
          }}
        />
        <CanvasMenuButton
          label={t("workflowEditor.addTerminalNode")}
          onClick={() => {
            closeMenu();
            onAddNode?.("terminal");
          }}
        />
      </PopoverContent>
    </Popover>
  );
}

function CanvasMenuButton({ label, onClick }: Readonly<{ label: string; onClick: () => void }>) {
  return (
    <button
      className="rounded-[var(--radius-m)] border border-transparent bg-transparent px-[var(--space-3)] py-[var(--space-2)] text-left text-sm font-semibold text-[var(--color-on-island)] outline-none transition-colors hover:bg-[var(--color-island-2)] focus-visible:border-[var(--color-primary)]"
      onClick={onClick}
      type="button"
    >
      {label}
    </button>
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

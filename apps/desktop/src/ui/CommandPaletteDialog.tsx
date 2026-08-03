import { useEffect, useRef, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";

import { Dialog } from "./Dialog";
import { useOpacityExit } from "./motion";

export type CommandPaletteDialogProps = Readonly<{
  title: string;
  closeLabel: string;
  open: boolean;
  input: ReactNode;
  inputFocusRef: RefObject<HTMLElement | null>;
  children: ReactNode;
  expanded: boolean;
  height: number;
  onClose: () => void;
  onExitComplete?: (() => void) | undefined;
}>;

export function CommandPaletteDialog({
  title,
  closeLabel,
  open,
  input,
  inputFocusRef,
  children,
  expanded,
  height,
  onClose,
  onExitComplete,
}: CommandPaletteDialogProps) {
  const phase = useOpacityExit(open, {
    durationVarName: "--motion-fast",
    fallbackDurationMs: 140,
  });
  const wasPresentedRef = useRef(false);
  useEffect(() => {
    if (phase !== "hidden") {
      wasPresentedRef.current = true;
      return;
    }
    if (wasPresentedRef.current) {
      wasPresentedRef.current = false;
      onExitComplete?.();
    }
  }, [onExitComplete, phase]);
  const dialog = (
    <Dialog
      backdrop="blur"
      className="[&>div:last-child]:overflow-hidden [&>div:last-child]:pr-0"
      chrome="floating-close"
      closeLabel={closeLabel}
      initialFocusRef={inputFocusRef}
      layout="content"
      motionPhase={phase}
      onClose={onClose}
      open={phase !== "hidden"}
      placement="command-palette"
      surfacePadding="none"
      title={title}
      width={720}
    >
      <div
        className="command-palette-size-motion grid min-h-0 grid-rows-[auto_minmax(0,1fr)]"
        style={{ height: `min(${height.toString()}px, calc(100vh - 48px))` }}
      >
        <div className={expanded ? "border-b border-[var(--color-outline)]" : undefined}>{input}</div>
        {expanded ? <div className="min-h-0">{children}</div> : null}
      </div>
    </Dialog>
  );
  return typeof document === "undefined" ? dialog : createPortal(dialog, document.body);
}

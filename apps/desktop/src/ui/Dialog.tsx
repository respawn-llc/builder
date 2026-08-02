import { useEffect, useId, useRef, type CSSProperties, type ReactNode, type RefObject } from "react";
import { X } from "lucide-react";

import { cx } from "./classes";
import { chromeContentPaddingClassName } from "./chromePadding";
import { islandSurfaceClassName } from "./islandSurfaceStyles";
import type { OpacityExitPhase } from "./motion";

export type DialogProps = Readonly<{
  title: string;
  closeLabel: string;
  open: boolean;
  children: ReactNode;
  backdrop?: "dimmed" | "blur";
  className?: string;
  chrome?: "header" | "floating-close";
  layout?: "standard" | "content";
  placement?: "center" | "command-palette";
  contentPadding?: "none" | "chrome";
  surfacePadding?: "chrome" | "none";
  surface?: "island" | "transparent";
  style?: CSSProperties;
  width?: number;
  closeDisabled?: boolean;
  initialFocusRef?: RefObject<HTMLElement | null>;
  motionPhase?: OpacityExitPhase;
  onClose: () => void;
}>;

export const compactDialogWidth = 420;

export function Dialog({
  title,
  closeLabel,
  open,
  children,
  backdrop = "dimmed",
  className,
  chrome = "header",
  layout = "standard",
  placement = "center",
  contentPadding = "none",
  surface = "island",
  surfacePadding = "chrome",
  style,
  width,
  closeDisabled = false,
  initialFocusRef,
  motionPhase,
  onClose,
}: DialogProps) {
  const titleId = useId();
  const dialogRef = useRef<HTMLElement | null>(null);
  const activeClose = dialogCloseHandler(closeDisabled, onClose);
  useModalDialogKeyboard(open, dialogRef, initialFocusRef, activeClose);

  if (!open) {
    return null;
  }

  return (
    <div
      className={cx(
        "app-region-no-drag fixed inset-0 z-50 grid p-[var(--space-4)]",
        dialogPlacementClassName(placement),
      )}
      role="presentation"
    >
      <div
        className={dialogBackdropClassName(backdrop, motionPhase)}
        onClick={activeClose}
        role="presentation"
      />
      <section
        aria-labelledby={titleId}
        aria-modal="true"
        className={cx(
          "relative grid max-h-[calc(100vh-48px)] w-[min(720px,calc(100vw-32px))] overflow-hidden",
          dialogLayoutClassName(layout),
          dialogSurfaceClassName(surface, surfacePadding),
          dialogSurfaceMotionClassName(motionPhase),
          className,
        )}
        role="dialog"
        ref={dialogRef}
        style={dialogStyle(style, width)}
        tabIndex={-1}
      >
        <DialogChrome
          chrome={chrome}
          closeDisabled={closeDisabled}
          closeLabel={closeLabel}
          onClose={onClose}
          title={title}
          titleID={titleId}
        />
        <div
          className={cx(
            "min-h-0 overflow-auto hide-scrollbar",
            contentPadding === "none" && "pr-[var(--space-1)]",
            contentPadding === "chrome" && chromeContentPaddingClassName,
          )}
        >
          {children}
        </div>
      </section>
    </div>
  );
}

function dialogBackdropClassName(
  backdrop: NonNullable<DialogProps["backdrop"]>,
  phase: OpacityExitPhase | undefined,
): string {
  return cx(
    "absolute inset-0 backdrop-blur-[6px]",
    backdrop === "dimmed" && "bg-black/35",
    dialogBackdropMotionClassName(phase),
  );
}

function dialogPlacementClassName(placement: NonNullable<DialogProps["placement"]>): string {
  return placement === "center" ? "place-items-center" : "dialog-placement-command-palette";
}

function dialogBackdropMotionClassName(phase: OpacityExitPhase | undefined): string | undefined {
  if (phase === "visible") {
    return "dialog-backdrop-motion-enter";
  }
  if (phase === "exiting") {
    return "dialog-backdrop-motion-exit";
  }
  return undefined;
}

function dialogSurfaceMotionClassName(phase: OpacityExitPhase | undefined): string | undefined {
  if (phase === "visible") {
    return "dialog-surface-motion-enter";
  }
  if (phase === "exiting") {
    return "dialog-surface-motion-exit";
  }
  return undefined;
}

function dialogLayoutClassName(layout: NonNullable<DialogProps["layout"]>): string {
  return layout === "standard"
    ? "grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-4)]"
    : "grid-rows-[minmax(0,1fr)] gap-0";
}

function dialogSurfaceClassName(
  surface: NonNullable<DialogProps["surface"]>,
  padding: NonNullable<DialogProps["surfacePadding"]>,
): string {
  if (surface === "transparent") {
    return "bg-transparent p-0 shadow-none";
  }
  return cx(
    islandSurfaceClassName(0),
    "rounded-[var(--radius-xl)]",
    padding === "chrome" ? "p-[var(--space-4)]" : "p-0",
  );
}

function DialogChrome({
  chrome,
  closeDisabled,
  closeLabel,
  onClose,
  title,
  titleID,
}: Readonly<{
  chrome: NonNullable<DialogProps["chrome"]>;
  closeDisabled: boolean;
  closeLabel: string;
  onClose(): void;
  title: string;
  titleID: string;
}>) {
  if (chrome === "header") {
    return (
      <header className="flex items-center justify-between gap-[var(--space-4)]">
        <h2 className="m-0 text-[1.15rem] font-bold" id={titleID}>
          {title}
        </h2>
        <DialogCloseButton closeLabel={closeLabel} disabled={closeDisabled} onClose={onClose} />
      </header>
    );
  }
  return (
    <>
      <h2 className="sr-only" id={titleID}>
        {title}
      </h2>
      <DialogCloseButton
        className="absolute top-[var(--space-3)] right-[var(--space-3)] z-10 bg-[var(--color-island-1)]"
        closeLabel={closeLabel}
        disabled={closeDisabled}
        onClose={onClose}
      />
    </>
  );
}

function dialogStyle(style: CSSProperties | undefined, width: number | undefined): CSSProperties | undefined {
  if (width === undefined) {
    return style;
  }
  return { ...style, width: responsiveDialogWidth(width) };
}

function responsiveDialogWidth(width: number): string {
  if (!Number.isFinite(width) || width <= 0) {
    throw new Error(`Dialog width must be a positive finite number; received ${String(width)}.`);
  }
  return `min(${width.toString()}px, calc(100vw - 32px))`;
}

function useModalDialogKeyboard(
  open: boolean,
  dialogRef: RefObject<HTMLElement | null>,
  initialFocusRef: RefObject<HTMLElement | null> | undefined,
  onClose: () => void,
): void {
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!open) {
      return undefined;
    }
    const dialog = dialogRef.current;
    if (dialog === null) {
      return undefined;
    }
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const requestedInitialFocus = initialFocusRef?.current;
    const initialFocus =
      requestedInitialFocus !== null &&
      requestedInitialFocus !== undefined &&
      dialog.contains(requestedInitialFocus)
        ? requestedInitialFocus
        : (focusableDialogElements(dialog)[0] ?? dialog);
    initialFocus.focus();

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key === "Tab") {
        trapTabFocus(event, dialog);
      }
    };

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      if (previousFocus?.isConnected === true) {
        previousFocus.focus();
      }
    };
  }, [dialogRef, initialFocusRef, onCloseRef, open]);
}

function trapTabFocus(event: KeyboardEvent, dialog: HTMLElement): void {
  const focusableElements = focusableDialogElements(dialog);
  if (focusableElements.length === 0) {
    event.preventDefault();
    dialog.focus();
    return;
  }
  const first = focusableElements[0];
  const last = focusableElements.at(-1);
  if (first === undefined || last === undefined) {
    return;
  }
  const activeElement = document.activeElement;
  if (event.shiftKey && (activeElement === first || !dialog.contains(activeElement))) {
    event.preventDefault();
    last.focus();
    return;
  }
  if (!event.shiftKey && (activeElement === last || !dialog.contains(activeElement))) {
    event.preventDefault();
    first.focus();
  }
}

function focusableDialogElements(dialog: HTMLElement): readonly HTMLElement[] {
  return Array.from(
    dialog.querySelectorAll<HTMLElement>(
      [
        "a[href]",
        "button:not([disabled])",
        "input:not([disabled])",
        "select:not([disabled])",
        "textarea:not([disabled])",
        "[tabindex]:not([tabindex='-1'])",
      ].join(","),
    ),
  ).filter((element) => element.getAttribute("aria-hidden") !== "true" && element.tabIndex >= 0);
}

function DialogCloseButton({
  className,
  closeLabel,
  disabled,
  onClose,
}: Readonly<{
  className?: string | undefined;
  closeLabel: string;
  disabled: boolean;
  onClose: () => void;
}>) {
  return (
    <button
      aria-label={closeLabel}
      className={cx(
        "grid h-9 w-9 place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)]",
        className,
      )}
      disabled={disabled}
      onClick={onClose}
      type="button"
    >
      <X aria-hidden="true" size={18} strokeWidth={1.5} />
    </button>
  );
}

function ignoreDialogClose(): void {
  return;
}

function dialogCloseHandler(disabled: boolean, onClose: () => void): () => void {
  return disabled ? ignoreDialogClose : onClose;
}

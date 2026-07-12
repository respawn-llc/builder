import { useEffect, useId, useRef, type CSSProperties, type ReactNode, type RefObject } from "react";
import { X } from "lucide-react";

import { cx } from "./classes";
import { chromeContentPaddingClassName } from "./chromePadding";
import { islandSurfaceClassName } from "./islandSurfaceStyles";

export type DialogProps = Readonly<{
  title: string;
  closeLabel: string;
  open: boolean;
  children: ReactNode;
  className?: string;
  chrome?: "header" | "floating-close";
  contentPadding?: "none" | "chrome";
  surface?: "island" | "transparent";
  style?: CSSProperties;
  closeDisabled?: boolean;
  onClose: () => void;
}>;

export function Dialog({
  title,
  closeLabel,
  open,
  children,
  className,
  chrome = "header",
  contentPadding = "none",
  surface = "island",
  style,
  closeDisabled = false,
  onClose,
}: DialogProps) {
  const titleId = useId();
  const dialogRef = useRef<HTMLElement | null>(null);
  const activeClose = dialogCloseHandler(closeDisabled, onClose);
  useModalDialogKeyboard(open, dialogRef, activeClose);

  if (!open) {
    return null;
  }

  return (
    <div
      className="app-region-no-drag fixed inset-0 z-50 grid place-items-center p-[var(--space-4)]"
      role="presentation"
    >
      <div
        className="absolute inset-0 bg-black/35 backdrop-blur-[6px]"
        onClick={activeClose}
        role="presentation"
      />
      <section
        aria-labelledby={titleId}
        aria-modal="true"
        className={cx(
          "relative grid max-h-[calc(100vh-48px)] w-[min(720px,calc(100vw-32px))] grid-rows-[auto_minmax(0,1fr)] gap-[var(--space-4)] overflow-hidden",
          surface === "island" && cx(islandSurfaceClassName(0), "rounded-[var(--radius-xl)] p-[var(--space-4)]"),
          surface === "transparent" && "bg-transparent p-0 shadow-none",
          className,
        )}
        role="dialog"
        ref={dialogRef}
        style={style}
        tabIndex={-1}
      >
        {chrome === "header" ? (
          <header className="flex items-center justify-between gap-[var(--space-4)]">
            <h2 className="m-0 text-[1.15rem] font-bold" id={titleId}>
              {title}
            </h2>
            <DialogCloseButton closeLabel={closeLabel} disabled={closeDisabled} onClose={onClose} />
          </header>
        ) : (
          <>
            <h2 className="sr-only" id={titleId}>
              {title}
            </h2>
            <DialogCloseButton
              className="absolute top-[var(--space-3)] right-[var(--space-3)] z-10 bg-[var(--color-island-1)]"
              closeLabel={closeLabel}
              disabled={closeDisabled}
              onClose={onClose}
            />
          </>
        )}
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

function useModalDialogKeyboard(
  open: boolean,
  dialogRef: RefObject<HTMLElement | null>,
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
    const initialFocus = focusableDialogElements(dialog)[0] ?? dialog;
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
  }, [dialogRef, onCloseRef, open]);
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

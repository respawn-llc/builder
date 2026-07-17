import { useLayoutEffect, useRef, type ReactNode, type RefObject } from "react";

import { useAppServices } from "@/app-facade";
import {
  chromeContentPaddingClassName,
  cx,
  islandSurfaceClassName,
  nativeChromeContentPaddingClassName,
} from "@/ui";

export type NativeDialogWindowProps = Readonly<{
  title: string;
  children: ReactNode;
  fitToContent?: boolean;
  contentMaxWidth?: string;
  contentPadding?: "none" | "chrome";
  showHeader?: boolean;
  surface?: "island" | "transparent";
}>;

export function NativeDialogWindow({
  title,
  children,
  fitToContent = true,
  contentMaxWidth = "560px",
  contentPadding = "none",
  showHeader = true,
  surface = "island",
}: NativeDialogWindowProps) {
  const shellRef = useRef<HTMLElement | null>(null);
  useFitNativeDialogWindow(shellRef, fitToContent);

  return (
    <main className={nativeDialogMainClassName(contentPadding, fitToContent)} ref={shellRef}>
      <div
        className="app-region-drag fixed inset-x-0 top-0 h-[var(--native-titlebar-height)]"
        data-tauri-drag-region
      />
      <section
        aria-label={title}
        aria-modal="true"
        className={nativeDialogSectionClassName({ fitToContent, showHeader, surface })}
        role="dialog"
      >
        <div
          className={nativeDialogContentClassName(showHeader)}
          data-testid="native-dialog-content"
          style={{ maxWidth: contentMaxWidth }}
        >
          {showHeader ? (
            <header className="min-w-0">
              <h1 className="m-0 text-[1.15rem] font-bold">{title}</h1>
            </header>
          ) : null}
          <div
            className={nativeDialogScrollportClassName(contentPadding)}
            data-testid="native-dialog-scrollport"
          >
            {children}
          </div>
        </div>
      </section>
    </main>
  );
}

function useFitNativeDialogWindow(shellRef: RefObject<HTMLElement | null>, fitToContent: boolean): void {
  const { logger, nativeBridge } = useAppServices();
  useLayoutEffect(() => {
    if (!fitToContent) {
      return undefined;
    }
    const shell = shellRef.current;
    if (shell === null) {
      return undefined;
    }
    let lastSize = "";
    let frame = 0;
    const fit = () => {
      const rect = shell.getBoundingClientRect();
      const width = Math.ceil(rect.width);
      const height = Math.ceil(rect.height);
      const key = `${width.toString()}x${height.toString()}`;
      if (key === lastSize || width <= 0 || height <= 0) {
        return;
      }
      lastSize = key;
      void nativeBridge.window.fitCurrentToContent({ height, width }).catch((error: unknown) => {
        void logger.append("warn", "Fit native dialog window failed.", {
          error: error instanceof Error ? error.message : "unknown",
        });
      });
    };
    const scheduleFit = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(fit);
    };
    scheduleFit();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(scheduleFit);
    observer?.observe(shell);
    return () => {
      cancelAnimationFrame(frame);
      observer?.disconnect();
    };
  }, [fitToContent, logger, nativeBridge.window, shellRef]);
}

function nativeDialogMainClassName(
  contentPadding: NativeDialogWindowProps["contentPadding"],
  fitToContent: boolean,
): string {
  return cx(
    "window-glass-fill grid",
    contentPadding === "chrome" ? "pt-[var(--native-titlebar-height)]" : nativeChromeContentPaddingClassName,
    fitToContent ? "w-max" : "h-screen w-screen",
  );
}

function nativeDialogSectionClassName({
  fitToContent,
  showHeader,
  surface,
}: Required<Pick<NativeDialogWindowProps, "fitToContent" | "showHeader" | "surface">>): string {
  return cx(
    "app-region-no-drag grid min-h-0 gap-[var(--space-3)]",
    surface === "island" && cx(islandSurfaceClassName(0), "rounded-[var(--radius-xl)] p-[var(--space-4)]"),
    surface === "transparent" && "bg-transparent p-0 shadow-none",
    showHeader ? "grid-rows-[auto_minmax(0,1fr)]" : "grid-rows-[minmax(0,1fr)]",
    fitToContent ? "w-max" : "h-full",
  );
}

function nativeDialogContentClassName(showHeader: boolean): string {
  return cx(
    "mx-auto grid h-full min-h-0 w-full gap-[var(--space-3)]",
    showHeader ? "grid-rows-[auto_minmax(0,1fr)]" : "grid-rows-[minmax(0,1fr)]",
  );
}

function nativeDialogScrollportClassName(contentPadding: NativeDialogWindowProps["contentPadding"]): string {
  return cx(
    "min-h-0 overflow-auto hide-scrollbar",
    contentPadding === "chrome" && chromeContentPaddingClassName,
  );
}

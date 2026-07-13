import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
} from "react";

import { cx } from "./classes";
import { lineCountForAssignedHeight } from "./lineClampGeometry";

export type AdaptiveLineClampProps = Readonly<{
  children: ReactNode;
  className?: string | undefined;
}> &
  Omit<HTMLAttributes<HTMLSpanElement>, "children" | "className">;

export function AdaptiveLineClamp({
  children,
  className,
  ...props
}: AdaptiveLineClampProps) {
  const viewportRef = useRef<HTMLSpanElement | null>(null);
  const lineProbeRef = useRef<HTMLSpanElement | null>(null);
  const [lineCount, setLineCount] = useState<number | null>(null);
  const measure = useCallback(() => {
    const viewport = viewportRef.current;
    const lineProbe = lineProbeRef.current;
    if (viewport === null || lineProbe === null) {
      return;
    }
    const nextLineCount = lineCountForAssignedHeight({
      assignedHeight: viewport.getBoundingClientRect().height,
      lineHeight: lineProbe.getBoundingClientRect().height,
    });
    setLineCount((currentLineCount) =>
      currentLineCount === nextLineCount ? currentLineCount : nextLineCount,
    );
  }, []);

  useLayoutEffect(() => {
    measure();
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    const viewport = viewportRef.current;
    const lineProbe = lineProbeRef.current;
    if (viewport !== null) {
      observer.observe(viewport);
    }
    if (lineProbe !== null) {
      observer.observe(lineProbe);
    }
    return () => {
      observer.disconnect();
    };
  }, [measure]);

  return (
    <span
      {...props}
      className={cx("relative block min-h-0 overflow-hidden", className)}
      ref={viewportRef}
    >
      <span style={lineClampStyle(lineCount)}>{children}</span>
      <span
        aria-hidden="true"
        className="pointer-events-none invisible absolute top-0 left-0 block whitespace-nowrap"
        ref={lineProbeRef}
      >
        M
      </span>
    </span>
  );
}

function lineClampStyle(lineCount: number | null): CSSProperties | undefined {
  if (lineCount === null) {
    return undefined;
  }
  return {
    display: "-webkit-box",
    overflow: "hidden",
    WebkitBoxOrient: "vertical",
    WebkitLineClamp: lineCount,
  };
}

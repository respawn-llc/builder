import { useDeferredValue, useLayoutEffect, useRef, useState } from "react";

import { cx } from "@/ui";

export function AnimatedBoardChipSummary({ text }: Readonly<{ text: string }>) {
  const measurementRef = useRef<HTMLSpanElement | null>(null);
  const [width, setWidth] = useState<number | null>(null);
  const deferredText = useDeferredValue(text);
  const outgoingText = deferredText === text ? null : deferredText;
  useLayoutEffect(() => {
    const measurement = measurementRef.current;
    if (measurement === null) {
      return;
    }
    const nextWidth = Math.ceil(measurement.getBoundingClientRect().width);
    setWidth((current) => (current === nextWidth ? current : nextWidth));
  }, [text]);
  return (
    <span
      className="board-label-filter-summary relative inline-block overflow-hidden align-middle"
      style={width === null ? undefined : { width }}
    >
      <span
        aria-hidden="true"
        className="pointer-events-none invisible absolute top-0 left-0 inline-block w-max whitespace-nowrap"
        ref={measurementRef}
      >
        {text}
      </span>
      {outgoingText === null ? null : (
        <span
          aria-hidden="true"
          className="board-label-filter-summary-outgoing pointer-events-none absolute top-0 left-0 inline-block w-max whitespace-nowrap"
          key={`outgoing-${outgoingText}`}
        >
          {outgoingText}
        </span>
      )}
      <span
        className={cx(
          "inline-block w-max whitespace-nowrap",
          outgoingText !== null && "board-label-filter-summary-incoming",
        )}
        key={`incoming-${text}`}
      >
        {text}
      </span>
    </span>
  );
}

import { useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";

export function TaskDetailBodyIslands({
  description,
  metadata,
}: Readonly<{
  description: ReactNode;
  metadata: ReactNode;
}>) {
  const rowRef = useRef<HTMLDivElement | null>(null);
  const descriptionRef = useRef<HTMLDivElement | null>(null);
  const metadataRef = useRef<HTMLDivElement | null>(null);
  const [rowWidthPx, setRowWidthPx] = useState<number | null>(null);
  const [measurement, setMeasurement] = useState<TaskDetailBodyIslandMeasurement | null>(null);
  const sideBySide = rowWidthPx !== null && rowWidthPx >= taskDetailSideBySideMinimumWidthPx;
  const measurementCurrent =
    measurement !== null &&
    measurement.description === description &&
    measurement.metadata === metadata &&
    measurement.rowWidthPx === rowWidthPx;

  useLayoutEffect(() => {
    if (!sideBySide || measurementCurrent) return;
    const descriptionElement = descriptionRef.current;
    const metadataElement = metadataRef.current;
    if (descriptionElement === null || metadataElement === null) return;
    setMeasurement({
      description,
      heightPx: taskDetailBodyIslandHeight(
        descriptionElement.getBoundingClientRect().height,
        metadataElement.getBoundingClientRect().height,
      ),
      metadata,
      rowWidthPx,
    });
  }, [description, measurementCurrent, metadata, rowWidthPx, sideBySide]);

  useLayoutEffect(() => {
    const row = rowRef.current;
    if (row === null) return;
    const measureWidth = () => {
      setRowWidthPx(row.getBoundingClientRect().width);
    };
    measureWidth();
    const observer = new ResizeObserver(measureWidth);
    observer.observe(row);
    return () => {
      observer.disconnect();
    };
  }, []);

  const rowStyle: CSSProperties | undefined = sideBySide
    ? { gridTemplateColumns: "minmax(0, 65fr) minmax(0, 35fr)" }
    : undefined;
  const sharedHeightPx = sideBySide && measurementCurrent ? measurement.heightPx : null;
  const slotStyle: CSSProperties | undefined =
    !sideBySide || sharedHeightPx === null ? undefined : { height: `${sharedHeightPx.toString()}px` };
  return (
    <div
      className="task-detail-body-split grid w-full min-w-0 max-w-full items-stretch gap-[var(--space-2)]"
      data-testid="task-detail-body-split"
      ref={rowRef}
      style={rowStyle}
    >
      <div
        className="task-detail-body-island-slot grid min-w-0"
        data-testid="task-detail-description-slot"
        ref={descriptionRef}
        style={slotStyle}
      >
        {description}
      </div>
      <div
        className="task-detail-body-island-slot grid min-w-0"
        data-testid="task-detail-metadata-slot"
        ref={metadataRef}
        style={slotStyle}
      >
        {metadata}
      </div>
    </div>
  );
}

type TaskDetailBodyIslandMeasurement = Readonly<{
  description: ReactNode;
  heightPx: number;
  metadata: ReactNode;
  rowWidthPx: number;
}>;

const taskDetailSideBySideMinimumWidthPx = 720;

function taskDetailBodyIslandHeight(
  descriptionIntrinsicHeightPx: number,
  metadataIntrinsicHeightPx: number,
): number {
  return Math.max(descriptionIntrinsicHeightPx, metadataIntrinsicHeightPx);
}

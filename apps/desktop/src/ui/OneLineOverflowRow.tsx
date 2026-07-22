import { useCallback, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";

import { cx } from "./classes";
import { oneLineOverflowLayout, type OneLineOverflowLayout } from "./oneLineOverflowGeometry";

export type OneLineOverflowItem = Readonly<{
  content: ReactNode;
  id: string;
}>;

export type OneLineOverflowRowProps = Readonly<{
  ariaLabel: string;
  items: readonly OneLineOverflowItem[];
  renderOverflow(hiddenCount: number): ReactNode;
}> &
  Omit<HTMLAttributes<HTMLDivElement>, "children">;

export function OneLineOverflowRow({
  ariaLabel,
  className,
  items,
  renderOverflow,
  ...props
}: OneLineOverflowRowProps) {
  const itemIDs = new Set<string>();
  for (const item of items) {
    if (itemIDs.has(item.id)) {
      throw new Error(`One-line overflow row "${ariaLabel}" contains duplicate item ID "${item.id}".`);
    }
    itemIDs.add(item.id);
  }
  const rowRef = useRef<HTMLDivElement | null>(null);
  const gapStartRef = useRef<HTMLSpanElement | null>(null);
  const gapEndRef = useRef<HTMLSpanElement | null>(null);
  const overflowRef = useRef<HTMLSpanElement | null>(null);
  const itemRefs = useRef(new Map<string, HTMLSpanElement>());
  const [layout, setLayout] = useState<OneLineOverflowLayout>({
    hiddenCount: 0,
    visibleCount: items.length,
  });
  const measure = useCallback(() => {
    const row = rowRef.current;
    const gapStart = gapStartRef.current;
    const gapEnd = gapEndRef.current;
    if (row === null || gapStart === null || gapEnd === null) {
      return;
    }
    if (items.length === 0) {
      setLayout(emptyLayout);
      return;
    }
    const overflow = overflowRef.current;
    if (overflow === null) {
      return;
    }
    const itemWidths: number[] = [];
    for (const item of items) {
      const itemElement = itemRefs.current.get(item.id);
      if (itemElement === undefined) {
        return;
      }
      itemWidths.push(itemElement.getBoundingClientRect().width);
    }
    const overflowItemWidth = overflow.getBoundingClientRect().width;
    const nextLayout = oneLineOverflowLayout({
      availableWidth: row.getBoundingClientRect().width,
      gap: Math.max(0, gapEnd.getBoundingClientRect().left - gapStart.getBoundingClientRect().right),
      itemWidths,
      overflowWidth: () => overflowItemWidth,
    });
    setLayout((current) => (layoutsEqual(current, nextLayout) ? current : nextLayout));
  }, [items]);

  useLayoutEffect(() => {
    measure();
    if (typeof ResizeObserver === "undefined") {
      if (typeof requestAnimationFrame === "undefined" || typeof cancelAnimationFrame === "undefined") {
        return;
      }
      const frame = requestAnimationFrame(measure);
      return () => {
        cancelAnimationFrame(frame);
      };
    }
    const observer = new ResizeObserver(measure);
    const row = rowRef.current;
    const overflow = overflowRef.current;
    if (row !== null) {
      observer.observe(row);
    }
    if (overflow !== null) {
      observer.observe(overflow);
    }
    for (const item of itemRefs.current.values()) {
      observer.observe(item);
    }
    return () => {
      observer.disconnect();
    };
  }, [measure]);

  const overflowCount = layout.hiddenCount === 0 ? items.length : layout.hiddenCount;

  return (
    <div className={cx("relative min-w-0", className)} {...props}>
      <div
        aria-label={ariaLabel}
        className="flex min-w-0 items-center gap-[var(--space-1)] overflow-hidden whitespace-nowrap"
        data-slot="one-line-overflow-row"
        ref={rowRef}
        role="group"
      >
        {items.map((item, index) => {
          const visible = index < layout.visibleCount;
          return (
            <span
              aria-hidden={visible ? undefined : true}
              className={cx(
                "min-w-0 shrink-0",
                !visible && "pointer-events-none invisible absolute left-0 top-0",
              )}
              data-slot="one-line-overflow-item"
              inert={visible ? undefined : true}
              key={item.id}
              ref={(element) => {
                if (element === null) {
                  itemRefs.current.delete(item.id);
                } else {
                  itemRefs.current.set(item.id, element);
                }
              }}
            >
              {item.content}
            </span>
          );
        })}
        {items.length === 0 ? null : (
          <span
            aria-hidden={layout.hiddenCount === 0 ? true : undefined}
            className={cx(
              "shrink-0",
              layout.hiddenCount === 0 && "pointer-events-none invisible absolute left-0 top-0",
            )}
            data-slot="one-line-overflow-count"
            inert={layout.hiddenCount === 0 ? true : undefined}
            ref={overflowRef}
          >
            {renderOverflow(overflowCount)}
          </span>
        )}
      </div>
      <span
        aria-hidden="true"
        className="pointer-events-none invisible absolute left-0 top-0 flex gap-[var(--space-1)]"
        inert
      >
        <span className="block h-px w-px" data-slot="one-line-overflow-gap-start" ref={gapStartRef} />
        <span className="block h-px w-px" data-slot="one-line-overflow-gap-end" ref={gapEndRef} />
      </span>
    </div>
  );
}

const emptyLayout = {
  hiddenCount: 0,
  visibleCount: 0,
} satisfies OneLineOverflowLayout;

function layoutsEqual(left: OneLineOverflowLayout, right: OneLineOverflowLayout): boolean {
  return left.hiddenCount === right.hiddenCount && left.visibleCount === right.visibleCount;
}

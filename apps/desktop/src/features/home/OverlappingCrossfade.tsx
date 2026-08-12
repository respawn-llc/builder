import { type ReactNode, useLayoutEffect, useRef, useState } from "react";

import { cx } from "@/ui";

export function OverlappingCrossfade({
  children,
  contentKey,
}: Readonly<{ children: ReactNode; contentKey: string }>) {
  const previous = useRef({ children, contentKey });
  const [outgoing, setOutgoing] = useState<Readonly<{
    children: ReactNode;
    contentKey: string;
  }> | null>(null);

  useLayoutEffect(() => {
    if (previous.current.contentKey !== contentKey) {
      setOutgoing(previous.current);
    }
  }, [contentKey]);

  useLayoutEffect(() => {
    previous.current = { children, contentKey };
  });

  return (
    <div className="relative h-full min-h-0">
      {outgoing === null ? null : (
        <div
          className="pointer-events-none absolute inset-0 z-0 animate-[detail-pane-crossfade-out_var(--motion-normal)_both]"
          key={`outgoing:${outgoing.contentKey}`}
          onAnimationEnd={(event) => {
            if (event.target === event.currentTarget) {
              setOutgoing(null);
            }
          }}
        >
          {outgoing.children}
        </div>
      )}
      <div
        className={cx(
          "absolute inset-0 z-10",
          outgoing !== null && "animate-[detail-pane-crossfade-in_var(--motion-normal)_both]",
        )}
        key={contentKey}
      >
        {children}
      </div>
    </div>
  );
}

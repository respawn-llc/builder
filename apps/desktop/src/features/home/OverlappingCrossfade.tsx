import { type ReactNode, useRef, useState } from "react";

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

  if (previous.current.contentKey !== contentKey) {
    const nextOutgoing = previous.current;
    previous.current = { children, contentKey };
    if (outgoing?.contentKey !== nextOutgoing.contentKey) {
      setOutgoing(nextOutgoing);
    }
  } else {
    previous.current = { children, contentKey };
  }

  return (
    <div className="relative h-full min-h-0">
      {outgoing === null ? null : (
        <div
          className="pointer-events-none absolute inset-0 z-0 animate-[detail-pane-crossfade-out_var(--motion-normal)_both]"
          key={outgoing.contentKey}
          onAnimationEnd={(event) => {
            if (event.target === event.currentTarget) {
              setOutgoing((current) =>
                current?.contentKey === outgoing.contentKey ? null : current,
              );
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

import { Zap } from "lucide-react";
import { useLayoutEffect, useRef, useState } from "react";

type SummaryProps = Readonly<{
  role: string;
  model: string;
  thinking: string | null;
  fast: boolean;
}>;

export function ChatSettingsSummary(props: SummaryProps) {
  const viewportRef = useRef<HTMLSpanElement>(null);
  const contentRef = useRef<HTMLSpanElement>(null);
  const [overflow, setOverflow] = useState(false);
  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    const content = contentRef.current;
    if (viewport === null || content === null) return;
    function measure() {
      if (viewport === null || content === null) return;
      setOverflow(content.getBoundingClientRect().width > viewport.getBoundingClientRect().width);
    }
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(viewport);
    observer.observe(content);
    return () => {
      observer.disconnect();
    };
  }, [props.role, props.model, props.thinking, props.fast]);
  return (
    <span className="relative block min-w-0 overflow-hidden whitespace-nowrap" ref={viewportRef}>
      <span className="pointer-events-none invisible absolute top-0 left-0 w-max" ref={contentRef}>
        <SummaryContent {...props} />
      </span>
      {overflow ? (
        <span className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
          <span className="overflow-hidden">
            <SummaryContent {...props} />
          </span>
          <span>…</span>
          <span className="flex justify-end overflow-hidden">
            <SummaryContent {...props} />
          </span>
        </span>
      ) : (
        <SummaryContent {...props} />
      )}
    </span>
  );
}

function SummaryContent({ role, model, thinking, fast }: SummaryProps) {
  return (
    <span
      className="inline-flex w-max shrink-0 items-center whitespace-pre animate-in fade-in duration-[var(--motion-fast)] motion-reduce:animate-none"
      key={JSON.stringify([role, model, thinking, fast])}
    >
      <span>{role}: </span>
      <span className="font-mono text-[var(--color-muted)]">{model}</span>
      {thinking === null ? null : (
        <>
          <span> </span>
          <span className="font-mono text-[var(--color-muted)]">{thinking}</span>
        </>
      )}
      {fast ? (
        <>
          <span> </span>
          <Zap className="shrink-0 text-[var(--color-secondary)]" size={14} />
        </>
      ) : null}
    </span>
  );
}

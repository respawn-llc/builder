import { useState, type CSSProperties, type PointerEvent, type ReactNode } from "react";

import { prefersReducedMotion } from "@/ui";

type ActiveTone = "neutral" | "primary" | "secondary";

type SteppedSelectorProps<Value extends string> = Readonly<{
  values: readonly Value[];
  value: Value;
  disabled: boolean;
  activeTone(value: Value): ActiveTone;
  onCommit(value: Value): void;
  children(value: Value): ReactNode;
}>;

// Internal to Chat: the caller owns labels and the value-to-tone policy.
export function SteppedSelector<Value extends string>({
  values,
  value,
  disabled,
  activeTone,
  onCommit,
  children,
}: SteppedSelectorProps<Value>) {
  const [previous, setPrevious] = useState({ values, value, disabled });
  const [gesture, setGesture] = useState<Readonly<{ pointerID: number; index: number }> | null>(null);
  const replaced =
    previous.value !== value ||
    previous.disabled !== disabled ||
    previous.values.length !== values.length ||
    previous.values.some((candidate, index) => candidate !== values[index]);
  if (replaced) {
    setPrevious({ values, value, disabled });
    setGesture(null);
  }

  const selectedIndex = selectionIndex(values, value);
  const index = (replaced ? null : gesture)?.index ?? selectedIndex;
  const displayedValue = values[index];
  if (displayedValue === undefined) {
    throw new Error("Stepped selector index is outside its ordered values.");
  }
  const position = stepPosition(index, values.length);
  const tone = activeTone(displayedValue);
  const style = {
    "--stepped-active": toneColors[tone],
    transitionDuration: prefersReducedMotion() ? "0ms" : undefined,
  } satisfies CSSProperties & Record<"--stepped-active", string>;

  function pointerIndex(event: PointerEvent<HTMLInputElement>): number {
    const bounds = event.currentTarget.getBoundingClientRect();
    const fraction = bounds.width === 0 ? 0 : (event.clientX - bounds.left) / bounds.width;
    return Math.round(Math.max(0, Math.min(1, fraction)) * (values.length - 1));
  }

  function commit(nextIndex: number) {
    const next = values[nextIndex];
    if (next === undefined) {
      throw new Error("Stepped selector received an invalid step.");
    }
    onCommit(next);
  }

  return (
    <div className="grid min-w-0 gap-[var(--space-2)]">
      {children(displayedValue)}
      <div
        className="relative mx-[var(--space-2)] h-[var(--space-6)] min-w-0 rounded-[var(--radius-m)] transition-colors duration-[var(--motion-fast)] motion-reduce:transition-none has-focus-visible:ring-[3px] has-focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)]"
        style={style}
      >
        <span className="pointer-events-none absolute inset-x-0 top-1/2 h-[var(--space-1)] -translate-y-1/2 rounded-full bg-[var(--color-outline)]" />
        <span
          className="pointer-events-none absolute top-1/2 left-0 h-[var(--space-1)] -translate-y-1/2 rounded-full bg-[var(--stepped-active)] transition-[width,background-color] duration-[var(--motion-fast)] motion-reduce:transition-none"
          style={{ width: `${String(position * 100)}%`, transitionDuration: style.transitionDuration }}
        />
        {values.map((step, stepIndex) => (
          <span
            className="pointer-events-none absolute top-1/2 h-[var(--space-1)] w-[var(--space-1)] -translate-1/2 rounded-full bg-[var(--color-on-island)]"
            key={step}
            style={{ left: `${String(stepPosition(stepIndex, values.length) * 100)}%` }}
          />
        ))}
        <span
          className="pointer-events-none absolute top-1/2 h-[var(--space-3)] w-[var(--space-3)] -translate-1/2 rounded-full bg-[var(--stepped-active)] transition-[left,background-color] duration-[var(--motion-fast)] motion-reduce:transition-none"
          style={{ left: `${String(position * 100)}%`, transitionDuration: style.transitionDuration }}
        />
        <input
          className="app-region-no-drag absolute inset-0 m-0 h-full w-full cursor-pointer touch-none opacity-0 disabled:cursor-not-allowed"
          disabled={disabled || values.length === 1}
          max={values.length - 1}
          min={0}
          onChange={(event) => {
            if (!disabled && gesture === null) commit(event.currentTarget.valueAsNumber);
          }}
          onClick={(event) => {
            event.stopPropagation();
          }}
          onKeyDown={(event) => {
            event.stopPropagation();
          }}
          onLostPointerCapture={() => {
            setGesture(null);
          }}
          onPointerCancel={() => {
            setGesture(null);
          }}
          onPointerDown={(event) => {
            event.stopPropagation();
            if (disabled || !event.isPrimary || event.button !== 0 || gesture !== null) return;
            event.preventDefault();
            event.currentTarget.focus();
            event.currentTarget.setPointerCapture(event.pointerId);
            setGesture({ pointerID: event.pointerId, index: pointerIndex(event) });
          }}
          onPointerMove={(event) => {
            if (!disabled && gesture?.pointerID === event.pointerId) {
              setGesture({ pointerID: event.pointerId, index: pointerIndex(event) });
            }
          }}
          onPointerUp={(event) => {
            event.stopPropagation();
            if (disabled || gesture?.pointerID !== event.pointerId) return;
            setGesture(null);
            event.currentTarget.releasePointerCapture(event.pointerId);
            commit(pointerIndex(event));
          }}
          step={1}
          type="range"
          value={index}
        />
      </div>
    </div>
  );
}

function selectionIndex<Value extends string>(values: readonly Value[], value: Value): number {
  const index = values.indexOf(value);
  if (index < 0 || new Set(values).size !== values.length) {
    throw new Error("Stepped selector requires unique ordered values containing its selection.");
  }
  return index;
}

function stepPosition(index: number, count: number): number {
  return count === 1 ? 0 : index / (count - 1);
}

const toneColors = {
  neutral: "var(--color-on-island)",
  primary: "var(--color-primary)",
  secondary: "var(--color-secondary)",
} satisfies Record<ActiveTone, string>;

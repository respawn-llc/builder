import { Button } from "./Button";
import { Spinner } from "./Spinner";

export type VirtualizedInfiniteListBoundaryState =
  | Readonly<{ state: "idle" }>
  | Readonly<{ state: "loading"; label: string }>
  | Readonly<{ state: "error"; message: string; retryLabel: string; onRetry: () => void }>;

export function InfiniteListBoundary({
  direction,
  state,
}: Readonly<{
  direction: "initial" | "previous" | "next";
  state: VirtualizedInfiniteListBoundaryState;
}>) {
  return (
    <div
      className="grid min-h-12 place-items-center"
      data-testid={`virtual-boundary-${direction}`}
      data-virtual-boundary={direction}
    >
      {state.state === "loading" ? (
        <div aria-label={state.label} aria-live="polite" className="grid place-items-center" role="status">
          <Spinner size="sm" />
          <span className="sr-only">{state.label}</span>
        </div>
      ) : null}
      {state.state === "error" ? (
        <div className="flex flex-wrap items-center justify-center gap-[var(--space-2)]" role="alert">
          <span className="text-sm text-[var(--color-muted)]">{state.message}</span>
          <Button onClick={state.onRetry}>{state.retryLabel}</Button>
        </div>
      ) : null}
    </div>
  );
}

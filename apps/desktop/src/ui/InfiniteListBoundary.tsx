import { Button } from "./Button";
import { Spinner } from "./Spinner";

export type VirtualizedInfiniteListBoundaryState =
  | Readonly<{ state: "loading"; label: string }>
  | Readonly<{ state: "error"; message: string; retryLabel: string; onRetry: () => void }>;

export function directionalBoundary(
  input: Readonly<{
    message: string;
    failed: boolean;
    loading: boolean;
    loadingLabel: string;
    onRetry: () => void;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (input.failed) {
    return {
      state: "error",
      message: input.message,
      retryLabel: input.retryLabel,
      onRetry: input.onRetry,
    };
  }
  if (input.loading) {
    return { state: "loading", label: input.loadingLabel };
  }
  return undefined;
}

export function autoLoadAvailable(
  available: boolean,
  boundary: VirtualizedInfiniteListBoundaryState | undefined,
): boolean {
  return available && boundary?.state !== "error";
}

export function InfiniteListBoundary({
  direction,
  state,
}: Readonly<{
  direction: "initial" | "previous" | "next" | "replacement";
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

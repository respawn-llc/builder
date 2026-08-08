import { useCallback, useLayoutEffect, useMemo, useRef } from "react";

export type TaskDetailDeleteDismissalResult =
  Readonly<{ kind: "accepted" }> | Readonly<{ kind: "stale" }> | Readonly<{ kind: "failed"; error: unknown }>;

export type TaskDetailDeleteDismissal = () => Promise<TaskDetailDeleteDismissalResult>;

export function useExactTaskDetailDeleteDismissal(
  identity: string,
  dismiss: () => Promise<void>,
): TaskDetailDeleteDismissal {
  const token = useMemo(() => Symbol(identity), [identity]);
  const currentToken = useRef<symbol | null>(null);
  useLayoutEffect(() => {
    currentToken.current = token;
    return () => {
      if (currentToken.current === token) {
        currentToken.current = null;
      }
    };
  }, [token]);
  return useCallback(async () => {
    if (currentToken.current !== token) {
      return { kind: "stale" };
    }
    try {
      await dismiss();
      return { kind: "accepted" };
    } catch (error) {
      return { kind: "failed", error };
    }
  }, [dismiss, token]);
}

import type { ReactNode } from "react";
import { useCallback, useMemo } from "react";

import { dismissStatusToast, showStatusToast, Toaster, type StatusNotice } from "../ui";
import { StatusContext, type StatusController } from "./statusContextValue";

export type StatusProviderProps = Readonly<{
  children: ReactNode;
}>;

export function StatusProvider({ children }: StatusProviderProps) {
  const push = useCallback((notice: StatusNotice) => {
    showStatusToast(notice);
  }, []);
  const dismiss = useCallback((id: string) => {
    dismissStatusToast(id);
  }, []);

  const controller = useMemo<StatusController>(
    () => ({
      push,
      dismiss,
    }),
    [dismiss, push],
  );

  return (
    <StatusContext.Provider value={controller}>
      {children}
      <Toaster />
    </StatusContext.Provider>
  );
}

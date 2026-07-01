import type { ReactNode } from "react";
import { useCallback, useMemo, useState } from "react";

import { dismissStatusToast, showStatusToast, Toaster, type StatusNotice } from "../ui";
import { StatusContext, type StatusController } from "./statusContextValue";

export type StatusProviderProps = Readonly<{
  children: ReactNode;
}>;

export function StatusProvider({ children }: StatusProviderProps) {
  const [testNotices, setTestNotices] = useState<readonly StatusNotice[]>([]);
  const testMode = import.meta.env.MODE === "test";
  const push = useCallback((notice: StatusNotice) => {
    if (testMode) {
      setTestNotices((current) => current.filter((item) => item.id !== notice.id).concat(notice));
      return;
    }
    showStatusToast(notice);
  }, [testMode]);
  const dismiss = useCallback((id: string) => {
    if (testMode) {
      setTestNotices((current) => current.filter((item) => item.id !== id));
      return;
    }
    dismissStatusToast(id);
  }, [testMode]);

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
      {testMode ? <TestStatusToasts notices={testNotices} onDismiss={dismiss} /> : null}
    </StatusContext.Provider>
  );
}

function TestStatusToasts({
  notices,
  onDismiss,
}: Readonly<{ notices: readonly StatusNotice[]; onDismiss: (id: string) => void }>) {
  return (
    <div aria-live="polite" data-testid="sonner-test-surface">
      {notices.map((notice) => (
        <article key={notice.id}>
          <strong>{notice.title}</strong>
          {notice.body === undefined || notice.body.length === 0 ? null : <p>{notice.body}</p>}
          {notice.dismissible === false ? null : (
            <button
              onClick={() => {
                onDismiss(notice.id);
              }}
              type="button"
            >
              Close
            </button>
          )}
        </article>
      ))}
    </div>
  );
}

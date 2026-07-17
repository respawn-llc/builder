import type { ReactNode } from "react";
import { useCallback, useMemo, useState } from "react";

import {
  CurrentWindowChromeTitleContext,
  WindowChromeTitleControllerContext,
  type WindowChromeTitleController,
  type WindowChromeTitleRegistration,
} from "./windowChromeTitle";

export type WindowChromeTitleProviderProps = Readonly<{
  children: ReactNode;
}>;

export function WindowChromeTitleProvider({ children }: WindowChromeTitleProviderProps) {
  const [registrations, setRegistrations] = useState<readonly WindowChromeTitleRegistration[]>([]);
  const title = registrations[registrations.length - 1]?.title ?? null;
  const setTitle = useCallback((nextTitle: string | null) => {
    const id = Symbol("window-chrome-title");
    const registration = { id, title: nextTitle };
    setRegistrations((current) => current.concat(registration));
    return () => {
      setRegistrations((current) => current.filter((item) => item.id !== id));
    };
  }, []);
  const controller = useMemo<WindowChromeTitleController>(
    () => ({
      setTitle,
    }),
    [setTitle],
  );

  return (
    <WindowChromeTitleControllerContext.Provider value={controller}>
      <CurrentWindowChromeTitleContext.Provider value={title}>
        {children}
      </CurrentWindowChromeTitleContext.Provider>
    </WindowChromeTitleControllerContext.Provider>
  );
}

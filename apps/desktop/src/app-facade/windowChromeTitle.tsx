import { createContext, useContext, useEffect } from "react";

export type WindowChromeTitleRegistration = Readonly<{
  id: symbol;
  title: string | null;
}>;

export type WindowChromeTitleController = Readonly<{
  setTitle(title: string | null): () => void;
}>;

export const WindowChromeTitleControllerContext = createContext<WindowChromeTitleController | null>(null);
export const CurrentWindowChromeTitleContext = createContext<string | null>(null);

export function useWindowChromeTitle(title: string | null, enabled = true): void {
  const controller = useContext(WindowChromeTitleControllerContext);
  const normalizedTitle = normalizeWindowChromeTitle(title);

  useEffect(() => {
    if (controller === null || !enabled) {
      return undefined;
    }
    return controller.setTitle(normalizedTitle);
  }, [controller, enabled, normalizedTitle]);
}

export function useCurrentWindowChromeTitle(): string | null {
  return useContext(CurrentWindowChromeTitleContext);
}

function normalizeWindowChromeTitle(title: string | null | undefined): string | null {
  const trimmed = title?.trim() ?? "";
  return trimmed.length > 0 ? trimmed : null;
}

import { useCallback } from "react";

import { useAppServices } from "./useAppServices";

export function useOpenExternalLink(): (url: string) => void {
  const { nativeBridge, logger } = useAppServices();
  return useCallback(
    (url: string) => {
      void nativeBridge.links.openExternal(url).catch((error: unknown) => {
        void logger.append("warn", "Open external link failed.", {
          error: error instanceof Error ? error.message : "unknown",
        });
      });
    },
    [logger, nativeBridge],
  );
}

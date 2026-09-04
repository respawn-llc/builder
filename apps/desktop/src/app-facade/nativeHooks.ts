import { useCallback, useEffect, useState } from "react";

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

export function useWindowFocus(): boolean | null {
  const { logger, nativeBridge } = useAppServices();
  const [focused, setFocused] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    let focusEventObserved = false;
    let focusListenerFailed = false;
    let unlisten: (() => void) | null = null;

    void nativeBridge.window
      .onFocusChanged((nextFocused) => {
        focusEventObserved = true;
        if (active) {
          setFocused(nextFocused);
        }
      })
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
        } else {
          nextUnlisten();
        }
      })
      .catch((error: unknown) => {
        focusListenerFailed = true;
        if (active) {
          setFocused(false);
        }
        void logger.append("warn", "Listening for native window focus changes failed.", {
          error: error instanceof Error ? error.message : "unknown",
        });
      });

    void nativeBridge.window
      .isFocused()
      .then((nextFocused) => {
        if (active && !focusEventObserved && !focusListenerFailed) {
          setFocused(nextFocused);
        }
      })
      .catch((error: unknown) => {
        if (active && !focusEventObserved) {
          setFocused(false);
        }
        void logger.append("warn", "Reading native window focus state failed.", {
          error: error instanceof Error ? error.message : "unknown",
        });
      });

    return () => {
      active = false;
      unlisten?.();
    };
  }, [logger, nativeBridge.window]);

  return focused;
}

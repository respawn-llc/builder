import { useCallback, useEffect, useRef, useState } from "react";

import type { NativeBridge } from "@app/native-bridge";

import type { AppLogger } from "@/app-facade";
import { checkForDesktopUpdate, installDesktopUpdate } from "./desktopUpdate";

export type DesktopUpdatePhase = "none" | "available" | "installing" | "error";

export type DesktopUpdateState = Readonly<{
  phase: DesktopUpdatePhase;
  version: string;
  // Download ratio 0..1 while installing, or null when the total size is unknown
  // or no download is in progress.
  progressRatio: number | null;
  install: () => void;
  // Hides the chip for this session; it returns on the next launch because the
  // check re-runs on mount (no update is permanently lost).
  dismiss: () => void;
}>;

// Drives the app-chrome update chip: checks once on mount (auto), exposes the
// available version, runs a one-shot download -> install -> relaunch on install(),
// and hides the chip for the session on dismiss(). On a successful install the app
// relaunches, so there is no terminal "done" state; failures fall back to "error"
// so the chip stays actionable.
export function useDesktopUpdate(nativeBridge: NativeBridge, logger: AppLogger): DesktopUpdateState {
  const [phase, setPhase] = useState<DesktopUpdatePhase>("none");
  const [version, setVersion] = useState("");
  const [progressRatio, setProgressRatio] = useState<number | null>(null);
  const installingRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    void checkForDesktopUpdate(nativeBridge, logger).then((result) => {
      if (cancelled || !result.available) {
        return;
      }
      setVersion(result.version);
      setPhase("available");
    });
    return () => {
      cancelled = true;
    };
  }, [nativeBridge, logger]);

  const dismiss = useCallback(() => {
    setPhase("none");
  }, []);

  const install = useCallback(() => {
    if (installingRef.current) {
      return;
    }
    installingRef.current = true;
    setProgressRatio(null);
    setPhase("installing");
    void installDesktopUpdate(nativeBridge, logger, (progress) => {
      setProgressRatio(
        progress.totalBytes !== null && progress.totalBytes > 0
          ? progress.downloadedBytes / progress.totalBytes
          : null,
      );
    }).then((result) => {
      installingRef.current = false;
      if (!result.ok) {
        setPhase("error");
      }
      // On success the app relaunches into the new version; no state change here.
    });
  }, [nativeBridge, logger]);

  return { phase, version, progressRatio, install, dismiss };
}

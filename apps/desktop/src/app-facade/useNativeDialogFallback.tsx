import { useCallback, useState, type ReactNode } from "react";

import { errorMessage } from "@/api";
import { useStatusController } from "./useStatusController";

export type NativeDialogFallbackController<TPayload> = Readonly<{
  fallback: ReactNode;
  open(payload: TPayload): Promise<void>;
}>;

export function useNativeDialogFallback<TPayload>({
  errorNoticeID,
  errorTitle,
  nativeAvailable = true,
  openNative,
  renderFallback,
}: Readonly<{
  errorNoticeID: string;
  errorTitle: string;
  nativeAvailable?: boolean | undefined;
  openNative: (payload: TPayload) => Promise<void>;
  renderFallback: (payload: TPayload, close: () => void) => ReactNode;
}>): NativeDialogFallbackController<TPayload> {
  const { push } = useStatusController();
  const [fallbackPayload, setFallbackPayload] = useState<TPayload | null>(null);
  const closeFallback = useCallback(() => {
    setFallbackPayload(null);
  }, []);
  const open = useCallback(
    async (payload: TPayload): Promise<void> => {
      if (!nativeAvailable) {
        setFallbackPayload(payload);
        return;
      }
      try {
        await openNative(payload);
        setFallbackPayload(null);
      } catch (error) {
        push({
          id: errorNoticeID,
          tone: "danger",
          title: errorTitle,
          body: errorMessage(error),
        });
        setFallbackPayload(payload);
      }
    },
    [errorNoticeID, errorTitle, nativeAvailable, openNative, push],
  );

  return {
    fallback: fallbackPayload === null ? null : renderFallback(fallbackPayload, closeFallback),
    open,
  };
}

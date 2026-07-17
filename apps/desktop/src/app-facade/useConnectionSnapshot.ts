import { useSyncExternalStore } from "react";

import { useAppServices } from "./useAppServices";

export function useConnectionSnapshot() {
  const { api } = useAppServices();
  return useSyncExternalStore(
    (listener) => api.connection.subscribe(listener),
    () => api.connection.snapshot(),
    () => api.connection.snapshot(),
  );
}

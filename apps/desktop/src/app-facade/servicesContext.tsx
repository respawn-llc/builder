import type { ReactNode } from "react";

import { AppServicesContext } from "./appServicesContextValue";
import type { AppServices } from "./services";

export type AppServicesProviderProps = Readonly<{
  services: AppServices;
  children: ReactNode;
}>;

export function AppServicesProvider({ services, children }: AppServicesProviderProps) {
  return <AppServicesContext.Provider value={services}>{children}</AppServicesContext.Provider>;
}

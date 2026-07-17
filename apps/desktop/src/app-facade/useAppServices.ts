import { useContext } from "react";

import { AppServicesContext } from "./appServicesContextValue";
import type { AppServices } from "./services";

export function useAppServices(): AppServices {
  const services = useContext(AppServicesContext);
  if (services === null) {
    throw new Error("App services provider is missing.");
  }
  return services;
}

import { useContext } from "react";

import { StatusContext, type StatusController } from "./statusContextValue";

export function useStatusController(): StatusController {
  const controller = useContext(StatusContext);
  if (controller === null) {
    throw new Error("Status provider is missing.");
  }
  return controller;
}

import { createContext } from "react";

import type { AppServices } from "./services";

export const AppServicesContext = createContext<AppServices | null>(null);

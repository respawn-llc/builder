import { createContext } from "react";

import type { StatusNotice } from "@/ui";

export type StatusController = Readonly<{
  push(notice: StatusNotice): void;
  dismiss(id: string): void;
}>;

export const StatusContext = createContext<StatusController | null>(null);

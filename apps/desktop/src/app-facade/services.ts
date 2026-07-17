import type { NativeBridge } from "@app/native-bridge";

import type { ApiService } from "@/api";
import type { AppLogger } from "./logging";

export type AppServices = Readonly<{
  api: ApiService;
  debugThemeOverrideEnabled: boolean;
  endpoint: string;
  homePath: string;
  logger: AppLogger;
  nativeBridge: NativeBridge;
  protocolVersion: string;
}>;

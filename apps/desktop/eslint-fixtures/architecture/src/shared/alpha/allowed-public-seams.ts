import type { VendorValue } from "@xyflow/react";
import { apiValue } from "@/api";
import { facadeValue } from "@/app-facade";
import { sharedBetaValue } from "@/shared/beta";
import { uiValue } from "@/ui";

export const allowedSharedValues = [
  apiValue,
  facadeValue,
  sharedBetaValue,
  uiValue,
] satisfies readonly string[];

export type AllowedSharedVendorValue = VendorValue;

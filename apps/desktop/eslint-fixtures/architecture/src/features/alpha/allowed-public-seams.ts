import type { VendorValue } from "@xyflow/react";
import { apiValue } from "@/api";
import { facadeValue } from "@/app-facade";
import { i18nValue } from "@/i18n";
import { sharedValue } from "@/shared/alpha";
import { uiValue } from "@/ui";

export const allowedFeatureValues = [
  apiValue,
  facadeValue,
  i18nValue,
  sharedValue,
  uiValue,
] satisfies readonly string[];

export type AllowedFeatureVendorValue = VendorValue;

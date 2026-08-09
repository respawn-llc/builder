import { nativeValue } from "@app/native-bridge";
import type { ElkVendorValue } from "elkjs/lib/elk-api";
import { elkVendorValue } from "elkjs/lib/elk.bundled.js";
import { featureValue } from "@/features/alpha";
import { facadeValue } from "@/app-facade";
import { sharedValue } from "@/shared/alpha";
import { uiValue } from "@/ui";
import { apiCompositionValue } from "@/api/composition";
import { apiValue } from "@/api";
import { i18nValue } from "@/i18n";
import type { VendorValue } from "@xyflow/react";

export const allowedShellValues = [
  apiCompositionValue,
  apiValue,
  elkVendorValue,
  facadeValue,
  featureValue,
  i18nValue,
  nativeValue,
  sharedValue,
  uiValue,
] satisfies readonly string[];

export type AllowedShellVendorValue = VendorValue;
export type AllowedShellElkVendorValue = ElkVendorValue;
